package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newEnv(t *testing.T) *Env {
	t.Helper()
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return &Env{AgentID: "teste", Role: "thread", Workspace: ws}
}

func writeFile(t *testing.T, env *Env, rel, content string) string {
	t.Helper()
	path := filepath.Join(env.Workspace.Root(), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func find(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("ferramenta %s não encontrada", name)
	return Tool{}
}

func call(t *testing.T, tl Tool, env *Env, input any) Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return tl.Run(context.Background(), env, raw)
}
