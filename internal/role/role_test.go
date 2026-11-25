package role

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    Role
		wantErr string
	}{
		{
			name:    "cabeçalho completo",
			content: "---\nname: documentador\ndescription: \"só documenta\"\nmodel: sonnet\neffort: low\ntools: ler_arquivo, escrever_arquivo ,glob\n---\n\nVocê só escreve documentação.\n",
			want:    Role{Name: "documentador", Description: "só documenta", Model: "sonnet", Effort: "low", Tools: []string{"ler_arquivo", "escrever_arquivo", "glob"}, Instructions: "Você só escreve documentação."},
		},
		{
			name:    "sem cabeçalho usa o nome do arquivo",
			content: "Só instruções.",
			want:    Role{Name: "arquivo", Instructions: "Só instruções."},
		},
		{
			name:    "lista entre colchetes",
			content: "---\ntools: [a, \"b\"]\n---\ntexto",
			want:    Role{Name: "arquivo", Tools: []string{"a", "b"}, Instructions: "texto"},
		},
		{name: "cabeçalho sem fechar", content: "---\nname: x\ntexto", wantErr: "não fecha"},
		{name: "chave desconhecida", content: "---\nmodelo: opus\n---\ntexto", wantErr: "chave desconhecida"},
		{name: "linha sem dois pontos", content: "---\nname\n---\ntexto", wantErr: "chave: valor"},
		{name: "esforço inválido", content: "---\neffort: turbo\n---\ntexto", wantErr: "effort"},
		{name: "sem instruções", content: "---\nname: x\n---\n  \n", wantErr: "sem instruções"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.content, "arquivo")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("erro = %v, quero conter %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.want.Name || got.Description != tt.want.Description || got.Model != tt.want.Model ||
				got.Effort != tt.want.Effort || got.Instructions != tt.want.Instructions ||
				strings.Join(got.Tools, ",") != strings.Join(tt.want.Tools, ",") {
				t.Errorf("Parse = %+v\nquero %+v", got, tt.want)
			}
		})
	}
}

func TestLoad_NativeRoles(t *testing.T) {
	t.Parallel()
	roles, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	host, thread := roles[Host], roles[Thread]
	if host.Source != "nativo" || thread.Source != "nativo" {
		t.Fatalf("host e thread deveriam ser nativos: %q %q", host.Source, thread.Source)
	}
	has := func(r Role, tool string) bool {
		for _, name := range r.Tools {
			if name == tool {
				return true
			}
		}
		return false
	}
	for _, tool := range []string{"delegar", "aguardar_threads", "continuar_thread", "listar_threads"} {
		if !has(host, tool) {
			t.Errorf("o host deveria ter %s", tool)
		}
		if has(thread, tool) {
			t.Errorf("o thread não pode ter %s", tool)
		}
	}
	for _, tool := range []string{"ler_arquivo", "escrever_arquivo", "editar_arquivo", "excluir_arquivo", "listar_pasta", "buscar", "glob", "rodar_comando", "rodar_pesado", "tirar_print", "pesquisar_web"} {
		if !has(host, tool) || !has(thread, tool) {
			t.Errorf("host e thread deveriam ter %s", tool)
		}
	}
	for _, want := range []string{"`sonnet`, esforço `medium`", "`opus`, esforço `high`", "`fable` só quando"} {
		if !strings.Contains(host.Instructions, want) {
			t.Errorf("o host perdeu a instrução de escolha de modelo %q", want)
		}
	}
	for _, want := range []string{
		"moram no `AGENTS.md`, na raiz do projeto",
		"confira se algum item já cobre",
		"com a data e a origem entre parênteses no fim",
		"Item que deixou de valer",
		"Só grave depois de ele dizer que pode",
	} {
		if !strings.Contains(host.Instructions, want) {
			t.Errorf("o host perdeu a regra das armadilhas %q", want)
		}
	}
	if !strings.Contains(thread.Instructions, "Não edite o `AGENTS.md`") || !strings.Contains(thread.Instructions, "**Armadilha nova**") {
		t.Error("o thread deveria relatar a armadilha e não editar o AGENTS.md")
	}
	if !strings.Contains(thread.Instructions, "você confere com `tirar_print` antes de devolver") || strings.Contains(thread.Instructions, "Você não confere tela") {
		t.Error("o thread deveria conferir a tela que mexeu com tirar_print")
	}
	if thread.Model != "opus" || !strings.Contains(thread.Instructions, "Sem commit, push") {
		t.Errorf("o agente thread não veio como esperado: modelo %q", thread.Model)
	}
}

func TestLoad_DirectoriesOverrideAndAdd(t *testing.T) {
	t.Parallel()
	global, project := t.TempDir(), t.TempDir()
	write := func(dir, name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(global, "documentador.md", "---\nmodel: haiku\ntools: ler_arquivo\n---\nglobal")
	write(global, "thread.md", "---\nmodel: sonnet\n---\nthread do usuário")
	write(project, "documentador.md", "---\nmodel: sonnet\n---\ndo projeto")

	roles, err := Load(global, project, filepath.Join(project, "nao-existe"))
	if err != nil {
		t.Fatal(err)
	}
	if got := roles["documentador"]; got.Instructions != "do projeto" || got.Model != "sonnet" {
		t.Errorf("o agente do projeto deveria vencer o global: %+v", got)
	}
	if got := roles[Thread]; got.Instructions != "thread do usuário" || got.Source == "nativo" {
		t.Errorf("um arquivo thread.md deveria trocar o agente nativo: %+v", got)
	}
	if roles[Host].Source != "nativo" {
		t.Error("o host nativo deveria continuar")
	}

	write(project, "quebrado.md", "---\nname: x\n")
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "quebrado.md") {
		t.Errorf("agente inválido deveria dar erro citando o arquivo: %v", err)
	}
}

func TestSystem(t *testing.T) {
	t.Parallel()
	r := Role{Instructions: "instruções do agente"}
	if got := System(r, "", "  \n"); got != "instruções do agente" {
		t.Errorf("sem regras, quero só as instruções: %q", got)
	}
	got := System(r, "regra do dono\n", "regra do projeto")
	want := "instruções do agente\n\n# Regras do dono\n\nregra do dono\n\n# Regras do projeto\n\nregra do projeto"
	if got != want {
		t.Errorf("System = %q\nquero %q", got, want)
	}
	if System(r, "a", "b") != System(r, "a", "b") {
		t.Error("o texto precisa ser idêntico a cada chamada")
	}
}
