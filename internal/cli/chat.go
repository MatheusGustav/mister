package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/MatheusGustav/mister/internal/agent"
	"github.com/MatheusGustav/mister/internal/browser"
	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/embed"
	"github.com/MatheusGustav/mister/internal/memory"
	"github.com/MatheusGustav/mister/internal/role"
	"github.com/MatheusGustav/mister/internal/tool"
	"github.com/MatheusGustav/mister/internal/web"
)

// ProjectRulesFile holds the rules of a project, at its root. It is the file
// other AI agents read too.
const ProjectRulesFile = "AGENTS.md"

type chatOptions struct {
	resume  bool
	session string
	model   string
	role    string
}

// printer serializes what the host, the threads and the prompt write.
type printer struct {
	mu  sync.Mutex
	out io.Writer
	err io.Writer
	// midLine says the answer being streamed has not ended its line yet.
	midLine bool
}

func (p *printer) text(delta string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprint(p.out, delta)
	p.midLine = !strings.HasSuffix(delta, "\n")
}

func (p *printer) endLine() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.midLine {
		fmt.Fprintln(p.out)
		p.midLine = false
	}
}

func (p *printer) prompt() {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprint(p.err, "› ")
}

func (p *printer) status(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.midLine {
		fmt.Fprintln(p.out)
		p.midLine = false
	}
	fmt.Fprintf(p.err, format+"\n", args...)
}

func runChat(cmd *cobra.Command, deps Deps, opts chatOptions) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	out := &printer{out: cmd.OutOrStdout(), err: cmd.ErrOrStderr()}

	home, err := config.ResolveHome()
	if err != nil {
		return err
	}
	if err := home.Ensure(); err != nil {
		return err
	}
	cfg, err := home.Load()
	if err != nil {
		return err
	}
	project, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("descobrindo a pasta atual: %w", err)
	}
	roles, err := role.Load(home.RolesDir(), filepath.Join(project, ".mister", "agents"))
	if err != nil {
		return err
	}
	ownerRules, err := readOptional(home.RulesFile())
	if err != nil {
		return err
	}
	projectRules, err := readOptional(filepath.Join(project, ProjectRulesFile))
	if err != nil {
		return err
	}
	prov, err := deps.NewProvider()
	if err != nil {
		return err
	}

	local := embed.NewLocal(home.EmbeddingDir())
	local.Sources = deps.EmbeddingSources
	defer local.Close()
	if !local.Installed() {
		out.status("memória: o servidor de embedding não está instalado; as notas são gravadas mas não buscadas. Rode 'mister install'.")
	}
	mem := memory.Open(home.MemoryDir(), home.IndexFile(), local, embed.Thresholds())
	registry := tool.NewRegistry()
	tools := append(tool.FileTools(), tool.SearchTools(home.RipgrepFile())...)
	tools = append(tools, tool.ShellTools(tool.DefaultShellLimits())...)
	tools = append(tools, web.NewExa().Tool())
	tools = append(tools, mem.Tools()...)
	shots := browser.New(home.BrowserDir(), cfg.BrowserTabs,
		time.Duration(cfg.BrowserIdleMinutes)*time.Minute, time.Duration(cfg.BrowserLoadSeconds)*time.Second)
	shots.Source = deps.BrowserSource
	shots.Logf = func(format string, args ...any) { out.status("navegador: "+format, args...) }
	defer shots.Close()
	tools = append(tools, shots.Tool())
	if err := registry.Register(tools...); err != nil {
		return err
	}

	sessionID, saved, err := pickSession(home, project, opts)
	if err != nil {
		return err
	}
	// A resumed session keeps the agent and the model it started with, unless
	// the flags ask for others.
	if saved != nil {
		if opts.role == "" {
			opts.role = saved.Host.Role
		}
		if opts.model == "" {
			opts.model = saved.Host.Model
		}
	}
	store := agent.OpenStore(home.SessionsDir(), sessionID)
	past := memory.OpenSessions(home.SessionsDir(), local, embed.SessionThreshold, sessionID)
	// Sessions closed since the last run are indexed in the background, so the
	// first message does not wait for it.
	var indexing sync.WaitGroup
	indexing.Go(func() { _ = past.Sync(ctx) })
	defer func() {
		cancel()
		indexing.Wait()
	}()

	rt, err := agent.NewRuntime(ctx, agent.Options{
		Config:       cfg,
		Provider:     prov,
		Registry:     registry,
		Authorize:    tool.AllowAll,
		Roles:        roles,
		Store:        store,
		SessionID:    sessionID,
		Project:      project,
		OwnerRules:   ownerRules,
		ProjectRules: projectRules,
		Recall:       recallFrom(mem, past),
		HostRole:     opts.role,
		HostModel:    opts.model,
		Hooks: agent.Hooks{
			OnText: func(id, delta string) {
				if id == agent.HostID {
					out.text(delta)
				}
			},
			OnToolCall: func(id string, call tool.Call) {
				out.status("  › [%s] %s %s", id, call.Name, shorten(string(call.Input), 160))
			},
			OnToolResult: func(id string, call tool.Call, res tool.Result) {
				if res.IsError {
					out.status("  ✗ [%s] %s: %s", id, call.Name, shorten(res.Content, 200))
				}
			},
			OnNotice: func(id, text string) { out.status("  · [%s] %s", id, text) },
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := rt.Close(); err != nil {
			out.status("não consegui gravar a sessão ao sair: %v", err)
		}
	}()
	if saved != nil {
		if err := rt.Resume(*saved); err != nil {
			return err
		}
		out.status("sessão %s retomada (%d threads)", sessionID, len(saved.Threads))
	} else {
		out.status("sessão %s em %s — /help mostra os comandos", sessionID, project)
	}

	return chatLoop(ctx, cancel, cmd.InOrStdin(), out, rt)
}

// recallFrom joins the two memories an agent receives by itself: the notes
// and what was said in past sessions.
func recallFrom(notes *memory.Store, past *memory.Sessions) agent.RecallFunc {
	return func(ctx context.Context, text string, skip map[string]bool) (string, []string) {
		noteText, names := notes.RecallText(ctx, text, skip)
		pastText, keys := past.RecallText(ctx, text, skip)
		switch {
		case noteText == "":
			return pastText, keys
		case pastText == "":
			return noteText, names
		}
		return noteText + "\n\n" + pastText, append(names, keys...)
	}
}

// chatLoop reads lines and hands them to the host. Ctrl+C cancels the turn in
// progress; with no turn in progress, it leaves.
func chatLoop(ctx context.Context, cancel context.CancelFunc, in io.Reader, out *printer, rt *agent.Runtime) error {
	var turnMu sync.Mutex
	var cancelTurn context.CancelFunc

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-interrupts:
				turnMu.Lock()
				stop := cancelTurn
				turnMu.Unlock()
				if stop == nil {
					cancel()
					return
				}
				stop()
			}
		}
	})

	// The reader blocks on the terminal and cannot be interrupted: when the
	// session ends first, this goroutine ends with the process.
	lines := make(chan string)
	go func() {
		defer close(lines)
		reader := bufio.NewReader(in)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	defer cancel()
	for {
		out.prompt()
		var line string
		var ok bool
		select {
		case <-ctx.Done():
			return nil
		case line, ok = <-lines:
			if !ok {
				return nil
			}
		}
		line = strings.TrimSpace(line)
		switch line {
		case "":
			continue
		case "/exit":
			return nil
		case "/memory":
			out.status("%s", rt.Report())
			continue
		case "/help":
			out.status("/memory  agentes, memória usada por agente e fila de comandos pesados\n/exit    encerra\nCtrl+C   interrompe a resposta em andamento")
			continue
		}

		turnCtx, stop := context.WithCancel(ctx)
		turnMu.Lock()
		cancelTurn = stop
		turnMu.Unlock()
		_, err := rt.Send(turnCtx, line)
		turnMu.Lock()
		cancelTurn = nil
		turnMu.Unlock()
		stop()
		out.endLine()

		switch {
		case err == nil:
		case errors.Is(err, context.Canceled):
			out.status("(interrompido)")
		default:
			out.status("erro: %v", err)
		}
	}
}

// pickSession decides which session the chat runs in: a saved one, when asked
// to resume, or a new one.
func pickSession(home config.Home, project string, opts chatOptions) (string, *agent.SessionMeta, error) {
	sessions, err := agent.ListSessions(home.SessionsDir())
	if err != nil {
		return "", nil, err
	}
	switch {
	case opts.session != "":
		for _, s := range sessions {
			if s.ID == opts.session {
				if s.Project != project {
					return "", nil, fmt.Errorf("a sessão %s é da pasta %s; entre nela para retomar", s.ID, s.Project)
				}
				return s.ID, &s, nil
			}
		}
		return "", nil, fmt.Errorf("não existe a sessão %q; veja 'mister sessions'", opts.session)
	case opts.resume:
		for _, s := range sessions {
			if s.Project == project {
				return s.ID, &s, nil
			}
		}
		return "", nil, fmt.Errorf("nenhuma sessão guardada para a pasta %s", project)
	}
	return agent.NewSessionID(time.Now()), nil, nil
}

func readOptional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lendo %s: %w", path, err)
	}
	return string(data), nil
}

// shorten shows a value on one line: JSON is compacted, long text is cut.
func shorten(text string, limit int) string {
	if json.Valid([]byte(text)) {
		var buf bytes.Buffer
		if json.Compact(&buf, []byte(text)) == nil {
			text = buf.String()
		}
	}
	s := strings.Join(strings.Fields(text), " ")
	if len(s) > limit {
		return strings.ToValidUTF8(s[:limit], "") + "…"
	}
	return s
}
