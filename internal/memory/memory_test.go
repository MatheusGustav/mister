package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MatheusGustav/mister/internal/tool"
)

// wordEmbedder turns a text into counts of a few known words: enough to give
// related texts a high cosine and unrelated ones zero.
type wordEmbedder struct {
	id string

	mu       sync.Mutex
	docCalls int
	docsSeen int
	fail     error
}

var vocabulary = []string{"celular", "redmi", "bateria", "docker", "porta", "gato"}

func (e *wordEmbedder) vector(text string) []float32 {
	text = strings.ToLower(text)
	v := make([]float32, len(vocabulary))
	for i, word := range vocabulary {
		v[i] = float32(strings.Count(text, word))
	}
	return v
}

func (e *wordEmbedder) ID() string { return e.id }

func (e *wordEmbedder) EmbedQuery(_ context.Context, query string) ([]float32, error) {
	if e.fail != nil {
		return nil, e.fail
	}
	return e.vector(query), nil
}

func (e *wordEmbedder) EmbedDocuments(_ context.Context, docs []Document) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return nil, e.fail
	}
	e.docCalls++
	e.docsSeen += len(docs)
	out := make([][]float32, len(docs))
	for i, d := range docs {
		out[i] = e.vector(d.Text)
	}
	return out, nil
}

func newStore(t *testing.T, e Embedder) *Store {
	t.Helper()
	dir := t.TempDir()
	return Open(filepath.Join(dir, "notas"), filepath.Join(dir, "indice.json"), e, Thresholds{Entry: 0.5, Walk: 0.3, Duplicate: 0.9})
}

func TestSlug(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"Celular Redmi", "celular-redmi"},
		{"Configuração do Ônibus", "configuracao-do-onibus"},
		{"  já/existe_assim.md  ", "ja-existe-assim-md"},
		{"celular-redmi", "celular-redmi"},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q, quero %q", tt.in, got, tt.want)
		}
	}
}

func TestChunk(t *testing.T) {
	t.Parallel()
	got := Chunk("intro\n\n# Um\ntexto um\n## Dois\ntexto dois\n\n#semespaco não é título\n# Vazio\n")
	want := []string{"intro", "# Um\ntexto um", "## Dois\ntexto dois\n\n#semespaco não é título", "# Vazio"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Chunk = %q\nquero %q", got, want)
	}
	if got := Chunk("  \n\n"); len(got) != 0 {
		t.Errorf("texto em branco não vira pedaço: %q", got)
	}
}

func TestStore_Write(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	name, err := s.Write("Celular Redmi", "tem 6 GB")
	if err != nil || name != "celular-redmi" {
		t.Fatalf("Write = %q, %v", name, err)
	}
	if _, err := s.Write("celular redmi", "bateria de 5000 mAh"); err != nil {
		t.Fatal(err)
	}
	body, err := s.Read("Celular Redmi")
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Celular Redmi\n\ntem 6 GB\n\nbateria de 5000 mAh"; body != want {
		t.Errorf("nota = %q\nquero %q", body, want)
	}
	if _, err := s.Write("???", "x"); err == nil {
		t.Error("título sem letra nem número deveria falhar")
	}
	if _, err := s.Write("ok", "  "); err == nil {
		t.Error("conteúdo vazio deveria falhar")
	}
}

func TestStore_EditRewriteDelete(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	if _, err := s.Write("Banco de Homologação", "atende na porta 5433\n\nsenha no cofre, porta 5433 só na VPN"); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		body, err := s.Read("banco de homologacao")
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	if _, err := s.Edit("Banco de Homologação", "porta 5433", "porta 5434"); err == nil || !strings.Contains(err.Error(), "2 vezes") {
		t.Errorf("trecho repetido deveria ser recusado dizendo quantas vezes aparece: %v", err)
	}
	if _, err := s.Edit("Banco de Homologação", "porta 9999", "x"); err == nil || !strings.Contains(err.Error(), "não está na nota") {
		t.Errorf("trecho que não existe deveria ser recusado: %v", err)
	}
	name, err := s.Edit("Banco de Homologação", "atende na porta 5433", "atende na porta 5434")
	if err != nil || name != "banco-de-homologacao" {
		t.Fatalf("Edit = %q, %v", name, err)
	}
	if want := "# Banco de Homologação\n\natende na porta 5434\n\nsenha no cofre, porta 5433 só na VPN"; read() != want {
		t.Errorf("nota = %q\nquero %q", read(), want)
	}
	if _, err := s.Edit("Banco de Homologação", ", porta 5433 só na VPN", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "5433") {
		t.Errorf("trecho trocado por vazio deveria sair: %q", read())
	}
	if _, err := s.Edit("Banco de Homologação", read(), ""); err == nil || !strings.Contains(err.Error(), "ficaria vazia") {
		t.Errorf("esvaziar a nota pela edição deveria ser recusado: %v", err)
	}

	if _, err := s.Rewrite("banco de homologacao", "porta 5500, sem VPN"); err != nil {
		t.Fatal(err)
	}
	if want := "# Banco de Homologação\n\nporta 5500, sem VPN"; read() != want {
		t.Errorf("reescrita deveria manter só a linha do título: %q", read())
	}
	if _, err := s.Rewrite("banco de homologacao", "# Banco de testes\n\nporta 5600"); err != nil {
		t.Fatal(err)
	}
	if want := "# Banco de testes\n\nporta 5600"; read() != want {
		t.Errorf("conteúdo com título próprio entra inteiro: %q", read())
	}
	if _, err := s.Rewrite("banco de homologacao", "  "); err == nil {
		t.Error("reescrever com conteúdo vazio deveria falhar")
	}
	if _, err := s.Rewrite("nota que não existe", "x"); err == nil || !strings.Contains(err.Error(), "não existe a nota") {
		t.Errorf("reescrever nota que não existe deveria falhar: %v", err)
	}

	if _, err := s.Delete("Banco de Homologação"); err != nil {
		t.Fatal(err)
	}
	if s.Exists("Banco de Homologação") {
		t.Error("a nota excluída deveria sumir")
	}
	if _, err := s.Delete("Banco de Homologação"); err == nil || !strings.Contains(err.Error(), "não existe a nota") {
		t.Errorf("excluir duas vezes deveria falhar: %v", err)
	}
	if _, err := s.Edit("../fora", "a", "b"); err == nil {
		t.Error("um título não pode levar para fora da pasta da memória")
	}
}

func TestTools_ChangeNotes(t *testing.T) {
	t.Parallel()
	e := &wordEmbedder{id: "v1"}
	s := newStore(t, e)
	tools := s.Tools()
	run := func(name string, input any) tool.Result {
		t.Helper()
		raw, _ := json.Marshal(input)
		for _, tl := range tools {
			if tl.Name == name {
				return tl.Run(context.Background(), nil, raw)
			}
		}
		t.Fatalf("ferramenta %s não existe", name)
		return tool.Result{}
	}
	search := func() string {
		t.Helper()
		return run("buscar_memoria", map[string]any{"busca": "qual porta o docker usa?"}).Content
	}

	if res := run("anotar_memoria", map[string]any{"titulo": "Docker", "conteudo": "o docker usa a porta 8080"}); res.IsError {
		t.Fatalf("anotar: %+v", res)
	}
	if res := run("editar_memoria", map[string]any{"titulo": "Docker", "antigo": "porta 8080", "novo": "porta 9090"}); res.IsError {
		t.Fatalf("editar: %+v", res)
	}
	if got := search(); !strings.Contains(got, "porta 9090") || strings.Contains(got, "8080") {
		t.Errorf("a busca deveria trazer a nota corrigida: %q", got)
	}
	for name, input := range map[string]map[string]any{
		"sem novo":      {"titulo": "Docker", "antigo": "porta 9090"},
		"antigo vazio":  {"titulo": "Docker", "antigo": "", "novo": "x"},
		"iguais":        {"titulo": "Docker", "antigo": "porta", "novo": "porta"},
		"fora da nota":  {"titulo": "Docker", "antigo": "porta 1", "novo": "x"},
		"nota inexiste": {"titulo": "Kubernetes", "antigo": "a", "novo": "b"},
	} {
		if res := run("editar_memoria", input); !res.IsError {
			t.Errorf("editar (%s) deveria falhar: %+v", name, res)
		}
	}

	if res := run("reescrever_memoria", map[string]any{"titulo": "Docker", "conteudo": "o docker agora usa a porta 7070"}); res.IsError {
		t.Fatalf("reescrever: %+v", res)
	}
	if got := search(); !strings.Contains(got, "porta 7070") || strings.Contains(got, "9090") {
		t.Errorf("a busca deveria trazer a nota reescrita: %q", got)
	}
	if res := run("reescrever_memoria", map[string]any{"titulo": "Kubernetes", "conteudo": "x"}); !res.IsError || !strings.Contains(res.Content, "anotar_memoria") {
		t.Errorf("reescrever nota que não existe deveria apontar o anotar_memoria: %+v", res)
	}

	if res := run("excluir_memoria", map[string]any{"titulo": "Docker"}); res.IsError {
		t.Fatalf("excluir: %+v", res)
	}
	if got := search(); !strings.Contains(got, "nenhuma nota") {
		t.Errorf("a nota excluída não pode voltar na busca: %q", got)
	}
	if res := run("excluir_memoria", map[string]any{"titulo": "Docker"}); !res.IsError {
		t.Errorf("excluir nota que não existe deveria falhar: %+v", res)
	}
}

func TestStore_SearchAndRecall(t *testing.T) {
	t.Parallel()
	e := &wordEmbedder{id: "palavras-v1"}
	s := newStore(t, e)
	write := func(title, content string) {
		if _, err := s.Write(title, content); err != nil {
			t.Fatal(err)
		}
	}
	write("Celular Redmi", "O celular redmi tem 6 GB. Veja [[Bateria]] e [[Docker do projeto]] e [[nota que não existe]].")
	write("Bateria", "A bateria do celular dura dois dias. Volta para [[celular-redmi]].")
	write("Docker do projeto", "O docker usa a porta 8080.")
	write("Gato", "O gato dorme.")
	ctx := context.Background()

	ranked, err := s.Search(ctx, "qual a bateria do celular redmi?")
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 4 || ranked[0].Name != "celular-redmi" && ranked[0].Name != "bateria" {
		t.Fatalf("ordem inesperada: %+v", ranked)
	}

	notes, err := s.Recall(ctx, "celular redmi")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range notes {
		names = append(names, n.Name)
	}
	if got := strings.Join(names, ","); got != "celular-redmi,bateria" {
		t.Errorf("Recall = %s; quero a nota de entrada e a ligada que ainda é do assunto, sem a de docker e sem repetir", got)
	}
	if !strings.Contains(notes[0].Body, "6 GB") {
		t.Error("a nota deveria vir inteira")
	}

	if notes, err := s.Recall(ctx, "como está o tempo hoje?"); err != nil || len(notes) != 0 {
		t.Errorf("assunto sem relação não deveria trazer nota: %v, %v", notes, err)
	}
	if notes, err := s.Recall(ctx, "  "); err != nil || notes != nil {
		t.Errorf("busca vazia: %v, %v", notes, err)
	}
}

func TestStore_IndexOnlyEmbedsWhatChanged(t *testing.T) {
	t.Parallel()
	e := &wordEmbedder{id: "palavras-v1"}
	s := newStore(t, e)
	ctx := context.Background()
	if _, err := s.Write("Um", "celular\n\n# Seção\nredmi"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("Dois", "docker"); err != nil {
		t.Fatal(err)
	}
	search := func() {
		t.Helper()
		if _, err := s.Search(ctx, "celular"); err != nil {
			t.Fatal(err)
		}
	}
	search()
	if e.docCalls != 1 || e.docsSeen != 3 {
		t.Fatalf("primeira busca: %d chamadas, %d pedaços; quero 1 lote com 3 pedaços", e.docCalls, e.docsSeen)
	}
	search()
	if e.docCalls != 1 {
		t.Errorf("sem mudança não deveria gerar embedding de nota de novo (%d chamadas)", e.docCalls)
	}

	time.Sleep(10 * time.Millisecond)
	if _, err := s.Write("Dois", "porta 8080"); err != nil {
		t.Fatal(err)
	}
	search()
	if e.docCalls != 2 || e.docsSeen != 4 {
		t.Errorf("só a nota alterada deveria ser refeita: %d chamadas, %d pedaços", e.docCalls, e.docsSeen)
	}

	if err := os.Remove(s.path("um")); err != nil {
		t.Fatal(err)
	}
	ranked, err := s.Search(ctx, "celular")
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 1 || ranked[0].Name != "dois" {
		t.Errorf("a nota apagada deveria sair do índice: %+v", ranked)
	}

	// Another model: the saved vectors no longer mean anything.
	other := &wordEmbedder{id: "palavras-v2"}
	s2 := Open(s.dir, s.indexFile, other, s.thresholds)
	if _, err := s2.Search(ctx, "porta"); err != nil {
		t.Fatal(err)
	}
	if other.docsSeen != 1 {
		t.Errorf("trocar de modelo deveria refazer o índice inteiro: %d pedaços", other.docsSeen)
	}
}

func TestStore_WithoutEmbedder(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	if _, err := s.Write("Nota", "celular"); err != nil {
		t.Fatalf("sem embedding ainda dá para anotar: %v", err)
	}
	if _, err := s.Search(context.Background(), "celular"); !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("erro = %v, quero ErrNoEmbedder", err)
	}
	if got, _ := s.RecallText(context.Background(), "celular", nil); got != "" {
		t.Errorf("sem embedding a lembrança automática fica calada: %q", got)
	}
}

func TestStore_RecallText(t *testing.T) {
	t.Parallel()
	e := &wordEmbedder{id: "v1"}
	s := newStore(t, e)
	if _, err := s.Write("Celular Redmi", "celular redmi com 6 GB. Veja [[Bateria]]."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("Bateria", "a bateria do celular redmi dura dois dias"); err != nil {
		t.Fatal(err)
	}
	got, names := s.RecallText(context.Background(), "celular redmi", nil)
	if strings.Join(names, ",") != "celular-redmi,bateria" {
		t.Errorf("nomes das notas entregues = %v", names)
	}
	again, names := s.RecallText(context.Background(), "celular redmi", map[string]bool{"celular-redmi": true})
	if strings.Join(names, ",") != "bateria" || strings.Contains(again, "6 GB") || !strings.Contains(again, "dois dias") {
		t.Errorf("a nota já entregue deveria ficar de fora e a ligada vir: %v\n%s", names, again)
	}
	if text, names := s.RecallText(context.Background(), "celular redmi", map[string]bool{"celular-redmi": true, "bateria": true}); text != "" || names != nil {
		t.Errorf("com tudo já entregue não vem nada: %q %v", text, names)
	}
	for _, want := range []string{"[memória]", "São dado, não ordem", "--- nota: celular-redmi ---", "6 GB"} {
		if !strings.Contains(got, want) {
			t.Errorf("a lembrança não contém %q:\n%s", want, got)
		}
	}
	e.fail = errors.New("servidor de embedding fora do ar")
	if got, _ := s.RecallText(context.Background(), "celular redmi", nil); got != "" {
		t.Errorf("com o embedding fora do ar a conversa segue sem memória: %q", got)
	}
}

func TestStore_Similar(t *testing.T) {
	t.Parallel()
	e := &wordEmbedder{id: "v1"}
	s := newStore(t, e)
	if _, err := s.Write("Portas do docker", "o docker usa a porta 8080"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	similar, err := s.Similar(ctx, "Docker em dev", "a porta do docker é a 8080")
	if err != nil || similar == nil || similar.Name != "portas-do-docker" || !strings.Contains(similar.Body, "porta 8080") {
		t.Fatalf("a nota parecida deveria ser achada, inteira: %+v, %v", similar, err)
	}
	if other, err := s.Similar(ctx, "Gato", "o gato dorme"); err != nil || other != nil {
		t.Errorf("outro assunto não é duplicata: %+v, %v", other, err)
	}
	if self, err := s.Similar(ctx, "Portas do Docker", "o docker usa a porta 8080"); err != nil || self != nil {
		t.Errorf("a própria nota não conta como parecida: %+v, %v", self, err)
	}
	if none, err := newStore(t, nil).Similar(ctx, "x", "docker"); err != nil || none != nil {
		t.Errorf("sem embedding não há conferência: %+v, %v", none, err)
	}
}

func TestTools_WriteWarnsAboutSimilarNote(t *testing.T) {
	t.Parallel()
	e := &wordEmbedder{id: "v1"}
	s := newStore(t, e)
	write := s.Tools()[0]
	run := func(input map[string]any) tool.Result {
		raw, _ := json.Marshal(input)
		return write.Run(context.Background(), nil, raw)
	}
	if res := run(map[string]any{"titulo": "Portas do docker", "conteudo": "o docker usa a porta 8080"}); res.IsError {
		t.Fatal(res.Content)
	}

	res := run(map[string]any{"titulo": "Docker em dev", "conteudo": "a porta do docker é a 8080"})
	if !res.IsError {
		t.Fatalf("deveria recusar e mostrar a nota parecida: %s", res.Content)
	}
	for _, want := range []string{"portas-do-docker", "o docker usa a porta 8080", "nota_nova: true"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("o aviso não contém %q:\n%s", want, res.Content)
		}
	}
	if s.Exists("Docker em dev") {
		t.Error("a nota parecida não deveria ter sido gravada")
	}

	if res := run(map[string]any{"titulo": "Docker em dev", "conteudo": "a porta do docker é a 8080", "nota_nova": true}); res.IsError || !s.Exists("Docker em dev") {
		t.Errorf("com nota_nova a nota é gravada mesmo parecida: %s", res.Content)
	}
	if res := run(map[string]any{"titulo": "Portas do docker", "conteudo": "a porta 3307 é do banco no docker"}); res.IsError {
		t.Errorf("completar uma nota que existe não passa pela conferência: %s", res.Content)
	}
	if res := run(map[string]any{"titulo": "Gato", "conteudo": "o gato dorme"}); res.IsError {
		t.Errorf("assunto novo é gravado direto: %s", res.Content)
	}

	e.fail = errors.New("servidor fora do ar")
	if res := run(map[string]any{"titulo": "Outra do docker", "conteudo": "docker porta"}); res.IsError {
		t.Errorf("com o embedding fora do ar a nota é gravada sem conferência: %s", res.Content)
	}
}

func TestTools(t *testing.T) {
	t.Parallel()
	e := &wordEmbedder{id: "v1"}
	s := newStore(t, e)
	tools := s.Tools()
	run := func(name string, input any) tool.Result {
		t.Helper()
		raw, _ := json.Marshal(input)
		for _, tl := range tools {
			if tl.Name == name {
				return tl.Run(context.Background(), nil, raw)
			}
		}
		t.Fatalf("ferramenta %s não existe", name)
		return tool.Result{}
	}

	if res := run("buscar_memoria", map[string]any{"busca": "docker"}); res.IsError || !strings.Contains(res.Content, "nenhuma nota") {
		t.Errorf("memória vazia: %+v", res)
	}
	if res := run("anotar_memoria", map[string]any{"titulo": "Docker", "conteudo": "o docker usa a porta 8080"}); res.IsError || !strings.Contains(res.Content, "docker") {
		t.Fatalf("anotar: %+v", res)
	}
	if res := run("anotar_memoria", map[string]any{"titulo": "Docker"}); !res.IsError {
		t.Error("anotar sem conteúdo deveria falhar")
	}
	res := run("buscar_memoria", map[string]any{"busca": "qual porta o docker usa?"})
	if res.IsError || !strings.Contains(res.Content, "porta 8080") {
		t.Errorf("buscar deveria trazer a nota inteira: %+v", res)
	}
	if res := run("buscar_memoria", map[string]any{"busca": ""}); !res.IsError {
		t.Error("busca vazia deveria falhar")
	}

	noEmbedder := newStore(t, nil)
	raw, _ := json.Marshal(map[string]any{"busca": "docker"})
	if res := noEmbedder.Tools()[1].Run(context.Background(), nil, raw); !res.IsError || !strings.Contains(res.Content, "sem provedor de embedding") {
		t.Errorf("sem embedding a busca deveria dizer o motivo: %+v", res)
	}
}
