//go:build integration

package embed

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MatheusGustav/mister/internal/agent"
	"github.com/MatheusGustav/mister/internal/memory"
	"github.com/MatheusGustav/mister/internal/provider"
)

// Runs against the real llama-server and model. Point MISTER_EMBEDDING_DIR at
// the folder 'mister install' fills (~/.mister/embedding):
//
//	MISTER_EMBEDDING_DIR=~/.mister/embedding go test -tags=integration ./internal/embed/
func TestLocal_RealServer_RecallInPortuguese(t *testing.T) {
	dir := os.Getenv("MISTER_EMBEDDING_DIR")
	if dir == "" {
		t.Skip("defina MISTER_EMBEDDING_DIR com a pasta do servidor de embedding instalado")
	}
	local := NewLocal(dir)
	if !local.Installed() {
		t.Fatalf("não há servidor de embedding instalado em %s", dir)
	}
	defer local.Close()

	base := t.TempDir()
	store := memory.Open(filepath.Join(base, "notas"), filepath.Join(base, "indice.json"), local, Thresholds())
	notes := map[string]string{
		"Celular Redmi":       "O celular de trabalho é um Redmi com 6 GB de RAM e 128 GB. Sobre a carga, veja [[Bateria do celular]]. O servidor fica em [[Servidor do cocar]].",
		"Bateria do celular":  "A bateria do celular Redmi é de 5000 mAh e dura dois dias. O aparelho está em [[Celular Redmi]].",
		"Servidor do cocar":   "O alias ssh é servidor, entra como root. A chave tem senha: rodar ssh-add antes, senão dá Permission denied.",
		"Portas do Curumirim": "O ambiente de desenvolvimento usa as portas 8080, 3307 e 5174.",
		"tuned powersave":     "Com o perfil powersave do tuned o CPU fica preso em 1200 MHz. Conferir no_turbo antes de investigar lentidão.",
	}
	for title, body := range notes {
		if _, err := store.Write(title, body); err != nil {
			t.Fatal(err)
		}
	}
	// A dense note, far more tokens per character than prose: it must not
	// break the index.
	rng := rand.New(rand.NewSource(1))
	var dense strings.Builder
	for range 9000 {
		dense.WriteByte("0123456789abcdef{}();=<>/"[rng.Intn(25)])
	}
	if _, err := store.Write("Despejo", dense.String()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()

	related := []struct {
		query string
		entry string
	}{
		{"quanta memória tem meu celular?", "celular-redmi"},
		{"deu permission denied no ssh", "servidor-do-cocar"},
		{"em qual porta roda o banco do curumirim?", "portas-do-curumirim"},
		{"o notebook está lento, o processador não passa de 1.2 GHz", "tuned-powersave"},
	}
	for _, tt := range related {
		got, err := store.Recall(ctx, tt.query)
		if err != nil {
			t.Fatalf("%q: %v", tt.query, err)
		}
		if len(got) == 0 || got[0].Name != tt.entry {
			t.Errorf("%q deveria trazer %s primeiro, trouxe %+v", tt.query, tt.entry, names(got))
		}
	}

	// The walk goes from the battery note to the phone note, which is still on
	// the subject, and does not follow the phone's link to the server.
	got, err := store.Recall(ctx, "quanto dura a bateria do celular redmi?")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(names(got), ",")
	if !strings.Contains(joined, "celular-redmi") || !strings.Contains(joined, "bateria-do-celular") || strings.Contains(joined, "servidor-do-cocar") {
		t.Errorf("a caminhada deveria trazer o celular e a bateria, sem o servidor: %s", joined)
	}

	for _, query := range []string{"oi, tudo bem?", "ajusta o padding do botão de salvar", "qual a previsão do tempo amanhã?", "crie o arquivo README do projeto"} {
		got, err := store.Recall(ctx, query)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		if len(got) != 0 {
			t.Errorf("%q não tem a ver com nenhuma nota, mas trouxe %v", query, names(got))
		}
	}
	t.Logf("6 notas indexadas e 9 buscas em %s", time.Since(start).Round(time.Millisecond))
}

func names(notes []memory.Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.Name
	}
	return out
}

func realServer(t *testing.T) *Local {
	t.Helper()
	dir := os.Getenv("MISTER_EMBEDDING_DIR")
	if dir == "" {
		t.Skip("defina MISTER_EMBEDDING_DIR com a pasta do servidor de embedding instalado")
	}
	local := NewLocal(dir)
	if !local.Installed() {
		t.Fatalf("não há servidor de embedding instalado em %s", dir)
	}
	t.Cleanup(local.Close)
	return local
}

func TestLocal_RealServer_SimilarNotes(t *testing.T) {
	local := realServer(t)
	base := t.TempDir()
	store := memory.Open(filepath.Join(base, "notas"), filepath.Join(base, "indice.json"), local, Thresholds())
	for title, body := range map[string]string{
		"Celular Redmi":       "O celular de trabalho é um Redmi com 6 GB de RAM e 128 GB. A depuração USB fica ligada e o ADB tem travas de segurança.",
		"Portas do Curumirim": "O ambiente de desenvolvimento usa as portas 8080, 3307 e 5174. O Cocar usa 8000, 3306 e 5173.",
		"Servidor do cocar":   "O alias ssh é servidor, entra como root. A chave tem senha: rodar ssh-add antes, senão dá Permission denied.",
	} {
		if _, err := store.Write(title, body); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	same := []struct{ title, content, want string }{
		{"Smartphone", "Meu telefone de trabalho tem 6 GB de memória e 128 de armazenamento, é um Redmi.", "celular-redmi"},
		{"Acesso SSH produção", "Para entrar no servidor do Cocar: ssh servidor (root). Precisa do ssh-add porque a chave tem senha.", "servidor-do-cocar"},
		{"Portas dev", "Curumirim sobe em 8080 (web), 3307 (mysql) e 5174 (vite).", "portas-do-curumirim"},
	}
	for _, tt := range same {
		got, err := store.Similar(ctx, tt.title, tt.content)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Name != tt.want {
			t.Errorf("%q deveria ser reconhecida como a nota %s: %+v", tt.title, tt.want, got)
		}
	}
	different := []struct{ title, content string }{
		{"Tablet Redmi Pad 2", "O tablet pessoal é um Redmi Pad 2 com 8 GB e 256 GB, Android 16, sem travas."},
		{"Bateria do celular", "A bateria do Redmi é de 5000 mAh e dura dois dias."},
		{"Portas do Cocar em produção", "Em produção o Cocar fica atrás do nginx na 443; o MySQL não é exposto."},
		{"Asaas", "O webhook do Asaas tem que responder 200 exato; outro 2xx conta como falha."},
	}
	for _, tt := range different {
		got, err := store.Similar(ctx, tt.title, tt.content)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Errorf("%q é outro assunto, mas foi tomada como a nota %s (%.2f)", tt.title, got.Name, got.Score)
		}
	}
}

func TestLocal_RealServer_PastSessions(t *testing.T) {
	local := realServer(t)
	dir := t.TempDir()
	write := func(id, project string, pairs ...string) {
		store, err := agent.OpenStore(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i+1 < len(pairs); i += 2 {
			for _, msg := range []provider.Message{
				provider.UserText(pairs[i]),
				{Role: provider.RoleAssistant, Blocks: []provider.Block{{Type: provider.BlockText, Text: pairs[i+1]}}},
			} {
				if err := store.Append(agent.HostID, msg); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := store.SaveMeta(agent.SessionMeta{ID: id, Project: project, Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	write("2026-11-20-100000-aaaa", "/proj/vitrine",
		"vamos usar pix ou cartão no checkout da vitrine?", "Decidimos começar só com Pix pelo Mercado Pago, com validade igual à da reserva; cartão fica para depois do lançamento.",
		"o botão de salvar da agenda fica cortado no celular", "Ajustei o padding do rodapé da agenda e troquei a grade para minmax(0,1fr). Testes verdes; falta você conferir a tela no celular.",
		"oi", "Oi! Em que posso ajudar?")
	write("2026-11-21-100000-bbbb", "/proj/vitrine",
		"a suíte tá demorando demais", "Rodei no sqlite em memória: 8 minutos. Os testes de concorrência no MySQL levam mais 11 e ficam para o host rodar depois do merge.")

	sessions := memory.OpenSessions(dir, local, SessionThreshold, "sessao-atual")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	related := []struct{ query, want string }{
		{"o que a gente decidiu sobre forma de pagamento no checkout?", "começar só com Pix"},
		{"quanto tempo leva a suíte de testes?", "8 minutos"},
		{"como ficou o problema do botão cortado na agenda?", "padding do rodapé"},
	}
	for _, tt := range related {
		found, err := sessions.Search(ctx, tt.query)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) == 0 || !strings.Contains(found[0].Text, tt.want) {
			t.Errorf("%q deveria trazer o trecho com %q: %+v", tt.query, tt.want, found)
		}
	}
	for _, query := range []string{"ajusta o login", "roda os testes", "crie um componente de calendário", "o que tem para hoje?"} {
		found, err := sessions.Search(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 0 {
			t.Errorf("%q não tem a ver com nenhuma conversa, mas trouxe %q (%.2f)", query, found[0].Text, found[0].Score)
		}
	}
}
