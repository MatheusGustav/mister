package embed

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MatheusGustav/mister/internal/memory"
)

// Sources pins what the managed server downloads: exact files, by checksum.
type Sources struct {
	ServerURL    string
	ServerSHA256 string
	// ServerDir is the folder the server archive unpacks to.
	ServerDir   string
	ModelURL    string
	ModelSHA256 string
	ModelFile   string
}

// DefaultSources is llama.cpp build b12686 for Linux x86_64 and the
// EmbeddingGemma 300M weights (Q4_0, under the Gemma terms of use).
func DefaultSources() Sources {
	return Sources{
		ServerURL:    "https://github.com/ggml-org/llama.cpp/releases/download/b12686/llama-b12686-bin-ubuntu-x64.tar.gz",
		ServerSHA256: "2325b7c1b54180a91136abaf9ca1d009ac4b2c8a68eb9b4709d45ed4d47ff90d",
		ServerDir:    "llama-b12686",
		ModelURL:     "https://huggingface.co/ggml-org/embeddinggemma-300M-qat-q4_0-GGUF/resolve/8dd0ca2a66a8f14470acb0e2a71f801afbc5fb73/embeddinggemma-300M-qat-Q4_0.gguf",
		ModelSHA256:  "50d28e22432a148f6f8a86eab3700f92add5d1f54baf7790675a2a4dadbccf26",
		ModelFile:    "embeddinggemma-300M-qat-Q4_0.gguf",
	}
}

// gemma is how EmbeddingGemma expects queries and documents.
var gemma = model{
	name:        "embeddinggemma-300m",
	queryPrefix: "task: search result | query: ",
	docPrefix:   "title: {titulo} | text: ",
}

// Thresholds are the similarity cuts that go with the model, measured in
// Portuguese on 24/11/2026: the right note scores 0.52 or more, an unrelated
// one 0.37 or less.
//
// Between two notes, the same subject written again scores 0.73 or more and
// another subject 0.61 or less.
func Thresholds() memory.Thresholds {
	return memory.Thresholds{Entry: 0.45, Walk: 0.40, Duplicate: 0.68}
}

// SessionThreshold is the similarity cut between a question and an excerpt of
// a past session (request and answer together), measured the same day on two
// sets: the right excerpt scores 0.59 or more, an unrelated one 0.50 or less.
//
// Comparing with the answer alone was measured too and is worse: right answers
// drop to 0.52-0.56 while unrelated ones reach 0.51, and a short answer that
// depends on its question ("3307.") is no longer found.
const SessionThreshold = 0.55

// installRetry spaces the download attempts: the memory is searched by itself
// at every message, and a machine without network must not try each time.
const installRetry = 10 * time.Minute

// Local is a llama-server kept in the Mister home. It is downloaded on the
// first search, when 'mister install' did not bring it before, starts on
// demand and stops after a while without use, so it only holds memory while
// the memory is being searched.
//
// The server is one for every Mister open on the same home: the first to
// search starts it, the others find it running, and it comes down when the
// last one stops using it. See shared.go.
type Local struct {
	Dir          string
	Sources      Sources
	IdleTimeout  time.Duration
	StartTimeout time.Duration
	// Logf receives what the user should know while waiting (the download).
	Logf      func(format string, args ...any)
	client    *client
	installer OnDemand

	mu sync.Mutex
	// use is the lock this Mister holds while it uses the server; nil when it
	// is not using it.
	use    *os.File
	server serverState
	// child is the server when this Mister started it.
	child    *process
	inflight int
	idle     *time.Timer
}

type process struct {
	cmd *exec.Cmd
	// done closes when the process exits.
	done chan struct{}
}

func NewLocal(dir string) *Local {
	return &Local{
		Dir:          dir,
		Sources:      DefaultSources(),
		IdleTimeout:  5 * time.Minute,
		StartTimeout: 90 * time.Second,
		client:       newClient(gemma),
		installer:    OnDemand{RetryAfter: installRetry},
	}
}

func (l *Local) serverPath() string {
	return filepath.Join(l.Dir, l.Sources.ServerDir, "llama-server")
}

func (l *Local) modelPath() string {
	return filepath.Join(l.Dir, l.Sources.ModelFile)
}

func (l *Local) Installed() bool {
	for _, path := range []string{l.serverPath(), l.modelPath()} {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func (l *Local) ID() string { return l.Sources.ModelFile + "|" + l.client.id() }

func (l *Local) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	url, release, err := l.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return l.client.embedQuery(ctx, url, query)
}

func (l *Local) EmbedDocuments(ctx context.Context, docs []memory.Document) ([][]float32, error) {
	url, release, err := l.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return l.client.embedDocuments(ctx, url, docs)
}

func (l *Local) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

// ensureInstalled downloads the server and the model when they are not on disk.
func (l *Local) ensureInstalled(ctx context.Context) error {
	return l.installer.Ensure(ctx, l.Installed, func(ctx context.Context) error {
		l.logf("o servidor de embedding ainda não está no disco: baixando agora, só desta vez")
		err := l.Install(ctx, l.logf)
		switch {
		case err == nil:
			l.logf("servidor de embedding instalado em %s", l.Dir)
		case ctx.Err() == nil:
			l.logf("não consegui baixar o servidor de embedding: %v. As notas seguem sendo gravadas, sem busca; tento de novo depois de %s", err, installRetry)
		}
		return err
	})
}

// acquire makes sure the server is installed and up, and returns its address.
// The caller must call release when the request ends; the idle clock starts then.
func (l *Local) acquire(ctx context.Context) (url string, release func(), err error) {
	if err := l.ensureInstalled(ctx); err != nil {
		return "", nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.use != nil && !l.running() {
		l.letGo()
	}
	if l.use == nil {
		if err := l.attach(ctx); err != nil {
			return "", nil, err
		}
	}
	if l.idle != nil {
		l.idle.Stop()
	}
	l.inflight++
	return l.server.url() + "/v1", l.release, nil
}

// running reports whether the server this Mister uses is still there.
func (l *Local) running() bool {
	if l.child != nil {
		select {
		case <-l.child.done:
			return false
		default:
		}
	}
	return alive(l.server.PID)
}

func (l *Local) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inflight--
	if l.inflight > 0 || l.use == nil {
		return
	}
	if l.idle != nil {
		l.idle.Stop()
	}
	l.idle = time.AfterFunc(l.IdleTimeout, l.stopIfIdle)
}

func (l *Local) stopIfIdle() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight == 0 {
		l.detach()
	}
}

// Close stops using the server, which comes down if no other Mister uses it.
func (l *Local) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.idle != nil {
		l.idle.Stop()
	}
	l.detach()
}

// start runs the server on a free port and waits until it answers.
func (l *Local) start(ctx context.Context) (serverState, *process, error) {
	port, err := freePort()
	if err != nil {
		return serverState{}, nil, fmt.Errorf("reservando uma porta para o servidor de embedding: %w", err)
	}
	// The server may outlive this Mister, so it writes to a file, not to a pipe.
	logPath := filepath.Join(l.Dir, logFile)
	logs, err := os.Create(logPath)
	if err != nil {
		return serverState{}, nil, fmt.Errorf("criando o registro do servidor de embedding: %w", err)
	}
	defer logs.Close()

	p := &process{done: make(chan struct{})}
	p.cmd = exec.Command(l.serverPath(),
		"--model", l.modelPath(),
		"--embedding",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--ctx-size", "2048",
		// A whole text goes in one batch: it has to fit the context.
		"--batch-size", "2048",
		"--ubatch-size", "2048",
		"--parallel", "1",
		"--threads", strconv.Itoa(max(1, runtime.NumCPU()/2)),
		"--no-webui",
	)
	p.cmd.Dir = l.Dir
	p.cmd.Stdout, p.cmd.Stderr = logs, logs
	// Own session: neither Ctrl+C in the terminal nor the end of this Mister
	// reaches it. Another Mister may be using it by then.
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := p.cmd.Start(); err != nil {
		return serverState{}, nil, fmt.Errorf("subindo o servidor de embedding: %w", err)
	}
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()

	state := serverState{PID: p.cmd.Process.Pid, Port: port}
	if err := p.waitReady(ctx, state.url(), l.StartTimeout, logPath); err != nil {
		stop(state, p.done)
		return serverState{}, nil, err
	}
	return state, p, nil
}

func (p *process) waitReady(ctx context.Context, url string, timeout time.Duration, logPath string) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if healthy(ctx, url) {
			return nil
		}
		select {
		case <-p.done:
			return fmt.Errorf("o servidor de embedding fechou ao subir: %s", tail(logPath, 4000))
		case <-deadline.C:
			return fmt.Errorf("o servidor de embedding não ficou pronto em %s: %s", timeout, tail(logPath, 4000))
		case <-ctx.Done():
			return fmt.Errorf("subida do servidor de embedding cancelada: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// tail returns the end of a file, to explain a server that failed.
func tail(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(data), ""))
}

// Install downloads the server and the model into Dir, checking each file
// against its pinned checksum. What is already there is kept.
func (l *Local) Install(ctx context.Context, logf func(format string, args ...any)) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("o servidor de embedding só existe para linux x86_64 (aqui é %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return fmt.Errorf("criando %s: %w", l.Dir, err)
	}

	if _, err := os.Stat(l.serverPath()); err != nil {
		logf("baixando o servidor de embedding (llama.cpp)")
		if err := l.installServer(ctx, logf); err != nil {
			return err
		}
	}
	if _, err := os.Stat(l.modelPath()); err != nil {
		logf("baixando o modelo de embedding (EmbeddingGemma 300M, cerca de 280 MB)")
		if err := Download(ctx, l.Sources.ModelURL, l.Sources.ModelSHA256, l.modelPath(), logf); err != nil {
			return err
		}
	}
	return nil
}

// installServer downloads and unpacks the server beside its final folder and
// only then moves it there, so a download cut in the middle leaves nothing
// that looks installed.
func (l *Local) installServer(ctx context.Context, logf func(format string, args ...any)) error {
	tmp, err := os.MkdirTemp(l.Dir, ".download-*")
	if err != nil {
		return fmt.Errorf("criando a pasta do download em %s: %w", l.Dir, err)
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "llama-server.tar.gz")
	if err := Download(ctx, l.Sources.ServerURL, l.Sources.ServerSHA256, archive, logf); err != nil {
		return err
	}
	unpacked := filepath.Join(tmp, "conteudo")
	if err := ExtractTarGz(archive, unpacked); err != nil {
		return fmt.Errorf("extraindo o servidor de embedding: %w", err)
	}
	if _, err := os.Stat(filepath.Join(unpacked, l.Sources.ServerDir, "llama-server")); err != nil {
		return fmt.Errorf("o arquivo baixado não trouxe o llama-server em %s", l.serverPath())
	}
	target := filepath.Join(l.Dir, l.Sources.ServerDir)
	// What an extraction cut in the middle by an older version left there.
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("limpando %s: %w", target, err)
	}
	if err := os.Rename(filepath.Join(unpacked, l.Sources.ServerDir), target); err != nil {
		return fmt.Errorf("instalando o servidor de embedding em %s: %w", target, err)
	}
	return nil
}

// Download saves a URL to dest through a partial file, and only keeps it when
// the checksum matches.
func Download(ctx context.Context, url, wantSHA256, dest string, logf func(format string, args ...any)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("montando o download de %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "Mister")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("baixando %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("baixando %s: HTTP %d", url, resp.StatusCode)
	}

	partial := dest + ".parcial"
	f, err := os.Create(partial)
	if err != nil {
		return fmt.Errorf("criando %s: %w", partial, err)
	}
	defer os.Remove(partial)
	hash := sha256.New()
	progress := &progressWriter{total: resp.ContentLength, logf: logf}
	if _, err := io.Copy(io.MultiWriter(f, hash, progress), resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("baixando %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("gravando %s: %w", partial, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != wantSHA256 {
		return fmt.Errorf("o arquivo baixado de %s não confere: sha256 %s, esperado %s", url, got, wantSHA256)
	}
	if err := os.Rename(partial, dest); err != nil {
		return fmt.Errorf("gravando %s: %w", dest, err)
	}
	return nil
}

type progressWriter struct {
	total   int64
	written int64
	lastPct int64
	logf    func(format string, args ...any)
}

func (p *progressWriter) Write(data []byte) (int, error) {
	p.written += int64(len(data))
	if p.total > 0 {
		if pct := p.written * 100 / p.total; pct >= p.lastPct+25 && pct < 100 {
			p.lastPct = pct - pct%25
			p.logf("  %d%% de %d MB", p.lastPct, p.total>>20)
		}
	}
	return len(data), nil
}

// ExtractTarGz unpacks an archive into dir, refusing entries that would land
// outside it.
func ExtractTarGz(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, header.Name)
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("entrada %q sai da pasta de destino", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, reader); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Only links to a sibling file: the libraries point at their versioned names.
			if filepath.IsAbs(header.Linkname) || strings.Contains(header.Linkname, "/") {
				return fmt.Errorf("link %q aponta para fora: %q", header.Name, header.Linkname)
			}
			os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
}
