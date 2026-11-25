package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	writeFile(t, env, "a.txt", "um\ndois\ntrês\nquatro\n")
	writeFile(t, env, "vazio.txt", "")
	writeFile(t, env, "bin.dat", "ab\x00cd\n")
	writeFile(t, env, "pasta/x.txt", "x")
	read := find(t, FileTools(), "ler_arquivo")

	tests := []struct {
		name     string
		input    map[string]any
		wantErr  bool
		contains []string
		excludes []string
	}{
		{name: "inteiro, numerado", input: map[string]any{"caminho": "a.txt"}, contains: []string{"     1\tum", "     4\tquatro"}},
		{name: "faixa", input: map[string]any{"caminho": "a.txt", "inicio": 2, "fim": 3}, contains: []string{"     2\tdois", "     3\ttrês"}, excludes: []string{"um", "quatro"}},
		{name: "só inicio", input: map[string]any{"caminho": "a.txt", "inicio": 4}, contains: []string{"quatro"}, excludes: []string{"três"}},
		{name: "faixa além do fim", input: map[string]any{"caminho": "a.txt", "inicio": 9}, wantErr: true, contains: []string{"só tem 4 linhas"}},
		{name: "faixa invertida", input: map[string]any{"caminho": "a.txt", "inicio": 3, "fim": 1}, wantErr: true, contains: []string{"faixa inválida"}},
		{name: "não existe", input: map[string]any{"caminho": "nada.txt"}, wantErr: true, contains: []string{"não existe"}},
		{name: "é pasta", input: map[string]any{"caminho": "pasta"}, wantErr: true, contains: []string{"listar_pasta"}},
		{name: "binário", input: map[string]any{"caminho": "bin.dat"}, wantErr: true, contains: []string{"binário"}},
		{name: "vazio", input: map[string]any{"caminho": "vazio.txt"}, contains: []string{"vazio"}},
		{name: "fora da pasta", input: map[string]any{"caminho": "/etc/hostname"}, wantErr: true, contains: []string{"só mexe dentro"}},
		{name: "sem caminho", input: map[string]any{}, wantErr: true, contains: []string{"vazio"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := call(t, read, env, tt.input)
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

func TestReadFile_TruncatesLongFile(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	writeFile(t, env, "longo.txt", strings.Repeat("linha\n", readMaxLines+50))
	res := call(t, find(t, FileTools(), "ler_arquivo"), env, map[string]any{"caminho": "longo.txt"})
	if res.IsError {
		t.Fatalf("erro inesperado: %s", res.Content)
	}
	if !strings.Contains(res.Content, "peça o resto com inicio=2001") {
		t.Errorf("faltou o aviso de corte com a próxima linha; fim do resultado: %q", res.Content[len(res.Content)-80:])
	}
}

func TestWriteFile(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	write := find(t, FileTools(), "escrever_arquivo")

	res := call(t, write, env, map[string]any{"caminho": "nova/pasta/a.txt", "conteudo": "olá"})
	if res.IsError {
		t.Fatalf("gravar: %s", res.Content)
	}
	got, err := os.ReadFile(filepath.Join(env.Workspace.Root(), "nova/pasta/a.txt"))
	if err != nil || string(got) != "olá" {
		t.Fatalf("conteúdo = %q, erro = %v", got, err)
	}

	if res := call(t, write, env, map[string]any{"caminho": "a.txt"}); !res.IsError || !strings.Contains(res.Content, "conteudo") {
		t.Errorf("sem conteudo deveria falhar citando o parâmetro: %s", res.Content)
	}
	if res := call(t, write, env, map[string]any{"caminho": "vazio.txt", "conteudo": ""}); res.IsError {
		t.Errorf("conteúdo vazio é válido: %s", res.Content)
	}
	if res := call(t, write, env, map[string]any{"caminho": "nova", "conteudo": "x"}); !res.IsError {
		t.Error("gravar por cima de uma pasta deveria falhar")
	}
	if res := call(t, write, env, map[string]any{"caminho": "../fora.txt", "conteudo": "x"}); !res.IsError {
		t.Error("gravar fora da pasta deveria falhar")
	}
}

func TestEditFile(t *testing.T) {
	t.Parallel()
	edit := find(t, FileTools(), "editar_arquivo")
	const original = "alfa\nbeta\nalfa\ngama\n"

	tests := []struct {
		name     string
		input    map[string]any
		wantErr  bool
		wantText string
		wantFile string
	}{
		{name: "troca trecho único", input: map[string]any{"caminho": "f.txt", "antigo": "beta", "novo": "BETA"}, wantFile: "alfa\nBETA\nalfa\ngama\n"},
		{name: "apaga trecho", input: map[string]any{"caminho": "f.txt", "antigo": "gama\n", "novo": ""}, wantFile: "alfa\nbeta\nalfa\n"},
		{name: "trecho não existe", input: map[string]any{"caminho": "f.txt", "antigo": "delta", "novo": "x"}, wantErr: true, wantText: "não existe", wantFile: original},
		{name: "trecho repetido", input: map[string]any{"caminho": "f.txt", "antigo": "alfa", "novo": "x"}, wantErr: true, wantText: "aparece 2 vezes", wantFile: original},
		{name: "antigo vazio", input: map[string]any{"caminho": "f.txt", "antigo": "", "novo": "x"}, wantErr: true, wantText: "vazio", wantFile: original},
		{name: "sem novo", input: map[string]any{"caminho": "f.txt", "antigo": "beta"}, wantErr: true, wantText: "novo", wantFile: original},
		{name: "igual", input: map[string]any{"caminho": "f.txt", "antigo": "beta", "novo": "beta"}, wantErr: true, wantText: "iguais", wantFile: original},
		{name: "arquivo não existe", input: map[string]any{"caminho": "nada.txt", "antigo": "a", "novo": "b"}, wantErr: true, wantText: "não existe", wantFile: original},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newEnv(t)
			path := writeFile(t, env, "f.txt", original)
			res := call(t, edit, env, tt.input)
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, quero %v: %s", res.IsError, tt.wantErr, res.Content)
			}
			if !strings.Contains(res.Content, tt.wantText) {
				t.Errorf("resultado %q não contém %q", res.Content, tt.wantText)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.wantFile {
				t.Errorf("arquivo = %q, quero %q", got, tt.wantFile)
			}
		})
	}
}

func TestDeleteFile(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	path := writeFile(t, env, "some.txt", "x")
	writeFile(t, env, "cheia/a.txt", "x")
	del := find(t, FileTools(), "excluir_arquivo")

	if res := call(t, del, env, map[string]any{"caminho": "some.txt"}); res.IsError {
		t.Fatalf("excluir: %s", res.Content)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("o arquivo ainda existe: %v", err)
	}
	if res := call(t, del, env, map[string]any{"caminho": "some.txt"}); !res.IsError || !strings.Contains(res.Content, "não existe") {
		t.Errorf("excluir o que não existe deveria dizer isso: %s", res.Content)
	}
	if res := call(t, del, env, map[string]any{"caminho": "cheia"}); !res.IsError || !strings.Contains(res.Content, "pasta com arquivos") {
		t.Errorf("excluir pasta cheia deveria explicar: %s", res.Content)
	}
	if res := call(t, del, env, map[string]any{"caminho": "."}); !res.IsError {
		t.Error("excluir a própria pasta de trabalho deveria falhar")
	}
}

func TestListDir(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	writeFile(t, env, "b.txt", "12345")
	writeFile(t, env, "sub/x.txt", "x")
	if err := os.Mkdir(filepath.Join(env.Workspace.Root(), "oca"), 0o755); err != nil {
		t.Fatal(err)
	}
	list := find(t, FileTools(), "listar_pasta")

	res := call(t, list, env, map[string]any{})
	if res.IsError {
		t.Fatalf("listar: %s", res.Content)
	}
	if want := "oca/\nsub/\nb.txt\t5 bytes\n"; res.Content != want {
		t.Errorf("listagem = %q, quero %q (pastas primeiro)", res.Content, want)
	}
	if res := call(t, list, env, map[string]any{"caminho": "oca"}); res.IsError || !strings.Contains(res.Content, "vazia") {
		t.Errorf("pasta vazia: %s", res.Content)
	}
	if res := call(t, list, env, map[string]any{"caminho": "b.txt"}); !res.IsError || !strings.Contains(res.Content, "ler_arquivo") {
		t.Errorf("listar arquivo deveria apontar ler_arquivo: %s", res.Content)
	}
	if res := call(t, list, env, map[string]any{"caminho": "nada"}); !res.IsError {
		t.Error("listar pasta que não existe deveria falhar")
	}
}

func TestDeleteFile_RemovesTheLinkAndKeepsItsTarget(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	root := env.Workspace.Root()
	target := writeFile(t, env, "alvo.txt", "x")
	outside := filepath.Join(t.TempDir(), "fora.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{"atalho": target, "sub/para-fora": outside}
	for name, to := range links {
		if err := os.Symlink(to, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	del := find(t, FileTools(), "excluir_arquivo")

	for name, to := range links {
		if res := call(t, del, env, map[string]any{"caminho": name}); res.IsError {
			t.Fatalf("excluir o link %s: %s", name, res.Content)
		}
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("o link %s ainda existe: %v", name, err)
		}
		if _, err := os.Stat(to); err != nil {
			t.Errorf("o arquivo apontado por %s foi apagado: %v", name, err)
		}
	}

	// A link to a folder outside does not open the way to what is inside it.
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "pasta-fora")); err != nil {
		t.Fatal(err)
	}
	if res := call(t, del, env, map[string]any{"caminho": "pasta-fora/fora.txt"}); !res.IsError {
		t.Error("excluir um arquivo de fora por dentro de um link deveria falhar")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("o arquivo de fora foi apagado: %v", err)
	}
}
