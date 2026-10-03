package permissions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/mogar/harnisch/internal/lineedit"
)

// ErrInterrupted is returned by TerminalPrompter when the user presses Ctrl-C at the prompt. It wraps
// context.Canceled so callers treat it like any other cancelled turn.
var ErrInterrupted = fmt.Errorf("permission prompt interrupted: %w", context.Canceled)

// LineReader reads one line of user input under a one-off prompt. *lineedit.Reader satisfies it;
// Ctrl-C must be reported as lineedit.ErrInterrupt.
type LineReader interface {
	ReadLineWithPrompt(prompt string) (string, error)
}

// TerminalPrompter asks for approval on an interactive terminal. Prompts are serialized, so concurrent
// tool calls never interleave on screen.
type TerminalPrompter struct {
	in  LineReader
	out io.Writer
	ws  Workspace // optional; flags accesses outside the workspace

	mu sync.Mutex
}

func NewTerminalPrompter(in LineReader, out io.Writer, ws Workspace) *TerminalPrompter {
	return &TerminalPrompter{in: in, out: out, ws: ws}
}

// key is what the user types to pick each choice.
var key = map[Choice]string{
	AllowOnce:    "y",
	AllowSession: "a",
	Deny:         "n",
}

// Ask shows the request and reads answers until one is valid. Ctrl-C returns ErrInterrupted;
// Ctrl-D denies.
func (p *TerminalPrompter) Ask(ctx context.Context, pr Prompt) (Answer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// The line reader can't be interrupted by ctx, so don't start a prompt for a cancelled turn.
	if err := ctx.Err(); err != nil {
		return Answer{}, err
	}

	p.render(pr)
	for {
		line, err := p.in.ReadLineWithPrompt("allow? ")
		switch {
		case errors.Is(err, lineedit.ErrInterrupt):
			return Answer{}, ErrInterrupted
		case errors.Is(err, io.EOF):
			fmt.Fprintln(p.out)
			return Answer{Choice: Deny}, nil
		case err != nil:
			return Answer{}, err
		}
		if a, ok := parseAnswer(line, pr.Options); ok {
			return a, nil
		}
		fmt.Fprintf(p.out, "Please answer %s.\n", optionKeys(pr.Options))
	}
}

func (p *TerminalPrompter) render(pr Prompt) {
	fmt.Fprintf(p.out, "Allow %s to:\n", pr.Tool)
	if len(pr.Accesses) == 0 {
		fmt.Fprintln(p.out, "  (the tool did not say what it will access)")
	}
	for _, a := range pr.Accesses {
		line := fmt.Sprintf("  %-7s %s", a.Op, a.Path)
		if a.Path != "" && p.ws != nil && !p.ws.Contains(a.Path) {
			line += "  (outside workspace)"
		}
		fmt.Fprintln(p.out, strings.TrimRight(line, " "))
	}

	labels := make([]string, 0, len(pr.Options))
	for _, c := range pr.Options {
		label := c.String()
		if c == Deny {
			label += " (add a reason after n to tell the model why)"
		}
		labels = append(labels, key[c]+" = "+label)
	}
	fmt.Fprintln(p.out, "  "+strings.Join(labels, ", "))
}

// parseAnswer reads "y", "a", or "n [reason]" (and long forms), accepting only offered choices.
func parseAnswer(line string, options []Choice) (Answer, bool) {
	word, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	var choice Choice
	switch strings.ToLower(word) {
	case "y", "yes":
		choice = AllowOnce
	case "a", "always", "session":
		choice = AllowSession
	case "n", "no":
		choice = Deny
	default:
		return Answer{}, false
	}
	for _, c := range options {
		if c == choice {
			a := Answer{Choice: choice}
			if choice == Deny {
				a.Reason = strings.TrimSpace(rest)
			}
			return a, true
		}
	}
	return Answer{}, false
}

func optionKeys(options []Choice) string {
	keys := make([]string, len(options))
	for i, c := range options {
		keys[i] = key[c]
	}
	return strings.Join(keys, ", ")
}
