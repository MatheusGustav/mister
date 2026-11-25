package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/role"
	"github.com/MatheusGustav/mister/internal/tool"
)

// newProject creates a git repository with one commit.
func newProject(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "LEIAME.md"), []byte("projeto de teste\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"-c", "user.name=teste", "-c", "user.email=teste@exemplo.test", "add", "."},
		{"-c", "user.name=teste", "-c", "user.email=teste@exemplo.test", "commit", "-q", "-m", "início"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

type session struct {
	rt       *Runtime
	provider *fakeProvider
	project  string
	store    *Store
	opts     Options
}

func isThread(req provider.Request) bool {
	return strings.HasPrefix(req.System, "Você escreve código")
}

// newSession builds a runtime over a real git project. The handlers answer for
// the host and for the threads; n counts the requests of each kind.
func newSession(t *testing.T, cfg config.Config, host, threads func(n int, req provider.Request) (*provider.Response, error)) *session {
	t.Helper()
	project := newProject(t)
	roles, err := role.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	tools := append(tool.FileTools(), tool.SearchTools("", nil)...)
	tools = append(tools, tool.ShellTools(tool.DefaultShellLimits())...)
	tools = append(tools, tool.Tool{
		Name:   WebSearchTool,
		Schema: tool.Object(nil, map[string]any{}),
		Run: func(context.Context, *tool.Env, json.RawMessage) tool.Result {
			return tool.OK("busca do cliente")
		},
	})
	if err := registry.Register(tools...); err != nil {
		t.Fatal(err)
	}
	store := OpenStore(t.TempDir(), "sessao-teste")

	var mu sync.Mutex
	hostN, threadN := 0, 0
	prov := &fakeProvider{handler: func(_ int, req provider.Request) (*provider.Response, error) {
		mu.Lock()
		var n int
		handler := host
		if isThread(req) {
			n, handler = threadN, threads
			threadN++
		} else {
			n = hostN
			hostN++
		}
		mu.Unlock()
		return handler(n, req)
	}}
	opts := Options{
		Config: cfg, Provider: prov, Registry: registry, Roles: roles, Store: store,
		SessionID: "sessao-teste", Project: project, ProjectRules: "regra do projeto: testes sempre",
	}
	rt, err := NewRuntime(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &session{rt: rt, provider: prov, project: project, store: store, opts: opts}
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.WebSearch = "exa"
	return cfg
}

func delegate(name, task string) *provider.Response {
	return use(toolUse{id: "d-" + name, name: "delegar", input: map[string]any{"nome": name, "tarefa": task, "modelo": "sonnet", "esforco": "medium"}})
}

func wait() *provider.Response {
	return use(toolUse{id: "w", name: "aguardar_threads", input: map[string]any{"timeout_segundos": 30}})
}

func TestRuntime_DelegateRunsThreadInItsOwnWorktree(t *testing.T) {
	t.Parallel()
	var report string
	s := newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return delegate("ajuste", "Pedido: crie o arquivo novo.txt"), nil
			case 1:
				return wait(), nil
			default:
				report = lastMessage(req)
				return say("revisado"), nil
			}
		},
		func(n int, req provider.Request) (*provider.Response, error) {
			if n == 0 {
				return use(toolUse{id: "w1", name: "escrever_arquivo", input: map[string]any{"caminho": "novo.txt", "conteudo": "feito pelo thread"}}), nil
			}
			return say("Relatório: criei novo.txt"), nil
		})

	got, err := s.rt.Send(context.Background(), "crie o arquivo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "revisado" {
		t.Errorf("resposta do host = %q", got)
	}
	if !strings.Contains(report, "Relatório: criei novo.txt") || !strings.Contains(report, "thread ajuste (terminou)") {
		t.Errorf("o relatório não voltou ao host:\n%s", report)
	}

	worktree := s.project + "-wt-ajuste"
	if data, err := os.ReadFile(filepath.Join(worktree, "novo.txt")); err != nil || string(data) != "feito pelo thread" {
		t.Errorf("o arquivo deveria estar na worktree do thread: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(s.project, "novo.txt")); !os.IsNotExist(err) {
		t.Errorf("o arquivo não deveria aparecer na pasta principal: %v", err)
	}
	if branch, err := git(context.Background(), worktree, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || branch != "wt/ajuste" {
		t.Errorf("branch da worktree = %q, %v; quero wt/ajuste", branch, err)
	}

	var hostReq, threadReq provider.Request
	for i := range s.provider.count() {
		req := s.provider.request(i)
		if isThread(req) && threadReq.System == "" {
			threadReq = req
		} else if !isThread(req) && hostReq.System == "" {
			hostReq = req
		}
	}
	for _, spec := range threadReq.Tools {
		if spec.Name == "delegar" || spec.Name == "aguardar_threads" || spec.Name == "continuar_thread" {
			t.Errorf("o thread recebeu a ferramenta %s", spec.Name)
		}
	}
	hasDelegate := false
	for _, spec := range hostReq.Tools {
		hasDelegate = hasDelegate || spec.Name == "delegar"
	}
	if !hasDelegate {
		t.Error("o host deveria ter a ferramenta delegar")
	}
	task := threadReq.Messages[0].Text()
	for _, want := range []string{"Pasta: " + worktree, "branch wt/ajuste", "Pedido: crie o arquivo novo.txt"} {
		if !strings.Contains(task, want) {
			t.Errorf("a tarefa do thread não contém %q:\n%s", want, task)
		}
	}
	if threadReq.Model != "claude-sonnet-5-5" || threadReq.Effort != "medium" {
		t.Errorf("modelo e esforço escolhidos pelo host não chegaram: %q %q", threadReq.Model, threadReq.Effort)
	}
	for _, req := range []provider.Request{hostReq, threadReq} {
		if !strings.Contains(req.System, "regra do projeto: testes sempre") {
			t.Error("as regras do projeto deveriam estar nas instruções dos dois agentes")
		}
	}
	if !strings.Contains(hostReq.Messages[0].Text(), "Projeto: "+s.project) {
		t.Errorf("a primeira mensagem do host deveria dizer o projeto:\n%s", hostReq.Messages[0].Text())
	}
}

func TestRuntime_ThreadIsConfinedToItsWorktree(t *testing.T) {
	t.Parallel()
	var results []string
	var s *session
	s = newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return delegate("preso", "tente sair"), nil
			case 1:
				return wait(), nil
			default:
				return say("ok"), nil
			}
		},
		func(n int, req provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return use(
					toolUse{id: "a", name: "escrever_arquivo", input: map[string]any{"caminho": filepath.Join(s.project, "invadido.txt"), "conteudo": "x"}},
					toolUse{id: "b", name: "editar_arquivo", input: map[string]any{"caminho": "../proj/LEIAME.md", "antigo": "projeto", "novo": "x"}},
					toolUse{id: "c", name: "ler_arquivo", input: map[string]any{"caminho": filepath.Join(s.project, "LEIAME.md")}},
					toolUse{id: "d", name: "delegar", input: map[string]any{"nome": "neto", "tarefa": "x"}},
				), nil
			default:
				for _, b := range req.Messages[len(req.Messages)-1].Blocks {
					results = append(results, fmt.Sprintf("%v:%s", b.IsError, b.Text))
				}
				return say("não consegui sair"), nil
			}
		})

	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("quero 4 resultados, veio %d: %v", len(results), results)
	}
	for i, want := range []string{"só mexe dentro", "só mexe dentro", "só mexe dentro", "não tem a ferramenta"} {
		if !strings.HasPrefix(results[i], "true:") || !strings.Contains(results[i], want) {
			t.Errorf("chamada %d deveria ser recusada com %q: %s", i, want, results[i])
		}
	}
	if _, err := os.Stat(filepath.Join(s.project, "invadido.txt")); !os.IsNotExist(err) {
		t.Error("o thread gravou na pasta principal")
	}
	if data, _ := os.ReadFile(filepath.Join(s.project, "LEIAME.md")); string(data) != "projeto de teste\n" {
		t.Errorf("o thread alterou a pasta principal: %q", data)
	}
}

func TestRuntime_HostReadsThreadWorktreeButDoesNotWriteThere(t *testing.T) {
	t.Parallel()
	var results []string
	var s *session
	s = newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			wt := s.project + "-wt-revisao"
			switch n {
			case 0:
				return delegate("revisao", "crie a.txt"), nil
			case 1:
				return wait(), nil
			case 2:
				return use(
					toolUse{id: "r", name: "ler_arquivo", input: map[string]any{"caminho": filepath.Join(wt, "a.txt")}},
					toolUse{id: "w", name: "escrever_arquivo", input: map[string]any{"caminho": filepath.Join(wt, "a.txt"), "conteudo": "do host"}},
				), nil
			default:
				for _, b := range req.Messages[len(req.Messages)-1].Blocks {
					results = append(results, fmt.Sprintf("%v:%s", b.IsError, b.Text))
				}
				return say("ok"), nil
			}
		},
		func(n int, _ provider.Request) (*provider.Response, error) {
			if n == 0 {
				return use(toolUse{id: "w1", name: "escrever_arquivo", input: map[string]any{"caminho": "a.txt", "conteudo": "do thread"}}), nil
			}
			return say("feito"), nil
		})

	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !strings.HasPrefix(results[0], "false:") || !strings.Contains(results[0], "do thread") {
		t.Errorf("o host deveria conseguir ler a worktree do thread: %v", results)
	}
	if len(results) == 2 && !strings.HasPrefix(results[1], "true:") {
		t.Errorf("o host não deveria gravar na worktree do thread: %s", results[1])
	}
}

func TestRuntime_DelegateValidation(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.MaxThreads = 1
	release := make(chan struct{})
	var results []string
	s := newSession(t, cfg,
		func(n int, req provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return delegate("primeiro", "fique rodando"), nil
			case 1:
				return use(
					toolUse{id: "1", name: "delegar", input: map[string]any{"nome": "segundo", "tarefa": "x"}},
					toolUse{id: "2", name: "delegar", input: map[string]any{"nome": "primeiro", "tarefa": "x"}},
					toolUse{id: "3", name: "delegar", input: map[string]any{"nome": "Nome Ruim", "tarefa": "x"}},
					toolUse{id: "4", name: "delegar", input: map[string]any{"nome": "vazio", "tarefa": " "}},
					toolUse{id: "5", name: "delegar", input: map[string]any{"nome": "outro", "tarefa": "x", "agente": "inexistente"}},
					toolUse{id: "6", name: "delegar", input: map[string]any{"nome": "esforco", "tarefa": "x", "esforco": "turbo"}},
					toolUse{id: "7", name: "continuar_thread", input: map[string]any{"nome": "primeiro", "mensagem": "oi"}},
					toolUse{id: "8", name: "continuar_thread", input: map[string]any{"nome": "ninguem", "mensagem": "oi"}},
				), nil
			default:
				for _, b := range req.Messages[len(req.Messages)-1].Blocks {
					results = append(results, b.Text)
				}
				close(release)
				return say("ok"), nil
			}
		},
		func(int, provider.Request) (*provider.Response, error) {
			<-release
			return say("liberado"), nil
		})

	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	wants := []string{"limite desta máquina", "já existe um thread", "inválido", "veio vazia", "não existe", "esforço \"turbo\" inválido", "ainda está rodando", "não existe thread"}
	if len(results) != len(wants) {
		t.Fatalf("quero %d resultados, veio %d: %v", len(wants), len(results), results)
	}
	for i, want := range wants {
		if !strings.Contains(results[i], want) {
			t.Errorf("chamada %d: quero %q em %q", i+1, want, results[i])
		}
	}
	if _, err := os.Stat(s.project + "-wt-segundo"); !os.IsNotExist(err) {
		t.Error("uma delegação recusada não deveria deixar worktree")
	}
}

func TestRuntime_ContinueThreadKeepsItsConversation(t *testing.T) {
	t.Parallel()
	var secondRound provider.Request
	s := newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return delegate("ajuste", "faça a primeira parte"), nil
			case 1:
				return wait(), nil
			case 2:
				return use(toolUse{id: "c", name: "continuar_thread", input: map[string]any{"nome": "ajuste", "mensagem": "faltou o teste"}}), nil
			case 3:
				return wait(), nil
			default:
				return say(lastMessage(req)), nil
			}
		},
		func(n int, req provider.Request) (*provider.Response, error) {
			if n == 0 {
				return say("primeiro relatório"), nil
			}
			secondRound = req
			return say("segundo relatório"), nil
		})

	got, err := s.rt.Send(context.Background(), "vai")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "segundo relatório") || strings.Contains(got, "primeiro relatório") {
		t.Errorf("a segunda espera deveria trazer só o relatório novo:\n%s", got)
	}
	if len(secondRound.Messages) != 3 || secondRound.Messages[2].Text() != "faltou o teste" {
		t.Errorf("o thread deveria continuar a mesma conversa: %d mensagens", len(secondRound.Messages))
	}
}

func TestRuntime_ThreadsOfTheSameRoleShareThePrefix(t *testing.T) {
	t.Parallel()
	s := newSession(t, testConfig(),
		func(n int, _ provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return use(
					toolUse{id: "1", name: "delegar", input: map[string]any{"nome": "um", "tarefa": "tarefa um"}},
					toolUse{id: "2", name: "delegar", input: map[string]any{"nome": "dois", "tarefa": "tarefa dois"}},
				), nil
			case 1, 2:
				return wait(), nil
			default:
				return say("ok"), nil
			}
		},
		func(int, provider.Request) (*provider.Response, error) { return say("feito"), nil })

	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	var threads []provider.Request
	for i := range s.provider.count() {
		if req := s.provider.request(i); isThread(req) {
			threads = append(threads, req)
		}
	}
	if len(threads) != 2 {
		t.Fatalf("quero 2 pedidos de thread, veio %d", len(threads))
	}
	a, b := threads[0], threads[1]
	if a.System != b.System || a.Model != b.Model || a.Effort != b.Effort {
		t.Error("threads do mesmo agente deveriam ter as mesmas instruções, modelo e esforço")
	}
	aTools, _ := json.Marshal(a.Tools)
	bTools, _ := json.Marshal(b.Tools)
	if string(aTools) != string(bTools) {
		t.Error("threads do mesmo agente deveriam ter as mesmas definições de ferramenta, na mesma ordem")
	}
	if strings.Contains(a.System, "tarefa um") || strings.Contains(a.System, "-wt-") {
		t.Error("o que é de cada thread (tarefa, pasta) não pode estar nas instruções fixas")
	}
	if a.Messages[0].Text() == b.Messages[0].Text() {
		t.Error("cada thread deveria receber a própria tarefa na primeira mensagem")
	}
}

func TestRuntime_HostIsToldAboutThreadFinishedMeanwhile(t *testing.T) {
	t.Parallel()
	s := newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			if n == 0 {
				return delegate("solto", "rápido"), nil
			}
			return say("ok"), nil
		},
		func(int, provider.Request) (*provider.Response, error) { return say("acabei"), nil })

	if _, err := s.rt.Send(context.Background(), "delegue e volte"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.rt.mu.Lock()
		status := s.rt.threads["solto"].meta.Status
		s.rt.mu.Unlock()
		if status == StatusDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("o thread não terminou")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := s.rt.Send(context.Background(), "e agora?"); err != nil {
		t.Fatal(err)
	}
	var hostReqs []provider.Request
	for i := range s.provider.count() {
		if req := s.provider.request(i); !isThread(req) {
			hostReqs = append(hostReqs, req)
		}
	}
	// The thread may finish before or after the host's first turn ends: either
	// way the host hears about it once, in the next message it receives.
	notices := 0
	for _, msg := range hostReqs[len(hostReqs)-1].Messages {
		for _, b := range msg.Blocks {
			if strings.Contains(b.Text, "[aviso] o thread solto terminou") {
				notices++
			}
		}
	}
	if notices != 1 {
		t.Errorf("o host deveria ser avisado uma vez do thread que terminou, foi %d", notices)
	}
	if !strings.Contains(s.rt.Report(), "relatório ainda não lido") {
		t.Errorf("o relatório de agentes deveria marcar o que falta ler:\n%s", s.rt.Report())
	}
}

func TestRuntime_ResumeRestoresHostAndThreads(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	s := newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return use(
					toolUse{id: "1", name: "delegar", input: map[string]any{"nome": "pronto", "tarefa": "termine"}},
					toolUse{id: "2", name: "delegar", input: map[string]any{"nome": "preso", "tarefa": "espere"}},
				), nil
			case 1:
				return wait(), nil
			default:
				return say("um terminou"), nil
			}
		},
		func(_ int, req provider.Request) (*provider.Response, error) {
			if strings.Contains(req.Messages[0].Text(), "espere") {
				<-release
				return nil, context.Canceled
			}
			return say("relatório pronto"), nil
		})
	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	// What the session file says while the thread still runs is what a crash leaves behind.
	crashed, err := s.store.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	close(release)

	registry := tool.NewRegistry()
	if err := registry.Register(tool.FileTools()...); err != nil {
		t.Fatal(err)
	}
	var resumedReq provider.Request
	opts := s.opts
	opts.Registry = registry
	opts.Provider = &fakeProvider{handler: func(_ int, req provider.Request) (*provider.Response, error) {
		resumedReq = req
		return say("continuei"), nil
	}}
	rt, err := NewRuntime(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := rt.Resume(crashed); err != nil {
		t.Fatal(err)
	}

	rt.mu.Lock()
	preso, pronto := rt.threads["preso"].meta, rt.threads["pronto"].meta
	rt.mu.Unlock()
	if preso.Status != StatusInterrupted || preso.Reported {
		t.Errorf("o thread que rodava deveria voltar como interrompido e por ler: %+v", preso)
	}
	if pronto.Status != StatusDone || !pronto.Reported {
		t.Errorf("o thread terminado e lido deveria voltar igual: %+v", pronto)
	}
	if _, err := rt.Send(context.Background(), "onde paramos?"); err != nil {
		t.Fatal(err)
	}
	if n := len(resumedReq.Messages); n != 7 {
		t.Errorf("o host retomado deveria mandar as 6 mensagens salvas mais a pergunta nova, mandou %d", n)
	}
	if strings.Contains(resumedReq.Messages[len(resumedReq.Messages)-1].Text(), "Projeto: ") {
		t.Error("a apresentação do projeto só vai na primeira mensagem da sessão")
	}
}

func TestRuntime_CloseStopsRunningThreads(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	s := newSession(t, testConfig(),
		func(n int, _ provider.Request) (*provider.Response, error) {
			if n == 0 {
				return delegate("longo", "rode para sempre"), nil
			}
			return say("ok"), nil
		},
		func(n int, _ provider.Request) (*provider.Response, error) {
			if n == 0 {
				close(started)
			}
			return use(toolUse{id: "s", name: "rodar_comando", input: map[string]any{"comando": "sleep 60"}}), nil
		})
	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	<-started
	done := make(chan error, 1)
	go func() { done <- s.rt.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Close não parou o thread")
	}
	meta, err := s.store.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Threads) != 1 || meta.Threads[0].Status != StatusInterrupted {
		t.Errorf("o thread parado deveria ficar gravado como interrompido: %+v", meta.Threads)
	}
}

func TestRuntime_WebSearch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mode       string
		native     bool
		wantNative bool
	}{
		{name: "provedor com pesquisa própria usa a nativa", mode: "auto", native: true, wantNative: true},
		{name: "provedor sem pesquisa própria usa a Exa", mode: "auto", native: false, wantNative: false},
		{name: "configurado para a Exa", mode: "exa", native: true, wantNative: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.WebSearch = tt.mode
			cfg.WebSearchMaxUses = 3
			s := newSession(t, cfg,
				func(int, provider.Request) (*provider.Response, error) { return say("ok"), nil },
				func(int, provider.Request) (*provider.Response, error) { return say("ok"), nil })
			s.provider.native = tt.native
			// The capability is read when the agent is built: build the host again.
			host, err := s.rt.newAgent(s.rt.hostMeta, s.opts.Roles[role.Host], "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := host.Run(context.Background(), "pesquise"); err != nil {
				t.Fatal(err)
			}
			req := s.provider.request(0)
			clientTool := false
			for _, spec := range req.Tools {
				clientTool = clientTool || spec.Name == WebSearchTool
			}
			if tt.wantNative {
				if req.WebSearch == nil || req.WebSearch.MaxUses != 3 {
					t.Errorf("quero a pesquisa nativa com limite 3: %+v", req.WebSearch)
				}
				if clientTool {
					t.Error("com a pesquisa nativa, a ferramenta do cliente não deveria ir junto")
				}
			} else {
				if req.WebSearch != nil {
					t.Error("não deveria pedir a pesquisa nativa")
				}
				if !clientTool {
					t.Error("a ferramenta de pesquisa do cliente deveria estar disponível")
				}
			}
		})
	}
}

func TestRuntime_DelegateNeedsGit(t *testing.T) {
	t.Parallel()
	var result string
	s := newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			if n == 0 {
				return delegate("semgit", "x"), nil
			}
			result = lastMessage(req)
			return say("ok"), nil
		},
		func(int, provider.Request) (*provider.Response, error) { return say("x"), nil })
	if err := os.RemoveAll(filepath.Join(s.project, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "repositório git") {
		t.Errorf("sem git, delegar deveria explicar o motivo: %s", result)
	}
}

func ask(question string) *provider.Response {
	return use(toolUse{id: "p", name: AskHostTool, input: map[string]any{"pergunta": question}})
}

func answer(name, text string) *provider.Response {
	return use(toolUse{id: "r-" + name, name: "continuar_thread", input: map[string]any{"nome": name, "mensagem": text}})
}

func threadStatus(s *session, name string) string {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	return s.rt.threads[name].meta.Status
}

// A thread stops in the middle of its task with a question, the host receives
// it, answers, and the thread carries on in the same round with the answer.
func TestRuntime_ThreadAsksTheHostAndCarriesOn(t *testing.T) {
	t.Parallel()
	var question, afterAnswer string
	var s *session
	s = newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			switch n {
			case 0:
				return delegate("cobranca", "ajuste a cobrança"), nil
			case 1:
				return wait(), nil
			case 2:
				question = lastMessage(req)
				if got := threadStatus(s, "cobranca"); got != StatusWaiting {
					t.Errorf("com a pergunta feita o thread deveria estar %q, está %q", StatusWaiting, got)
				}
				if report := s.rt.Report(); !strings.Contains(report, "esperando resposta") || !strings.Contains(report, "Pergunta: cobra no cancelamento?") {
					t.Errorf("listar_threads deveria mostrar o thread parado e a pergunta:\n%s", report)
				}
				return answer("cobranca", "não cobra"), nil
			case 3:
				return wait(), nil
			default:
				return say(lastMessage(req)), nil
			}
		},
		func(n int, req provider.Request) (*provider.Response, error) {
			if n == 0 {
				return ask("  cobra no cancelamento?  "), nil
			}
			afterAnswer = lastMessage(req)
			return say("relatório: fiz sem cobrar"), nil
		})

	got, err := s.rt.Send(context.Background(), "vai")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"thread cobranca parou com uma pergunta", "cobra no cancelamento?", "Responda com continuar_thread"} {
		if !strings.Contains(question, want) {
			t.Errorf("a pergunta deveria chegar ao host por aguardar_threads com %q:\n%s", want, question)
		}
	}
	if afterAnswer != "Resposta do host:\nnão cobra" {
		t.Errorf("o thread deveria receber a resposta como resultado da ferramenta: %q", afterAnswer)
	}
	if !strings.Contains(got, "relatório: fiz sem cobrar") || !strings.Contains(got, "(terminou)") {
		t.Errorf("depois da resposta o thread deveria terminar a mesma rodada:\n%s", got)
	}
	meta, err := s.store.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if th := meta.Threads[0]; th.Status != StatusDone || th.Question != "" || th.Asked {
		t.Errorf("a pergunta respondida não deveria ficar gravada: %+v", th)
	}
}

func TestRuntime_WaitingThread(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.MaxThreads = 1
	var results []string
	var s *session
	s = newSession(t, cfg,
		func(n int, req provider.Request) (*provider.Response, error) {
			results = append(results, lastMessage(req))
			switch n {
			case 0:
				return delegate("duvida", "tarefa com dúvida"), nil
			case 1:
				return wait(), nil
			case 2:
				// The question was read: waiting again does not block nor repeat it.
				return wait(), nil
			case 3:
				return delegate("outro", "outra tarefa"), nil
			case 4:
				return use(toolUse{id: "h", name: AskHostTool, input: map[string]any{"pergunta": "posso?"}}), nil
			default:
				return say("fim do turno"), nil
			}
		},
		func(int, provider.Request) (*provider.Response, error) { return ask("qual o prazo?"), nil })

	if _, err := s.rt.Send(context.Background(), "vai"); err != nil {
		t.Fatal(err)
	}
	if len(results) < 6 {
		t.Fatalf("o host fez %d pedidos, quero 6", len(results))
	}
	if !strings.Contains(results[2], "qual o prazo?") {
		t.Errorf("a primeira espera deveria trazer a pergunta:\n%s", results[2])
	}
	if strings.Contains(results[3], "qual o prazo?") || !strings.Contains(results[3], "Parado esperando a sua resposta (continuar_thread): duvida") {
		t.Errorf("a segunda espera deveria voltar na hora, sem repetir a pergunta e lembrando quem espera:\n%s", results[3])
	}
	if !strings.Contains(results[4], "limite desta máquina") {
		t.Errorf("o thread que espera resposta guarda o seu lugar no limite:\n%s", results[4])
	}
	if !strings.Contains(results[5], `o agente "host" não tem a ferramenta "perguntar_host"`) {
		t.Errorf("o host não pergunta a si mesmo:\n%s", results[5])
	}
	// A custom host role that lists the tool is refused by the tool itself.
	asked, _ := s.rt.opts.Registry.Get(AskHostTool)
	if res := asked.Run(context.Background(), &tool.Env{AgentID: HostID}, json.RawMessage(`{"pergunta":"posso?"}`)); !res.IsError || !strings.Contains(res.Content, "é para um thread perguntar a você") {
		t.Errorf("a ferramenta deveria recusar o host: %+v", res)
	}

	// The host ended its turn without answering: the next message reminds
	// nothing, because the question was already read.
	meta, err := s.store.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if th := meta.Threads[0]; th.Status != StatusWaiting || th.Question != "qual o prazo?" || !th.Asked {
		t.Errorf("o thread parado deveria ficar gravado com a pergunta: %+v", th)
	}

	// A session closed with the question pending tells the host what it was.
	if err := s.rt.Close(); err != nil {
		t.Fatal(err)
	}
	closed, err := s.store.LoadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if th := closed.Threads[0]; th.Status != StatusInterrupted || !strings.Contains(th.Report, "esperava a resposta desta pergunta: qual o prazo?") || th.Question != "" {
		t.Errorf("fechar com a pergunta pendente deveria guardar a pergunta no relatório: %+v", th)
	}

	// A crash leaves the waiting status on disk: resuming turns it into a
	// report with the question.
	opts := s.opts
	opts.Registry = tool.NewRegistry()
	if err := opts.Registry.Register(tool.FileTools()...); err != nil {
		t.Fatal(err)
	}
	opts.Provider = &fakeProvider{handler: func(int, provider.Request) (*provider.Response, error) { return say("ok"), nil }}
	rt, err := NewRuntime(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if err := rt.Resume(meta); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	resumed := rt.threads["duvida"].meta
	rt.mu.Unlock()
	if resumed.Status != StatusInterrupted || resumed.Reported || resumed.Question != "" ||
		!strings.Contains(resumed.Report, "esperava a resposta desta pergunta: qual o prazo?") {
		t.Errorf("o thread que esperava deveria voltar interrompido, com a pergunta no relatório: %+v", resumed)
	}
}

func TestRuntime_HostIsToldAboutAQuestionAskedMeanwhile(t *testing.T) {
	t.Parallel()
	s := newSession(t, testConfig(),
		func(n int, req provider.Request) (*provider.Response, error) {
			if n == 0 {
				return delegate("solto", "tarefa"), nil
			}
			return say("ok"), nil
		},
		func(int, provider.Request) (*provider.Response, error) { return ask("pode apagar?"), nil })

	if _, err := s.rt.Send(context.Background(), "delegue e volte"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for threadStatus(s, "solto") != StatusWaiting {
		if time.Now().After(deadline) {
			t.Fatal("o thread não chegou a perguntar")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for range 2 {
		if _, err := s.rt.Send(context.Background(), "e agora?"); err != nil {
			t.Fatal(err)
		}
	}
	notices := 0
	for i := range s.provider.count() {
		req := s.provider.request(i)
		if isThread(req) {
			continue
		}
		last := req.Messages[len(req.Messages)-1]
		for _, b := range last.Blocks {
			if strings.Contains(b.Text, "[aviso] o thread solto parou com uma pergunta") {
				notices++
			}
		}
	}
	if notices != 1 {
		t.Errorf("o host deveria ser avisado uma vez da pergunta, foi %d", notices)
	}
}
