package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mogar/harnisch/internal/chat"
	"github.com/mogar/harnisch/internal/provider/ollama"
	"github.com/mogar/harnisch/internal/tool"
)

// fakeOllama replays canned NDJSON responses in order. This is the seed of the
// record/replay harness: capture real chunks from a live server into files,
// then run the loop against them deterministically in CI.
func fakeOllama(t *testing.T, responses ...string) *httptest.Server {
	t.Helper()
	var n int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n >= len(responses) {
			t.Errorf("unexpected request %d", n+1)
			http.Error(w, "no more canned responses", http.StatusInternalServerError)
			return
		}
		body := responses[n]
		n++
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(body))
	}))
}

func chunk(content string, done bool) string {
	b, _ := json.Marshal(map[string]any{
		"message": map[string]any{"role": "assistant", "content": content},
		"done":    done,
	})
	return string(b) + "\n"
}

func toolChunk(name string, args map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"role": "assistant",
			"tool_calls": []map[string]any{
				{"function": map[string]any{"name": name, "arguments": args}},
			},
		},
		"done": false,
	})
	return string(b) + "\n"
}

func newTestAgent(t *testing.T, srv *httptest.Server, root string) *Agent {
	t.Helper()
	r, err := tool.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	for _, tl := range []tool.Tool{tool.ReadFile{Root: r}, tool.ListDir{Root: r}} {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return &Agent{Provider: ollama.New(srv.URL, "test-model"), Tools: reg, System: "test"}
}

func TestPlainTextTurn(t *testing.T) {
	srv := fakeOllama(t, chunk("Hello ", false)+chunk("world.", true))
	defer srv.Close()

	a := newTestAgent(t, srv, t.TempDir())
	var out bytes.Buffer
	if err := a.Turn(context.Background(), "hi", &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Hello world." {
		t.Errorf("streamed output = %q", got)
	}
	if len(a.Messages) != 2 {
		t.Fatalf("expected user+assistant, got %d messages", len(a.Messages))
	}
	if got := a.Messages[1].TextContent(); got != "Hello world." {
		t.Errorf("assistant text = %q", got)
	}
}

func TestPromptForwardedToOllama(t *testing.T) {
	type requestMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type requestBody struct {
		Messages []requestMessage `json:"messages"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body requestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		found := false
		for _, message := range body.Messages {
			if message.Role == "user" && message.Content == "Tell me a joke" {
				found = true
			}
		}
		if !found {
			t.Errorf("request messages do not contain the user prompt: %+v", body.Messages)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(chunk("A small joke.", true)))
	}))
	defer srv.Close()

	a := newTestAgent(t, srv, t.TempDir())
	if err := a.Turn(context.Background(), "Tell me a joke", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("line one\nline two"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := fakeOllama(t,
		toolChunk("read_file", map[string]any{"path": "hello.txt"})+chunk("", true),
		chunk("The file has two lines.", true),
	)
	defer srv.Close()

	a := newTestAgent(t, srv, dir)
	var out bytes.Buffer
	if err := a.Turn(context.Background(), "what's in hello.txt?", &out); err != nil {
		t.Fatal(err)
	}

	// user, assistant(tool_use), user(tool_result), assistant(text)
	if len(a.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(a.Messages))
	}
	res, ok := a.Messages[2].Blocks[0].(chat.ToolResult)
	if !ok {
		t.Fatalf("message 2 block 0 is %T, want ToolResult", a.Messages[2].Blocks[0])
	}
	if res.IsError {
		t.Fatalf("tool errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "line two") {
		t.Errorf("tool result missing file content: %q", res.Content)
	}
	// Every tool_use must have a result carrying the same ID.
	if res.ID != a.Messages[1].ToolUses()[0].ID {
		t.Error("tool result ID does not match tool use ID")
	}
}

func TestUnknownToolIsReportedNotFatal(t *testing.T) {
	srv := fakeOllama(t,
		toolChunk("delete_everything", map[string]any{})+chunk("", true),
		chunk("Sorry, I'll use the real tools.", true),
	)
	defer srv.Close()

	a := newTestAgent(t, srv, t.TempDir())
	if err := a.Turn(context.Background(), "go", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	res := a.Messages[2].Blocks[0].(chat.ToolResult)
	if !res.IsError || !strings.Contains(res.Content, "read_file") {
		t.Errorf("expected error result listing available tools, got %q", res.Content)
	}
}

func TestRootConfinement(t *testing.T) {
	dir := t.TempDir()
	srv := fakeOllama(t,
		toolChunk("read_file", map[string]any{"path": "../../../etc/passwd"})+chunk("", true),
		chunk("Blocked.", true),
	)
	defer srv.Close()

	a := newTestAgent(t, srv, dir)
	if err := a.Turn(context.Background(), "read passwd", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	res := a.Messages[2].Blocks[0].(chat.ToolResult)
	if !res.IsError || !strings.Contains(res.Content, "outside the workspace") {
		t.Errorf("escape was not blocked: %q", res.Content)
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	orig := chat.Message{
		Role:     chat.RoleAssistant,
		Provider: "ollama",
		Model:    "test-model",
		Blocks: []chat.Block{
			chat.Thinking{Text: "hmm"},
			chat.Text{Text: "here goes"},
			chat.ToolUse{ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back chat.Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Blocks) != 3 {
		t.Fatalf("got %d blocks", len(back.Blocks))
	}
	if _, ok := back.Blocks[2].(chat.ToolUse); !ok {
		t.Errorf("block 2 decoded as %T", back.Blocks[2])
	}
	if back.Model != "test-model" {
		t.Errorf("model lost in round trip")
	}
}

func TestCancellationSettlesToolUses(t *testing.T) {
	srv := fakeOllama(t, toolChunk("read_file", map[string]any{"path": "x"})+chunk("", true))
	defer srv.Close()

	a := newTestAgent(t, srv, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the turn even starts

	_ = a.Turn(ctx, "go", &bytes.Buffer{})

	// Whatever happened, no tool_use may be left without a matching result.
	uses, results := 0, 0
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			switch b.(type) {
			case chat.ToolUse:
				uses++
			case chat.ToolResult:
				results++
			}
		}
	}
	if uses != results {
		t.Errorf("%d tool uses but %d results: transcript is invalid", uses, results)
	}
}
