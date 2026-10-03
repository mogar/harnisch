package permissions

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Policy, evaluated per access; a call runs only if every access is allowed:
//
//	read inside the workspace   → allow
//	write inside the workspace  → allow with a session grant for the tool, otherwise ask (once or session)
//	anything else               → ask (once only); this covers paths outside the workspace, missing or
//	                              relative paths, exec, network, and unknown ops
//
// A call that reports no accesses is also asked about, since the engine can't tell what it does.

// Workspace reports whether an absolute, symlink-resolved path lies inside the workspace.
type Workspace interface {
	Contains(path string) bool
}

// Choice is the user's answer to a permission prompt. The zero value denies.
type Choice int

const (
	Deny Choice = iota
	AllowOnce
	AllowSession
)

func (c Choice) String() string {
	switch c {
	case Deny:
		return "deny"
	case AllowOnce:
		return "allow once"
	case AllowSession:
		return "allow for session"
	default:
		return fmt.Sprintf("choice(%d)", int(c))
	}
}

// Prompt asks the user to approve the accesses of one tool call that policy didn't already allow.
// Options lists the choices the user may pick from, in display order.
type Prompt struct {
	Tool     string
	Accesses []Access
	Options  []Choice
}

// Answer is the user's response. Reason is optional feedback passed back to the model on denial.
type Answer struct {
	Choice Choice
	Reason string
}

// Prompter asks the user to approve a tool call. It should return promptly with an error once ctx is
// cancelled.
type Prompter interface {
	Ask(ctx context.Context, p Prompt) (Answer, error)
}

// Verdict is the outcome of an authorization. When Allowed is false, Message explains why, worded for
// the model.
type Verdict struct {
	Allowed bool
	Message string
}

// grant is a session-wide approval of one tool writing inside the workspace.
type grant struct {
	tool string
	op   Op
}

// Engine authorizes tool calls against the policy above and remembers session grants.
// It is safe for concurrent use.
type Engine struct {
	workspace Workspace
	prompter  Prompter // nil denies anything that needs approval

	mu     sync.Mutex
	grants map[grant]struct{}
}

func NewEngine(ws Workspace, p Prompter) *Engine {
	return &Engine{
		workspace: ws,
		prompter:  p,
		grants:    make(map[grant]struct{}),
	}
}

// Authorize decides whether a call of the named tool with the given accesses may run, prompting the
// user when policy requires it. A non-nil error means the prompt itself failed (e.g. ctx was
// cancelled); the verdict is then always a denial.
func (e *Engine) Authorize(ctx context.Context, tool string, accesses []Access) (Verdict, error) {
	pending, sessionable := e.pending(tool, accesses)
	if len(accesses) > 0 && len(pending) == 0 {
		return Verdict{Allowed: true}, nil
	}
	if len(accesses) == 0 {
		sessionable = false
	}

	if e.prompter == nil {
		return denied(tool, pending, "no way to ask the user for approval"), nil
	}

	options := []Choice{AllowOnce}
	if sessionable {
		options = append(options, AllowSession)
	}
	options = append(options, Deny)

	answer, err := e.prompter.Ask(ctx, Prompt{Tool: tool, Accesses: pending, Options: options})
	if err != nil {
		return denied(tool, pending, "the approval prompt failed"), err
	}

	switch answer.Choice {
	case AllowSession:
		// The prompter may only pick what was offered; treat an unoffered session grant as once.
		if sessionable {
			e.mu.Lock()
			for _, a := range pending {
				e.grants[grant{tool: tool, op: a.Op}] = struct{}{}
			}
			e.mu.Unlock()
		}
		return Verdict{Allowed: true}, nil
	case AllowOnce:
		return Verdict{Allowed: true}, nil
	default:
		return denied(tool, pending, answer.Reason), nil
	}
}

// pending returns the accesses that need the user's approval, and whether all of them are eligible
// for a session grant.
func (e *Engine) pending(tool string, accesses []Access) (pending []Access, sessionable bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	sessionable = true
	for _, a := range accesses {
		inside := a.Path != "" && e.workspace.Contains(a.Path)
		switch {
		case a.Op == OpRead && inside:
			continue
		case a.Op == OpWrite && inside:
			if _, ok := e.grants[grant{tool: tool, op: OpWrite}]; ok {
				continue
			}
		default:
			sessionable = false
		}
		pending = append(pending, a)
	}
	return pending, sessionable
}

func denied(tool string, accesses []Access, reason string) Verdict {
	var b strings.Builder
	fmt.Fprintf(&b, "permission denied for %s", tool)
	if len(accesses) > 0 {
		parts := make([]string, len(accesses))
		for i, a := range accesses {
			parts[i] = a.String()
		}
		fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	}
	if reason != "" {
		fmt.Fprintf(&b, ": %s", reason)
	}
	b.WriteString(". Do not retry the same call; adjust your approach or ask the user.")
	return Verdict{Message: b.String()}
}
