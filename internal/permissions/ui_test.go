package permissions

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mogar/harnisch/internal/lineedit"
)

// line is one scripted ReadLineWithPrompt result.
type line struct {
	text string
	err  error
}

type scriptedReader struct {
	t     *testing.T
	lines []line
	reads int
}

func (r *scriptedReader) ReadLineWithPrompt(prompt string) (string, error) {
	r.reads++
	if len(r.lines) == 0 {
		r.t.Fatal("unexpected read")
	}
	l := r.lines[0]
	r.lines = r.lines[1:]
	return l.text, l.err
}

func ask(t *testing.T, pr Prompt, lines ...line) (Answer, error, string, *scriptedReader) {
	t.Helper()
	var out strings.Builder
	r := &scriptedReader{t: t, lines: lines}
	p := NewTerminalPrompter(r, &out, dirWorkspace(ws))
	a, err := p.Ask(context.Background(), pr)
	return a, err, out.String(), r
}

var writePrompt = Prompt{Tool: "create_file", Accesses: []Access{write(insideFile)}, Options: onceSessDeny}

func TestTerminalPrompterAnswers(t *testing.T) {
	tests := []struct {
		input string
		want  Answer
	}{
		{"y", Answer{Choice: AllowOnce}},
		{"  YES  ", Answer{Choice: AllowOnce}},
		{"a", Answer{Choice: AllowSession}},
		{"n", Answer{Choice: Deny}},
		{"n   put it in docs/ instead ", Answer{Choice: Deny, Reason: "put it in docs/ instead"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err, _, _ := ask(t, writePrompt, line{text: tt.input})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("answer = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTerminalPrompterRetriesInvalidInput(t *testing.T) {
	// "a" isn't offered for an outside access, and blank or unknown input must not pick a default.
	pr := Prompt{Tool: "read_file", Accesses: []Access{read(outsideFile)}, Options: onceOrDeny}
	got, err, out, r := ask(t, pr, line{text: ""}, line{text: "maybe"}, line{text: "a"}, line{text: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Choice != AllowOnce || r.reads != 4 {
		t.Errorf("answer = %+v after %d reads, want allow once after 4", got, r.reads)
	}
	if n := strings.Count(out, "Please answer y, n."); n != 3 {
		t.Errorf("got %d retry hints, want 3:\n%s", n, out)
	}
}

func TestTerminalPrompterRendering(t *testing.T) {
	pr := Prompt{Tool: "grep", Accesses: []Access{read(insideFile), read(outsideFile), {Op: OpExec}}, Options: onceOrDeny}
	_, _, out, _ := ask(t, pr, line{text: "n"})

	for _, want := range []string{
		"Allow grep to:",
		"read    " + insideFile + "\n",
		"read    " + outsideFile + "  (outside workspace)",
		"exec\n",
		"y = allow once, n = deny",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "a = ") {
		t.Errorf("session option shown when not offered:\n%s", out)
	}

	_, _, out, _ = ask(t, writePrompt, line{text: "n"})
	if !strings.Contains(out, "y = allow once, a = allow for session, n = deny") {
		t.Errorf("session option missing:\n%s", out)
	}
}

func TestTerminalPrompterInterrupt(t *testing.T) {
	_, err, _, _ := ask(t, writePrompt, line{err: lineedit.ErrInterrupt})
	if !errors.Is(err, ErrInterrupted) || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want ErrInterrupted wrapping context.Canceled", err)
	}
}

func TestTerminalPrompterEOFDenies(t *testing.T) {
	got, err, _, _ := ask(t, writePrompt, line{err: io.EOF})
	if err != nil || got.Choice != Deny {
		t.Errorf("got (%+v, %v), want deny", got, err)
	}
}

func TestTerminalPrompterCancelledContext(t *testing.T) {
	r := &scriptedReader{t: t}
	p := NewTerminalPrompter(r, io.Discard, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Ask(ctx, writePrompt); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if r.reads != 0 {
		t.Error("prompted despite a cancelled context")
	}
}

func TestLineeditReaderIsLineReader(t *testing.T) {
	var _ LineReader = (*lineedit.Reader)(nil)
}
