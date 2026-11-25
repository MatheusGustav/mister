package browser

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/MatheusGustav/mister/internal/provider"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func zipOf(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o755)
		f, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func newTestManager(t *testing.T, src Source) *Manager {
	t.Helper()
	m := New(filepath.Join(t.TempDir(), "browser"), 2, time.Minute, 5*time.Second)
	m.Source = src
	t.Cleanup(m.Close)
	return m
}

func TestManager_Install_ManyRequestsDownloadOnce(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("o navegador só é baixado para linux x86_64")
	}
	t.Parallel()
	archive := zipOf(t, map[string]string{"shell/chrome-headless-shell": "#!/bin/sh\n", "shell/locales/pt-BR.pak": "pak"})
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		// Long enough for every request to arrive while the first one downloads.
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	m := newTestManager(t, Source{Version: "1.2.3", URL: srv.URL + "/shell.zip", SHA256: sum(archive), Binary: "shell/chrome-headless-shell"})
	var logMu sync.Mutex
	var log []string
	m.Logf = func(format string, _ ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		log = append(log, format)
	}

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { errs[i] = m.ensureInstalled(context.Background()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("pedido %d: %v", i, err)
		}
	}
	if got := downloads.Load(); got != 1 {
		t.Errorf("houve %d downloads para 8 pedidos ao mesmo tempo, quero 1", got)
	}
	if !m.Installed() {
		t.Fatal("depois do download, Installed deveria ser verdadeiro")
	}
	if want := filepath.Join(m.Dir, "1.2.3", "shell", "chrome-headless-shell"); m.execPath() != want {
		t.Errorf("o navegador fica em %s, quero %s", m.execPath(), want)
	}
	if info, err := os.Stat(m.execPath()); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("o navegador deveria ficar executável: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "1.2.3", "shell", "locales", "pt-BR.pak")); err != nil {
		t.Errorf("os arquivos ao lado do navegador se perderam: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(m.Dir, ".download-*")); len(leftovers) > 0 {
		t.Errorf("a pasta do download deveria ser apagada: %v", leftovers)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(log) == 0 || !strings.Contains(log[0], "baixando o navegador") {
		t.Errorf("o download deveria ser avisado ao usuário: %v", log)
	}
}

func TestManager_Install_Failures(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("o navegador só é baixado para linux x86_64")
	}
	t.Parallel()
	archive := zipOf(t, map[string]string{"shell/chrome-headless-shell": "#!/bin/sh\n"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shell.zip" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	// The subtests run in parallel, after this function returns.
	t.Cleanup(srv.Close)
	good := Source{Version: "1", URL: srv.URL + "/shell.zip", SHA256: sum(archive), Binary: "shell/chrome-headless-shell"}

	tests := []struct {
		name   string
		change func(*Source)
		want   string
	}{
		{"arquivo que não confere", func(s *Source) { s.SHA256 = strings.Repeat("0", 64) }, "não confere"},
		{"download que falha", func(s *Source) { s.URL = srv.URL + "/nao-existe" }, "HTTP 404"},
		{"arquivo sem o navegador dentro", func(s *Source) { s.Binary = "outro/chrome" }, "não trouxe o navegador"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := good
			tt.change(&src)
			m := newTestManager(t, src)
			err := m.ensureInstalled(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("erro = %v, quero um com %q", err, tt.want)
			}
			if m.Installed() {
				t.Error("uma instalação que falhou não pode parecer instalada")
			}
			if left, _ := os.ReadDir(m.Dir); len(left) > 0 {
				t.Errorf("a instalação que falhou deixou arquivos em %s: %v", m.Dir, left)
			}
		})
	}

}

func TestExtractZip_RefusesEntriesOutsideTheFolder(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	archive := filepath.Join(base, "a.zip")
	if err := os.WriteFile(archive, zipOf(t, map[string]string{"../fora.txt": "x"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(archive, filepath.Join(base, "destino")); err == nil {
		t.Error("deveria recusar a entrada")
	}
	if _, err := os.Stat(filepath.Join(base, "fora.txt")); err == nil {
		t.Error("um arquivo foi gravado fora da pasta de destino")
	}
}

func TestParseSizes(t *testing.T) {
	t.Parallel()
	celular, tablet, desktop := presets["celular"], presets["tablet"], presets["desktop"]
	tests := []struct {
		name    string
		in      toolInput
		want    []Size
		wantErr string
	}{
		{name: "sem nada é desktop", want: []Size{desktop}},
		{name: "um tamanho", in: toolInput{Tamanho: "celular"}, want: []Size{celular}},
		{name: "lista na ordem pedida", in: toolInput{Tamanhos: []string{"tablet", "desktop", "celular"}}, want: []Size{tablet, desktop, celular}},
		{name: "lista com repetido", in: toolInput{Tamanhos: []string{"celular", "celular"}}, want: []Size{celular}},
		{name: "largura e altura", in: toolInput{Largura: 1280, Altura: 720}, want: []Size{{Width: 1280, Height: 720}}},
		{name: "tamanho que não existe", in: toolInput{Tamanho: "tv"}, wantErr: `tamanho "tv" não existe`},
		{name: "tamanho e lista", in: toolInput{Tamanho: "celular", Tamanhos: []string{"tablet"}}, wantErr: "não os dois"},
		{name: "só a largura", in: toolInput{Largura: 800}, wantErr: "informe as duas"},
		{name: "medida com tamanho", in: toolInput{Tamanho: "celular", Largura: 800, Altura: 600}, wantErr: "não combine"},
		{name: "largura pequena demais", in: toolInput{Largura: 10, Altura: 600}, wantErr: "'largura' precisa ficar entre"},
		{name: "altura negativa", in: toolInput{Largura: 800, Altura: -1}, wantErr: "'altura' precisa ficar entre"},
		{name: "altura grande demais", in: toolInput{Largura: 800, Altura: 9000}, wantErr: "'altura' precisa ficar entre"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseSizes(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("erro = %v, quero um com %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("tamanhos = %v, quero %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tamanho %d = %v, quero %v", i, got[i], tt.want[i])
				}
			}
		})
	}
	if celular.Width != 390 || celular.Height != 844 || tablet.Width != 768 || tablet.Height != 1024 || desktop.Width != 1440 || desktop.Height != 900 {
		t.Errorf("os três tamanhos mudaram: %v, %v, %v", celular, tablet, desktop)
	}
}

func TestCheckURL(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"http://localhost:8000/painel?a=1": "http://localhost:8000/painel?a=1",
		" https://exemplo.com ":            "https://exemplo.com/",
		"http://127.0.0.1:5173":            "http://127.0.0.1:5173/",
	} {
		if got, err := checkURL(raw); err != nil || got != want {
			t.Errorf("checkURL(%q) = %q, %v; quero %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "   ", "localhost:8000", "/painel", "file:///etc/passwd", "chrome://version", "http://"} {
		if got, err := checkURL(raw); err == nil {
			t.Errorf("checkURL(%q) = %q, deveria recusar", raw, got)
		}
	}
}

// A call with a wrong parameter must come back as an error the agent can fix,
// without downloading or opening the browser.
func TestTool_RejectsBadInputBeforeTheBrowser(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Source{Version: "1", URL: "http://127.0.0.1:1/nao-deveria-baixar", Binary: "x"})
	run := m.Tool().Run
	for _, input := range []string{
		`{}`,
		`{"url":"localhost:3000"}`,
		`{"url":"http://localhost:3000","tamanho":"gigante"}`,
		`{"url":"http://localhost:3000","largura":800}`,
		`{"url":"http://localhost:3000","inventado":true}`,
	} {
		res := run(context.Background(), nil, json.RawMessage(input))
		if !res.IsError || len(res.Images) != 0 {
			t.Errorf("%s deveria voltar como erro: %+v", input, res)
		}
	}
	if _, err := os.Stat(m.Dir); err == nil {
		t.Error("um pedido inválido não pode chegar a baixar o navegador")
	}
}

func TestReport(t *testing.T) {
	t.Parallel()
	img := func(label string) provider.Image {
		return provider.Image{Label: label, MediaType: "image/png", Data: []byte("x")}
	}
	address := "http://localhost:8000/painel"
	res := report(address, []Shot{
		{Size: presets["desktop"], Status: 200, Title: "Painel", FinalURL: address + "#topo", Images: []provider.Image{img("a")}},
		{Size: presets["celular"], Status: 500, FinalURL: "http://localhost:8000/login", Overflow: 520, PageHeight: 15000, Cut: true,
			Images: []provider.Image{img("b"), img("c")}},
	}, 4*time.Second)

	if res.IsError || len(res.Images) != 3 || res.Images[0].Label != "a" || res.Images[2].Label != "c" {
		t.Fatalf("as imagens deveriam vir todas, na ordem dos tamanhos: %+v", res)
	}
	lines := strings.Split(res.Content, "\n")
	if len(lines) != 4 {
		t.Fatalf("quero fila, cabeçalho e uma linha por tamanho, veio:\n%s", res.Content)
	}
	if !strings.Contains(lines[0], "esperou 4s na fila") {
		t.Errorf("a espera na fila não foi avisada: %q", lines[0])
	}
	if want := `- desktop 1440x900, HTTP 200, título "Painel"`; lines[2] != want {
		t.Errorf("linha do desktop = %q, quero %q", lines[2], want)
	}
	for _, want := range []string{"celular 390x844", "HTTP 500", "respondeu com erro", "foi parar em http://localhost:8000/login", "520px de largura", "15000px de altura", "só até 12000px"} {
		if !strings.Contains(lines[3], want) {
			t.Errorf("a linha do celular não diz %q: %q", want, lines[3])
		}
	}

	quick := report(address, []Shot{{Size: Size{Width: 800, Height: 600}}}, 200*time.Millisecond)
	if quick.Content != "Print de "+address+":\n- 800x600" {
		t.Errorf("sem espera, status e título, o texto deveria ser só o tamanho: %q", quick.Content)
	}
}
