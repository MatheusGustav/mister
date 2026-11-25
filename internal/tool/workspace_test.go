package tool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspace_Resolve(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	readable := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ws.AllowRead(readable)
	if err := os.Symlink(outside, filepath.Join(root, "atalho")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		write   bool
		wantErr bool
	}{
		{name: "relativo dentro", path: "a/b.txt", write: true},
		{name: "absoluto dentro", path: filepath.Join(root, "novo.txt"), write: true},
		{name: "a própria raiz", path: ".", write: false},
		{name: "sobe com ..", path: "../fora.txt", write: false, wantErr: true},
		{name: "absoluto fora", path: filepath.Join(outside, "x"), write: false, wantErr: true},
		{name: "symlink que sai", path: "atalho/segredo.txt", write: false, wantErr: true},
		{name: "symlink que sai, arquivo novo", path: "atalho/novo.txt", write: true, wantErr: true},
		{name: "pasta de leitura, lendo", path: filepath.Join(readable, "x"), write: false},
		{name: "pasta de leitura, gravando", path: filepath.Join(readable, "x"), write: true, wantErr: true},
		{name: "vazio", path: " ", write: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ws.Resolve(tt.path, tt.write)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Resolve(%q, write=%v) erro = %v, wantErr %v", tt.path, tt.write, err, tt.wantErr)
			}
			if tt.wantErr && tt.path != " " && !errors.Is(err, ErrOutsideWorkspace) {
				t.Errorf("erro %v não é ErrOutsideWorkspace", err)
			}
		})
	}
}
