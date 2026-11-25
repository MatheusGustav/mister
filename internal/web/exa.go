// Package web holds the web search used when the model provider has no search
// of its own: Exa, through its public MCP endpoint (no key needed).
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MatheusGustav/mister/internal/tool"
)

const (
	DefaultExaURL  = "https://mcp.exa.ai/mcp"
	exaMaxResults  = 10
	exaMaxOutput   = 20_000
	exaMaxBody     = 2 << 20
	exaHTTPTimeout = 30 * time.Second
)

// Exa talks to the Exa MCP endpoint.
type Exa struct {
	URL    string
	Client *http.Client
}

func NewExa() *Exa {
	return &Exa{URL: DefaultExaURL, Client: &http.Client{Timeout: exaHTTPTimeout}}
}

// Tool is the client-side web search tool.
func (e *Exa) Tool() tool.Tool {
	type input struct {
		Busca      string `json:"busca"`
		Quantidade int    `json:"quantidade"`
	}
	return tool.Tool{
		Name: "pesquisar_web",
		Description: "Pesquisa na internet e devolve os achados (título, URL, data e trechos). " +
			"Use para o que depende de informação de fora do projeto: documentação, versão de biblioteca, erro desconhecido. " +
			"Escreva a busca como uma frase que descreve a página ideal, não como palavras soltas.",
		Schema: tool.Object([]string{"busca"}, map[string]any{
			"busca":      tool.Str("o que procurar, em uma frase"),
			"quantidade": tool.Int(fmt.Sprintf("quantos achados trazer, de 1 a %d (opcional, padrão 5)", exaMaxResults)),
		}),
		Parallel: true,
		Run: func(ctx context.Context, _ *tool.Env, raw json.RawMessage) tool.Result {
			in, err := tool.Decode[input](raw)
			if err != nil {
				return tool.Fail("%v", err)
			}
			query := strings.Join(strings.Fields(in.Busca), " ")
			if query == "" {
				return tool.Fail("'busca' veio vazia. Diga o que procurar.")
			}
			count := in.Quantidade
			if count == 0 {
				count = 5
			}
			count = max(1, min(count, exaMaxResults))
			text, err := e.call(ctx, "web_search_exa", map[string]any{"query": query, "numResults": count})
			if err != nil {
				return tool.Fail("a pesquisa na web falhou: %v. Siga sem ela ou tente de novo.", err)
			}
			if len(text) > exaMaxOutput {
				text = strings.ToValidUTF8(text[:exaMaxOutput], "") + "\n[resultado cortado]"
			}
			return tool.OK("%s", text)
		},
	}
}

func (e *Exa) call(ctx context.Context, name string, args map[string]any) (string, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		return "", fmt.Errorf("montando o pedido: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("montando o pedido: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	// The endpoint answers 403 to Go's default User-Agent.
	req.Header.Set("User-Agent", "Mister")

	resp, err := e.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sem resposta da Exa: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, exaMaxBody))
	if err != nil {
		return "", fmt.Errorf("lendo a resposta da Exa: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("a Exa devolveu HTTP %d", resp.StatusCode)
	}
	return parseExa(data)
}

// parseExa reads the JSON-RPC answer, which comes wrapped in SSE ("data: {...}")
// or as plain JSON.
func parseExa(data []byte) (string, error) {
	type rpc struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	candidates := []string{strings.TrimSpace(string(data))}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			candidates = append(candidates, strings.TrimSpace(rest))
		}
	}
	for _, c := range candidates {
		var msg rpc
		if json.Unmarshal([]byte(c), &msg) != nil {
			continue
		}
		if msg.Error != nil {
			return "", fmt.Errorf("a Exa recusou a chamada: %s", msg.Error.Message)
		}
		if msg.Result == nil {
			continue
		}
		var parts []string
		for _, block := range msg.Result.Content {
			if block.Type == "text" && block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		text := strings.TrimSpace(strings.Join(parts, "\n"))
		if msg.Result.IsError {
			return "", fmt.Errorf("a Exa não conseguiu atender: %s", text)
		}
		if text == "" {
			return "", errors.New("a Exa respondeu sem texto")
		}
		return text, nil
	}
	return "", errors.New("a Exa respondeu fora do formato esperado")
}
