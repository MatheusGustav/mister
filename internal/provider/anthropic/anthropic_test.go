package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/MatheusGustav/mister/internal/provider"
)

type recorded struct {
	body map[string]any
	beta string
	key  string
}

// fakeAPI answers every request with the given SSE stream and keeps the
// requests it received.
type fakeAPI struct {
	mu       sync.Mutex
	requests []recorded
	stream   string
	status   int
}

func (f *fakeAPI) client(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		f.mu.Lock()
		f.requests = append(f.requests, recorded{body: body, beta: r.Header.Get("anthropic-beta"), key: r.Header.Get("x-api-key") + r.Header.Get("Authorization")})
		f.mu.Unlock()
		if f.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, f.stream)
	}))
	t.Cleanup(srv.Close)
	return NewWithOptions(option.WithBaseURL(srv.URL), option.WithAPIKey("chave-de-teste"), option.WithMaxRetries(0))
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &probe)
		b.WriteString("event: " + probe.Type + "\ndata: " + e + "\n\n")
	}
	return b.String()
}

const messageStart = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":5,"cache_read_input_tokens":20,"output_tokens":1}}}`

func toolUseStream() string {
	return sse(
		messageStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"vou ler o arquivo"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"assinatura-123"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Vou "}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ler."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"ler_arquivo","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"caminho\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.txt\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":15}}`,
		`{"type":"message_stop"}`,
	)
}

func schema(required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"caminho": map[string]any{"type": "string"}}, "required": required}
}

func baseRequest() provider.Request {
	return provider.Request{
		Model:     "claude-opus-5-5",
		Effort:    "high",
		MaxTokens: 4000,
		System:    "instruções do agente",
		Tools: []provider.ToolSpec{
			{Name: "editar_arquivo", Description: "edita", Schema: schema("caminho")},
			{Name: "ler_arquivo", Description: "lê", Schema: schema("caminho")},
		},
		WebSearch: &provider.WebSearch{MaxUses: 4},
		Messages:  []provider.Message{provider.UserText("leia a.txt")},
	}
}

func TestClient_Complete_RequestShape(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{stream: toolUseStream()}
	if _, err := api.client(t).Complete(context.Background(), baseRequest()); err != nil {
		t.Fatal(err)
	}
	req := api.requests[0]
	body := req.body

	if !strings.Contains(req.key, "chave-de-teste") {
		t.Errorf("a chave não foi no cabeçalho: %q", req.key)
	}
	if body["model"] != "claude-opus-5-5" || body["max_tokens"] != float64(4000) || body["stream"] != true {
		t.Errorf("modelo, limite ou streaming inesperados: %v %v %v", body["model"], body["max_tokens"], body["stream"])
	}
	if effort := body["output_config"].(map[string]any)["effort"]; effort != "high" {
		t.Errorf("effort = %v", effort)
	}
	if _, has := body["thinking"]; has {
		t.Error("thinking não deve ser enviado: os modelos atuais já pensam por conta própria")
	}

	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != "instruções do agente" || system["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Errorf("o bloco de instruções deveria fechar o começo fixo com um ponto de cache: %v", system)
	}
	if top := body["cache_control"].(map[string]any); top["type"] != "ephemeral" {
		t.Errorf("faltou o ponto de cache automático da conversa: %v", top)
	}

	tools := body["tools"].([]any)
	var names []string
	for _, raw := range tools {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	if got := strings.Join(names, ","); got != "editar_arquivo,ler_arquivo,web_search" {
		t.Errorf("ferramentas = %s; quero as do cliente na ordem recebida e a pesquisa nativa por último", got)
	}
	first := tools[0].(map[string]any)
	if first["eager_input_streaming"] != true || first["description"] != "edita" {
		t.Errorf("definição da ferramenta inesperada: %v", first)
	}
	inputSchema := first["input_schema"].(map[string]any)
	if inputSchema["type"] != "object" || inputSchema["required"].([]any)[0] != "caminho" || inputSchema["properties"].(map[string]any)["caminho"] == nil {
		t.Errorf("schema da ferramenta inesperado: %v", inputSchema)
	}
	search := tools[2].(map[string]any)
	if search["type"] != "web_search_20260209" || search["max_uses"] != float64(4) {
		t.Errorf("pesquisa nativa inesperada: %v", search)
	}

	if body["fallbacks"] != "default" || !strings.Contains(req.beta, "server-side-fallback-2026-07-01") {
		t.Errorf("faltou o fallback de recusa: fallbacks=%v beta=%q", body["fallbacks"], req.beta)
	}
}

func TestClient_Complete_ParsesResponse(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{stream: toolUseStream()}
	var streamed strings.Builder
	req := baseRequest()
	req.OnText = func(delta string) { streamed.WriteString(delta) }
	resp, err := api.client(t).Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Stop != provider.StopToolUse {
		t.Errorf("Stop = %q, quero tool_use", resp.Stop)
	}
	if streamed.String() != "Vou ler." || resp.Message.Text() != "Vou ler." {
		t.Errorf("texto transmitido %q, texto final %q", streamed.String(), resp.Message.Text())
	}
	want := provider.Usage{InputTokens: 10, OutputTokens: 15, CacheReadTokens: 20, CacheWriteTokens: 5}
	if resp.Usage != want {
		t.Errorf("Usage = %+v, quero %+v", resp.Usage, want)
	}
	if resp.Usage.ContextTokens() != 35 {
		t.Errorf("ContextTokens = %d, quero 35", resp.Usage.ContextTokens())
	}
	if resp.Message.Provider != Name || len(resp.Message.Blocks) != 3 {
		t.Fatalf("mensagem inesperada: %+v", resp.Message)
	}
	if resp.Message.Blocks[0].Type != provider.BlockOpaque {
		t.Errorf("o raciocínio deveria ser um bloco opaco: %+v", resp.Message.Blocks[0])
	}
	uses := resp.Message.ToolUses()
	if len(uses) != 1 || uses[0].ToolUseID != "toolu_1" || uses[0].ToolName != "ler_arquivo" {
		t.Fatalf("chamada de ferramenta inesperada: %+v", uses)
	}
	var input map[string]string
	if err := json.Unmarshal(uses[0].Input, &input); err != nil || input["caminho"] != "a.txt" {
		t.Errorf("entrada da ferramenta = %s (%v)", uses[0].Input, err)
	}
}

// The assistant message must go back exactly as it came, thinking signature
// included, even after being saved to disk and loaded again.
func TestClient_Complete_ReplaysAssistantMessage(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{stream: toolUseStream()}
	client := api.client(t)
	first, err := client.Complete(context.Background(), baseRequest())
	if err != nil {
		t.Fatal(err)
	}

	saved, err := json.Marshal(first.Message)
	if err != nil {
		t.Fatal(err)
	}
	var loaded provider.Message
	if err := json.Unmarshal(saved, &loaded); err != nil {
		t.Fatal(err)
	}
	req := baseRequest()
	req.Messages = append(req.Messages, loaded, provider.Message{Role: provider.RoleUser, Blocks: []provider.Block{
		provider.ToolResult("toolu_1", "conteúdo de a.txt", false),
		{Type: provider.BlockText, Text: "[aviso] algo"},
	}})
	if _, err := client.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	messages := api.requests[1].body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("quero 3 mensagens, veio %d", len(messages))
	}
	assistant := messages[1].(map[string]any)
	content := assistant["content"].([]any)
	if assistant["role"] != "assistant" || len(content) != 3 {
		t.Fatalf("mensagem do assistente inesperada: %v", assistant)
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["thinking"] != "vou ler o arquivo" || thinking["signature"] != "assinatura-123" {
		t.Errorf("o raciocínio não voltou igual: %v", thinking)
	}
	toolUse := content[2].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "toolu_1" || toolUse["input"].(map[string]any)["caminho"] != "a.txt" {
		t.Errorf("a chamada de ferramenta não voltou igual: %v", toolUse)
	}
	results := messages[2].(map[string]any)["content"].([]any)
	result := results[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" {
		t.Errorf("resultado da ferramenta inesperado: %v", result)
	}
	if results[1].(map[string]any)["text"] != "[aviso] algo" {
		t.Errorf("o texto depois do resultado se perdeu: %v", results[1])
	}
}

func TestClient_Complete_MessagesFromAnotherProvider(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{stream: toolUseStream()}
	req := baseRequest()
	req.Messages = append(req.Messages, provider.Message{Role: provider.RoleAssistant, Provider: "outro", Blocks: []provider.Block{
		{Type: provider.BlockOpaque, Raw: json.RawMessage(`{"type":"coisa_do_outro"}`)},
		{Type: provider.BlockText, Text: "texto do outro", Raw: json.RawMessage(`{"formato":"do outro"}`)},
		{Type: provider.BlockToolUse, ToolUseID: "x1", ToolName: "ler_arquivo", Input: json.RawMessage(`{"caminho":"b"}`)},
	}}, provider.Message{Role: provider.RoleUser, Blocks: []provider.Block{provider.ToolResult("x1", "falhou", true)}})
	if _, err := api.client(t).Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	messages := api.requests[0].body["messages"].([]any)
	content := messages[1].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("o bloco opaco de outro provedor deveria ficar de fora: %v", content)
	}
	if content[0].(map[string]any)["text"] != "texto do outro" || content[1].(map[string]any)["name"] != "ler_arquivo" {
		t.Errorf("texto e chamada deveriam ser remontados dos campos neutros: %v", content)
	}
	if result := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any); result["is_error"] != true {
		t.Errorf("o erro da ferramenta deveria ir marcado: %v", result)
	}
}

func TestClient_Complete_ModelVariations(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{stream: toolUseStream()}
	req := baseRequest()
	req.Model = "claude-haiku-5-5"
	req.Effort = ""
	req.System = ""
	if _, err := api.client(t).Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	body := api.requests[0].body
	if _, has := body["fallbacks"]; has {
		t.Error("o haiku não tem fallback de recusa no servidor")
	}
	if _, has := body["output_config"]; has {
		t.Error("sem esforço definido o campo não deve ir")
	}
	if _, has := body["system"]; has {
		t.Error("instruções vazias não viram bloco")
	}
	tools := body["tools"].([]any)
	if search := tools[len(tools)-1].(map[string]any); search["type"] != "web_search_20250305" {
		t.Errorf("o haiku usa a versão básica da pesquisa: %v", search["type"])
	}
}

func TestClient_Complete_Refusal(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{stream: sse(
		messageStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"resposta pela metade"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"motivo"}},"usage":{"output_tokens":3}}`,
		`{"type":"message_stop"}`,
	)}
	resp, err := api.client(t).Complete(context.Background(), baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != provider.StopRefusal {
		t.Fatalf("Stop = %q, quero refusal", resp.Stop)
	}
	if len(resp.Message.Blocks) != 0 {
		t.Errorf("a saída parcial de uma recusa não é resposta: %+v", resp.Message.Blocks)
	}
}

func TestClient_Complete_APIError(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{status: http.StatusUnauthorized}
	_, err := api.client(t).Complete(context.Background(), baseRequest())
	if err == nil || !strings.Contains(err.Error(), "recusou a chave") || !strings.Contains(err.Error(), APIKeyEnv) {
		t.Errorf("erro = %v; quero dizer que a chave foi recusada e qual variável conferir", err)
	}
}

func TestNew_RequiresAPIKey(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	if _, err := New(); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("sem a variável, erro = %v; quero ErrNoAPIKey", err)
	}
	t.Setenv(APIKeyEnv, "qualquer")
	if client, err := New(); err != nil || client == nil || !client.Capabilities().NativeWebSearch {
		t.Errorf("com a variável deveria criar o cliente: %v", err)
	}
}
