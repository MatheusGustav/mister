package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func echoTool(name string) Tool {
	return Tool{
		Name:   name,
		Schema: object(nil, map[string]any{}),
		Run: func(context.Context, *Env, json.RawMessage) Result {
			return OK("eco de %s", name)
		},
	}
}

func TestRegistry_Register(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.Register(echoTool("a")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register(echoTool("a")); !errors.Is(err, ErrDuplicateTool) {
		t.Errorf("registrar repetido: erro = %v, quero ErrDuplicateTool", err)
	}
	if err := r.Register(Tool{Name: "sem-funcao"}); err == nil {
		t.Error("registrar ferramenta sem função deveria falhar")
	}
	if got, _ := r.Get("a"); got.Source != "nativa" {
		t.Errorf("Source = %q, quero nativa", got.Source)
	}
}

func TestRegistry_Specs(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.Register(echoTool("zeta"), echoTool("alfa"), echoTool("meio")); err != nil {
		t.Fatal(err)
	}
	specs := r.Specs([]string{"zeta", "inexistente", "alfa", "zeta"})
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "alfa,zeta" {
		t.Errorf("Specs = %s, quero alfa,zeta (ordenado, sem repetir, sem o que não existe)", got)
	}
}

func TestExecutor_Execute(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	boom := Tool{Name: "quebra", Schema: object(nil, nil), Run: func(context.Context, *Env, json.RawMessage) Result {
		panic("estourou")
	}}
	if err := r.Register(echoTool("eco"), echoTool("proibida"), boom); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("regra de teste")
	allowed := []string{"eco", "quebra", "negada"}

	tests := []struct {
		name      string
		call      string
		authorize Authorizer
		wantErr   bool
		wantText  string
	}{
		{name: "roda", call: "eco", authorize: AllowAll, wantText: "eco de eco"},
		{name: "ferramenta que não existe", call: "fantasma", authorize: AllowAll, wantErr: true, wantText: "não existe"},
		{name: "fora do agente", call: "proibida", authorize: AllowAll, wantErr: true, wantText: "não tem a ferramenta"},
		{name: "autorização nega", call: "eco", authorize: func(context.Context, *Env, Call) error { return denied }, wantErr: true, wantText: "regra de teste"},
		{name: "pânico vira erro", call: "quebra", authorize: AllowAll, wantErr: true, wantText: "quebrou por dentro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			exec := &Executor{Registry: r, Authorize: tt.authorize}
			res := exec.Execute(context.Background(), &Env{Role: "thread"}, allowed, Call{Name: tt.call})
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, quero %v (%s)", res.IsError, tt.wantErr, res.Content)
			}
			if !strings.Contains(res.Content, tt.wantText) {
				t.Errorf("resultado %q não contém %q", res.Content, tt.wantText)
			}
		})
	}
}

func TestExecutor_AuthorizerSeesEveryCall(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if err := r.Register(echoTool("eco")); err != nil {
		t.Fatal(err)
	}
	var seen []string
	exec := &Executor{Registry: r, Authorize: func(_ context.Context, _ *Env, c Call) error {
		seen = append(seen, c.Name)
		return nil
	}}
	exec.Execute(context.Background(), &Env{}, []string{"eco"}, Call{Name: "eco"})
	if len(seen) != 1 || seen[0] != "eco" {
		t.Errorf("a autorização viu %v, quero [eco]", seen)
	}
}

func TestDecode(t *testing.T) {
	t.Parallel()
	type input struct {
		Caminho string `json:"caminho"`
	}
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "válido", raw: `{"caminho":"a.txt"}`, want: "a.txt"},
		{name: "vazio vira objeto vazio", raw: ``},
		{name: "campo inventado", raw: `{"caminho":"a","inventado":1}`, wantErr: true},
		{name: "tipo errado", raw: `{"caminho":5}`, wantErr: true},
		{name: "json cortado", raw: `{"caminho":"a`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decode[input](json.RawMessage(tt.raw))
			if (err != nil) != tt.wantErr {
				t.Fatalf("erro = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got.Caminho != tt.want {
				t.Errorf("Caminho = %q, quero %q", got.Caminho, tt.want)
			}
		})
	}
}
