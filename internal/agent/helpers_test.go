package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/tool"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeProvider answers with whatever the handler decides and keeps every
// request it received.
type fakeProvider struct {
	native  bool
	handler func(n int, req provider.Request) (*provider.Response, error)

	mu       sync.Mutex
	requests []provider.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{NativeWebSearch: f.native}
}

func (f *fakeProvider) Complete(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	n := len(f.requests)
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return f.handler(n, req)
}

// DropReasoning treats every opaque block as reasoning.
func (f *fakeProvider) DropReasoning(m provider.Message) provider.Message {
	kept := make([]provider.Block, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		if b.Type != provider.BlockOpaque {
			kept = append(kept, b)
		}
	}
	m.Blocks = kept
	return m
}

func (f *fakeProvider) request(i int) provider.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

func (f *fakeProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func say(text string) *provider.Response {
	return &provider.Response{
		Stop:    provider.StopEndTurn,
		Message: provider.Message{Role: provider.RoleAssistant, Blocks: []provider.Block{{Type: provider.BlockText, Text: text}}},
	}
}

type toolUse struct {
	id    string
	name  string
	input any
}

func use(calls ...toolUse) *provider.Response {
	msg := provider.Message{Role: provider.RoleAssistant}
	for _, c := range calls {
		raw, err := json.Marshal(c.input)
		if err != nil {
			panic(err)
		}
		msg.Blocks = append(msg.Blocks, provider.Block{Type: provider.BlockToolUse, ToolUseID: c.id, ToolName: c.name, Input: raw})
	}
	return &provider.Response{Stop: provider.StopToolUse, Message: msg}
}

// lastMessage joins the text and tool results of the last message of a request.
func lastMessage(req provider.Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	var parts []string
	for _, b := range req.Messages[len(req.Messages)-1].Blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}

type memJournal struct {
	mu      sync.Mutex
	appends []provider.Message
	resets  [][]provider.Message
}

func (j *memJournal) Append(msg provider.Message) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appends = append(j.appends, msg)
	return nil
}

func (j *memJournal) Reset(msgs []provider.Message) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.resets = append(j.resets, msgs)
	return nil
}

type harness struct {
	agent    *Agent
	provider *fakeProvider
	journal  *memJournal
	registry *tool.Registry
}

// newHarness builds an agent over a fake provider with the given tools.
func newHarness(t *testing.T, set Settings, handler func(n int, req provider.Request) (*provider.Response, error), tools ...tool.Tool) *harness {
	t.Helper()
	registry := tool.NewRegistry()
	if err := registry.Register(tools...); err != nil {
		t.Fatal(err)
	}
	ws, err := tool.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if set.ID == "" {
		set.ID = "teste"
	}
	if set.MaxIterations == 0 {
		set.MaxIterations = 20
	}
	if set.MaxTokens == 0 {
		set.MaxTokens = 1000
	}
	set.Allowed = registry.Names()
	set.Tools = registry.Specs(set.Allowed)
	h := &harness{provider: &fakeProvider{handler: handler}, journal: &memJournal{}, registry: registry}
	h.agent = New(set, Deps{
		Provider: h.provider,
		Executor: &tool.Executor{Registry: registry, Authorize: tool.AllowAll},
		Env:      &tool.Env{AgentID: set.ID, Role: "teste", Workspace: ws},
		Journal:  h.journal,
	})
	return h
}

func simpleTool(name string, parallel bool, run func(ctx context.Context) tool.Result) tool.Tool {
	return tool.Tool{
		Name:     name,
		Schema:   tool.Object(nil, map[string]any{}),
		Parallel: parallel,
		Run: func(ctx context.Context, _ *tool.Env, _ json.RawMessage) tool.Result {
			return run(ctx)
		},
	}
}
