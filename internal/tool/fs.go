package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Root defines the intended root directory for tool call. It is used to resolve relative paths in tool definitions.
// Note that it does not serve a security purpose, and LLMs can still request arbitrary paths.
type Root struct {
	dir string
}

func NewRoot(dir string) (*Root, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path of root directory: %w", err)
	}
	// Support symlinks in the root directory by resolving them to their real paths.
	realDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve symlinks in root directory: %w", err)
	}
	return &Root{dir: realDir}, nil
}

func (r *Root) Dir() string {
	return r.dir
}

func (r *Root) ResolvePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	joined := path
	if !filepath.IsAbs(path) {
		joined = filepath.Join(r.dir, path)
	}
	cleaned := filepath.Clean(joined)

	// Support symlinks in the resolved path by resolving them to their real paths.
	realPath, err := filepath.EvalSymlinks(cleaned)
	if err == nil {
		cleaned = realPath
	}

	// Ensure the resolved path is within the root directory.
	if !strings.HasPrefix(cleaned, r.dir) {
		return "", fmt.Errorf("resolved path %q is outside the workspace %q", cleaned, r.dir)
	}
	return cleaned, nil
}

// Read File Tool

type ReadFile struct{ Root *Root }

func (ReadFile) Name() string {
	return "read_file"
}

func (ReadFile) Description() string {
	return "Read the contents of a text file within the workspace. Returns the file with 1-based line numbers prefixed."
}

func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The path to the file to read, relative to the workspace root."
			}
		},
		"required": ["path"]
	}`)
}

func (ReadFile) ReadOnly() bool {
	return true
}

func (rf ReadFile) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("failed to unmarshal input: %w", err)
	}
	resolvedPath, err := rf.Root.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		lines[i] = fmt.Sprintf("%d: %s", i+1, line)
	}
	return Truncate(strings.Join(lines, "\n")), nil
}

// List Directory Tool

type ListDir struct{ Root *Root }

func (ListDir) Name() string {
	return "list_dir"
}

func (ListDir) Description() string {
	return "List the contents of a directory within the workspace. Returns a JSON array of file and directory names."
}

func (ListDir) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The path to the directory to list, relative to the workspace root."
			}
		},
		"required": ["path"]
	}`)
}

func (ListDir) ReadOnly() bool {
	return true
}

func (ld ListDir) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	// Models may send null or omit the path, which we treat as the root directory.
	if len(input) > 0 {
		_ = json.Unmarshal(input, &params)
	}
	if params.Path == "" {
		params.Path = "."
	}
	resolvedPath, err := ld.Root.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // Skip hidden files and directories.
		}
		if entry.IsDir() {
			names = append(names, entry.Name()+"/")
		} else {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(empty directory)", nil
	}
	return Truncate(strings.Join(names, "\n")), nil
}
