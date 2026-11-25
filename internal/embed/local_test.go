package embed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/MatheusGustav/mister/internal/memory"
)

// TestHelperServer is not a test: it is the fake llama-server the other tests
// start as a child process.
func TestHelperServer(t *testing.T) {
	if os.Getenv("MISTER_FAKE_LLAMA") != "1" {
		t.Skip("só roda como processo filho dos outros testes")
	}
	port := ""
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if os.Getenv("MISTER_FAKE_LLAMA_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "error: modelo corrompido")
		os.Exit(3)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		items := make([]item, len(body.Input))
		for i, text := range body.Input {
			items[i] = item{Index: i, Embedding: []float32{float32(len(text)), float32(os.Getpid())}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	})
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_ = http.Serve(listener, mux)
}

// installFake puts a script named llama-server where Local expects the real
// one; the script runs this test binary as the fake server.
func installFake(t *testing.T, fail bool) *Local {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	l := NewLocal(t.TempDir())
	l.client.model = model{name: "m", queryPrefix: "q: ", docPrefix: "d {titulo}: "}
	l.StartTimeout = 20 * time.Second
	if err := os.MkdirAll(filepath.Dir(l.serverPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	env := "MISTER_FAKE_LLAMA=1"
	if fail {
		env += " MISTER_FAKE_LLAMA_FAIL=1"
	}
	script := fmt.Sprintf("#!/bin/sh\n%s exec %q -test.run=TestHelperServer -- \"$@\"\n", env, self)
	if err := os.WriteFile(l.serverPath(), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.modelPath(), []byte("pesos de mentira"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

func pidOf(t *testing.T, l *Local) int {
	t.Helper()
	v, err := l.EmbedQuery(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	return int(v[1])
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("o processo %d continua de pé", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLocal_StartsOnDemandAndReusesTheServer(t *testing.T) {
	t.Parallel()
	l := installFake(t, false)
	if l.proc != nil {
		t.Fatal("o servidor não deveria subir antes da primeira busca")
	}
	vectors, err := l.EmbedDocuments(context.Background(), []memory.Document{{Title: "nota", Text: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	if int(vectors[0][0]) != len("d nota: abc") {
		t.Errorf("o prefixo da nota não chegou ao servidor: %v", vectors[0])
	}
	first := int(vectors[0][1])
	if second := pidOf(t, l); second != first {
		t.Errorf("a segunda busca deveria usar o mesmo servidor (%d), usou %d", first, second)
	}
	l.Close()
	waitGone(t, first)
	if third := pidOf(t, l); third == first {
		t.Error("depois de fechado, a busca seguinte deveria subir um servidor novo")
	}
}

func TestLocal_StopsWhenIdle(t *testing.T) {
	t.Parallel()
	l := installFake(t, false)
	l.IdleTimeout = 150 * time.Millisecond
	pid := pidOf(t, l)
	waitGone(t, pid)
	if again := pidOf(t, l); again == pid {
		t.Error("depois de parar por falta de uso, deveria subir de novo na busca seguinte")
	}
}

func TestLocal_RestartsAServerThatDied(t *testing.T) {
	t.Parallel()
	l := installFake(t, false)
	pid := pidOf(t, l)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid)
	if again := pidOf(t, l); again == pid {
		t.Error("deveria subir um servidor novo no lugar do que morreu")
	}
}

// fakeDownloads serves the fake llama-server and a model, and counts the downloads.
func fakeDownloads(t *testing.T) (Sources, *atomic.Int32) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nMISTER_FAKE_LLAMA=1 exec %q -test.run=TestHelperServer -- \"$@\"\n", self)
	archive := tarGz(t, map[string]string{"llama-x/llama-server": script}, nil)
	model := []byte("pesos de mentira")
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		// Long enough for every search to arrive during the download.
		time.Sleep(100 * time.Millisecond)
		switch r.URL.Path {
		case "/servidor.tar.gz":
			_, _ = w.Write(archive)
		case "/modelo.gguf":
			_, _ = w.Write(model)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return Sources{
		ServerURL: srv.URL + "/servidor.tar.gz", ServerSHA256: sum(archive), ServerDir: "llama-x",
		ModelURL: srv.URL + "/modelo.gguf", ModelSHA256: sum(model), ModelFile: "modelo.gguf",
	}, &downloads
}

func TestLocal_DownloadsOnTheFirstSearch(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("o servidor gerenciado só existe para linux x86_64")
	}
	t.Parallel()
	sources, downloads := fakeDownloads(t)
	l := NewLocal(filepath.Join(t.TempDir(), "embedding"))
	l.Sources = sources
	l.client.model = model{name: "m", queryPrefix: "q: ", docPrefix: "d {titulo}: "}
	l.StartTimeout = 20 * time.Second
	var logMu sync.Mutex
	var log []string
	l.Logf = func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		log = append(log, fmt.Sprintf(format, args...))
	}
	t.Cleanup(l.Close)
	if l.Installed() {
		t.Fatal("pasta vazia não é instalação")
	}

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Go(func() { _, errs[i] = l.EmbedQuery(context.Background(), "x") })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("busca %d: %v", i, err)
		}
	}
	if got := downloads.Load(); got != 2 {
		t.Errorf("houve %d downloads para 6 buscas ao mesmo tempo, quero 2 (servidor e modelo)", got)
	}
	if !l.Installed() {
		t.Error("depois da primeira busca o servidor deveria estar instalado")
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(log) == 0 || !strings.Contains(log[0], "baixando agora, só desta vez") || !strings.Contains(log[len(log)-1], "instalado em") {
		t.Errorf("o download deveria ser avisado ao usuário, do começo ao fim: %q", log)
	}
}

func TestLocal_DownloadThatFailsIsNotRetriedAtEverySearch(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("o servidor gerenciado só existe para linux x86_64")
	}
	t.Parallel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	l := NewLocal(filepath.Join(t.TempDir(), "embedding"))
	l.Sources = Sources{ServerURL: srv.URL + "/s.tar.gz", ServerDir: "llama-x", ModelURL: srv.URL + "/m.gguf", ModelFile: "m.gguf"}
	var log []string
	l.Logf = func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }
	t.Cleanup(l.Close)

	if _, err := l.EmbedQuery(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("erro = %v", err)
	}
	if _, err := l.EmbedQuery(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "falhou há pouco") {
		t.Errorf("a segunda busca deveria receber o mesmo erro sem novo download: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("houve %d pedidos de download, quero 1", got)
	}
	failures := 0
	for _, line := range log {
		if strings.Contains(line, "não consegui baixar o servidor de embedding") {
			failures++
		}
	}
	if failures != 1 {
		t.Errorf("a falha deveria ser avisada uma vez, foi %d: %q", failures, log)
	}
	if left, _ := filepath.Glob(filepath.Join(l.Dir, ".download-*")); len(left) > 0 || l.Installed() {
		t.Errorf("o download que falhou não pode deixar restos: %v", left)
	}
}

func TestLocal_ServerThatFailsToStart(t *testing.T) {
	t.Parallel()
	l := installFake(t, true)
	_, err := l.EmbedQuery(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "fechou ao subir") || !strings.Contains(err.Error(), "modelo corrompido") {
		t.Errorf("o erro deveria trazer o que o servidor escreveu: %v", err)
	}
}

func tarGz(t *testing.T, entries map[string]string, links map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		if err := tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink}); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestLocal_Install(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("o servidor gerenciado só existe para linux x86_64")
	}
	t.Parallel()
	archive := tarGz(t, map[string]string{"llama-x/llama-server": "#!/bin/sh\n", "llama-x/libllama.so.1.0.0": "lib"}, map[string]string{"llama-x/libllama.so": "libllama.so.1.0.0"})
	model := []byte("pesos do modelo")
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads++
		switch r.URL.Path {
		case "/servidor.tar.gz":
			_, _ = w.Write(archive)
		case "/modelo.gguf":
			_, _ = w.Write(model)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	sources := Sources{
		ServerURL: srv.URL + "/servidor.tar.gz", ServerSHA256: sum(archive), ServerDir: "llama-x",
		ModelURL: srv.URL + "/modelo.gguf", ModelSHA256: sum(model), ModelFile: "modelo.gguf",
	}
	var log []string
	logf := func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }

	l := NewLocal(filepath.Join(t.TempDir(), "embedding"))
	l.Sources = sources
	if err := l.Install(context.Background(), logf); err != nil {
		t.Fatal(err)
	}
	if !l.Installed() {
		t.Fatal("depois de instalar, Installed deveria ser verdadeiro")
	}
	if info, err := os.Stat(l.serverPath()); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("o llama-server deveria ficar executável: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(l.Dir, "llama-x", "libllama.so")); err != nil || target != "libllama.so.1.0.0" {
		t.Errorf("o link da biblioteca se perdeu: %q, %v", target, err)
	}
	if data, _ := os.ReadFile(l.modelPath()); string(data) != string(model) {
		t.Errorf("modelo gravado = %q", data)
	}
	leftovers, _ := filepath.Glob(filepath.Join(l.Dir, "*.parcial"))
	downloading, _ := filepath.Glob(filepath.Join(l.Dir, ".download-*"))
	if _, err := os.Stat(filepath.Join(l.Dir, "llama-server.tar.gz")); err == nil || len(leftovers) > 0 || len(downloading) > 0 {
		t.Error("o arquivo baixado, os parciais e a pasta do download deveriam ser apagados")
	}

	before := downloads
	if err := l.Install(context.Background(), logf); err != nil {
		t.Fatal(err)
	}
	if downloads != before {
		t.Error("instalar de novo não deveria baixar o que já está no disco")
	}

	t.Run("extração cortada no meio é refeita", func(t *testing.T) {
		half := NewLocal(filepath.Join(t.TempDir(), "embedding"))
		half.Sources = sources
		stale := filepath.Join(half.Dir, "llama-x", "resto-antigo")
		if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := half.Install(context.Background(), logf); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stale); err == nil || !half.Installed() {
			t.Error("a pasta deixada pela metade deveria ser trocada pela instalação inteira")
		}
	})

	t.Run("arquivo que não confere é recusado", func(t *testing.T) {
		bad := NewLocal(filepath.Join(t.TempDir(), "embedding"))
		bad.Sources = sources
		bad.Sources.ModelSHA256 = strings.Repeat("0", 64)
		err := bad.Install(context.Background(), logf)
		if err == nil || !strings.Contains(err.Error(), "não confere") {
			t.Fatalf("erro = %v", err)
		}
		if _, statErr := os.Stat(bad.modelPath()); statErr == nil || bad.Installed() {
			t.Error("um arquivo com checksum errado não pode ficar instalado")
		}
	})

	t.Run("download que falha", func(t *testing.T) {
		bad := NewLocal(filepath.Join(t.TempDir(), "embedding"))
		bad.Sources = sources
		bad.Sources.ServerURL = srv.URL + "/nao-existe"
		if err := bad.Install(context.Background(), logf); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("erro = %v", err)
		}
	})
}

func TestExtractTarGz_RefusesEntriesOutsideTheFolder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries map[string]string
		links   map[string]string
	}{
		{name: "arquivo que sobe", entries: map[string]string{"../fora.txt": "x"}},
		{name: "link para outro lugar", links: map[string]string{"dentro": "/etc/passwd"}},
		{name: "link que sobe", links: map[string]string{"dentro": "../fora"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			dir := filepath.Join(base, "destino")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(base, "a.tar.gz")
			if err := os.WriteFile(archive, tarGz(t, tt.entries, tt.links), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := ExtractTarGz(archive, dir); err == nil {
				t.Error("deveria recusar a entrada")
			}
			if _, err := os.Stat(filepath.Join(base, "fora.txt")); err == nil {
				t.Error("um arquivo foi gravado fora da pasta de destino")
			}
		})
	}
}
