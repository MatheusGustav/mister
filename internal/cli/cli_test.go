package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatheusGustav/mister/internal/agent"
	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/provider/anthropic"
)

type scriptedProvider struct {
	requests []provider.Request
	answer   func(req provider.Request) string
}

func (p *scriptedProvider) Name() string                        { return "fake" }
func (p *scriptedProvider) Capabilities() provider.Capabilities { return provider.Capabilities{} }

func (p *scriptedProvider) Complete(_ context.Context, req provider.Request) (*provider.Response, error) {
	p.requests = append(p.requests, req)
	text := p.answer(req)
	if req.OnText != nil {
		req.OnText(text)
	}
	return &provider.Response{
		Stop:    provider.StopEndTurn,
		Message: provider.Message{Role: provider.RoleAssistant, Blocks: []provider.Block{{Type: provider.BlockText, Text: text}}},
	}, nil
}

// run executes the command tree in an isolated home and project directory.
func run(t *testing.T, deps Deps, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := NewRootCommand(deps)
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func isolate(t *testing.T) (home config.Home, project string) {
	t.Helper()
	homeDir := filepath.Join(t.TempDir(), "mister-home")
	t.Setenv(config.HomeEnv, homeDir)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	return config.Home{Dir: homeDir}, project
}

func fakeDeps(p provider.Provider) Deps {
	return Deps{NewProvider: func() (provider.Provider, error) { return p, nil }}
}

func TestVersion(t *testing.T) {
	out, _, err := run(t, DefaultDeps(), "", "version")
	if err != nil || out != "mister "+Version+"\n" {
		t.Errorf("version = %q, %v", out, err)
	}
}

func TestRoot_RejectsArguments(t *testing.T) {
	isolate(t)
	if _, _, err := run(t, DefaultDeps(), "", "comando-que-nao-existe"); err == nil {
		t.Error("um argumento solto deveria ser erro de uso")
	}
	if _, _, err := run(t, DefaultDeps(), "", "--continuar", "--sessao", "x"); err == nil {
		t.Error("--continuar e --sessao juntos deveriam ser recusados")
	}
}

func TestChat_WithoutAPIKey(t *testing.T) {
	isolate(t)
	t.Setenv(anthropic.APIKeyEnv, "")
	_, _, err := run(t, DefaultDeps(), "oi\n")
	if !errors.Is(err, anthropic.ErrNoAPIKey) {
		t.Errorf("sem a chave, erro = %v; quero ErrNoAPIKey", err)
	}
}

func TestChat_TalksToTheHostAndSavesTheSession(t *testing.T) {
	home, project := isolate(t)
	if err := os.WriteFile(filepath.Join(project, ProjectRulesFile), []byte("regra do projeto X"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home.RulesFile(), []byte("regra do dono Y"), 0o644); err != nil {
		t.Fatal(err)
	}
	prov := &scriptedProvider{answer: func(req provider.Request) string {
		return "resposta " + string(rune('0'+len(req.Messages)/2+1))
	}}

	stdout, stderr, err := run(t, fakeDeps(prov), "primeira pergunta\n\n/memoria\nsegunda pergunta\n/sair\nnunca lida\n")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "resposta 1\nresposta 2\n" {
		t.Errorf("a saída padrão deveria trazer só as respostas: %q", stdout)
	}
	for _, want := range []string{"sessão ", "host [host, claude-opus-5-5, esforço high]", "fila de comandos pesados: 0 rodando, 0 esperando (limite 1)", "processo: heap"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("o terminal deveria mostrar %q:\n%s", want, stderr)
		}
	}
	if len(prov.requests) != 2 {
		t.Fatalf("quero 2 pedidos ao modelo, veio %d", len(prov.requests))
	}
	first := prov.requests[0]
	if !strings.Contains(first.System, "regra do projeto X") || !strings.Contains(first.System, "regra do dono Y") {
		t.Error("as regras do dono e do projeto deveriam entrar nas instruções")
	}
	if !strings.Contains(first.Messages[0].Text(), "primeira pergunta") || !strings.Contains(first.Messages[0].Text(), "Projeto: "+project) {
		t.Errorf("primeira mensagem inesperada: %q", first.Messages[0].Text())
	}
	hasExa := false
	for _, spec := range first.Tools {
		hasExa = hasExa || spec.Name == agent.WebSearchTool
	}
	if !hasExa {
		t.Error("com um provedor sem pesquisa própria, a pesquisa pela Exa deveria estar disponível")
	}

	sessions, err := agent.ListSessions(home.SessionsDir())
	if err != nil || len(sessions) != 1 || sessions[0].Project != project {
		t.Fatalf("a sessão deveria estar gravada: %+v, %v", sessions, err)
	}

	// Resuming continues the same conversation.
	prov2 := &scriptedProvider{answer: func(provider.Request) string { return "retomada" }}
	stdout, stderr, err = run(t, fakeDeps(prov2), "terceira pergunta\n", "--continuar")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "retomada\n" || !strings.Contains(stderr, "retomada") {
		t.Errorf("saída da retomada: %q / %q", stdout, stderr)
	}
	if got := len(prov2.requests[0].Messages); got != 5 {
		t.Errorf("a retomada deveria mandar as 4 mensagens salvas mais a nova, mandou %d", got)
	}
	if _, _, err := run(t, fakeDeps(prov2), "", "--sessao", "nao-existe"); err == nil || !strings.Contains(err.Error(), "não existe a sessão") {
		t.Errorf("sessão desconhecida: %v", err)
	}

	out, _, err := run(t, DefaultDeps(), "", "sessoes")
	if err != nil || !strings.Contains(out, sessions[0].ID) || !strings.Contains(out, project) {
		t.Errorf("'mister sessoes' deveria listar a sessão: %q, %v", out, err)
	}
}

func TestChat_ResumeWithoutSession(t *testing.T) {
	isolate(t)
	prov := &scriptedProvider{answer: func(provider.Request) string { return "x" }}
	if _, _, err := run(t, fakeDeps(prov), "", "--continuar"); err == nil || !strings.Contains(err.Error(), "nenhuma sessão guardada") {
		t.Errorf("erro = %v", err)
	}
}

func TestRoles_ListsNativeAndCustom(t *testing.T) {
	home, project := isolate(t)
	dir := filepath.Join(project, ".mister", "papeis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "---\ndescription: só documenta\nmodel: sonnet\ntools: ler_arquivo, escrever_arquivo\n---\nVocê só escreve documentação."
	if err := os.WriteFile(filepath.Join(dir, "documentador.md"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, DefaultDeps(), "", "papeis")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"host", "thread", "nativo", "documentador", "sonnet", "ler_arquivo, escrever_arquivo", "documentador.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("a lista de papéis não contém %q:\n%s", want, out)
		}
	}

	// A role created by a file is usable as the main agent, with no code change.
	prov := &scriptedProvider{answer: func(provider.Request) string { return "documentado" }}
	if _, _, err := run(t, fakeDeps(prov), "documente\n", "--papel", "documentador"); err != nil {
		t.Fatal(err)
	}
	req := prov.requests[0]
	if req.System != "Você só escreve documentação." || req.Model != "claude-sonnet-5-5" || len(req.Tools) != 2 {
		t.Errorf("o papel novo não foi aplicado: system=%q model=%q tools=%d", req.System, req.Model, len(req.Tools))
	}
	if _, _, err := run(t, fakeDeps(prov), "", "--papel", "fantasma"); err == nil {
		t.Error("papel que não existe deveria dar erro")
	}
	_ = home
}

func TestInstall(t *testing.T) {
	home, _ := isolate(t)
	dest := filepath.Join(t.TempDir(), "bin")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv(anthropic.APIKeyEnv, "")

	out, _, err := run(t, DefaultDeps(), "", "install", "--destino", dest)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dest, "mister")
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("o binário deveria estar instalado e executável: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if selfInfo, _ := os.Stat(self); selfInfo.Size() != info.Size() {
		t.Errorf("o binário instalado tem %d bytes, o original %d", info.Size(), selfInfo.Size())
	}
	for _, want := range []string{"binário instalado em " + target, "configuração criada em " + home.ConfigFile(), "não está no PATH", "ANTHROPIC_API_KEY não está definida"} {
		if !strings.Contains(out, want) {
			t.Errorf("a saída da instalação não contém %q:\n%s", want, out)
		}
	}
	if _, err := home.Load(); err != nil {
		t.Errorf("o config criado deveria carregar: %v", err)
	}
	for _, dir := range []string{home.RolesDir(), home.SessionsDir(), home.MemoryDir()} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("faltou criar %s", dir)
		}
	}

	// Installing again keeps the user's config.
	if err := os.WriteFile(home.ConfigFile(), []byte(`{"max_threads": 7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err = run(t, DefaultDeps(), "", "install", "--destino", dest)
	if err != nil || !strings.Contains(out, "configuração mantida") {
		t.Errorf("segunda instalação: %q, %v", out, err)
	}
	if cfg, _ := home.Load(); cfg.MaxThreads != 7 {
		t.Errorf("a instalação alterou o config do usuário: %d", cfg.MaxThreads)
	}

}

func TestShorten(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "json compactado", in: "{\n  \"caminho\": \"a.txt\"\n}", limit: 80, want: `{"caminho":"a.txt"}`},
		{name: "texto em várias linhas", in: "linha um\n  linha dois", limit: 80, want: "linha um linha dois"},
		{name: "cortado", in: strings.Repeat("a", 50), limit: 10, want: strings.Repeat("a", 10) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shorten(tt.in, tt.limit); got != tt.want {
				t.Errorf("shorten = %q, quero %q", got, tt.want)
			}
		})
	}
}
