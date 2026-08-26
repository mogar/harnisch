// Define the canonical conversation model.
//
// This package defines an ontology for LLM chats. It includes types for the various phases of a chat,
// including text, thinking, tool-use, etc. It is intended to be a superset of all possible chat formats, 
// and to be compatible with OpenAI/Anthropic/ollama chat formats. Individual provider libraries may 
// downgrade to what they are capable of.
package chat

import (
	"encoding/json"
	"fmt"
)

type Role string

const (
	RoleUser	  Role = "user"
	RoleAssistant Role = "assistant"
)

type BlockKind string

const (
	KindText	   BlockKind = "text"
	KindThinking   BlockKind = "thinking"
	KindToolUse	   BlockKind = "tool_use"
	KindToolResult BlockKind = "tool_result"
)

// A Block is one unit of a conversation.
type Block interface {
	Kind() BlockKind
}

// Text is a Block of ordinary conversational text.
type Text struct {
	Text string `json:"text"`
}

func (t Text) Kind() BlockKind {
	return KindText
}

// Thinking is a Block of text that represents the model's internal reasoning.
// Raw holds the provider's exact serialization (necessary for Anthropic).
type Thinking struct {
	Text string `json:"text"`
	Raw json.RawMessage `json:"raw,omitempty"`
}

func (t Thinking) Kind() BlockKind {
	return KindThinking
}

// ToolUse is a Block that represents the model's request to use a tool. 
// ID is the tool's unique identifier (possibly generated internally if unused by the provider).
// Name is the human-readable name of the tool.
// Input is the input to the tool.
type ToolUse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (t ToolUse) Kind() BlockKind {
	return KindToolUse
}

// ToolResult is a Block that represents our response resulting from using a tool.
// ID is the tool's unique identifier (possibly generated internally if unused by the provider).
// Name is the human-readable name of the tool.
// Content is the content of the result from the tool.
// IsError indicates whether the tool result is an error.
type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

func (t ToolResult) Kind() BlockKind {
	return KindToolResult
}

// Message is a single message in a conversation, consisting of a role and a block.
// Provider and Model record where the conversation came from (useful for mid-conversation 
// model switching).
type Message struct {
	Role     Role
	Blocks   []Block
	Provider string
	Model    string
}

// ToolUses returns every tool call in the message, in order. This keeps parallel tool calls 
// representable.
func (m Message) ToolUses() []ToolUse {
	var toolUses []ToolUse
	for _, block := range m.Blocks {
		if toolUse, ok := block.(ToolUse); ok {
			toolUses = append(toolUses, toolUse)
		}
	}
	return toolUses
}

// TextContent concatenates all text blocks in the message, in order. This is useful for 
// providers that don't support thinking or tool use.
func (m Message) TextContent() string {
	var text string
	for _, block := range m.Blocks {
		if textBlock, ok := block.(Text); ok {
			text += textBlock.Text
		}
	}
	return text
}

// JSON Persistence
//
// encoding/json can't unmarhsal into an interface, so we have to do some manual work to persist the Block interface.
// The wire form tags each block with its kind, then stores the value under "data".

type wireBlock struct {
	Kind BlockKind       `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type wireMessage struct {
	Role     Role         `json:"role"`
	Blocks   []wireBlock  `json:"blocks"`
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	wm := wireMessage{
		Role:     m.Role,
		Provider: m.Provider,
		Model:    m.Model,
	}
	for _, block := range m.Blocks {
		data, err := json.Marshal(block)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal block %s: %w", block.Kind(), err)
		}
		wm.Blocks = append(wm.Blocks, wireBlock{
			Kind: block.Kind(),
			Data: data,
		})
	}
	return json.Marshal(wm)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var wm wireMessage
	if err := json.Unmarshal(data, &wm); err != nil {
		return fmt.Errorf("failed to unmarshal wire message: %w", err)
	}
	m.Role = wm.Role
	m.Provider = wm.Provider
	m.Model = wm.Model
	for _, wb := range wm.Blocks {
		var block Block
		switch wb.Kind {
		case KindText:
			var text Text
			if err := json.Unmarshal(wb.Data, &text); err != nil {
				return fmt.Errorf("failed to unmarshal text block: %w", err)
			}
			block = text
		case KindThinking:
			var thinking Thinking
			if err := json.Unmarshal(wb.Data, &thinking); err != nil {
				return fmt.Errorf("failed to unmarshal thinking block: %w", err)
			}
			block = thinking
		case KindToolUse:
			var toolUse ToolUse
			if err := json.Unmarshal(wb.Data, &toolUse); err != nil {
				return fmt.Errorf("failed to unmarshal tool use block: %w", err)
			}
			block = toolUse
		case KindToolResult:
			var toolResult ToolResult
			if err := json.Unmarshal(wb.Data, &toolResult); err != nil {
				return fmt.Errorf("failed to unmarshal tool result block: %w", err)
			}
			block = toolResult
		default:
			return fmt.Errorf("unknown block kind: %s", wb.Kind)
		}
		m.Blocks = append(m.Blocks, block)
	}
	return nil
}