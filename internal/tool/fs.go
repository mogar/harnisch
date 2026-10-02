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

// Find and Replace in File tool

type FindReplaceInFile struct{ Root *Root }

func (FindReplaceInFile) Name() string {
	return "find_replace_in_file"
}

func (FindReplaceInFile) Description() string {
	return "Find and replace text in a file within the workspace. Returns the new contents of the file with 1-based line numbers prefixed."
}

func (FindReplaceInFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The path to the file to modify, relative to the workspace root."
			},
			"old_text": {
				"type": "string",
				"description": "The text to find and replace."
			},
			"new_text": {
				"type": "string",
				"description": "The text to replace with."
			}
		},
		"required": ["path", "old_text", "new_text"]
	}`)
}

func (FindReplaceInFile) ReadOnly() bool {
	return false
}

func (fr FindReplaceInFile) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("failed to unmarshal input: %w", err)
	}
	resolvedPath, err := fr.Root.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	newData := strings.ReplaceAll(string(data), params.OldText, params.NewText)
	if err := os.WriteFile(resolvedPath, []byte(newData), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	lines := strings.Split(newData, "\n")
	for i, line := range lines {
		lines[i] = fmt.Sprintf("%d: %s", i+1, line)
	}
	return Truncate(strings.Join(lines, "\n")), nil
}

// grep

type Grep struct{ Root *Root }

func (Grep) Name() string {
	return "grep"
}

func (Grep) Description() string {
	return "Search for a pattern in files within the workspace. Returns matching lines with 1-based line numbers prefixed."
}

func (Grep) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The pattern to search for."
			},
			"path": {
				"type": "string",
				"description": "The path to the directory to search, relative to the workspace root."
			}
		},
		"required": ["pattern"]
	}`)
}

func (Grep) ReadOnly() bool {
	return true
}

func (g Grep) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("failed to unmarshal input: %w", err)
	}
	if params.Path == "" {
		params.Path = "."
	}
	resolvedPath, err := g.Root.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	var matches []string
	err = filepath.WalkDir(resolvedPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil // Skip directories and hidden files.
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, params.Pattern) {
				relPath, _ := filepath.Rel(g.Root.Dir(), path)
				matches = append(matches, fmt.Sprintf("%s:%d: %s", relPath, i+1, line))
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to search files: %w", err)
	}
	if len(matches) == 0 {
		return "(no matches found)", nil
	}
	return Truncate(strings.Join(matches, "\n")), nil
}

// Create File Tool

type CreateFile struct{ Root *Root }

func (CreateFile) Name() string {
	return "create_file"
}

func (CreateFile) Description() string {
	return "Create a file at the given path, optionally with provided string contents."
}

func (CreateFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "The path to the file to create, relative to the workspace root."
			},
			"contents": {
				"type": "string",
				"description": "Optional string contents to write to the file."
			}
		},
		"required": ["path"]
	}`)
}

func (CreateFile) ReadOnly() bool {
	return false
}

func (cf CreateFile) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Path     string `json:"path"`
		Contents string `json:"contents"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("failed to unmarshal input: %w", err)
	}
	resolvedPath, err := cf.Root.ResolvePath(params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	// Ensure parent directories exist.
	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create parent directories: %w", err)
	}
	// Write the file.
	if err := os.WriteFile(resolvedPath, []byte(params.Contents), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	// Format with line numbers for consistency with other file tools.
	lines := strings.Split(params.Contents, "\n")
	for i, line := range lines {
		lines[i] = fmt.Sprintf("%d: %s", i+1, line)
	}
	return Truncate(strings.Join(lines, "\n")), nil
}
