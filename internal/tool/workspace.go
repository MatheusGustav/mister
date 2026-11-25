package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Workspace confines an agent to its own directory. Writes only happen inside
// Root; reads may also reach the extra read roots (a host reviewing the
// worktrees of its threads).
type Workspace struct {
	root string

	mu        sync.RWMutex
	readRoots []string
}

func NewWorkspace(root string) (*Workspace, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("pasta de trabalho %q: %w", root, err)
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("pasta de trabalho %q: %w", root, err)
	}
	return &Workspace{root: abs}, nil
}

func (w *Workspace) Root() string { return w.root }

// AllowRead adds a directory the agent may read but not change.
func (w *Workspace) AllowRead(dir string) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.readRoots = append(w.readRoots, resolved)
}

// ErrOutsideWorkspace marks a path that leaves the agent's directory.
var ErrOutsideWorkspace = errors.New("caminho fora da pasta do agente")

// Resolve turns a path given by the model into an absolute path, refusing
// anything outside the allowed roots, symlinks included.
func (w *Workspace) Resolve(path string, write bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("o caminho veio vazio")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.root, path)
	}
	real := realPath(filepath.Clean(path))

	if within(w.root, real) {
		return real, nil
	}
	if !write {
		w.mu.RLock()
		defer w.mu.RUnlock()
		for _, r := range w.readRoots {
			if within(r, real) {
				return real, nil
			}
		}
	}
	action := "ler"
	if write {
		action = "alterar"
	}
	return "", fmt.Errorf("%w: não é permitido %s %q; este agente só mexe dentro de %s", ErrOutsideWorkspace, action, path, w.root)
}

// realPath resolves symlinks on the part of the path that exists, so a link
// pointing outside the workspace is caught even when the target file is new.
func realPath(path string) string {
	existing, rest := path, ""
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return path
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// Rel shows a path relative to the workspace root when it is inside it.
func (w *Workspace) Rel(path string) string {
	if rel, err := filepath.Rel(w.root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
