// Implements provider.Provider for Ollama.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mogar/harnisch/internal/chat"
	"github.com/mogar/harnisch/internal/provider"
)

type Client struct {
	Host  string // Ollama host, e.g. "http://localhost:11434"
	name  string
	model string
	HTTP  *http.Client
}

func New(host, model string) *Client {
	return &Client{
		Host:  strings.TrimRight(host, "/"),
		name:  "ollama",
		model: model,
		HTTP: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 100 * time.Second,
			},
		},
	}
}

func (c *Client) Name() string {
	return c.name
}

func (c *Client) Model() string {
	return c.model
}

func (c *Client) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Thinking:          true,
		ToolUse:           true,
		ParallelToolCalls: false,
		PromptCaching:     false,
		EditFormat:        "whole_file", // small models can struggle with exact-match replacement
		Images:            false,
	}
}

// wire types
// Use struct fields instead of maps to ensure JSON key order is deterministic.

type wireFunction struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
}

type wireToolCall struct {
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Thinking  string         `json:"thinking,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model    string         `json:"model"`
	Messages []wireMessage  `json:"messages"`
	Tools    []wireToolDef  `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type wireChunk struct {
	Message         wireMessage `json:"message"`
	Done            bool        `json:"done"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
}

// Translation

// Flatten canonical model into ollama's format.
// Two lossy conversions:
//   - one flat content string, so multiple text blocks are concatenated with newlines
//   - tool results are separate messages with the role "tool", identified by name instead of ID
func toWire(msgs []chat.Message) []wireMessage {
	var wireMsgs []wireMessage
	for _, msg := range msgs {
		var text, thinking strings.Builder
		var calls []wireToolCall
		var results []wireMessage

		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case chat.Text:
				text.WriteString(b.Text)
			case chat.Thinking:
				thinking.WriteString(b.Text)
			case chat.ToolUse:
				calls = append(calls, wireToolCall{
					Function: wireFunction{
						Name: b.Name,
						Args: b.Input,
					},
				})
			case chat.ToolResult:
				results = append(results, wireMessage{
					Role:     "tool",
					ToolName: b.Name,
					Content:  b.Content,
				})
			}
		}

		if text.Len() > 0 || thinking.Len() > 0 || len(calls) > 0 {
			wireMsgs = append(wireMsgs, wireMessage{
				Role:      string(msg.Role),
				Content:   text.String(),
				Thinking:  thinking.String(),
				ToolCalls: calls,
			})
		}
		wireMsgs = append(wireMsgs, results...)
	}
	return wireMsgs
}

func toolDefs(tools []provider.ToolSpec) []wireToolDef {
	var defs []wireToolDef
	for _, t := range tools {
		var d wireToolDef
		d.Type = "function"
		d.Function.Name = t.Name
		d.Function.Description = t.Description
		d.Function.Parameters = t.Schema
		defs = append(defs, d)
	}
	return defs
}

// Streaming

func (c *Client) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	msgs := toWire(req.Messages)
	if req.System != "" {
		msgs = append([]wireMessage{{
			Role:    "system",
			Content: req.System,
		}}, msgs...)
	}

	body, err := json.Marshal(wireRequest{
		Model:    c.model,
		Messages: msgs,
		Tools:    toolDefs(req.Tools),
		Stream:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// NewRequestWithContext is what wries cancellation into the transport:
	// cancelling ctx tears down the TCP connection mis-response.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) // limit to 4KB to avoid overwhelming the user with error text
		return nil, fmt.Errorf("request failed: %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	events := make(chan provider.Event)

	// Start a goroutine to read the response and send events.
	go func() {
		defer closeBoth(resp.Body.Close, events)

		dec := json.NewDecoder(resp.Body)
		var callSeq int

		for {
			var chunk wireChunk
			if err := dec.Decode(&chunk); err != nil {
				if err == io.EOF || ctx.Err() != nil {
					send(ctx, events, provider.Event{Kind: provider.EventDone})
					return
				}
				send(ctx, events, provider.Event{Kind: provider.EventError, Err: fmt.Errorf("failed to decode response: %w", err)})
				return
			}

			if t := chunk.Message.Thinking; t != "" {
				if !send(ctx, events, provider.Event{Kind: provider.EventThinkingDelta, Text: t}) {
					return
				}
			}
			if t := chunk.Message.Content; t != "" {
				if !send(ctx, events, provider.Event{Kind: provider.EventTextDelta, Text: t}) {
					return
				}
			}
			for _, call := range chunk.Message.ToolCalls {
				callSeq++
				toolUse := chat.ToolUse{
					ID:    fmt.Sprintf("call_%d_%d", callSeq, time.Now().UnixNano()),
					Name:  call.Function.Name,
					Input: call.Function.Args,
				}
				if !send(ctx, events, provider.Event{Kind: provider.EventToolCall, ToolUse: &toolUse}) {
					return
				}
			}

			if chunk.Done {
				send(ctx, events, provider.Event{Kind: provider.EventUsage, Usage: &provider.Usage{
					InputTokens:  chunk.PromptEvalCount,
					OutputTokens: chunk.EvalCount,
				}})
				send(ctx, events, provider.Event{Kind: provider.EventDone})
				return
			}
		}
	}()

	return events, nil
}

func send(ctx context.Context, ch chan<- provider.Event, ev provider.Event) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func closeBoth(closeBody func() error, ch chan provider.Event) {
	closeBody()
	close(ch)
}
