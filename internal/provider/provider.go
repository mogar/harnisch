// Define the interface every LLM backend must satisfy. This is the contract between the core and the provider.
package provider

import (
	"context"
	"encoding/json"

	"github.com/mogar/harnisch/internal/chat"
)

// Capabilities lets the loop know what the provider is capable of.
type Capabilities struct {
	Thinking          bool   // Whether the provider can return Thinking blocks.
	ToolUse           bool   // Whether the provider can return ToolUse blocks.
	ParallelToolCalls bool   // Whether the provider can handle multiple tool calls in parallel.
	PromptCaching     bool   // Whether the provider can cache prompts and results for repeated calls.
	EditFormat        string // The format the provider expects for edits. Empty if edits are not supported.
	Images            bool   // Whether the provider can generate images.
}

// Tool definition as presented to the model.
type ToolSpec struct {
	Name        string          // The name of the tool.
	Description string          // A description of the tool.
	Schema      json.RawMessage // JSON Schema for the tool's input.
}

// Request is one inference call. Caching markers and truncation are decided here.
type Request struct {
	System    string
	Messages  []chat.Message
	Tools     []ToolSpec
	MaxTokens int
}

type EventKind int

const (
	EventTextDelta EventKind = iota
	EventThinkingDelta
	EventToolCall // complete tool call
	EventUsage    // token accounting
	EventDone     // inference is complete
	EventError    // an error occurred
)

// Usage is broken out by category, since they are billed at different rates
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

type Event struct {
	Kind    EventKind
	Text    string
	ToolUse *chat.ToolUse
	Usage   *Usage
	Err     error
}

// Provider is the abstraction interface.
// Stream returns a receive only channel of events. The caller must drain it until the channel is closed, or the
// context is canceled. Cancelling ctx must terminate the underlying HTTP request and close the channel in order to
// support Ctrl-C cancellation of long-running requests.
type Provider interface {
	Name() string
	Model() string
	Capabilities() Capabilities
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}
