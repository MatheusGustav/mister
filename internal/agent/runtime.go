package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MatheusGustav/mister/internal/config"
	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/role"
	"github.com/MatheusGustav/mister/internal/tool"
)

// HostID is the id of the main agent of a session.
const HostID = "host"

// WebSearchTool is the tool name a role lists to get web search, whether the
// provider's native one or the client-side one.
const WebSearchTool = "pesquisar_web"

type Options struct {
	Config    config.Config
	Provider  provider.Provider
	Registry  *tool.Registry
	Authorize tool.Authorizer
	Roles     map[string]role.Role
	Store     *Store
	SessionID string
	// Project is the directory the host works in.
	Project      string
	OwnerRules   string
	ProjectRules string
	Hooks        Hooks
	Recall       RecallFunc
	// HostRole and HostModel override the defaults ("host" and the role's model).
	HostRole  string
	HostModel string
	Now       func() time.Time
}

// Runtime owns the agents of one session: the host and the threads it
// delegates to, each thread running in its own goroutine and git worktree.
type Runtime struct {
	opts  Options
	exec  *tool.Executor
	heavy *tool.HeavyQueue
	host  *Agent
	// events wakes whoever waits for a thread to finish. The thread table is
	// the source of truth; the channel only carries the wake-up.
	events chan struct{}
	wg     sync.WaitGroup

	mu       sync.Mutex
	hostMeta AgentMeta
	created  time.Time
	started  bool
	threads  map[string]*thread
	order    []string
}

type thread struct {
	agent  *Agent
	cancel context.CancelFunc
	// answer delivers the host's answer to a waiting thread. Guarded by Runtime.mu.
	answer chan string

	// Guarded by Runtime.mu.
	meta     AgentMeta
	noticed  bool
	since    time.Time
	finished time.Time
}

type storeJournal struct {
	store *Store
	id    string
}

func (j storeJournal) Append(msg provider.Message) error   { return j.store.Append(j.id, msg) }
func (j storeJournal) Reset(msgs []provider.Message) error { return j.store.Reset(j.id, msgs) }

// NewRuntime builds the session and its host agent, and registers the
// delegation tools in the registry.
func NewRuntime(ctx context.Context, opts Options) (*Runtime, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Authorize == nil {
		opts.Authorize = tool.AllowAll
	}
	if opts.HostRole == "" {
		opts.HostRole = role.Host
	}
	rt := &Runtime{
		opts:    opts,
		exec:    &tool.Executor{Registry: opts.Registry, Authorize: opts.Authorize},
		heavy:   tool.NewHeavyQueue(opts.Config.HeavyLimit),
		events:  make(chan struct{}, 1),
		threads: make(map[string]*thread),
		created: opts.Now(),
	}
	if err := opts.Registry.Register(rt.delegationTools()...); err != nil {
		return nil, fmt.Errorf("registrando as ferramentas de delegação: %w", err)
	}

	r, ok := opts.Roles[opts.HostRole]
	if !ok {
		return nil, fmt.Errorf("o agente %q não existe", opts.HostRole)
	}
	model := opts.HostModel
	if model == "" {
		model = r.Model
	}
	meta := AgentMeta{ID: HostID, Role: r.Name, Model: opts.Config.Model(model), Effort: r.Effort,
		Root: opts.Project, Branch: currentBranch(ctx, opts.Project), Status: StatusIdle}
	preamble := fmt.Sprintf("Projeto: %s\nBranch: %s\nData: %s", meta.Root, orDash(meta.Branch), opts.Now().Format("02/01/2006"))
	host, err := rt.newAgent(meta, r, preamble, rt.hostNotices)
	if err != nil {
		return nil, err
	}
	meta.Prefix = host.prefix()
	rt.host, rt.hostMeta = host, meta
	return rt, nil
}

// newAgent builds an agent of a role confined to meta.Root.
func (rt *Runtime) newAgent(meta AgentMeta, r role.Role, preamble string, notices func() []string) (*Agent, error) {
	ws, err := tool.NewWorkspace(meta.Root)
	if err != nil {
		return nil, err
	}
	allowed := r.Tools
	var search *provider.WebSearch
	if rt.nativeSearch() {
		// The provider searches on its side: the client tool leaves the list.
		allowed = without(allowed, WebSearchTool)
		if len(allowed) != len(r.Tools) {
			search = &provider.WebSearch{MaxUses: rt.opts.Config.WebSearchMaxUses}
		}
	}
	set := Settings{
		ID:            meta.ID,
		Model:         meta.Model,
		Effort:        meta.Effort,
		System:        role.System(r, rt.opts.OwnerRules, rt.opts.ProjectRules),
		Allowed:       allowed,
		Tools:         rt.opts.Registry.Specs(allowed),
		WebSearch:     search,
		Preamble:      preamble,
		MaxTokens:     rt.opts.Config.MaxTokens,
		CompactTokens: rt.opts.Config.CompactTokens,
		MaxIterations: rt.opts.Config.MaxIterations,
	}
	deps := Deps{
		Provider: rt.opts.Provider,
		Executor: rt.exec,
		Env:      &tool.Env{AgentID: meta.ID, Role: r.Name, Workspace: ws, Heavy: rt.heavy},
		Hooks:    rt.opts.Hooks,
		Notices:  notices,
		Recall:   rt.opts.Recall,
	}
	if rt.opts.Store != nil {
		deps.Journal = storeJournal{store: rt.opts.Store, id: meta.ID}
	}
	return New(set, deps), nil
}

func (rt *Runtime) nativeSearch() bool {
	return rt.opts.Config.WebSearch == "auto" && rt.opts.Provider.Capabilities().NativeWebSearch
}

func (rt *Runtime) Host() *Agent { return rt.host }

// Send hands a message of the user to the host and returns its answer.
func (rt *Runtime) Send(ctx context.Context, input string) (string, error) {
	rt.mu.Lock()
	first := !rt.started
	rt.started = true
	rt.mu.Unlock()
	header := ""
	if first {
		header = rt.host.set.Preamble
	}
	answer, err := rt.host.RunWith(ctx, header, input)
	if saveErr := rt.saveMeta(); err == nil && saveErr != nil {
		return answer, saveErr
	}
	return answer, err
}

// Resume restores the agents of a saved session. Threads that were running
// when the program stopped come back as interrupted.
func (rt *Runtime) Resume(meta SessionMeta) error {
	history, err := rt.opts.Store.Load(HostID)
	if err != nil {
		return err
	}
	if meta.Host.Prefix != rt.hostMeta.Prefix {
		if history, err = rt.forget(HostID, history); err != nil {
			return err
		}
	}
	if err := rt.host.Restore(history, meta.Host.Stats); err != nil {
		return err
	}
	rt.mu.Lock()
	rt.created = meta.Created
	rt.started = len(history) > 0
	rt.mu.Unlock()

	for _, tm := range meta.Threads {
		switch tm.Status {
		case StatusRunning:
			tm.Status = StatusInterrupted
			tm.Report = "o programa foi encerrado enquanto este thread rodava. Use continuar_thread para retomar."
			tm.Reported = false
		case StatusWaiting:
			tm.Status = StatusInterrupted
			tm.Report = "o programa foi encerrado enquanto este thread esperava a resposta desta pergunta: " + tm.Question +
				"\nUse continuar_thread para responder e retomar."
			tm.Reported = false
			tm.Question, tm.Asked = "", false
		}
		r, ok := rt.opts.Roles[tm.Role]
		if !ok {
			return fmt.Errorf("o thread %s usa o agente %q, que não existe mais", tm.ID, tm.Role)
		}
		t := &thread{noticed: tm.Reported}
		// A worktree removed since then only matters if the host continues the thread.
		if a, err := rt.newAgent(tm, r, threadPreamble(tm, rt.opts.Project, rt.opts.Now()), nil); err == nil {
			h, err := rt.opts.Store.Load(tm.ID)
			if err != nil {
				return err
			}
			if tm.Prefix != a.prefix() {
				if h, err = rt.forget(tm.ID, h); err != nil {
					return err
				}
				tm.Prefix = a.prefix()
			}
			if err := a.Restore(h, tm.Stats); err != nil {
				return err
			}
			t.agent = a
			rt.host.env.Workspace.AllowRead(tm.Root)
		}
		t.meta = tm
		rt.mu.Lock()
		rt.threads[tm.ID] = t
		rt.order = append(rt.order, tm.ID)
		rt.mu.Unlock()
	}
	return rt.saveMeta()
}

// forget drops the reasoning from the saved history of an agent whose system
// prompt or tools changed since it was written: the provider refuses to read
// that reasoning back. The agent carries on with the rules of today.
func (rt *Runtime) forget(id string, history []provider.Message) ([]provider.Message, error) {
	dropper, ok := rt.opts.Provider.(provider.ReasoningDropper)
	if !ok {
		return history, nil
	}
	kept := make([]provider.Message, 0, len(history))
	dropped := false
	for _, m := range history {
		clean := dropper.DropReasoning(m)
		if len(clean.Blocks) != len(m.Blocks) {
			dropped = true
		}
		if len(clean.Blocks) > 0 {
			kept = append(kept, clean)
		}
	}
	if !dropped {
		return history, nil
	}
	if err := rt.opts.Store.Reset(id, kept); err != nil {
		return nil, err
	}
	if rt.opts.Hooks.OnNotice != nil {
		rt.opts.Hooks.OnNotice(id, "as instruções ou as ferramentas mudaram desde que esta sessão foi gravada: o raciocínio antigo saiu do histórico")
	}
	return kept, nil
}

// Close stops every running thread and waits for their goroutines to end.
func (rt *Runtime) Close() error {
	rt.mu.Lock()
	for _, t := range rt.threads {
		if t.cancel != nil {
			t.cancel()
		}
	}
	rt.mu.Unlock()
	rt.wg.Wait()
	return rt.saveMeta()
}

func (rt *Runtime) saveMeta() error {
	if rt.opts.Store == nil {
		return nil
	}
	rt.mu.Lock()
	// Nothing is saved before the first message: a session opened and closed
	// without a word must not be the one --continue picks.
	if !rt.started {
		rt.mu.Unlock()
		return nil
	}
	meta := SessionMeta{ID: rt.opts.SessionID, Project: rt.opts.Project, Created: rt.created, Host: rt.hostMeta}
	agents := []*Agent{rt.host}
	for _, id := range rt.order {
		meta.Threads = append(meta.Threads, rt.threads[id].meta)
		agents = append(agents, rt.threads[id].agent)
	}
	rt.mu.Unlock()

	// A thread whose worktree is gone has no agent: it keeps what was saved.
	hostStats := agents[0].Stats()
	meta.Host.Stats = &hostStats
	for i, a := range agents[1:] {
		if a != nil {
			stats := a.Stats()
			meta.Threads[i].Stats = &stats
		}
	}
	return rt.opts.Store.SaveMeta(meta)
}

// hostNotices tells the host, once, about threads that finished while it was
// not waiting for them.
func (rt *Runtime) hostNotices() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var out []string
	for _, id := range rt.order {
		t := rt.threads[id]
		switch {
		case t.noticed, t.meta.Status == StatusRunning:
		case t.meta.Status == StatusWaiting:
			if !t.meta.Asked {
				t.noticed = true
				out = append(out, fmt.Sprintf("o thread %s parou com uma pergunta e espera a resposta. Use aguardar_threads para ler.", id))
			}
		case !t.meta.Reported:
			t.noticed = true
			out = append(out, fmt.Sprintf("o thread %s %s. Use aguardar_threads para ler o relatório.", id, t.meta.Status))
		}
	}
	return out
}

// start runs one turn of a thread in a new goroutine: header and input are
// what Agent.RunWith receives. The goroutine ends when
// the turn ends or the context is cancelled; the result stays in the thread
// table and the events channel wakes the host.
func (rt *Runtime) start(ctx context.Context, t *thread, header, input string) {
	// The thread outlives the tool call that launched it; Close cancels it.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	rt.mu.Lock()
	t.cancel = cancel
	t.meta.Status = StatusRunning
	t.meta.Report = ""
	t.meta.Reported = false
	t.noticed = false
	t.since = rt.opts.Now()
	id := t.meta.ID
	rt.mu.Unlock()

	rt.wg.Go(func() {
		defer cancel()
		report, err := t.agent.RunWith(runCtx, header, input)

		rt.mu.Lock()
		t.finished = rt.opts.Now()
		switch {
		case err == nil:
			t.meta.Status, t.meta.Report = StatusDone, report
		case errors.Is(err, context.Canceled) && t.meta.Question != "":
			t.meta.Status = StatusInterrupted
			t.meta.Report = "o thread foi interrompido enquanto esperava a resposta desta pergunta: " + t.meta.Question
		case errors.Is(err, context.Canceled):
			t.meta.Status, t.meta.Report = StatusInterrupted, "o thread foi interrompido antes de terminar."
		default:
			t.meta.Status, t.meta.Report = StatusFailed, "o thread falhou: "+err.Error()
		}
		t.meta.Question, t.meta.Asked, t.answer = "", false, nil
		status := t.meta.Status
		rt.mu.Unlock()

		if err := rt.saveMeta(); err != nil && rt.opts.Hooks.OnNotice != nil {
			rt.opts.Hooks.OnNotice(id, "não consegui gravar a sessão: "+err.Error())
		}
		if rt.opts.Hooks.OnNotice != nil {
			rt.opts.Hooks.OnNotice(id, "thread "+status)
		}
		select {
		case rt.events <- struct{}{}:
		default:
		}
	})
}

// activeLocked counts the threads that hold a place in the limit: the ones
// running and the ones waiting for an answer, which go back to running.
func (rt *Runtime) activeLocked() int {
	n := 0
	for _, t := range rt.threads {
		if t.meta.Status == StatusRunning || t.meta.Status == StatusWaiting {
			n++
		}
	}
	return n
}

// ask stops a thread on a question until the host answers it, and returns the
// answer. The thread keeps its place and its conversation while it waits.
func (rt *Runtime) ask(ctx context.Context, id, question string) (string, error) {
	answer := make(chan string, 1)
	rt.mu.Lock()
	t, ok := rt.threads[id]
	if !ok {
		rt.mu.Unlock()
		return "", fmt.Errorf("não existe thread %q nesta sessão", id)
	}
	t.answer = answer
	t.meta.Status, t.meta.Question, t.meta.Asked = StatusWaiting, question, false
	t.noticed = false
	rt.mu.Unlock()

	if err := rt.saveMeta(); err != nil && rt.opts.Hooks.OnNotice != nil {
		rt.opts.Hooks.OnNotice(id, "não consegui gravar a sessão: "+err.Error())
	}
	if rt.opts.Hooks.OnNotice != nil {
		rt.opts.Hooks.OnNotice(id, "thread parou com uma pergunta ao host")
	}
	select {
	case rt.events <- struct{}{}:
	default:
	}

	select {
	case text := <-answer:
		return text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// reply hands the host's answer to a waiting thread, which goes back to work.
// It reports false when the thread is not waiting.
func (rt *Runtime) reply(id, text string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	t, ok := rt.threads[id]
	if !ok || t.meta.Status != StatusWaiting || t.answer == nil {
		return false
	}
	t.answer <- text
	t.answer = nil
	t.meta.Status, t.meta.Question, t.meta.Asked = StatusRunning, "", false
	return true
}

var threadNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// threadHeader tells a thread where it works. It opens the thread's first
// message, before the task.
func threadHeader(meta AgentMeta, project string, now time.Time) string {
	return fmt.Sprintf("Pasta: %s (branch %s)\nProjeto principal: %s (não mexa nele)\nData: %s",
		meta.Root, orDash(meta.Branch), project, now.Format("02/01/2006"))
}

// threadPreamble is what a thread keeps word for word through a compaction:
// where it works and the task it was given.
func threadPreamble(meta AgentMeta, project string, now time.Time) string {
	return threadHeader(meta, project, now) + "\n\n" + meta.Task
}

// Report describes the agents, the memory each one uses and the heavy queue.
func (rt *Runtime) Report() string {
	var b strings.Builder
	line := func(meta AgentMeta, a *Agent, extra string) {
		fmt.Fprintf(&b, "%s [%s, %s", meta.ID, meta.Role, meta.Model)
		if meta.Effort != "" {
			fmt.Fprintf(&b, ", esforço %s", meta.Effort)
		}
		fmt.Fprintf(&b, "] %s%s\n", meta.Status, extra)
		if a == nil {
			return
		}
		s := a.Stats()
		fmt.Fprintf(&b, "  memória: histórico %s (pico %s) em %d mensagens; maior comando filho %s\n",
			formatBytes(int64(s.HistoryBytes)), formatBytes(int64(s.PeakHistoryBytes)), s.Messages, formatBytes(s.PeakChildKB*1024))
		fmt.Fprintf(&b, "  contexto: %d tokens; %d pedidos, %d chamadas de ferramenta, %d compactações\n",
			s.ContextTokens, s.Requests, s.ToolCalls, s.Compactions)
		fmt.Fprintf(&b, "  tokens: entrada %d, saída %d, cache lido %d, cache gravado %d\n",
			s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens)
	}

	rt.mu.Lock()
	now := rt.opts.Now()
	type row struct {
		meta  AgentMeta
		agent *Agent
		extra string
	}
	rows := []row{{meta: rt.hostMeta, agent: rt.host}}
	for _, id := range rt.order {
		t := rt.threads[id]
		extra := ""
		switch {
		case t.meta.Status == StatusRunning:
			extra = fmt.Sprintf(" há %s, em %s", now.Sub(t.since).Round(time.Second), t.meta.Root)
		case t.meta.Status == StatusWaiting:
			extra = fmt.Sprintf(", em %s. Pergunta: %s", t.meta.Root, t.meta.Question)
		case !t.finished.IsZero():
			extra = fmt.Sprintf(" em %s, pasta %s", t.finished.Sub(t.since).Round(time.Second), t.meta.Root)
		default:
			extra = ", pasta " + t.meta.Root
		}
		if t.meta.Status != StatusRunning && t.meta.Status != StatusWaiting && !t.meta.Reported {
			extra += " (relatório ainda não lido)"
		}
		rows = append(rows, row{meta: t.meta, agent: t.agent, extra: extra})
	}
	rt.mu.Unlock()

	for _, r := range rows {
		line(r.meta, r.agent, r.extra)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(&b, "processo: heap em uso %s, reservado do sistema %s, %d goroutines\n",
		formatBytes(int64(mem.HeapInuse)), formatBytes(int64(mem.Sys)), runtime.NumGoroutine())
	fmt.Fprintf(&b, "fila de comandos pesados: %d rodando, %d esperando (limite %d)\n",
		rt.heavy.Running(), rt.heavy.Waiting(), rt.heavy.Limit())
	return b.String()
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func without(list []string, item string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != item {
			out = append(out, v)
		}
	}
	return out
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func currentBranch(ctx context.Context, dir string) string {
	branch, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return branch
}

// worktreePath is where the worktree of a thread lives: beside the project.
func worktreePath(project, name string) string {
	return filepath.Join(filepath.Dir(project), filepath.Base(project)+"-wt-"+name)
}
