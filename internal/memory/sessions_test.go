package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MatheusGustav/mister/internal/agent"
	"github.com/MatheusGustav/mister/internal/provider"
)

func assistant(blocks ...provider.Block) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Blocks: blocks}
}

func text(s string) provider.Block { return provider.Block{Type: provider.BlockText, Text: s} }

func TestExchanges(t *testing.T) {
	t.Parallel()
	toolUse := provider.Block{Type: provider.BlockToolUse, ToolUseID: "t1", ToolName: "ler_arquivo", Input: json.RawMessage(`{}`)}
	messages := []provider.Message{
		{Role: provider.RoleUser, Blocks: []provider.Block{
			text("Projeto: /x\nBranch: master\nData: 24/11/2026\n\n"),
			text("o botão de salvar da agenda fica cortado no celular"),
			text("[memória] nota que o programa anexou"),
		}},
		assistant(text("Vou olhar o rodapé."), toolUse),
		{Role: provider.RoleUser, Blocks: []provider.Block{
			provider.ToolResult("t1", "conteúdo enorme do arquivo que não interessa", false),
			text("[aviso] o thread x terminou"),
		}},
		assistant(text("Ajustei o padding do rodapé; falta conferir no celular.")),
		{Role: provider.RoleUser, Blocks: []provider.Block{text("oi")}},
		assistant(text("Oi!")),
		{Role: provider.RoleUser, Blocks: []provider.Block{text("e o pix do docker, decidimos o quê?")}},
		assistant(provider.Block{Type: provider.BlockOpaque, Raw: json.RawMessage(`{"type":"thinking"}`)}, text("Decidimos começar só com Pix.")),
		{Role: provider.RoleUser, Blocks: []provider.Block{text("pergunta que ficou sem resposta, comprida o bastante para passar do mínimo de tamanho")}},
	}
	got := Exchanges(messages)
	want := []string{
		"Pedido: o botão de salvar da agenda fica cortado no celular\nResposta: Vou olhar o rodapé.\nAjustei o padding do rodapé; falta conferir no celular.",
		"Pedido: e o pix do docker, decidimos o quê?\nResposta: Decidimos começar só com Pix.",
	}
	if strings.Join(got, "\n===\n") != strings.Join(want, "\n===\n") {
		t.Errorf("Exchanges =\n%s\n\nquero\n%s", strings.Join(got, "\n===\n"), strings.Join(want, "\n===\n"))
	}

	long := []provider.Message{provider.UserText(strings.Repeat("p", 5000)), assistant(text(strings.Repeat("r", 9000)))}
	if got := Exchanges(long); len(got) != 1 || len(got[0]) > exchangeAskMax+exchangeAnswerMax+40 {
		t.Errorf("um trecho comprido deveria ser cortado: %d caracteres", len(got[0]))
	}
}

// writeSession saves a session with one agent transcript.
func writeSession(t *testing.T, dir, id, project, agentID string, messages ...provider.Message) {
	t.Helper()
	store := agent.OpenStore(dir, id)
	for _, m := range messages {
		if err := store.Append(agentID, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveMeta(agent.SessionMeta{ID: id, Project: project, Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func exchange(ask, answer string) []provider.Message {
	return []provider.Message{provider.UserText(ask), assistant(text(answer))}
}

func TestSessions_SearchAndRecall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e := &wordEmbedder{id: "v1"}
	writeSession(t, dir, "2026-11-20-100000-aaaa", "/proj/loja", agent.HostID,
		append(exchange("como fica a bateria do celular redmi no teste de campo?", "Decidimos medir a bateria do celular redmi por dois dias."),
			exchange("e o gato da vizinha, alguém alimentou o gato hoje?", "O gato dorme e o gato come; o gato está bem.")...)...)
	writeSession(t, dir, "2026-11-21-100000-bbbb", "/proj/infra", "ajuste",
		exchange("Pedido: suba o docker na porta certa do docker", "Relatório: o docker usa a porta 8080; a porta 3307 é do banco no docker.")...)
	writeSession(t, dir, "2026-11-24-090000-cccc", "/proj/loja", agent.HostID,
		exchange("o docker de hoje e a porta do docker de hoje", "Resposta da sessão em andamento sobre docker e porta, que não deve voltar.")...)

	s := OpenSessions(dir, e, 0.5, "2026-11-24-090000-cccc")
	ctx := context.Background()

	found, err := s.Search(ctx, "qual porta o docker usa?")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Session != "2026-11-21-100000-bbbb" || found[0].Agent != "ajuste" || found[0].Project != "/proj/infra" {
		t.Fatalf("quero só o trecho do thread da sessão de infra, sem a sessão em andamento: %+v", found)
	}
	if !strings.Contains(found[0].Text, "o docker usa a porta 8080") {
		t.Errorf("o trecho deveria trazer a resposta: %q", found[0].Text)
	}

	text, keys := s.RecallText(ctx, "bateria do celular redmi", nil)
	for _, want := range []string{"[conversas antigas]", "sessão 2026-11-20-100000-aaaa, projeto /proj/loja, agente host", "medir a bateria do celular redmi por dois dias"} {
		if !strings.Contains(text, want) {
			t.Errorf("a lembrança não contém %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "gato") {
		t.Errorf("o trecho sobre outro assunto da mesma sessão não deveria vir:\n%s", text)
	}
	if len(keys) != 1 {
		t.Fatalf("chaves = %v", keys)
	}
	if again, _ := s.RecallText(ctx, "bateria do celular redmi", map[string]bool{keys[0]: true}); again != "" {
		t.Errorf("um trecho já entregue não deveria voltar: %q", again)
	}
	if text, _ := s.RecallText(ctx, "como está o tempo hoje?", nil); text != "" {
		t.Errorf("assunto sem relação não traz trecho: %q", text)
	}
}

func TestSessions_IndexOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e := &wordEmbedder{id: "v1"}
	writeSession(t, dir, "s1", "/p", agent.HostID, exchange("pergunta sobre o docker e a porta do docker", "resposta sobre o docker e a porta do docker")...)
	writeSession(t, dir, "s2", "/p", agent.HostID, exchange("pergunta sobre o gato e o gato de novo aqui", "resposta sobre o gato e o gato de novo aqui")...)
	s := OpenSessions(dir, e, 0.5, "atual")
	ctx := context.Background()

	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if e.docsSeen != 2 {
		t.Fatalf("primeira indexação: %d trechos, quero 2", e.docsSeen)
	}
	if _, err := os.Stat(filepath.Join(dir, "s1", sessionIndexFile)); err != nil {
		t.Errorf("o índice da sessão deveria ficar na pasta dela: %v", err)
	}
	if _, err := s.Search(ctx, "docker"); err != nil {
		t.Fatal(err)
	}
	if e.docsSeen != 2 {
		t.Errorf("sem mudança não deveria indexar de novo (%d trechos)", e.docsSeen)
	}

	// A fresh process reads the saved index instead of embedding again.
	e2 := &wordEmbedder{id: "v1"}
	s2 := OpenSessions(dir, e2, 0.5, "atual")
	if found, err := s2.Search(ctx, "docker porta"); err != nil || len(found) != 1 {
		t.Fatalf("busca com o índice salvo: %v, %v", found, err)
	}
	if e2.docsSeen != 0 {
		t.Errorf("com o índice salvo não deveria gerar embedding dos trechos (%d)", e2.docsSeen)
	}

	// A session that got new messages is indexed again; a deleted one leaves.
	time.Sleep(10 * time.Millisecond)
	store := agent.OpenStore(dir, "s1")
	for _, m := range exchange("mais uma pergunta sobre o celular redmi e bateria", "mais uma resposta sobre o celular redmi e bateria") {
		if err := store.Append(agent.HostID, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, "s2")); err != nil {
		t.Fatal(err)
	}
	if found, err := s2.Search(ctx, "celular redmi bateria"); err != nil || len(found) != 1 {
		t.Fatalf("o trecho novo deveria ser achado: %v, %v", found, err)
	}
	if e2.docsSeen != 2 {
		t.Errorf("só a sessão alterada deveria ser refeita: %d trechos", e2.docsSeen)
	}
	if found, _ := s2.Search(ctx, "gato"); len(found) != 0 {
		t.Errorf("a sessão apagada deveria sair da busca: %+v", found)
	}

	other := &wordEmbedder{id: "v2"}
	s3 := OpenSessions(dir, other, 0.5, "atual")
	if _, err := s3.Search(ctx, "docker"); err != nil {
		t.Fatal(err)
	}
	if other.docsSeen != 2 {
		t.Errorf("trocar de modelo deveria refazer o índice: %d trechos", other.docsSeen)
	}
}

func TestSessions_SearchWaitsALittleForIndexing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e := &wordEmbedder{id: "v1"}
	writeSession(t, dir, "s1", "/p", agent.HostID, exchange("pergunta sobre o docker e a porta do docker", "resposta sobre o docker e a porta do docker")...)
	s := OpenSessions(dir, e, 0.5, "atual")
	ctx := context.Background()

	// An indexing run that never ends: the search gives up waiting and uses
	// what is indexed so far (nothing).
	s.searchWait = 50 * time.Millisecond
	s.syncing <- struct{}{}
	start := time.Now()
	if found, err := s.Search(ctx, "docker"); err != nil || len(found) != 0 {
		t.Errorf("com a indexação travada a busca usa o que já há (nada): %v, %v", found, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("a busca esperou %s pela indexação", elapsed)
	}

	// An indexing run that ends soon: the search waits for it and finds the excerpt.
	s.searchWait = 10 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		<-s.syncing
	}()
	if found, err := s.Search(ctx, "docker"); err != nil || len(found) != 1 {
		t.Errorf("a busca deveria esperar a indexação terminar e achar o trecho: %v, %v", found, err)
	}
}

func TestSessions_WithoutEmbedder(t *testing.T) {
	t.Parallel()
	s := OpenSessions(t.TempDir(), nil, 0.5, "atual")
	if err := s.Sync(context.Background()); err != ErrNoEmbedder {
		t.Errorf("Sync sem embedding: %v", err)
	}
	if text, keys := s.RecallText(context.Background(), "docker", nil); text != "" || keys != nil {
		t.Errorf("sem embedding a lembrança fica calada: %q %v", text, keys)
	}
}
