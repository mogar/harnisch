# Permissions

The permission engine decides whether a tool call may run, protecting the workspace from unauthorized operations.

## Operations

| Operation | Description |
|-----------|-------------|
| `OpRead`  | Reads a file or directory |
| `OpWrite` | Creates or modifies a file; implies read |
| `OpExec`  | Runs a command (effects not statically describable) |
| `OpNetwork` | Talks to the network |

## Policy Rules

Every tool call describes its accesses as a list of `Access{Op, Path}` pairs. The engine evaluates each access against these rules:

1. **Read inside workspace** → auto-allowed
2. **Write inside workspace** → allowed with session grant (once or session-wide approval required)
3. **Anything else** → approval required (paths outside workspace, missing/relative paths, exec, network, unknown ops)
4. **No accesses specified** → approval required (engine can't evaluate the tool)

A call only succeeds if **all** its accesses are allowed.

## Session Grants

When a user approves a tool's write operation inside the workspace, the engine can remember this:

- `AllowOnce` → approval valid for this call only
- `AllowSession` → store a grant in the session map; future calls from the same tool writing to the workspace are auto-approved

Grants are stored as `(tool, operation)` pairs in a thread-safe map.

## User Prompts

The engine presents a structured prompt showing:
- Tool name
- List of accesses with their operation type and path
- Flag for accesses outside the workspace
- Available choices

### Interaction

- **y / yes** → allow once
- **a / always / session** → allow for session (if supported)
- **n / no** → deny (with optional reason for the model)

Non-terminal mode (no interactive stdin) denies all approvals automatically.

## Architecture

- `Engine` → main authorization logic with session state
- `Prompter` → interface for asking users (e.g., `TerminalPrompter`)
- `Workspace` → interface for checking if a path is inside the workspace
- `Verdict` → outcome with `Allowed` flag and explanatory message
