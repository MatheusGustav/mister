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

	"github.com/MatheusGustav/mister/internal/memory"
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
