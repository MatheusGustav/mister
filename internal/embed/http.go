// Package embed holds the embedding providers of the memory: a client for any
// server that speaks the OpenAI /v1/embeddings format, and a managed local
// llama-server for when nothing else is configured.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MatheusGustav/mister/internal/memory"
)

const (
	batchSize = 16
	// maxChars is where a text is cut before the first try. A text that is
	// still too long for the model (dense text has many tokens per character)
	// is halved until the server accepts it.
	maxChars    = 6000
	minChars    = 200
	maxBodySize = 64 << 20
)

// statusError is an answer of the embedding server with an error status.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("o servidor de embedding devolveu HTTP %d: %s", e.code, e.body)
}

// retryShorter reports whether a shorter text could be accepted: the server
// refused the input, not the caller.
func (e *statusError) retryShorter() bool {
	switch e.code {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusInternalServerError:
		return true
	}
	return false
}

// Model says how to talk to an embedding model. The prefixes are the task
// dialect some models are trained with; "{titulo}" in DocPrefix becomes the
// title of the note.
type Model struct {
	Name        string
	QueryPrefix string
	DocPrefix   string
}

// HTTP embeds through POST {BaseURL}/embeddings. It serves Ollama,
// llama-server, LM Studio, OpenAI, Gemini's compatibility endpoint and others.
type HTTP struct {
	// BaseURL ends at the API root, for example "http://localhost:11434/v1".
	BaseURL string
	APIKey  string
	Model   Model
	Client  *http.Client
}

func NewHTTP(baseURL, apiKey string, model Model) *HTTP {
	return &HTTP{BaseURL: baseURL, APIKey: apiKey, Model: model, Client: &http.Client{Timeout: 2 * time.Minute}}
}

func (h *HTTP) ID() string {
	return h.Model.Name + "|" + h.Model.QueryPrefix + "|" + h.Model.DocPrefix
}

func (h *HTTP) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	return h.embedOne(ctx, h.BaseURL, h.Model.QueryPrefix+query)
}

func (h *HTTP) EmbedDocuments(ctx context.Context, docs []memory.Document) ([][]float32, error) {
	return h.embedDocuments(ctx, h.BaseURL, docs)
}

func (h *HTTP) embedDocuments(ctx context.Context, baseURL string, docs []memory.Document) ([][]float32, error) {
	out := make([][]float32, 0, len(docs))
	for start := 0; start < len(docs); start += batchSize {
		end := min(start+batchSize, len(docs))
		texts := make([]string, 0, end-start)
		for _, d := range docs[start:end] {
			texts = append(texts, strings.ReplaceAll(h.Model.DocPrefix, "{titulo}", d.Title)+d.Text)
		}
		vectors, err := h.embed(ctx, baseURL, texts)
		var refused *statusError
		if errors.As(err, &refused) && refused.retryShorter() {
			// One text of the batch may be too long: send them one by one.
			vectors = vectors[:0]
			for _, text := range texts {
				v, oneErr := h.embedOne(ctx, baseURL, text)
				if oneErr != nil {
					return nil, oneErr
				}
				vectors = append(vectors, v)
			}
			err = nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, vectors...)
	}
	return out, nil
}

// embedOne embeds one text, cutting it in half while the server refuses it.
func (h *HTTP) embedOne(ctx context.Context, baseURL, text string) ([]float32, error) {
	for {
		vectors, err := h.embed(ctx, baseURL, []string{text})
		if err == nil {
			return vectors[0], nil
		}
		var refused *statusError
		if !errors.As(err, &refused) || !refused.retryShorter() || len(text) <= minChars {
			return nil, err
		}
		text = strings.ToValidUTF8(text[:len(text)/2], "")
	}
}

func (h *HTTP) embed(ctx context.Context, baseURL string, texts []string) ([][]float32, error) {
	cut := make([]string, len(texts))
	for i, t := range texts {
		if len(t) > maxChars {
			t = strings.ToValidUTF8(t[:maxChars], "")
		}
		cut[i] = t
	}
	body, err := json.Marshal(map[string]any{"model": h.Model.Name, "input": cut})
	if err != nil {
		return nil, fmt.Errorf("montando o pedido de embedding: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("montando o pedido de embedding: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.APIKey)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sem resposta do servidor de embedding em %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("lendo a resposta do servidor de embedding: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{code: resp.StatusCode, body: snippet(data)}
	}

	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("resposta de embedding fora do formato: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("o servidor de embedding devolveu %d vetores para %d textos", len(parsed.Data), len(texts))
	}
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	vectors := make([][]float32, len(texts))
	for i, item := range parsed.Data {
		if len(item.Embedding) == 0 {
			return nil, errors.New("o servidor de embedding devolveu um vetor vazio")
		}
		vectors[i] = item.Embedding
	}
	return vectors, nil
}

func snippet(data []byte) string {
	text := strings.Join(strings.Fields(string(data)), " ")
	if len(text) > 300 {
		text = strings.ToValidUTF8(text[:300], "") + "…"
	}
	return text
}
