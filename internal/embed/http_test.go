package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MatheusGustav/mister/internal/memory"
)

// fakeEmbeddings answers /embeddings with one vector per input, whose first
// value is the length of the text; it refuses inputs longer than maxLen.
type fakeEmbeddings struct {
	maxLen int
	status int

	mu      sync.Mutex
	batches [][]string
	model   string
	path    string
}

func (f *fakeEmbeddings) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	f.mu.Lock()
	f.batches = append(f.batches, body.Input)
	f.model, f.path = body.Model, r.URL.Path
	f.mu.Unlock()

	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `{"error":"sem acesso"}`)
		return
	}
	type item struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	var items []item
	for i, text := range body.Input {
		if f.maxLen > 0 && len(text) > f.maxLen {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"input is too large to process"}}`)
			return
		}
		items = append(items, item{Index: i, Embedding: []float32{float32(len(text)), 1}})
	}
	// Out of order on purpose: the client must sort by index.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": items})
}

// serve starts the fake server and returns a client for it and its base URL.
func serve(t *testing.T, f *fakeEmbeddings) (*client, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	h := newClient(model{name: "modelo-x", queryPrefix: "busca: ", docPrefix: "titulo: {titulo} | texto: "})
	h.http = srv.Client()
	return h, srv.URL + "/v1/"
}

func TestClient_EmbedQuery(t *testing.T) {
	t.Parallel()
	f := &fakeEmbeddings{}
	h, base := serve(t, f)
	v, err := h.embedQuery(context.Background(), base, "porta do banco")
	if err != nil {
		t.Fatal(err)
	}
	want := "busca: porta do banco"
	if len(f.batches) != 1 || f.batches[0][0] != want || v[0] != float32(len(want)) {
		t.Errorf("pedido %v, vetor %v", f.batches, v)
	}
	if f.path != "/v1/embeddings" || f.model != "modelo-x" {
		t.Errorf("caminho %q, modelo %q", f.path, f.model)
	}
}

func TestClient_EmbedDocuments_BatchesAndKeepsOrder(t *testing.T) {
	t.Parallel()
	f := &fakeEmbeddings{}
	h, base := serve(t, f)
	var docs []memory.Document
	for i := range batchSize + 3 {
		docs = append(docs, memory.Document{Title: "nota", Text: strings.Repeat("x", i+1)})
	}
	vectors, err := h.embedDocuments(context.Background(), base, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.batches) != 2 || len(f.batches[0]) != batchSize || len(f.batches[1]) != 3 {
		t.Fatalf("quero 2 lotes (%d e 3), veio %d", batchSize, len(f.batches))
	}
	if got := f.batches[0][0]; got != "titulo: nota | texto: x" {
		t.Errorf("prefixo da nota = %q", got)
	}
	prefix := len("titulo: nota | texto: ")
	for i, v := range vectors {
		if int(v[0]) != prefix+i+1 {
			t.Fatalf("o vetor %d não corresponde ao texto %d (tamanho %v)", i, i, v[0])
		}
	}
}

func TestClient_ShortensTextTheServerRefuses(t *testing.T) {
	t.Parallel()
	f := &fakeEmbeddings{maxLen: 1000}
	h, base := serve(t, f)
	docs := []memory.Document{
		{Title: "a", Text: "curto"},
		{Title: "b", Text: strings.Repeat("denso ", 2000)},
		{Title: "c", Text: "curto também"},
	}
	vectors, err := h.embedDocuments(context.Background(), base, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 3 {
		t.Fatalf("quero 3 vetores, veio %d", len(vectors))
	}
	if got := int(vectors[1][0]); got > 1000 || got < 500 {
		t.Errorf("o texto longo deveria ser cortado pela metade até caber: foi com %d caracteres", got)
	}
	if int(vectors[0][0]) != len("titulo: a | texto: curto") || int(vectors[2][0]) != len("titulo: c | texto: curto também") {
		t.Errorf("os textos curtos não deveriam ser cortados: %v %v", vectors[0], vectors[2])
	}
	for _, batch := range f.batches {
		for _, text := range batch {
			if len(text) > maxChars {
				t.Errorf("um texto de %d caracteres passou do corte inicial", len(text))
			}
		}
	}
}

func TestClient_Errors(t *testing.T) {
	t.Parallel()
	t.Run("erro que não é de tamanho não insiste", func(t *testing.T) {
		t.Parallel()
		f := &fakeEmbeddings{status: http.StatusServiceUnavailable}
		h, base := serve(t, f)
		_, err := h.embedDocuments(context.Background(), base, []memory.Document{{Text: strings.Repeat("x", 3000)}, {Text: "y"}})
		if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "sem acesso") {
			t.Errorf("erro = %v", err)
		}
		if len(f.batches) != 1 {
			t.Errorf("fez %d pedidos; servidor indisponível não melhora cortando o texto", len(f.batches))
		}
	})
	t.Run("servidor fora do ar", func(t *testing.T) {
		t.Parallel()
		h := newClient(model{name: "m"})
		if _, err := h.embedQuery(context.Background(), "http://127.0.0.1:1/v1", "x"); err == nil || !strings.Contains(err.Error(), "sem resposta") {
			t.Errorf("erro = %v", err)
		}
	})
	t.Run("resposta com vetores a menos", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"data":[]}`)
		}))
		defer srv.Close()
		h := newClient(model{name: "m"})
		if _, err := h.embedQuery(context.Background(), srv.URL, "x"); err == nil || !strings.Contains(err.Error(), "0 vetores para 1 textos") {
			t.Errorf("erro = %v", err)
		}
	})
}

func TestClient_ID_ChangesWithModelAndPrefixes(t *testing.T) {
	t.Parallel()
	a := newClient(model{name: "m", queryPrefix: "q", docPrefix: "d"})
	b := newClient(model{name: "m", queryPrefix: "q", docPrefix: "d"})
	c := newClient(model{name: "m", queryPrefix: "q2", docPrefix: "d"})
	if a.id() != b.id() {
		t.Error("o mesmo modelo com os mesmos prefixos dá os mesmos vetores: o índice vale")
	}
	if a.id() == c.id() {
		t.Error("mudar o prefixo muda os vetores: o índice precisa ser refeito")
	}
}
