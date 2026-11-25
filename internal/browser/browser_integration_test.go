//go:build integration

package browser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These run against the real browser. Point MISTER_BROWSER_DIR at the folder
// the Mister downloads it to (~/.mister/browser); an empty folder makes the
// first test download it:
//
//	MISTER_BROWSER_DIR=~/.mister/browser go test -tags=integration ./internal/browser/
func realManager(t *testing.T, maxTabs int) *Manager {
	t.Helper()
	dir := os.Getenv("MISTER_BROWSER_DIR")
	if dir == "" {
		t.Skip("defina MISTER_BROWSER_DIR com a pasta onde o navegador fica instalado")
	}
	m := New(dir, maxTabs, time.Minute, 15*time.Second)
	m.Logf = t.Logf
	t.Cleanup(m.Close)
	return m
}

const samplePage = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Página de teste</title>
<style>body{margin:0;font-family:sans-serif} .faixa{height:%dpx;background:linear-gradient(#0a5,#05a)} .largo{width:%dpx;height:40px;background:#c00}</style>
</head><body><h1>Olá</h1><button>Salvar</button><div class="largo"></div><div class="faixa"></div></body></html>`

func servePage(t *testing.T, height, width int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, samplePage, height, width)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dimensions(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("o print não é uma imagem válida: %v", err)
	}
	return cfg.Width, cfg.Height
}

// browserProcesses lists the running processes of the browser at exec.
func browserProcesses(exec string) []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if target, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe")); err == nil && strings.TrimSuffix(target, " (deleted)") == exec {
			pids = append(pids, pid)
		}
	}
	return pids
}

// mainProcesses counts the browsers themselves, leaving out the helper
// processes each one starts (renderers, GPU, network).
func mainProcesses(exec string) int {
	n := 0
	for _, pid := range browserProcesses(exec) {
		cmdline, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if !bytes.Contains(cmdline, []byte("--type=")) {
			n++
		}
	}
	return n
}

// memoryKB adds up the memory of the browser processes, counting the pages
// they share once (PSS).
func memoryKB(exec string) int {
	total := 0
	for _, pid := range browserProcesses(exec) {
		data, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "smaps_rollup"))
		for _, line := range strings.Split(string(data), "\n") {
			if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "Pss:" {
				kb, _ := strconv.Atoi(fields[1])
				total += kb
			}
		}
	}
	return total
}

func waitNoProcess(t *testing.T, exec string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		left := browserProcesses(exec)
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sobraram %d processos do navegador depois de %s: %v", len(left), limit, left)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestCapture_OnePicturePerSize(t *testing.T) {
	m := realManager(t, 2)
	srv := servePage(t, 300, 100)
	sizes := []Size{presets["desktop"], presets["tablet"], presets["celular"], {Width: 1024, Height: 600}}

	shots, _, err := m.Capture(context.Background(), Request{URL: srv.URL + "/", Sizes: sizes})
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != len(sizes) {
		t.Fatalf("vieram %d prints para %d tamanhos", len(shots), len(sizes))
	}
	for i, shot := range shots {
		if len(shot.Images) != 1 {
			t.Fatalf("%s: quero 1 imagem, vieram %d", sizes[i], len(shot.Images))
		}
		w, h := dimensions(t, shot.Images[0].Data)
		if w != sizes[i].Width || h != sizes[i].Height {
			t.Errorf("%s: a imagem tem %dx%d", sizes[i], w, h)
		}
		if shot.Status != 200 || shot.Title != "Página de teste" || shot.FinalURL != srv.URL+"/" || shot.Overflow != 0 {
			t.Errorf("%s: dados da página inesperados: %+v", sizes[i], shot)
		}
		if shot.Images[0].Label != sizes[i].String() || shot.Images[0].MediaType != "image/png" {
			t.Errorf("%s: rótulo %q, tipo %q", sizes[i], shot.Images[0].Label, shot.Images[0].MediaType)
		}
	}
	if n := mainProcesses(m.execPath()); n != 1 {
		t.Errorf("há %d navegadores abertos, quero 1", n)
	}
}

// The tool as an agent calls it: the text first, then one labelled picture per size.
func TestTool_RealPage(t *testing.T) {
	m := realManager(t, 2)
	srv := servePage(t, 300, 900)
	input := fmt.Sprintf(`{"url": %q, "tamanhos": ["desktop", "celular"]}`, srv.URL)
	res := m.Tool().Run(context.Background(), nil, json.RawMessage(input))
	if res.IsError {
		t.Fatalf("a ferramenta falhou: %s", res.Content)
	}
	want := "Print de " + srv.URL + "/:\n" +
		"- desktop 1440x900, HTTP 200, título \"Página de teste\"\n" +
		"- celular 390x844, HTTP 200, título \"Página de teste\". Atenção: o conteúdo tem 900px de largura e vaza da tela (rolagem para o lado)"
	if res.Content != want {
		t.Errorf("texto da ferramenta:\n%s\nquero:\n%s", res.Content, want)
	}
	if len(res.Images) != 2 || res.Images[0].Label != "desktop 1440x900" || res.Images[1].Label != "celular 390x844" {
		t.Fatalf("quero uma imagem por tamanho, com o rótulo: %d imagens", len(res.Images))
	}
	if w, h := dimensions(t, res.Images[1].Data); w != 390 || h != 844 {
		t.Errorf("o print do celular tem %dx%d", w, h)
	}

	// Port 6000 is one of those Chrome refuses to open.
	blocked := m.Tool().Run(context.Background(), nil, json.RawMessage(`{"url": "http://127.0.0.1:6000/"}`))
	if !blocked.IsError || !strings.Contains(blocked.Content, "bloqueia essa porta") || len(blocked.Images) != 0 {
		t.Errorf("porta bloqueada pelo navegador deveria voltar como erro claro: %+v", blocked)
	}
}

func TestCapture_FullPageComesInParts(t *testing.T) {
	m := realManager(t, 2)
	short, tall, endless := servePage(t, 100, 100), servePage(t, 4500, 100), servePage(t, 20000, 100)
	celular := presets["celular"]

	shots, _, err := m.Capture(context.Background(), Request{URL: tall.URL + "/", Sizes: []Size{celular}, FullPage: true})
	if err != nil {
		t.Fatal(err)
	}
	shot := shots[0]
	if len(shot.Images) != 3 || shot.Cut {
		t.Fatalf("uma página de %dpx deveria vir em 3 partes, sem corte: %d partes, corte %v", shot.PageHeight, len(shot.Images), shot.Cut)
	}
	total := 0
	for i, img := range shot.Images {
		w, h := dimensions(t, img.Data)
		if w != celular.Width || h > sliceHeight {
			t.Errorf("parte %d tem %dx%d", i+1, w, h)
		}
		total += h
	}
	if total != shot.PageHeight {
		t.Errorf("as partes somam %dpx, a página tem %dpx", total, shot.PageHeight)
	}
	if want := "celular 390x844, página inteira, parte 2 de 3"; shot.Images[1].Label != want {
		t.Errorf("rótulo = %q, quero %q", shot.Images[1].Label, want)
	}

	shots, _, err = m.Capture(context.Background(), Request{URL: short.URL + "/", Sizes: []Size{celular}, FullPage: true})
	if err != nil {
		t.Fatal(err)
	}
	if w, h := dimensions(t, shots[0].Images[0].Data); len(shots[0].Images) != 1 || w != celular.Width || h != celular.Height {
		t.Errorf("página menor que a tela deveria vir em 1 parte do tamanho da tela: %d partes, %dx%d", len(shots[0].Images), w, h)
	}

	shots, _, err = m.Capture(context.Background(), Request{URL: endless.URL + "/", Sizes: []Size{celular}, FullPage: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shots[0].Images) != maxSlices || !shots[0].Cut {
		t.Errorf("página de %dpx deveria parar em %d partes e avisar o corte: %d partes, corte %v", shots[0].PageHeight, maxSlices, len(shots[0].Images), shots[0].Cut)
	}
}

func TestCapture_ReportsContentWiderThanTheScreen(t *testing.T) {
	m := realManager(t, 2)
	srv := servePage(t, 300, 900)
	shots, _, err := m.Capture(context.Background(), Request{URL: srv.URL + "/", Sizes: []Size{presets["celular"], presets["desktop"]}})
	if err != nil {
		t.Fatal(err)
	}
	if shots[0].Overflow != 900 {
		t.Errorf("no celular o conteúdo de 900px vaza da tela: Overflow = %d", shots[0].Overflow)
	}
	if shots[1].Overflow != 0 {
		t.Errorf("no desktop 900px cabem na tela: Overflow = %d", shots[1].Overflow)
	}
}

// Each call starts clean: a cookie the page set in one call is not sent in the next.
func TestCapture_EachCallIsIsolated(t *testing.T) {
	m := realManager(t, 2)
	var mu sync.Mutex
	var cookies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		cookies = append(cookies, r.Header.Get("Cookie"))
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "sessao", Value: "abc", Path: "/"})
		fmt.Fprint(w, "<!doctype html><title>cookie</title><script>localStorage.setItem('x','1')</script>ok")
	}))
	defer srv.Close()
	for range 2 {
		// Two sizes in one call share the tab, so the second load does carry the cookie.
		if _, _, err := m.Capture(context.Background(), Request{URL: srv.URL + "/", Sizes: []Size{presets["desktop"], presets["celular"]}}); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cookies) != 4 || cookies[0] != "" || cookies[2] != "" {
		t.Errorf("a primeira carga de cada chamada deveria chegar sem cookie: %q", cookies)
	}
}

func TestCapture_Failures(t *testing.T) {
	m := realManager(t, 2)
	m.LoadTimeout = 2 * time.Second

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + listener.Addr().String() + "/"
	listener.Close()
	if _, _, err := m.Capture(context.Background(), Request{URL: closed, Sizes: []Size{presets["desktop"]}}); err == nil ||
		!strings.Contains(err.Error(), "nada responde nesse endereço") || !strings.Contains(err.Error(), closed) {
		t.Errorf("porta fechada: erro = %v", err)
	}

	release := make(chan struct{})
	stuck := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer stuck.Close()
	defer close(release)
	start := time.Now()
	_, _, err = m.Capture(context.Background(), Request{URL: stuck.URL + "/", Sizes: []Size{presets["desktop"]}})
	if err == nil || !strings.Contains(err.Error(), "não terminou de carregar em 2s") {
		t.Errorf("página que não responde: erro = %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("o tempo limite de 2s levou %s para valer", took)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	if _, _, err := m.Capture(ctx, Request{URL: stuck.URL + "/", Sizes: []Size{presets["desktop"]}}); err == nil || !strings.Contains(err.Error(), "cancelado") {
		t.Errorf("pedido cancelado: erro = %v", err)
	}

	errorPage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "quebrou", http.StatusInternalServerError)
	}))
	defer errorPage.Close()
	shots, _, err := m.Capture(context.Background(), Request{URL: errorPage.URL + "/", Sizes: []Size{presets["desktop"]}})
	if err != nil || shots[0].Status != 500 || len(shots[0].Images) != 1 {
		t.Errorf("página de erro deveria vir com print e o status 500: %+v, %v", shots, err)
	}

	// The failures above must leave the browser usable and no tab behind.
	ok := servePage(t, 300, 100)
	if _, _, err := m.Capture(context.Background(), Request{URL: ok.URL + "/", Sizes: []Size{presets["desktop"]}}); err != nil {
		t.Errorf("depois das falhas o navegador deveria seguir funcionando: %v", err)
	}
}

// Twenty requests at once: one browser, never more tabs than the limit, the
// rest waits in line and every request gets its picture.
func TestCapture_TwentyAtOnceRespectTheTabLimit(t *testing.T) {
	const limit, requests = 3, 20
	m := realManager(t, limit)
	var loading, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := loading.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
		loading.Add(-1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, samplePage, 1500, 100)
	}))
	defer srv.Close()

	var peakKB atomic.Int64
	sampling, sampled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			if kb := int64(memoryKB(m.execPath())); kb > peakKB.Load() {
				peakKB.Store(kb)
			}
			select {
			case <-sampling:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()

	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, requests)
	waits := make([]time.Duration, requests)
	for i := range requests {
		wg.Go(func() {
			var shots []Shot
			shots, waits[i], errs[i] = m.Capture(context.Background(), Request{URL: srv.URL + "/", Sizes: []Size{presets["desktop"]}})
			if errs[i] == nil && len(shots[0].Images) != 1 {
				errs[i] = fmt.Errorf("veio sem imagem")
			}
		})
	}
	wg.Wait()
	took := time.Since(start)
	browsers := mainProcesses(m.execPath())
	idleKB := memoryKB(m.execPath())
	close(sampling)
	<-sampled

	waited := 0
	var longest time.Duration
	for i, err := range errs {
		if err != nil {
			t.Errorf("pedido %d: %v", i, err)
		}
		if waits[i] > 100*time.Millisecond {
			waited++
		}
		longest = max(longest, waits[i])
	}
	if got := peak.Load(); got > limit || got < 2 {
		t.Errorf("no pico havia %d páginas carregando ao mesmo tempo; o limite é %d", got, limit)
	}
	if waited < requests-limit {
		t.Errorf("só %d pedidos esperaram na fila; com limite %d, pelo menos %d deveriam", waited, limit, requests-limit)
	}
	if browsers != 1 {
		t.Errorf("há %d navegadores abertos, quero 1", browsers)
	}
	t.Logf("%d pedidos em %s; pico de %d abas carregando (limite %d); %d esperaram na fila, o que mais esperou ficou %s",
		requests, took.Round(100*time.Millisecond), peak.Load(), limit, waited, longest.Round(100*time.Millisecond))
	t.Logf("memória do navegador (todos os processos): pico de %d MB durante os pedidos, %d MB parado depois", peakKB.Load()>>10, idleKB>>10)
}

func TestManager_StopsWhenIdleAndReopens(t *testing.T) {
	m := realManager(t, 2)
	m.IdleTimeout = 300 * time.Millisecond
	srv := servePage(t, 300, 100)
	req := Request{URL: srv.URL + "/", Sizes: []Size{presets["desktop"]}}

	if _, _, err := m.Capture(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(browserProcesses(m.execPath())) == 0 {
		t.Fatal("logo depois do print o navegador deveria estar aberto")
	}
	waitNoProcess(t, m.execPath(), 10*time.Second)
	if _, _, err := m.Capture(context.Background(), req); err != nil {
		t.Fatalf("depois de fechar por falta de uso, o navegador deveria reabrir: %v", err)
	}
}

func TestManager_ReopensABrowserThatDied(t *testing.T) {
	m := realManager(t, 2)
	srv := servePage(t, 300, 100)
	req := Request{URL: srv.URL + "/", Sizes: []Size{presets["desktop"]}}
	if _, _, err := m.Capture(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, pid := range browserProcesses(m.execPath()) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	waitNoProcess(t, m.execPath(), 10*time.Second)
	if _, _, err := m.Capture(context.Background(), req); err != nil {
		t.Fatalf("depois de o navegador morrer, o próximo print deveria reabrir: %v", err)
	}
}

func TestManager_Close_LeavesNoProcess(t *testing.T) {
	m := realManager(t, 2)
	srv := servePage(t, 300, 100)
	req := Request{URL: srv.URL + "/", Sizes: []Size{presets["desktop"]}}
	// A Mister that was killed leaves its profile behind, so compare with what
	// was already there.
	profiles := func() int {
		found, _ := filepath.Glob(filepath.Join(os.TempDir(), "chromedp-runner*"))
		return len(found)
	}
	before := profiles()
	if _, _, err := m.Capture(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if profiles() != before+1 {
		t.Fatalf("o navegador aberto deveria ter um perfil temporário: havia %d, há %d", before, profiles())
	}
	m.Close()
	waitNoProcess(t, m.execPath(), 2*time.Second)
	if profiles() != before {
		t.Errorf("o perfil temporário do navegador deveria ser apagado: havia %d, há %d", before, profiles())
	}
	if _, _, err := m.Capture(context.Background(), req); err == nil || !strings.Contains(err.Error(), "já foi encerrado") {
		t.Errorf("depois de Close não se tira print: erro = %v", err)
	}
}

// TestHelperHolder is not a test: it is the Mister the next test kills. It
// opens the browser and holds it until it is killed.
func TestHelperHolder(t *testing.T) {
	if os.Getenv("MISTER_BROWSER_HOLDER") != "1" {
		t.Skip("só roda como processo filho do teste seguinte")
	}
	m := New(os.Getenv("MISTER_BROWSER_DIR"), 2, time.Hour, 15*time.Second)
	srv := servePage(t, 300, 100)
	if _, _, err := m.Capture(context.Background(), Request{URL: srv.URL + "/", Sizes: []Size{presets["desktop"]}}); err != nil {
		t.Fatal(err)
	}
	fmt.Println("navegador aberto")
	select {}
}

// The Mister killed without a chance to clean up (kill -9) takes the browser
// with it.
func TestManager_KilledMisterLeavesNoProcess(t *testing.T) {
	m := realManager(t, 2)
	if !m.Installed() {
		t.Skip("o navegador ainda não foi baixado; rode os outros testes antes")
	}
	holder := exec.Command(os.Args[0], "-test.run=^TestHelperHolder$", "-test.v")
	holder.Env = append(os.Environ(), "MISTER_BROWSER_HOLDER=1")
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer holder.Wait()
	defer holder.Process.Kill()

	opened := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "navegador aberto") {
				opened <- true
				return
			}
		}
		opened <- false
	}()
	select {
	case ok := <-opened:
		if !ok {
			t.Fatal("o processo filho terminou sem abrir o navegador")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("o processo filho não abriu o navegador em 60s")
	}
	before := browserProcesses(m.execPath())
	if len(before) == 0 {
		t.Fatal("o processo filho deveria ter um navegador aberto")
	}

	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitNoProcess(t, m.execPath(), 5*time.Second)
	t.Logf("o Mister foi morto com %d processos do navegador abertos; nenhum sobrou", len(before))
}
