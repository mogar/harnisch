// Implements the core loop for the agent: request, response, tool calls.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mogar/harnisch/internal/chat"
	"github.com/mogar/harnisch/internal/provider"
	"github.com/mogar/harnisch/internal/tool"
)

type Agent struct {
	Provider provider.Provider
	Tools *tool.Registry
	System string
	MaxTurns int

	Messages []chat.Message
	Usage provider.Usage // accumulated across the session
}

// Turn sends one user message and runs the loop to completion
func (a *Agent) Turn(ctx context.Context, userText string, out io.Writer) error {
	a.Messages = append(a.Messages, chat.Message{
		Role: chat.RoleUser,
		Blocks: []chat.Block{chat.Text{Text: userText}},
	})

	maxTurns := a.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}

	for turn := 0; turn < maxTurns; turn++ {
		assistant, err := a.stream(ctx, out)
		if len(assistant.Blocks) > 0 {
			a.Messages = append(a.Messages, assistant)
		}
		if err != nil {
			a.settleOrphans(assistant, "interrupted by user")
			return err
		}

		calls := assistant.ToolUses()
		if len(calls) == 0 {
			return nil // no more tool calls, we're done
		}

		results := make([]chat.Block, 0, len(calls))
		for _, call := range calls {
			// check cancellation
			if ctx.Err() != nil {
				results = append(results, errorResult(call, "cancelled before execution"))
				continue
			}
			results = append(results, a.execute(ctx, call, out))
		}

		a.Messages = append(a.Messages, chat.Message{
			Role: chat.RoleUser,
			Blocks: results,
		})

		if ctx.Err() != nil {
			return ctx.Err() // cancelled during tool execution
		}
	}
	return fmt.Errorf("max turns exceeded (%d)", maxTurns)
}

func (a *Agent) stream(ctx context.Context, out io.Writer) (chat.Message, error) {
	msg := chat.Message{
		Role: chat.RoleAssistant,
		Provider: a.Provider.Name(),
		Model: a.Provider.Model(),
	}

	var specs []provider.ToolSpec
	for _, t := range a.Tools.All() {
		specs = append(specs, provider.ToolSpec{
			Name: t.Name(),
			Description: t.Description(),
			Schema: t.Schema(),
		})
	}

	events, err := a.Provider.Stream(ctx, provider.Request{
		System: a.System,
		Messages: a.Messages,
		Tools: specs,
	})
	if err != nil {
		return msg, fmt.Errorf("failed to start stream: %w", err)
	}

	var text, thinking strings.Builder

	// range over channel receives until closed
	for ev := range events {
		switch ev.Kind {
		case provider.EventTextDelta:
			text.WriteString(ev.Text)
			fmt.Fprint(out, ev.Text) // stream to user on arrival
		case provider.EventThinkingDelta:
			thinking.WriteString(ev.Text)
		case provider.EventToolCall:
			msg.Blocks = append(msg.Blocks, *ev.ToolUse)
		case provider.EventUsage:
			a.Usage.InputTokens += ev.Usage.InputTokens
			a.Usage.OutputTokens += ev.Usage.OutputTokens
			a.Usage.CacheReadTokens += ev.Usage.CacheReadTokens
			a.Usage.CacheWriteTokens += ev.Usage.CacheWriteTokens
		case provider.EventError:
			err = ev.Err
		}
	}

	// Prepend accumulated text in block order
	var lead []chat.Block
	if thinking.Len() > 0 {
		lead = append(lead, chat.Thinking{Text: thinking.String()})
	}
	if text.Len() > 0 {
		lead = append(lead, chat.Text{Text: text.String()})
	}

	msg.Blocks = append(lead, msg.Blocks...)

	if err == nil {
		err = ctx.Err() // surface early cancellation
	}
	return msg, err
}

func (a *Agent) execute(ctx context.Context, call chat.ToolUse, out io.Writer) chat.Block {
	fmt.Fprintf(out, "\n[tool call: %s %s]\n", call.Name, compact(call.Input))

	t, ok := a.Tools.Get(call.Name)
	if !ok {
		return errorResult(call, fmt.Sprintf("tool %s not found in %s", call.Name, a.toolNames()))
	}

	result, err := t.Execute(ctx, call.Input)
	if err != nil {
		return errorResult(call, fmt.Sprintf("tool %s execution failed: %v", call.Name, err))
	}

	return chat.ToolResult{
		ID: call.ID,
		Name: call.Name,
		Content: tool.Truncate(result),
	}
}


// settleOrphans guarantees every tool call has a tool result. This is necessary for some models.
func (a *Agent) settleOrphans(assistant chat.Message, reason string) {
	calls := assistant.ToolUses()
	if len(calls) == 0 {
		return
	}
	results := make([]chat.Block, 0, len(calls))
	for _, c := range calls {
		results = append(results, errorResult(c, reason))
	}
	a.Messages = append(a.Messages, chat.Message{Role: chat.RoleUser, Blocks: results})
}

func errorResult(call chat.ToolUse, msg string) chat.ToolResult {
	return chat.ToolResult {
		ID: call.ID,
		Name: call.Name,
		Content: "Error: " + msg,
		IsError: true,
	}
}

func (a *Agent) toolNames() string {
	var names []string
	for _, t := range a.Tools.All() {
		names = append(names, t.Name())
	}
	return strings.Join(names, ", ")
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	s := buf.String()
	// TODO: better length handling here?
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s

}