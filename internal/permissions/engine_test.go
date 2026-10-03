package permissions

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const ws = "/ws"

// dirWorkspace treats dir and everything beneath it as the workspace.
type dirWorkspace string

func (d dirWorkspace) Contains(path string) bool {
	rel, err := filepath.Rel(string(d), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// scriptedPrompter returns queued answers in order and records every prompt it receives.
type scriptedPrompter struct {
	t       *testing.T
	answers []Answer
	err     error
	prompts []Prompt
}

func (p *scriptedPrompter) Ask(ctx context.Context, pr Prompt) (Answer, error) {
	p.prompts = append(p.prompts, pr)
	if p.err != nil {
		return Answer{}, p.err
	}
	if len(p.answers) == 0 {
		p.t.Fatalf("unexpected prompt: %+v", pr)
	}
	a := p.answers[0]
	p.answers = p.answers[1:]
	return a, nil
}

func read(path string) Access  { return Access{Op: OpRead, Path: path} }
func write(path string) Access { return Access{Op: OpWrite, Path: path} }

var (
	onceOrDeny    = []Choice{AllowOnce, Deny}
	onceSessDeny  = []Choice{AllowOnce, AllowSession, Deny}
	allowOnce     = Answer{Choice: AllowOnce}
	allowSession  = Answer{Choice: AllowSession}
	deny          = Answer{Choice: Deny}
	insideFile    = ws + "/a.go"
	outsideFile   = "/etc/hosts"
	siblingPrefix = ws + "2/a.go"
)

func TestAuthorizePolicy(t *testing.T) {
	tests := []struct {
		name        string
		accesses    []Access
		answer      *Answer  // nil means no prompt is expected
		wantPrompt  []Access // accesses shown in the prompt
		wantOptions []Choice
		wantAllowed bool
	}{
		{
			name:        "read inside is auto-allowed",
			accesses:    []Access{read(insideFile)},
			wantAllowed: true,
		},
		{
			name:        "read of workspace root is auto-allowed",
			accesses:    []Access{read(ws)},
			wantAllowed: true,
		},
		{
			name:        "write inside offers session",
			accesses:    []Access{write(insideFile)},
			answer:      &allowOnce,
			wantPrompt:  []Access{write(insideFile)},
			wantOptions: onceSessDeny,
			wantAllowed: true,
		},
		{
			name:        "write inside denied",
			accesses:    []Access{write(insideFile)},
			answer:      &deny,
			wantPrompt:  []Access{write(insideFile)},
			wantOptions: onceSessDeny,
		},
		{
			name:        "read outside is once only",
			accesses:    []Access{read(outsideFile)},
			answer:      &allowOnce,
			wantPrompt:  []Access{read(outsideFile)},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "write outside is once only",
			accesses:    []Access{write(outsideFile)},
			answer:      &allowOnce,
			wantPrompt:  []Access{write(outsideFile)},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "sibling sharing the workspace prefix is outside",
			accesses:    []Access{read(siblingPrefix)},
			answer:      &allowOnce,
			wantPrompt:  []Access{read(siblingPrefix)},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "relative path is not trusted as inside",
			accesses:    []Access{read("a.go")},
			answer:      &deny,
			wantPrompt:  []Access{read("a.go")},
			wantOptions: onceOrDeny,
		},
		{
			name:        "missing path is not trusted as inside",
			accesses:    []Access{read("")},
			answer:      &deny,
			wantPrompt:  []Access{read("")},
			wantOptions: onceOrDeny,
		},
		{
			name:        "exec is once only",
			accesses:    []Access{{Op: OpExec}},
			answer:      &allowOnce,
			wantPrompt:  []Access{{Op: OpExec}},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "network is once only",
			accesses:    []Access{{Op: OpNetwork, Path: insideFile}},
			answer:      &allowOnce,
			wantPrompt:  []Access{{Op: OpNetwork, Path: insideFile}},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "unknown op is once only",
			accesses:    []Access{{Op: Op(99), Path: insideFile}},
			answer:      &allowOnce,
			wantPrompt:  []Access{{Op: Op(99), Path: insideFile}},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "no accesses is asked once only",
			accesses:    nil,
			answer:      &allowOnce,
			wantPrompt:  nil,
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "only unapproved accesses are shown",
			accesses:    []Access{read(insideFile), read(outsideFile)},
			answer:      &allowOnce,
			wantPrompt:  []Access{read(outsideFile)},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "any outside access removes the session option",
			accesses:    []Access{write(insideFile), read(outsideFile)},
			answer:      &allowOnce,
			wantPrompt:  []Access{write(insideFile), read(outsideFile)},
			wantOptions: onceOrDeny,
			wantAllowed: true,
		},
		{
			name:        "unknown choice denies",
			accesses:    []Access{write(insideFile)},
			answer:      &Answer{Choice: Choice(42)},
			wantPrompt:  []Access{write(insideFile)},
			wantOptions: onceSessDeny,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedPrompter{t: t}
			if tt.answer != nil {
				p.answers = []Answer{*tt.answer}
			}
			e := NewEngine(dirWorkspace(ws), p)

			v, err := e.Authorize(context.Background(), "tool", tt.accesses)
			if err != nil {
				t.Fatalf("Authorize error: %v", err)
			}
			if v.Allowed != tt.wantAllowed {
				t.Errorf("Allowed = %v, want %v (message %q)", v.Allowed, tt.wantAllowed, v.Message)
			}
			if !v.Allowed && v.Message == "" {
				t.Error("denial has no message for the model")
			}
			if tt.answer == nil {
				return // the scripted prompter fails the test on any prompt
			}
			if len(p.prompts) != 1 {
				t.Fatalf("got %d prompts, want 1", len(p.prompts))
			}
			got := p.prompts[0]
			if got.Tool != "tool" {
				t.Errorf("prompt tool = %q, want %q", got.Tool, "tool")
			}
			if !reflect.DeepEqual(got.Accesses, tt.wantPrompt) {
				t.Errorf("prompt accesses = %v, want %v", got.Accesses, tt.wantPrompt)
			}
			if !reflect.DeepEqual(got.Options, tt.wantOptions) {
				t.Errorf("prompt options = %v, want %v", got.Options, tt.wantOptions)
			}
		})
	}
}

// step is one Authorize call in a sequence; answer is nil when no prompt is expected.
type step struct {
	tool     string
	accesses []Access
	answer   *Answer
	allowed  bool
}

func runSteps(t *testing.T, steps []step) {
	t.Helper()
	p := &scriptedPrompter{t: t}
	e := NewEngine(dirWorkspace(ws), p)
	for i, s := range steps {
		before := len(p.prompts)
		if s.answer != nil {
			p.answers = []Answer{*s.answer}
		}
		v, err := e.Authorize(context.Background(), s.tool, s.accesses)
		if err != nil {
			t.Fatalf("step %d: Authorize error: %v", i, err)
		}
		if v.Allowed != s.allowed {
			t.Errorf("step %d: Allowed = %v, want %v", i, v.Allowed, s.allowed)
		}
		prompted := len(p.prompts) > before
		if prompted != (s.answer != nil) {
			t.Errorf("step %d: prompted = %v, want %v", i, prompted, s.answer != nil)
		}
	}
}

func TestSessionGrants(t *testing.T) {
	other := ws + "/b.go"
	t.Run("once does not persist", func(t *testing.T) {
		runSteps(t, []step{
			{"edit", []Access{write(insideFile)}, &allowOnce, true},
			{"edit", []Access{write(insideFile)}, &allowOnce, true},
		})
	})
	t.Run("session covers later writes inside by the same tool", func(t *testing.T) {
		runSteps(t, []step{
			{"edit", []Access{write(insideFile)}, &allowSession, true},
			{"edit", []Access{write(insideFile)}, nil, true},
			{"edit", []Access{write(other)}, nil, true},
			{"edit", []Access{read(insideFile), write(other)}, nil, true},
		})
	})
	t.Run("session is per tool", func(t *testing.T) {
		runSteps(t, []step{
			{"edit", []Access{write(insideFile)}, &allowSession, true},
			{"create", []Access{write(insideFile)}, &deny, false},
		})
	})
	t.Run("session does not cover outside writes", func(t *testing.T) {
		runSteps(t, []step{
			{"edit", []Access{write(insideFile)}, &allowSession, true},
			{"edit", []Access{write(outsideFile)}, &allowOnce, true},
			{"edit", []Access{write(outsideFile)}, &deny, false},
		})
	})
	t.Run("session grant still asks about the outside part of a mixed call", func(t *testing.T) {
		runSteps(t, []step{
			{"edit", []Access{write(insideFile)}, &allowSession, true},
			{"edit", []Access{write(insideFile), write(outsideFile)}, &deny, false},
		})
	})
	t.Run("unoffered session answer counts as once", func(t *testing.T) {
		runSteps(t, []step{
			{"edit", []Access{write(outsideFile)}, &allowSession, true},
			{"edit", []Access{write(outsideFile)}, &deny, false},
			{"edit", []Access{write(insideFile)}, &deny, false},
		})
	})
	t.Run("deny records nothing", func(t *testing.T) {
		runSteps(t, []step{
			{"edit", []Access{write(insideFile)}, &deny, false},
			{"edit", []Access{write(insideFile)}, &allowOnce, true},
		})
	})
}

func TestDenialMessage(t *testing.T) {
	p := &scriptedPrompter{t: t, answers: []Answer{{Choice: Deny, Reason: "edit b.go instead"}}}
	e := NewEngine(dirWorkspace(ws), p)

	v, err := e.Authorize(context.Background(), "edit", []Access{read(insideFile), write(insideFile)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"edit", "write " + insideFile, "edit b.go instead", "Do not retry"} {
		if !strings.Contains(v.Message, want) {
			t.Errorf("message %q missing %q", v.Message, want)
		}
	}
	if strings.Contains(v.Message, "read "+insideFile) {
		t.Errorf("message %q lists an access that was already allowed", v.Message)
	}
}

func TestPrompterError(t *testing.T) {
	p := &scriptedPrompter{t: t, err: context.Canceled}
	e := NewEngine(dirWorkspace(ws), p)

	v, err := e.Authorize(context.Background(), "edit", []Access{write(insideFile)})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if v.Allowed {
		t.Error("allowed despite prompter error")
	}
}

func TestNilPrompter(t *testing.T) {
	e := NewEngine(dirWorkspace(ws), nil)

	if v, _ := e.Authorize(context.Background(), "read", []Access{read(insideFile)}); !v.Allowed {
		t.Errorf("read inside denied without a prompter: %q", v.Message)
	}
	for _, acc := range [][]Access{{write(insideFile)}, {read(outsideFile)}, nil} {
		v, err := e.Authorize(context.Background(), "tool", acc)
		if err != nil {
			t.Fatalf("Authorize(%v) error: %v", acc, err)
		}
		if v.Allowed {
			t.Errorf("Authorize(%v) allowed without a prompter", acc)
		}
	}
}
