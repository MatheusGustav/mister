package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatheusGustav/mister/internal/agent"
	"github.com/MatheusGustav/mister/internal/browser"
	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/embed"
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
	if _, _, err := run(t, DefaultDeps(), "", "--continue", "--session", "x"); err == nil {
		t.Error("--continue e --session juntos deveriam ser recusados")
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

	stdout, stderr, err := run(t, fakeDeps(prov), "primeira pergunta\n\n/memory\nsegunda pergunta\n/exit\nnunca lida\n")
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
	hasExa, hasShot := false, false
	for _, spec := range first.Tools {
		hasExa = hasExa || spec.Name == agent.WebSearchTool
		hasShot = hasShot || spec.Name == browser.ToolName
	}
	if !hasExa {
		t.Error("com um provedor sem pesquisa própria, a pesquisa pela Exa deveria estar disponível")
	}
	if !hasShot {
		t.Error("o host deveria ter a ferramenta de print")
	}
	if _, err := os.Stat(home.BrowserDir()); err == nil {
		t.Error("o navegador só é baixado no primeiro print, não ao abrir a conversa")
	}

	sessions, err := agent.ListSessions(home.SessionsDir())
	if err != nil || len(sessions) != 1 || sessions[0].Project != project {
		t.Fatalf("a sessão deveria estar gravada: %+v, %v", sessions, err)
	}

	// Resuming continues the same conversation.
	prov2 := &scriptedProvider{answer: func(provider.Request) string { return "retomada" }}
	stdout, stderr, err = run(t, fakeDeps(prov2), "terceira pergunta\n", "--continue")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "retomada\n" || !strings.Contains(stderr, "retomada") {
		t.Errorf("saída da retomada: %q / %q", stdout, stderr)
	}
	if got := len(prov2.requests[0].Messages); got != 5 {
		t.Errorf("a retomada deveria mandar as 4 mensagens salvas mais a nova, mandou %d", got)
	}
	if _, _, err := run(t, fakeDeps(prov2), "", "--session", "nao-existe"); err == nil || !strings.Contains(err.Error(), "não existe a sessão") {
		t.Errorf("sessão desconhecida: %v", err)
	}

	out, _, err := run(t, DefaultDeps(), "", "sessions")
	if err != nil || !strings.Contains(out, sessions[0].ID) || !strings.Contains(out, project) {
		t.Errorf("'mister sessions' deveria listar a sessão: %q, %v", out, err)
	}
}

func TestChat_ResumeWithoutSession(t *testing.T) {
	isolate(t)
	prov := &scriptedProvider{answer: func(provider.Request) string { return "x" }}
	if _, _, err := run(t, fakeDeps(prov), "", "--continue"); err == nil || !strings.Contains(err.Error(), "nenhuma sessão guardada") {
		t.Errorf("erro = %v", err)
	}
}

func TestRoles_ListsNativeAndCustom(t *testing.T) {
	home, project := isolate(t)
	dir := filepath.Join(project, ".mister", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "---\ndescription: só documenta\nmodel: sonnet\ntools: ler_arquivo, escrever_arquivo\n---\nVocê só escreve documentação."
	if err := os.WriteFile(filepath.Join(dir, "documentador.md"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, DefaultDeps(), "", "agents")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"host", "thread", "nativo", "documentador", "sonnet", "ler_arquivo, escrever_arquivo", "documentador.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("a lista de agentes não contém %q:\n%s", want, out)
		}
	}

	// A role created by a file is usable as the main agent, with no code change.
	prov := &scriptedProvider{answer: func(provider.Request) string { return "documentado" }}
	if _, _, err := run(t, fakeDeps(prov), "documente\n", "--agent", "documentador"); err != nil {
		t.Fatal(err)
	}
	req := prov.requests[0]
	if req.System != "Você só escreve documentação." || req.Model != "claude-sonnet-5-5" || len(req.Tools) != 2 {
		t.Errorf("o agente novo não foi aplicado: system=%q model=%q tools=%d", req.System, req.Model, len(req.Tools))
	}
	if _, _, err := run(t, fakeDeps(prov), "", "--agent", "fantasma"); err == nil {
		t.Error("agente que não existe deveria dar erro")
	}
	_ = home
}

// fakeSources serves a tiny archive and a tiny model in place of the real
// downloads of the embedding server.
func fakeSources(t *testing.T) embed.Sources {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\nexit 1\n"
	if err := tw.WriteHeader(&tar.Header{Name: "llama-teste/llama-server", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	model := []byte("pesos de teste")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/modelo.gguf" {
			_, _ = w.Write(model)
			return
		}
		_, _ = w.Write(archive.Bytes())
	}))
	t.Cleanup(srv.Close)
	sum := func(data []byte) string {
		h := sha256.Sum256(data)
		return hex.EncodeToString(h[:])
	}
	return embed.Sources{
		ServerURL: srv.URL + "/servidor.tar.gz", ServerSHA256: sum(archive.Bytes()), ServerDir: "llama-teste",
		ModelURL: srv.URL + "/modelo.gguf", ModelSHA256: sum(model), ModelFile: "modelo.gguf",
	}
}

func fakeRipgrep(t *testing.T) RipgrepSource {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\nexit 1\n"
	for name, content := range map[string]string{"rg-teste/rg": script, "rg-teste/LEIAME": "sobra"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive.Bytes())
	}))
	t.Cleanup(srv.Close)
	h := sha256.Sum256(archive.Bytes())
	return RipgrepSource{URL: srv.URL + "/rg.tar.gz", SHA256: hex.EncodeToString(h[:]), Binary: "rg-teste/rg"}
}

func TestInstall(t *testing.T) {
	home, _ := isolate(t)
	dest := filepath.Join(t.TempDir(), "bin")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv(anthropic.APIKeyEnv, "")
	deps := DefaultDeps()
	deps.EmbeddingSources = fakeSources(t)
	deps.RipgrepSource = fakeRipgrep(t)

	out, _, err := run(t, deps, "", "install", "--dest", dest)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(home.RipgrepFile()); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("o ripgrep deveria estar instalado e executável em %s: %v", home.RipgrepFile(), err)
	}
	if left, _ := os.ReadDir(filepath.Dir(home.RipgrepFile())); len(left) != 1 {
		t.Errorf("a pasta do ripgrep deveria ter só o rg, tem %d itens", len(left))
	}
	if !strings.Contains(out, "ripgrep da busca instalado em "+home.RipgrepFile()) {
		t.Errorf("a saída deveria dizer onde o ripgrep ficou:\n%s", out)
	}
	for _, path := range []string{"llama-teste/llama-server", "modelo.gguf"} {
		if _, err := os.Stat(filepath.Join(home.EmbeddingDir(), path)); err != nil {
			t.Errorf("faltou instalar %s do servidor de embedding: %v", path, err)
		}
	}
	if !strings.Contains(out, "servidor de embedding da memória instalado em "+home.EmbeddingDir()) {
		t.Errorf("a saída deveria dizer onde o servidor de embedding ficou:\n%s", out)
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
	out, _, err = run(t, deps, "", "install", "--dest", dest)
	if err != nil || !strings.Contains(out, "configuração mantida") || !strings.Contains(out, "embedding da memória já instalado") || !strings.Contains(out, "ripgrep da busca já instalado") {
		t.Errorf("segunda instalação: %q, %v", out, err)
	}
	if cfg, _ := home.Load(); cfg.MaxThreads != 7 {
		t.Errorf("a instalação alterou o config do usuário: %d", cfg.MaxThreads)
	}
}

func TestInstall_EmbeddingDownloadFails(t *testing.T) {
	home, _ := isolate(t)
	deps := DefaultDeps()
	deps.EmbeddingSources = fakeSources(t)
	deps.EmbeddingSources.ModelSHA256 = strings.Repeat("0", 64)
	deps.RipgrepSource = fakeRipgrep(t)
	dest := filepath.Join(t.TempDir(), "bin")

	_, _, err := run(t, deps, "", "install", "--dest", dest)
	if err == nil || !strings.Contains(err.Error(), "o binário e a configuração foram instalados") {
		t.Fatalf("erro = %v; quero dizer o que ficou instalado e o que não", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "mister")); statErr != nil {
		t.Errorf("o binário deveria estar instalado mesmo assim: %v", statErr)
	}
	if _, statErr := os.Stat(home.ConfigFile()); statErr != nil {
		t.Errorf("o config deveria estar criado mesmo assim: %v", statErr)
	}
}

func TestInstall_RipgrepDownloadFails(t *testing.T) {
	home, _ := isolate(t)
	deps := DefaultDeps()
	deps.EmbeddingSources = fakeSources(t)
	deps.RipgrepSource = fakeRipgrep(t)
	deps.RipgrepSource.SHA256 = strings.Repeat("0", 64)

	_, _, err := run(t, deps, "", "install", "--dest", filepath.Join(t.TempDir(), "bin"))
	if err == nil || !strings.Contains(err.Error(), "mas o ripgrep da busca não") {
		t.Fatalf("erro = %v; quero dizer o que ficou instalado e o que não", err)
	}
	if _, statErr := os.Stat(home.RipgrepFile()); statErr == nil {
		t.Error("um ripgrep que não confere com o checksum não deveria ficar instalado")
	}
	if left, _ := os.ReadDir(filepath.Dir(home.RipgrepFile())); len(left) != 0 {
		t.Errorf("o download que falhou deixou %d itens para trás", len(left))
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

func TestChat_SessionWithoutMessagesIsNotSaved(t *testing.T) {
	home, _ := isolate(t)
	prov := &scriptedProvider{answer: func(provider.Request) string { return "conversa" }}
	if _, _, err := run(t, fakeDeps(prov), "uma pergunta\n"); err != nil {
		t.Fatal(err)
	}
	// Opened and closed without a word to the host.
	if _, _, err := run(t, fakeDeps(prov), "/help\n"); err != nil {
		t.Fatal(err)
	}
	sessions, err := agent.ListSessions(home.SessionsDir())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("só a sessão com conversa deveria estar gravada: %+v, %v", sessions, err)
	}
	if dirs, _ := os.ReadDir(home.SessionsDir()); len(dirs) != 1 {
		t.Errorf("a sessão sem mensagem deixou uma pasta: %d pastas", len(dirs))
	}

	prov2 := &scriptedProvider{answer: func(provider.Request) string { return "retomada" }}
	if _, _, err := run(t, fakeDeps(prov2), "outra pergunta\n", "--continue"); err != nil {
		t.Fatal(err)
	}
	if got := len(prov2.requests[0].Messages); got != 3 {
		t.Errorf("--continue deveria retomar a sessão com conversa (2 mensagens mais a nova), mandou %d", got)
	}
}

func TestChat_ResumeKeepsTheAgentAndTheModel(t *testing.T) {
	home, _ := isolate(t)
	if err := os.MkdirAll(home.RolesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	revisor := "---\nname: revisor\nmodel: opus\ntools: ler_arquivo\n---\ninstruções do revisor"
	if err := os.WriteFile(filepath.Join(home.RolesDir(), "revisor.md"), []byte(revisor), 0o644); err != nil {
		t.Fatal(err)
	}
	ask := func(input string, args ...string) provider.Request {
		t.Helper()
		prov := &scriptedProvider{answer: func(provider.Request) string { return "ok" }}
		if _, _, err := run(t, fakeDeps(prov), input, args...); err != nil {
			t.Fatal(err)
		}
		return prov.requests[0]
	}

	first := ask("começo\n", "--agent", "revisor", "--model", "sonnet")
	if first.Model != "claude-sonnet-5-5" || !strings.Contains(first.System, "instruções do revisor") {
		t.Fatalf("sessão nova: modelo %q, instruções %q", first.Model, first.System)
	}
	resumed := ask("de novo\n", "--continue")
	if resumed.Model != "claude-sonnet-5-5" || !strings.Contains(resumed.System, "instruções do revisor") {
		t.Errorf("a retomada deveria manter o agente e o modelo: modelo %q, instruções %q", resumed.Model, resumed.System)
	}
	changed := ask("outro modelo\n", "--continue", "--model", "haiku")
	if changed.Model != "claude-haiku-5-5" || !strings.Contains(changed.System, "instruções do revisor") {
		t.Errorf("a flag deveria trocar só o modelo: modelo %q, instruções %q", changed.Model, changed.System)
	}
}

// toolProvider asks for one tool call and then answers with what came back.
type toolProvider struct {
	name  string
	input string
	calls int
}

func (p *toolProvider) Name() string                        { return "fake" }
func (p *toolProvider) Capabilities() provider.Capabilities { return provider.Capabilities{} }

func (p *toolProvider) Complete(_ context.Context, req provider.Request) (*provider.Response, error) {
	p.calls++
	if p.calls == 1 {
		return &provider.Response{Stop: provider.StopToolUse, Message: provider.Message{Role: provider.RoleAssistant, Blocks: []provider.Block{
			{Type: provider.BlockToolUse, ToolUseID: "t1", ToolName: p.name, Input: json.RawMessage(p.input)},
		}}}, nil
	}
	last := req.Messages[len(req.Messages)-1]
	text := "resultado: " + last.Blocks[0].Text
	if req.OnText != nil {
		req.OnText(text)
	}
	return &provider.Response{Stop: provider.StopEndTurn, Message: provider.Message{Role: provider.RoleAssistant,
		Blocks: []provider.Block{{Type: provider.BlockText, Text: text}}}}, nil
}

// Without 'mister install', what a tool needs is downloaded the first time
// the tool is used, and not before.
func TestChat_DownloadsWhatIsMissingOnFirstUse(t *testing.T) {
	home, project := isolate(t)
	t.Setenv("PATH", "/usr/bin")
	if err := os.WriteFile(filepath.Join(project, "a.txt"), []byte("agulha no palheiro\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home.MemoryDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home.MemoryDir(), "portas.md"), []byte("O ambiente usa as portas 8080 e 3307.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := fakeDeps(&scriptedProvider{answer: func(provider.Request) string { return "oi" }})
	deps.EmbeddingSources = fakeSources(t)
	deps.RipgrepSource = fakeRipgrep(t)

	// A conversation that searches nothing in the files downloads no ripgrep;
	// the memory, searched at every message, brings its server.
	_, stderr, err := run(t, deps, "qual a porta?\n/exit\n")
	if err != nil {
		t.Fatal(err)
	}
	local := embed.NewLocal(home.EmbeddingDir())
	local.Sources = deps.EmbeddingSources
	if !local.Installed() || !strings.Contains(stderr, "memória: o servidor de embedding ainda não está no disco: baixando agora") {
		t.Errorf("a primeira busca na memória deveria baixar o servidor de embedding:\n%s", stderr)
	}
	if _, err := os.Stat(home.RipgrepFile()); err == nil || strings.Contains(stderr, "busca:") {
		t.Errorf("sem busca nos arquivos, o ripgrep não deveria ser baixado:\n%s", stderr)
	}

	// The fake ripgrep finds nothing (exit 1), which tells it was the one used.
	searcher := &toolProvider{name: "buscar", input: `{"padrao": "agulha"}`}
	deps.NewProvider = func() (provider.Provider, error) { return searcher, nil }
	stdout, stderr, err := run(t, deps, "procure\n/exit\n")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(home.RipgrepFile()); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("a primeira busca deveria baixar o ripgrep para %s: %v", home.RipgrepFile(), err)
	}
	if !strings.Contains(stderr, "busca: o ripgrep ainda não está no disco: baixando agora") || !strings.Contains(stderr, "busca: ripgrep instalado em") {
		t.Errorf("o download do ripgrep deveria ser avisado:\n%s", stderr)
	}
	if strings.Contains(stdout, "a.txt") {
		t.Errorf("a busca deveria ter usado o ripgrep baixado, não o do sistema nem a busca própria:\n%s", stdout)
	}
	if strings.Contains(stderr, "memória: o servidor de embedding ainda não está no disco") {
		t.Errorf("o servidor de embedding já estava no disco e não deveria ser baixado de novo:\n%s", stderr)
	}
}
