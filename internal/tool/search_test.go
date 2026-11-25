package tool

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func searchFixture(t *testing.T) *Env {
	t.Helper()
	env := newEnv(t)
	writeFile(t, env, "main.go", "package main\n\nfunc Alvo() {}\n")
	writeFile(t, env, "internal/a/a.go", "package a\n\n// Alvo aparece aqui também\n")
	writeFile(t, env, "internal/a/a_test.go", "package a\n")
	writeFile(t, env, "docs/leia.md", "nada de interessante\n")
	writeFile(t, env, ".git/config", "Alvo escondido\n")
	writeFile(t, env, "node_modules/x/index.js", "Alvo em dependência\n")
	return env
}

func TestGrep(t *testing.T) {
	t.Parallel()
	env := searchFixture(t)
	grep := find(t, SearchTools(), "buscar")

	tests := []struct {
		name     string
		input    map[string]any
		wantErr  bool
		contains []string
		excludes []string
	}{
		{name: "acha em dois arquivos", input: map[string]any{"padrao": "Alvo"}, contains: []string{"main.go:3:func Alvo() {}", "internal/a/a.go:3:"}, excludes: []string{".git/config"}},
		{name: "filtra por glob", input: map[string]any{"padrao": "package", "glob": "*_test.go"}, contains: []string{"a_test.go:1:"}, excludes: []string{"main.go"}},
		{name: "limita à pasta", input: map[string]any{"padrao": "Alvo", "caminho": "internal"}, contains: []string{"internal/a/a.go"}, excludes: []string{"main.go:"}},
		{name: "nada encontrado", input: map[string]any{"padrao": "inexistente_xyz"}, contains: []string{"nenhuma ocorrência"}},
		{name: "regex inválida", input: map[string]any{"padrao": "("}, wantErr: true, contains: []string{"inválida"}},
		{name: "padrão vazio", input: map[string]any{"padrao": ""}, wantErr: true, contains: []string{"vazio"}},
		{name: "pasta que não existe", input: map[string]any{"padrao": "x", "caminho": "nada"}, wantErr: true, contains: []string{"não existe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := call(t, grep, env, tt.input)
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, quero %v: %s", res.IsError, tt.wantErr, res.Content)
			}
			for _, want := range tt.contains {
				if !strings.Contains(res.Content, want) {
					t.Errorf("resultado não contém %q:\n%s", want, res.Content)
				}
			}
			for _, not := range tt.excludes {
				if strings.Contains(res.Content, not) {
					t.Errorf("resultado não deveria conter %q:\n%s", not, res.Content)
				}
			}
		})
	}
}

// The fallback runs on machines without ripgrep; it is tested directly so the
// result does not depend on what is installed here.
func TestGrepWalking(t *testing.T) {
	t.Parallel()
	env := searchFixture(t)
	root := env.Workspace.Root()

	lines, err := grepWalking(context.Background(), regexp.MustCompile("Alvo"), "", root)
	if err != nil {
		t.Fatal(err)
	}
	got := formatMatches(lines, env.Workspace)
	for _, want := range []string{"main.go:3:func Alvo() {}", "internal/a/a.go:3:// Alvo aparece aqui também"} {
		if !strings.Contains(got, want) {
			t.Errorf("faltou %q em:\n%s", want, got)
		}
	}
	for _, not := range []string{".git", "node_modules"} {
		if strings.Contains(got, not) {
			t.Errorf("não deveria entrar em %s:\n%s", not, got)
		}
	}

	lines, err = grepWalking(context.Background(), regexp.MustCompile("package"), "*.{md,go}", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Errorf("com glob *.{md,go} achei %d linhas, quero 3: %v", len(lines), lines)
	}
}

func TestGlob(t *testing.T) {
	t.Parallel()
	env := searchFixture(t)
	glob := find(t, SearchTools(), "glob")

	tests := []struct {
		name    string
		input   map[string]any
		want    string
		wantErr bool
	}{
		{name: "atravessa pastas", input: map[string]any{"padrao": "**/*.go"}, want: "internal/a/a.go\ninternal/a/a_test.go\nmain.go\n"},
		{name: "só a raiz", input: map[string]any{"padrao": "*.go"}, want: "main.go\n"},
		{name: "prefixo e sufixo", input: map[string]any{"padrao": "internal/**/*_test.go"}, want: "internal/a/a_test.go\n"},
		{name: "alternativas", input: map[string]any{"padrao": "**/*.{md,js}"}, want: "docs/leia.md\n"},
		{name: "a partir de uma pasta", input: map[string]any{"padrao": "*.md", "caminho": "docs"}, want: "docs/leia.md\n"},
		{name: "nada casa", input: map[string]any{"padrao": "**/*.rs"}, want: "nenhum arquivo casa com \"**/*.rs\"."},
		{name: "padrão inválido", input: map[string]any{"padrao": "[a"}, wantErr: true},
		{name: "pasta que não existe", input: map[string]any{"padrao": "*", "caminho": "nada"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := call(t, glob, env, tt.input)
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, quero %v: %s", res.IsError, tt.wantErr, res.Content)
			}
			if !tt.wantErr && res.Content != tt.want {
				t.Errorf("glob = %q, quero %q", res.Content, tt.want)
			}
		})
	}
}

func TestMatchPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "x/y/a.go", true},
		{"*.go", "x/a.go", false},
		{"x/**", "x/y/z", true},
		{"x/**/z", "x/z", true},
		{"x/**/z", "x/a/b/z", true},
		{"x/**/z", "y/a/z", false},
		{"**", "qualquer/coisa", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" em "+tt.path, func(t *testing.T) {
			t.Parallel()
			if got := matchPath(strings.Split(tt.pattern, "/"), strings.Split(tt.path, "/")); got != tt.want {
				t.Errorf("matchPath = %v, quero %v", got, tt.want)
			}
		})
	}
}
