// Define the tool contract and the registry. Built-in tools and MCP tools will satisfy this contract.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/mogar/harnisch/internal/permissions"
)

// MaxResultBytes is the maximum number of bytes that a tool can return in its result.
// This is to prevent tools from returning excessively large results that could overwhelm the system (or bill).
const MaxResultBytes = 32 * 1024 // 32 KB

// Tool runs in two phases so permission checks see exactly what will execute. Prepare parses and
// validates the input and resolves any paths, without side effects; the returned Call reports what it
// will touch and runs against those same resolved values.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's input.
	Prepare(input json.RawMessage) (Call, error)
}

// Call is a prepared tool invocation.
type Call interface {
	Accesses() []permissions.Access
	Run(ctx context.Context) (string, error)
}

// NewCall builds a Call from a run function and the accesses it performs.
func NewCall(run func(ctx context.Context) (string, error), accesses ...permissions.Access) Call {
	return funcCall{run: run, accesses: accesses}
}

type funcCall struct {
	run      func(ctx context.Context) (string, error)
	accesses []permissions.Access
}

func (c funcCall) Accesses() []permissions.Access          { return c.accesses }
func (c funcCall) Run(ctx context.Context) (string, error) { return c.run(ctx) }

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

func (r *Registry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[tool.Name()]; exists {
		return fmt.Errorf("tool %q already registered", tool.Name())
	}
	r.tools[tool.Name()] = tool
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, exists := r.tools[name]
	return tool, exists
}

// All returns tools in name order. Unstable tool ordering can change serialization between requests,
// leading to cache misses and higher billing.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name() < tools[j].Name()
	})
	return tools
}

// Truncate clips the result to MaxResultBytes and indicates that it did so.
func Truncate(result string) string {
	if len(result) < MaxResultBytes {
		return result
	}
	return result[:MaxResultBytes] + fmt.Sprintf("\n\n[truncated %d bytes. Showing %d.]", len(result), MaxResultBytes)
}
