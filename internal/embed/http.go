// Package embed is the embedding provider of the memory: a llama-server kept
// and run by the Mister itself, with the EmbeddingGemma model.
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

// model says how to talk to an embedding model. The prefixes are the task
// dialect the model is trained with; "{titulo}" in docPrefix becomes the title
// of the note.
type model struct {
	name        string
	queryPrefix string
	docPrefix   string
}

// client embeds through POST {baseURL}/embeddings, the OpenAI format that
// llama-server speaks.
type client struct {
	model model
	http  *http.Client
}

func newClient(m model) *client {
	return &client{model: m, http: &http.Client{Timeout: 2 * time.Minute}}
}

// id changes whenever the vectors would: another model or other prefixes.
func (h *client) id() string {
	return h.model.name + "|" + h.model.queryPrefix + "|" + h.model.docPrefix
}

func (h *client) embedQuery(ctx context.Context, baseURL, query string) ([]float32, error) {
	return h.embedOne(ctx, baseURL, h.model.queryPrefix+query)
}

func (h *client) embedDocuments(ctx context.Context, baseURL string, docs []memory.Document) ([][]float32, error) {
	out := make([][]float32, 0, len(docs))
	for start := 0; start < len(docs); start += batchSize {
		end := min(start+batchSize, len(docs))
		texts := make([]string, 0, end-start)
		for _, d := range docs[start:end] {
			texts = append(texts, strings.ReplaceAll(h.model.docPrefix, "{titulo}", d.Title)+d.Text)
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
func (h *client) embedOne(ctx context.Context, baseURL, text string) ([]float32, error) {
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

func (h *client) embed(ctx context.Context, baseURL string, texts []string) ([][]float32, error) {
	cut := make([]string, len(texts))
	for i, t := range texts {
		if len(t) > maxChars {
			t = strings.ToValidUTF8(t[:maxChars], "")
		}
		cut[i] = t
	}
	body, err := json.Marshal(map[string]any{"model": h.model.name, "input": cut})
	if err != nil {
		return nil, fmt.Errorf("montando o pedido de embedding: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("montando o pedido de embedding: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
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
