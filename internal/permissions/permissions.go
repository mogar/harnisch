// Package permissions decides whether a tool call may run. Tools describe what a call will touch as
// a list of Accesses; the engine evaluates those against the workspace and the user's grants.
package permissions

import "fmt"

// Op is the kind of operation an Access performs.
type Op int

const (
	OpRead    Op = iota // reads a file or directory
	OpWrite             // creates or modifies a file; implies read
	OpExec              // runs a command whose effects can't be described statically
	OpNetwork           // talks to the network
)

func (o Op) String() string {
	switch o {
	case OpRead:
		return "read"
	case OpWrite:
		return "write"
	case OpExec:
		return "exec"
	case OpNetwork:
		return "network"
	default:
		return fmt.Sprintf("op(%d)", int(o))
	}
}

// Access is one thing a tool call will touch.
// Path is absolute with symlinks resolved; it is empty for operations that don't act on a path.
type Access struct {
	Op   Op
	Path string
}

func (a Access) String() string {
	if a.Path == "" {
		return a.Op.String()
	}
	return a.Op.String() + " " + a.Path
}
