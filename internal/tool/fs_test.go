package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newWorkspace returns a root over <tmp>/ws alongside an <tmp>/outside directory holding a secret file.
func newWorkspace(t *testing.T) (root *Root, outside string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "ws")
	outside = filepath.Join(base, "outside")
	for _, dir := range []string{ws, outside} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	root, err = NewRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	return root, outside
}

func TestResolvePath(t *testing.T) {
	root, outside := newWorkspace(t)
	ws := root.Dir()
	base := filepath.Dir(ws)

	if err := os.Mkdir(filepath.Join(ws, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "ws2"), 0755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"outlink":  outside,                          // directory outside the workspace
		"inlink":   filepath.Join(ws, "sub"),         // directory inside the workspace
		"dangling": filepath.Join(outside, "absent"), // target doesn't exist yet
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(ws, name)); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{name: "root", path: ".", want: ws},
		{name: "relative existing", path: "sub", want: filepath.Join(ws, "sub")},
		{name: "relative missing", path: "new.txt", want: filepath.Join(ws, "new.txt")},
		{name: "missing nested dirs", path: "a/b/c.txt", want: filepath.Join(ws, "a/b/c.txt")},
		{name: "symlink inside", path: "inlink/new.txt", want: filepath.Join(ws, "sub/new.txt")},
		{name: "absolute inside", path: filepath.Join(ws, "sub"), want: filepath.Join(ws, "sub")},
		{name: "empty", path: "", wantErr: true},
		{name: "dotdot escape", path: "../outside/secret.txt", wantErr: true},
		{name: "absolute outside", path: filepath.Join(outside, "secret.txt"), wantErr: true},
		{name: "sibling with shared prefix", path: filepath.Join(base, "ws2"), wantErr: true},
		{name: "missing file under symlinked dir", path: "outlink/new.txt", wantErr: true},
		{name: "missing nested under symlinked dir", path: "outlink/a/b.txt", wantErr: true},
		{name: "dangling symlink", path: "dangling", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := root.ResolvePath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolvePath(%q) = %q, want error", tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePath(%q) error: %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("ResolvePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestCreateFileThroughSymlinkedDirIsRejected(t *testing.T) {
	root, outside := newWorkspace(t)
	if err := os.Symlink(outside, filepath.Join(root.Dir(), "link")); err != nil {
		t.Fatal(err)
	}

	input := json.RawMessage(`{"path":"link/new.txt","contents":"pwned"}`)
	if _, err := (CreateFile{Root: root}).Execute(context.Background(), input); err == nil {
		t.Fatal("expected error creating a file through a symlink that leaves the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("file was written outside the workspace (stat err: %v)", err)
	}
}

func TestGrepSymlinks(t *testing.T) {
	root, outside := newWorkspace(t)
	ws := root.Dir()
	if err := os.WriteFile(filepath.Join(ws, "real.txt"), []byte("secret here\n"), 0644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"outfile.txt": filepath.Join(outside, "secret.txt"), // must not leak
		"outdir":      outside,                              // must not break the walk
		"infile.txt":  filepath.Join(ws, "real.txt"),        // still searched
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(ws, name)); err != nil {
			t.Fatal(err)
		}
	}

	out, err := (Grep{Root: root}).Execute(context.Background(), json.RawMessage(`{"pattern":"secret"}`))
	if err != nil {
		t.Fatalf("grep failed: %v", err)
	}
	if strings.Contains(out, "outfile.txt") {
		t.Errorf("grep read through a symlink outside the workspace:\n%s", out)
	}
	for _, want := range []string{"real.txt:1:", "infile.txt:1:"} {
		if !strings.Contains(out, want) {
			t.Errorf("grep output missing %q:\n%s", want, out)
		}
	}
}
