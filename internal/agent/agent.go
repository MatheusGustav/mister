// Package agent runs agents: the loop that talks to the model and executes
// tools, the history on disk, compaction, and the runtime that lets a host
// delegate work to threads.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/tool"
)

const (
	maxPauseResumes  = 5
	maxCutContinues  = 3
	maxToolResultLen = 100_000
)

// ErrMaxIterations stops a run that keeps calling tools without finishing.
var ErrMaxIterations = errors.New("o agente passou do limite de voltas sem terminar")

// RefusalError is returned when the model declines the request.
type RefusalError struct {
	Detail string
}

func (e *RefusalError) Error() string {
	if e.Detail == "" {
		return "o modelo recusou o pedido"
	}
	return "o modelo recusou o pedido: " + e.Detail
}

// Journal persists the history of one agent.
type Journal interface {
	Append(msg provider.Message) error
	Reset(msgs []provider.Message) error
}

// Hooks let the interface follow what an agent is doing. Any field may be nil.
type Hooks struct {
	OnText       func(agentID, delta string)
	OnToolCall   func(agentID string, call tool.Call)
	OnToolResult func(agentID string, call tool.Call, res tool.Result)
	OnNotice     func(agentID, text string)
}

// Settings is everything fixed about an agent for the whole session. System,
// Tools and Model form the cached prefix of every request and never change.
type Settings struct {
	ID     string
	Model  string
	Effort string
	System string
	// Allowed are the tool names of the role; Tools are their definitions,
	// sorted by name.
	Allowed   []string
	Tools     []provider.ToolSpec
	WebSearch *provider.WebSearch
	// Preamble is kept word for word when the history is compacted (a thread's
	// task, the host's project line).
	Preamble      string
	MaxTokens     int
	CompactTokens int
	MaxIterations int
}

// Stats measures one agent.
type Stats struct {
	Messages int
	// HistoryBytes is the memory the conversation holds; PeakHistoryBytes is
	// the most it ever held.
	HistoryBytes     int
	PeakHistoryBytes int
	// PeakChildKB is the largest peak memory of a command the agent ran.
	PeakChildKB int64
	// ContextTokens is the size of the last prompt sent.
	ContextTokens    int64
	Requests         int
	ToolCalls        int
	Compactions      int
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

type Agent struct {
	set      Settings
	provider provider.Provider
	exec     *tool.Executor
	env      *tool.Env
	journal  Journal
	hooks    Hooks
	// notices returns what happened outside the turn (a thread that finished).
	notices func() []string
	recall  RecallFunc
	// delivered are the notes already in this conversation: each one arrives once.
	delivered map[string]bool

	mu      sync.Mutex
	history []provider.Message
	stats   Stats
}

type Deps struct {
	Provider provider.Provider
	Executor *tool.Executor
	Env      *tool.Env
	Journal  Journal
	Hooks    Hooks
	Notices  func() []string
	Recall   RecallFunc
}

// RecallFunc returns the memory notes related to a text, ready to attach to a
// message, and their names. Notes in skip stay out. No related note is "".
type RecallFunc func(ctx context.Context, text string, skip map[string]bool) (notes string, names []string)

func New(set Settings, deps Deps) *Agent {
	a := &Agent{
		set:      set,
		provider: deps.Provider,
		exec:     deps.Executor,
		env:      deps.Env,
		journal:  deps.Journal,
		hooks:    deps.Hooks,
		notices:  deps.Notices,
		recall:   deps.Recall,
	}
	if a.env != nil {
		a.env.ChildMem = a.recordChildMem
	}
	return a
}

func (a *Agent) ID() string { return a.set.ID }

// prefix identifies the system prompt and the tools of the agent.
func (a *Agent) prefix() string {
	data, err := json.Marshal(struct {
		System    string
		Tools     []provider.ToolSpec
		WebSearch *provider.WebSearch
	}{a.set.System, a.set.Tools, a.set.WebSearch})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Restore loads a saved history. A tool call left without a result by a crash
// gets one saying so, which keeps the conversation valid.
func (a *Agent) Restore(history []provider.Message) error {
	a.mu.Lock()
	a.history = append([]provider.Message(nil), history...)
	a.refreshSizeLocked()
	var dangling []provider.Block
	if n := len(a.history); n > 0 && a.history[n-1].Role == provider.RoleAssistant {
		dangling = a.history[n-1].ToolUses()
	}
	a.mu.Unlock()

	if len(dangling) == 0 {
		return nil
	}
	results := make([]provider.Block, 0, len(dangling))
	for _, use := range dangling {
		results = append(results, provider.ToolResult(use.ToolUseID,
			"o programa foi interrompido antes de esta ferramenta devolver o resultado. Confira o estado atual antes de repetir.", true))
	}
	return a.append(provider.Message{Role: provider.RoleUser, Blocks: results})
}

func (a *Agent) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

func (a *Agent) recordChildMem(peakKB int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stats.PeakChildKB = max(a.stats.PeakChildKB, peakKB)
}

// Run takes one input (the user's message, a task, a correction) and loops
// until the model answers without asking for tools. It returns that answer.
func (a *Agent) Run(ctx context.Context, input string) (string, error) {
	return a.RunWith(ctx, "", input)
}

// RunWith is Run with a header placed before the input (where the agent is
// working, the date). The memory is searched with the input alone, so the
// header does not blur the subject.
func (a *Agent) RunWith(ctx context.Context, header, input string) (string, error) {
	// Compacting before the new input keeps it out of the summary, and lets
	// the memory search below deliver again the notes that left with the history.
	if a.needsCompaction() {
		if err := a.compact(ctx, false); err != nil {
			a.notice("não consegui compactar o contexto: " + err.Error())
		}
	}
	var blocks []provider.Block
	if header != "" {
		blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: header + "\n\n"})
	}
	blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: input})
	blocks = append(blocks, a.remember(ctx, input)...)
	if err := a.appendUser(blocks); err != nil {
		return "", err
	}

	pauses, cuts := 0, 0
	for range a.set.MaxIterations {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("agente %s interrompido: %w", a.set.ID, err)
		}
		if a.needsCompaction() {
			if err := a.compact(ctx, true); err != nil {
				a.notice("não consegui compactar o contexto: " + err.Error())
			}
		}

		resp, err := a.provider.Complete(ctx, a.request(a.snapshot()))
		if err != nil {
			return "", fmt.Errorf("agente %s: %w", a.set.ID, err)
		}
		a.recordUsage(resp.Usage)
		if resp.Stop == provider.StopRefusal {
			return "", &RefusalError{Detail: resp.StopDetail}
		}
		if len(resp.Message.Blocks) > 0 {
			if err := a.append(resp.Message); err != nil {
				return "", err
			}
		}

		uses := resp.Message.ToolUses()
		switch {
		case resp.Stop == provider.StopPause:
			if pauses++; pauses > maxPauseResumes {
				return "", fmt.Errorf("agente %s: o provedor pausou a resposta %d vezes seguidas", a.set.ID, pauses)
			}
		case len(uses) > 0 && resp.Stop == provider.StopMaxTokens:
			// The call may be cut in the middle: running it would act on half an input.
			results := make([]provider.Block, 0, len(uses))
			for _, use := range uses {
				results = append(results, provider.ToolResult(use.ToolUseID,
					"a resposta bateu no limite de tokens antes de terminar esta chamada, e ela não rodou. Refaça em partes menores.", true))
			}
			if err := a.appendUser(results); err != nil {
				return "", err
			}
		case len(uses) > 0:
			results := a.runTools(ctx, uses)
			// The subject may have changed in the middle of the work, with no
			// new message: search the memory with what just happened.
			results = append(results, a.remember(ctx, roundSubject(uses, results))...)
			if err := a.appendUser(results); err != nil {
				return "", err
			}
		case resp.Stop == provider.StopMaxTokens:
			if cuts++; cuts > maxCutContinues {
				return resp.Message.Text(), nil
			}
			err := a.appendUser([]provider.Block{{Type: provider.BlockText,
				Text: "[sistema] A resposta bateu no limite de tokens e foi cortada. Continue exatamente de onde parou."}})
			if err != nil {
				return "", err
			}
		default:
			return resp.Message.Text(), nil
		}
	}
	return "", fmt.Errorf("agente %s: %w (%d)", a.set.ID, ErrMaxIterations, a.set.MaxIterations)
}

// remember searches the memory with a text and returns the related notes not
// yet delivered in this conversation, as a block to attach to a message.
func (a *Agent) remember(ctx context.Context, text string) []provider.Block {
	if a.recall == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	notes, names := a.recall(ctx, text, a.delivered)
	if notes == "" {
		return nil
	}
	if a.delivered == nil {
		a.delivered = make(map[string]bool)
	}
	for _, name := range names {
		a.delivered[name] = true
	}
	return []provider.Block{{Type: provider.BlockText, Text: notes}}
}

const (
	subjectInputLen  = 300
	subjectErrorLen  = 500
	subjectMaxLength = 1500
)

// roundSubject describes one round of tools for the memory search: what was
// asked of each tool and what went wrong. Successful outputs stay out: a file
// that was read says little about what the agent is stuck on.
func roundSubject(uses, results []provider.Block) string {
	var b strings.Builder
	for i, use := range uses {
		fmt.Fprintf(&b, "%s %s\n", use.ToolName, clip(string(use.Input), subjectInputLen))
		if i < len(results) && results[i].IsError {
			fmt.Fprintf(&b, "%s\n", clip(results[i].Text, subjectErrorLen))
		}
	}
	return clip(b.String(), subjectMaxLength)
}

func clip(text string, limit int) string {
	if len(text) > limit {
		return strings.ToValidUTF8(text[:limit], "")
	}
	return text
}

func (a *Agent) request(messages []provider.Message) provider.Request {
	req := provider.Request{
		Model:     a.set.Model,
		Effort:    a.set.Effort,
		MaxTokens: a.set.MaxTokens,
		System:    a.set.System,
		Tools:     a.set.Tools,
		WebSearch: a.set.WebSearch,
		Messages:  messages,
	}
	if a.hooks.OnText != nil {
		req.OnText = func(delta string) { a.hooks.OnText(a.set.ID, delta) }
	}
	if a.hooks.OnToolCall != nil {
		// The provider's own search shows up like any other tool call.
		req.OnWebSearch = func(input json.RawMessage) {
			a.hooks.OnToolCall(a.set.ID, tool.Call{Name: WebSearchTool, Input: input})
		}
	}
	return req
}

// runTools executes the calls of one model answer and returns the results in
// the same order. Neighbouring read-only calls run at the same time; a call
// that changes something runs alone, in its turn.
func (a *Agent) runTools(ctx context.Context, uses []provider.Block) []provider.Block {
	results := make([]provider.Block, len(uses))
	run := func(i int) {
		use := uses[i]
		call := tool.Call{Name: use.ToolName, Input: use.Input}
		if a.hooks.OnToolCall != nil {
			a.hooks.OnToolCall(a.set.ID, call)
		}
		res := a.exec.Execute(ctx, a.env, a.set.Allowed, call)
		if len(res.Content) > maxToolResultLen {
			res.Content = strings.ToValidUTF8(res.Content[:maxToolResultLen], "") + "\n[resultado cortado: era grande demais]"
		}
		if res.Content == "" {
			res.Content = "(sem saída)"
		}
		if a.hooks.OnToolResult != nil {
			a.hooks.OnToolResult(a.set.ID, call, res)
		}
		results[i] = provider.ToolResult(use.ToolUseID, res.Content, res.IsError)
		results[i].Images = res.Images
	}

	for start := 0; start < len(uses); {
		end := start + 1
		if a.parallel(uses[start].ToolName) {
			for end < len(uses) && a.parallel(uses[end].ToolName) {
				end++
			}
		}
		if end-start == 1 {
			run(start)
		} else {
			var wg sync.WaitGroup
			for i := start; i < end; i++ {
				wg.Go(func() { run(i) })
			}
			wg.Wait()
		}
		start = end
	}

	a.mu.Lock()
	a.stats.ToolCalls += len(uses)
	a.mu.Unlock()
	return results
}

func (a *Agent) parallel(name string) bool {
	t, ok := a.exec.Registry.Get(name)
	return ok && t.Parallel
}

// appendUser adds a user message, with whatever happened outside the turn
// attached after its blocks.
func (a *Agent) appendUser(blocks []provider.Block) error {
	if a.notices != nil {
		for _, n := range a.notices() {
			blocks = append(blocks, provider.Block{Type: provider.BlockText, Text: "[aviso] " + n})
		}
	}
	return a.append(provider.Message{Role: provider.RoleUser, Blocks: blocks})
}

func (a *Agent) append(msg provider.Message) error {
	a.mu.Lock()
	a.history = append(a.history, msg)
	a.refreshSizeLocked()
	a.mu.Unlock()
	if a.journal == nil {
		return nil
	}
	if err := a.journal.Append(msg); err != nil {
		return fmt.Errorf("agente %s: %w", a.set.ID, err)
	}
	return nil
}

func (a *Agent) snapshot() []provider.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]provider.Message(nil), a.history...)
}

func (a *Agent) refreshSizeLocked() {
	size := 0
	for _, m := range a.history {
		size += m.Size()
	}
	a.stats.Messages = len(a.history)
	a.stats.HistoryBytes = size
	a.stats.PeakHistoryBytes = max(a.stats.PeakHistoryBytes, size)
}

func (a *Agent) recordUsage(u provider.Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stats.Requests++
	a.stats.ContextTokens = u.ContextTokens() + u.OutputTokens
	a.stats.InputTokens += u.InputTokens
	a.stats.OutputTokens += u.OutputTokens
	a.stats.CacheReadTokens += u.CacheReadTokens
	a.stats.CacheWriteTokens += u.CacheWriteTokens
}

func (a *Agent) notice(text string) {
	if a.hooks.OnNotice != nil {
		a.hooks.OnNotice(a.set.ID, text)
	}
}
