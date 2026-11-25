package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseExa(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{name: "embrulhado em SSE", body: "event: message\ndata: {\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Title: A\"},{\"type\":\"text\",\"text\":\"Title: B\"}]}}\n\n", want: "Title: A\nTitle: B"},
		{name: "JSON puro", body: `{"result":{"content":[{"type":"text","text":" achado "}]}}`, want: "achado"},
		{name: "quadro de controle antes", body: "data: ping\ndata: {\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n", want: "ok"},
		{name: "erro do protocolo", body: `data: {"error":{"message":"tool not found"}}`, wantErr: "tool not found"},
		{name: "erro da ferramenta", body: `{"result":{"isError":true,"content":[{"type":"text","text":"url inválida"}]}}`, wantErr: "url inválida"},
		{name: "sem texto", body: `{"result":{"content":[]}}`, wantErr: "sem texto"},
		{name: "fora do formato", body: "<html>erro</html>", wantErr: "fora do formato"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseExa([]byte(tt.body))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("erro = %v, quero conter %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("parseExa = %q, %v; quero %q", got, err, tt.want)
			}
		})
	}
}

func TestExaTool(t *testing.T) {
	t.Parallel()
	var gotBody map[string]any
	var gotAgent string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgent = r.Header.Get("User-Agent")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "event: message\ndata: {\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Title: Go\\nURL: https://go.dev\"}]}}\n\n")
	}))
	defer srv.Close()
	exa := &Exa{URL: srv.URL, Client: srv.Client()}
	search := exa.Tool()
	run := func(input string) (string, bool) {
		res := search.Run(context.Background(), nil, json.RawMessage(input))
		return res.Content, res.IsError
	}

	out, isErr := run(`{"busca":"  como   usar errgroup  ","quantidade":99}`)
	if isErr || !strings.Contains(out, "https://go.dev") {
		t.Fatalf("pesquisa: %q erro=%v", out, isErr)
	}
	params := gotBody["params"].(map[string]any)
	args := params["arguments"].(map[string]any)
	if params["name"] != "web_search_exa" || args["query"] != "como usar errgroup" || args["numResults"] != float64(exaMaxResults) {
		t.Errorf("pedido inesperado: %v", gotBody)
	}
	if gotAgent == "" || strings.HasPrefix(gotAgent, "Go-http-client") {
		t.Errorf("User-Agent = %q; a Exa recusa o padrão do Go", gotAgent)
	}

	if out, isErr := run(`{"busca":" "}`); !isErr || !strings.Contains(out, "vazia") {
		t.Errorf("busca vazia: %q", out)
	}
	status = http.StatusForbidden
	if out, isErr := run(`{"busca":"x"}`); !isErr || !strings.Contains(out, "HTTP 403") {
		t.Errorf("erro HTTP deveria voltar claro: %q", out)
	}
}
