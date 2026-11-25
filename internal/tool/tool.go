// Package tool holds the tool registry, the single authorization gate every
// call goes through, and the native tools.
package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/MatheusGustav/mister/internal/provider"
)

// Result is what a tool hands back to the model.
type Result struct {
	Content string
	IsError bool
}

func OK(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...)}
}

// Fail builds an error result. The message must say what went wrong and how to
// fix it: the model reads it to correct itself.
func Fail(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

// Env is what a tool knows about the agent that called it.
type Env struct {
	AgentID   string
	Role      string
	Workspace *Workspace
	Heavy     *HeavyQueue
	// ChildMem records the peak memory of a child process, in KB.
	ChildMem func(peakKB int64)
}

type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema of the input ("properties" and "required").
	Schema map[string]any
	// Parallel marks a tool that only reads, safe to run alongside other calls
	// of the same turn.
	Parallel bool
	// Source says where the tool comes from ("nativa", later an MCP server).
	Source string
	Run    func(ctx context.Context, env *Env, input json.RawMessage) Result
}

func (t Tool) Spec() provider.ToolSpec {
	return provider.ToolSpec{Name: t.Name, Description: t.Description, Schema: t.Schema}
}

// Registry maps names to tools. External tools register through the same door
// as the native ones.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

var ErrDuplicateTool = errors.New("ferramenta já registrada")

func (r *Registry) Register(tools ...Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range tools {
		if t.Name == "" || t.Run == nil {
			return fmt.Errorf("ferramenta sem nome ou sem função: %q", t.Name)
		}
		if _, exists := r.tools[t.Name]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicateTool, t.Name)
		}
		if t.Source == "" {
			t.Source = "nativa"
		}
		r.tools[t.Name] = t
	}
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Specs returns the definitions of the allowed tools that exist, sorted by
// name so the prompt prefix is identical for every agent of the same role.
func (r *Registry) Specs(allowed []string) []provider.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool, len(allowed))
	specs := make([]provider.ToolSpec, 0, len(allowed))
	for _, name := range allowed {
		t, ok := r.tools[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		specs = append(specs, t.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// Call is one tool request made by an agent.
type Call struct {
	Name  string
	Input json.RawMessage
}

// Authorizer decides whether a call may run. Every call goes through it.
type Authorizer func(ctx context.Context, env *Env, call Call) error

// AllowAll approves every call. Permission rules replace it later.
func AllowAll(context.Context, *Env, Call) error { return nil }

// Executor runs calls: existence, role permission, authorization, execution.
type Executor struct {
	Registry  *Registry
	Authorize Authorizer
}

func (e *Executor) Execute(ctx context.Context, env *Env, allowed []string, call Call) (res Result) {
	t, ok := e.Registry.Get(call.Name)
	if !ok {
		return Fail("a ferramenta %q não existe. Disponíveis: %s", call.Name, strings.Join(allowed, ", "))
	}
	if !contains(allowed, call.Name) {
		return Fail("o agente %q não tem a ferramenta %q. Disponíveis: %s", env.Role, call.Name, strings.Join(allowed, ", "))
	}
	if err := e.Authorize(ctx, env, call); err != nil {
		return Fail("chamada de %s não autorizada: %v", call.Name, err)
	}
	defer func() {
		if r := recover(); r != nil {
			res = Fail("a ferramenta %s quebrou por dentro: %v. Tente de outro jeito.", call.Name, r)
		}
	}()
	return t.Run(ctx, env, call.Input)
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

// decode fills the typed input of a tool, rejecting unknown fields so a
// made-up parameter comes back as an error instead of being ignored.
func decode[T any](input json.RawMessage) (T, error) {
	var v T
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("parâmetros inválidos: %w", err)
	}
	return v, nil
}

// Decode is decode for tools defined outside this package.
func Decode[T any](input json.RawMessage) (T, error) { return decode[T](input) }

func object(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

// Object builds an input schema for tools defined outside this package.
func Object(required []string, properties map[string]any) map[string]any {
	return object(required, properties)
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integer(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// Str and Int build schema properties for tools defined outside this package.
func Str(description string) map[string]any { return str(description) }
func Int(description string) map[string]any { return integer(description) }
