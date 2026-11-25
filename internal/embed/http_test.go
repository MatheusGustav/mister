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
	auth    string
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
	f.auth, f.model, f.path = r.Header.Get("Authorization"), body.Model, r.URL.Path
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

func newClient(t *testing.T, f *fakeEmbeddings) *HTTP {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	h := NewHTTP(srv.URL+"/v1/", "segredo", Model{Name: "modelo-x", QueryPrefix: "busca: ", DocPrefix: "titulo: {titulo} | texto: "})
	h.Client = srv.Client()
	return h
}

func TestHTTP_EmbedQuery(t *testing.T) {
	t.Parallel()
	f := &fakeEmbeddings{}
	h := newClient(t, f)
	v, err := h.EmbedQuery(context.Background(), "porta do banco")
	if err != nil {
		t.Fatal(err)
	}
	want := "busca: porta do banco"
	if len(f.batches) != 1 || f.batches[0][0] != want || v[0] != float32(len(want)) {
		t.Errorf("pedido %v, vetor %v", f.batches, v)
	}
	if f.path != "/v1/embeddings" || f.model != "modelo-x" || f.auth != "Bearer segredo" {
		t.Errorf("caminho %q, modelo %q, autorização %q", f.path, f.model, f.auth)
	}
}

func TestHTTP_EmbedDocuments_BatchesAndKeepsOrder(t *testing.T) {
	t.Parallel()
	f := &fakeEmbeddings{}
	h := newClient(t, f)
	var docs []memory.Document
	for i := range batchSize + 3 {
		docs = append(docs, memory.Document{Title: "nota", Text: strings.Repeat("x", i+1)})
	}
	vectors, err := h.EmbedDocuments(context.Background(), docs)
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

func TestHTTP_ShortensTextTheServerRefuses(t *testing.T) {
	t.Parallel()
	f := &fakeEmbeddings{maxLen: 1000}
	h := newClient(t, f)
	docs := []memory.Document{
		{Title: "a", Text: "curto"},
		{Title: "b", Text: strings.Repeat("denso ", 2000)},
		{Title: "c", Text: "curto também"},
	}
	vectors, err := h.EmbedDocuments(context.Background(), docs)
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

func TestHTTP_Errors(t *testing.T) {
	t.Parallel()
	t.Run("sem acesso não insiste", func(t *testing.T) {
		t.Parallel()
		f := &fakeEmbeddings{status: http.StatusUnauthorized}
		h := newClient(t, f)
		_, err := h.EmbedDocuments(context.Background(), []memory.Document{{Text: strings.Repeat("x", 3000)}, {Text: "y"}})
		if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "sem acesso") {
			t.Errorf("erro = %v", err)
		}
		if len(f.batches) != 1 {
			t.Errorf("fez %d pedidos; erro de chave não melhora cortando o texto", len(f.batches))
		}
	})
	t.Run("servidor fora do ar", func(t *testing.T) {
		t.Parallel()
		h := NewHTTP("http://127.0.0.1:1/v1", "", Model{Name: "m"})
		if _, err := h.EmbedQuery(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "sem resposta") {
			t.Errorf("erro = %v", err)
		}
	})
	t.Run("resposta com vetores a menos", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"data":[]}`)
		}))
		defer srv.Close()
		h := NewHTTP(srv.URL, "", Model{Name: "m"})
		if _, err := h.EmbedQuery(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "0 vetores para 1 textos") {
			t.Errorf("erro = %v", err)
		}
	})
}

func TestHTTP_ID_ChangesWithModelAndPrefixes(t *testing.T) {
	t.Parallel()
	a := NewHTTP("u", "", Model{Name: "m", QueryPrefix: "q", DocPrefix: "d"})
	b := NewHTTP("outra-url", "chave", Model{Name: "m", QueryPrefix: "q", DocPrefix: "d"})
	c := NewHTTP("u", "", Model{Name: "m", QueryPrefix: "q2", DocPrefix: "d"})
	if a.ID() != b.ID() {
		t.Error("o mesmo modelo em outro endereço dá os mesmos vetores: o índice vale")
	}
	if a.ID() == c.ID() {
		t.Error("mudar o prefixo muda os vetores: o índice precisa ser refeito")
	}
}
