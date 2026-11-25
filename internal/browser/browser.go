// Package browser keeps one headless browser for all the agents of a session
// and takes screenshots with it.
package browser

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/MatheusGustav/mister/internal/provider"
	"github.com/MatheusGustav/mister/internal/tool"
)

const (
	// sliceHeight and maxSlices cut a whole page into pictures the model can
	// still read: a single tall picture would be shrunk until nothing is legible.
	sliceHeight = 2000
	maxSlices   = 6
	// settleTimeout is how long a loaded page may keep fetching before the
	// screenshot is taken anyway.
	settleTimeout = 5 * time.Second
	// A PNG over maxPNGBytes is taken again as JPEG: every picture goes back
	// to the model on each request of the conversation.
	maxPNGBytes   = 600 << 10
	maxImageBytes = 5 << 20
	jpegQuality   = 85
	stopTimeout   = 5 * time.Second
	lostGrace     = time.Second
)

// ErrClosed is returned for a screenshot asked after Close.
var ErrClosed = errors.New("o navegador já foi encerrado junto com o Mister")

// Manager owns the browser process. It starts on the first screenshot, serves
// every agent, and stops after a while without use.
type Manager struct {
	// Dir is where the browser is installed.
	Dir    string
	Source Source
	// Exec, when set, is the browser to run instead of the installed one.
	Exec         string
	IdleTimeout  time.Duration
	LoadTimeout  time.Duration
	StartTimeout time.Duration
	// Logf receives what the user should know while waiting (the download).
	Logf func(format string, args ...any)

	tabs       *tool.HeavyQueue
	installing chan struct{}

	mu       sync.Mutex
	proc     *process
	inflight int
	idle     *time.Timer
	closed   bool
}

type process struct {
	// ctx is the chromedp context of the browser: it ends when the browser dies.
	ctx    context.Context
	cancel context.CancelFunc
}

// New builds a manager that allows maxTabs tabs open at the same time.
func New(dir string, maxTabs int, idleTimeout, loadTimeout time.Duration) *Manager {
	return &Manager{
		Dir:          dir,
		Source:       DefaultSource(),
		IdleTimeout:  idleTimeout,
		LoadTimeout:  loadTimeout,
		StartTimeout: 30 * time.Second,
		tabs:         tool.NewHeavyQueue(maxTabs),
		installing:   make(chan struct{}, 1),
	}
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// Size is the screen a page is drawn on.
type Size struct {
	Name   string
	Width  int
	Height int
	// Mobile draws the page as a phone does: the viewport tag is honored and
	// the pointer is a finger.
	Mobile bool
}

func (s Size) String() string {
	if s.Name == "" {
		return fmt.Sprintf("%dx%d", s.Width, s.Height)
	}
	return fmt.Sprintf("%s %dx%d", s.Name, s.Width, s.Height)
}

// Request is one page to capture, once per size.
type Request struct {
	URL      string
	Sizes    []Size
	FullPage bool
}

// Shot is the capture of a page in one size.
type Shot struct {
	Size Size
	// Status is the HTTP status of the page; 0 when the browser did not say.
	Status int64
	// FinalURL is where the page ended up, after redirects.
	FinalURL string
	Title    string
	// Overflow is the width of the content when it is wider than the screen.
	Overflow int
	// PageHeight is the height of the whole page; Cut says the capture
	// stopped before its end.
	PageHeight int
	Cut        bool
	Images     []provider.Image
}

// Capture loads the page in a tab of its own, takes one screenshot per size
// and closes the tab. It waits in line when the tab limit is reached, and
// reports how long it waited.
func (m *Manager) Capture(ctx context.Context, req Request) (shots []Shot, waited time.Duration, err error) {
	waited, queueErr := m.tabs.Do(ctx, func() { shots, err = m.capture(ctx, req) })
	if queueErr != nil {
		return nil, waited, fmt.Errorf("o pedido saiu da fila do navegador sem rodar: %w", queueErr)
	}
	return shots, waited, err
}

func (m *Manager) capture(ctx context.Context, req Request) ([]Shot, error) {
	shots, browserCtx, err := m.attempt(ctx, req)
	if err != nil && browserCtx != nil && ctx.Err() == nil && lost(browserCtx, err) {
		// The browser died under this request: the next attempt opens another.
		shots, browserCtx, err = m.attempt(ctx, req)
	}
	if err != nil && browserCtx != nil {
		err = m.explain(ctx, browserCtx, req.URL, err)
	}
	return shots, err
}

// attempt takes the screenshots in one tab. It returns the context of the
// browser it used, nil when no browser could be had.
func (m *Manager) attempt(ctx context.Context, req Request) ([]Shot, context.Context, error) {
	browserCtx, err := m.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer m.release()

	// A browser context of its own: no cookie, cache or storage of another call.
	tab, closeTab := chromedp.NewContext(browserCtx, chromedp.WithNewBrowserContext())
	// closeTab closes the tab and waits for it, so the slot is only handed on
	// when the tab is gone.
	defer closeTab()
	stop := context.AfterFunc(ctx, closeTab)
	defer stop()

	// chromedp ties a tab to the context of its first action, so the tab is
	// opened here, before any action that carries the load timeout.
	if err := m.within(ctx, m.LoadTimeout, closeTab, func() error { return chromedp.Do(tab) }); err != nil {
		return nil, browserCtx, fmt.Errorf("abrindo a aba: %w", err)
	}

	shots := make([]Shot, 0, len(req.Sizes))
	for _, size := range req.Sizes {
		shot, err := m.shoot(tab, req.URL, size, req.FullPage)
		if err != nil {
			return nil, browserCtx, err
		}
		shots = append(shots, shot)
	}
	return shots, browserCtx, nil
}

// lost reports whether a failure came from the browser dying. chromedp ends
// the context of a dead browser a moment after its commands start to fail.
func lost(browserCtx context.Context, err error) bool {
	var load *chromedp.LoadError
	if errors.As(err, &load) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	select {
	case <-browserCtx.Done():
		return true
	case <-time.After(lostGrace):
		return false
	}
}

// within runs fn and gives up after limit or when ctx ends, calling abort to
// unblock fn.
func (m *Manager) within(ctx context.Context, limit time.Duration, abort func(), fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		abort()
		<-done
		return context.DeadlineExceeded
	case <-ctx.Done():
		abort()
		<-done
		return ctx.Err()
	}
}

// explain turns a failure into a message that tells the agent what happened
// and what to do.
func (m *Manager) explain(ctx, browserCtx context.Context, url string, err error) error {
	var load *chromedp.LoadError
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("o print de %s foi cancelado", url)
	case browserCtx.Err() != nil:
		return fmt.Errorf("o navegador fechou no meio do print de %s. Tente de novo: ele reabre sozinho", url)
	case errors.As(err, &load):
		switch load.ErrorText {
		case "net::ERR_CONNECTION_REFUSED":
			return fmt.Errorf("não consegui abrir %s: nada responde nesse endereço (%s). O servidor do projeto está no ar, nessa porta?", url, load.ErrorText)
		case "net::ERR_UNSAFE_PORT":
			return fmt.Errorf("não consegui abrir %s: o navegador bloqueia essa porta por segurança (%s). Suba o servidor em outra porta", url, load.ErrorText)
		case "net::ERR_NAME_NOT_RESOLVED":
			return fmt.Errorf("não consegui abrir %s: esse nome de endereço não existe (%s). Confira a URL", url, load.ErrorText)
		}
		return fmt.Errorf("não consegui abrir %s: o navegador devolveu %s", url, load.ErrorText)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("a página %s não terminou de carregar em %s. Confira se o servidor responde, ou se a página fica presa num recurso que nunca chega", url, m.LoadTimeout)
	}
	return fmt.Errorf("não consegui tirar o print de %s: %w", url, err)
}

func (m *Manager) shoot(tab context.Context, url string, size Size, fullPage bool) (Shot, error) {
	shot := Shot{Size: size}
	ctx, cancel := context.WithTimeout(tab, m.LoadTimeout)
	defer cancel()

	var screen []chromedp.EmulateViewportOption
	if size.Mobile {
		screen = append(screen, chromedp.EmulateMobile, chromedp.EmulateTouch)
	}
	if size.Height > size.Width {
		screen = append(screen, chromedp.EmulatePortrait)
	}
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(int64(size.Width), int64(size.Height), screen...)); err != nil {
		return shot, err
	}

	// Subscribed before the navigation, so none of its events is lost.
	settleCtx, settled := context.WithCancel(ctx)
	defer settled()
	lifecycle := chromedp.Events(settleCtx, page.LifecycleEvent)

	resp, err := chromedp.Run(ctx, chromedp.NavigateResponse(url))
	if err != nil {
		return shot, err
	}
	if resp != nil {
		shot.Status = resp.Status
	}
	tree, err := chromedp.Call(ctx, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return shot, err
	}
	shot.FinalURL = tree.FrameTree.Frame.URL

	// The page has loaded, but may still be fetching what it shows: wait until
	// the network goes quiet, up to settleTimeout.
	timer := time.AfterFunc(settleTimeout, settled)
	for ev, err := range lifecycle {
		if err != nil || (ev.LoaderID == tree.FrameTree.Frame.LoaderID && ev.Name == "networkIdle") {
			break
		}
	}
	timer.Stop()
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`document.fonts.ready.then(() => true)`, chromedp.EvalAwaitPromise)); err != nil {
		return shot, err
	}
	if shot.Title, err = chromedp.Run(ctx, chromedp.Title()); err != nil {
		return shot, err
	}

	metrics, err := chromedp.Call(ctx, page.GetLayoutMetrics, cdp.Empty{})
	if err != nil {
		return shot, err
	}
	if metrics.CSSContentSize == nil || metrics.CSSLayoutViewport == nil {
		return shot, errors.New("o navegador não informou o tamanho da página")
	}
	// Compared with the screen asked for, not with the layout viewport: a
	// phone stretches that viewport to fit content that is too wide.
	if metrics.CSSContentSize.Width > float64(size.Width)+0.5 {
		shot.Overflow = int(math.Ceil(metrics.CSSContentSize.Width))
	}
	shot.PageHeight = int(math.Ceil(metrics.CSSContentSize.Height))

	if !fullPage {
		img, err := snap(ctx, nil)
		if err != nil {
			return shot, err
		}
		img.Label = size.String()
		shot.Images = []provider.Image{img}
		return shot, nil
	}

	height := max(metrics.CSSContentSize.Height, float64(metrics.CSSLayoutViewport.ClientHeight))
	parts := int(math.Ceil(height / sliceHeight))
	if parts > maxSlices {
		parts, shot.Cut = maxSlices, true
	}
	for i := range parts {
		top := float64(i * sliceHeight)
		img, err := snap(ctx, &page.Viewport{X: 0, Y: top, Width: float64(metrics.CSSLayoutViewport.ClientWidth), Height: min(sliceHeight, height-top), Scale: 1})
		if err != nil {
			return shot, err
		}
		img.Label = fmt.Sprintf("%s, página inteira, parte %d de %d", size, i+1, parts)
		if parts == 1 {
			img.Label = fmt.Sprintf("%s, página inteira", size)
		}
		shot.Images = append(shot.Images, img)
	}
	return shot, nil
}

// snap captures the screen, or the given region of the page.
func snap(ctx context.Context, clip *page.Viewport) (provider.Image, error) {
	take := func(format page.CaptureScreenshotFormat, quality *int64) ([]byte, error) {
		res, err := chromedp.Call(ctx, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format:                format,
			Quality:               quality,
			Clip:                  clip,
			CaptureBeyondViewport: new(clip != nil),
		})
		return res.Data, err
	}
	img := provider.Image{MediaType: "image/png"}
	data, err := take(page.CaptureScreenshotFormatPng, nil)
	if err == nil && len(data) > maxPNGBytes {
		img.MediaType = "image/jpeg"
		data, err = take(page.CaptureScreenshotFormatJpeg, new(int64(jpegQuality)))
	}
	if err != nil {
		return img, err
	}
	if len(data) > maxImageBytes {
		return img, fmt.Errorf("o print ficou com %d MB, grande demais para mandar ao modelo. Use um tamanho de tela menor", len(data)>>20)
	}
	img.Data = data
	return img, nil
}

// acquire makes sure the browser is installed and running and returns its
// context. The caller must call release when done; the idle clock starts then.
func (m *Manager) acquire(ctx context.Context) (context.Context, error) {
	if err := m.ensureInstalled(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if m.proc != nil && m.proc.ctx.Err() != nil {
		m.proc.stop()
		m.proc = nil
	}
	if m.proc == nil {
		p, err := m.start(ctx)
		if err != nil {
			return nil, err
		}
		m.proc = p
	}
	if m.idle != nil {
		m.idle.Stop()
	}
	m.inflight++
	return m.proc.ctx, nil
}

func (m *Manager) release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight--
	if m.inflight > 0 || m.proc == nil {
		return
	}
	if m.idle != nil {
		m.idle.Stop()
	}
	m.idle = time.AfterFunc(m.IdleTimeout, m.stopIfIdle)
}

func (m *Manager) stopIfIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight == 0 && m.proc != nil {
		m.proc.stop()
		m.proc = nil
	}
}

// Close stops the browser, if it is running. No screenshot is taken after it.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.idle != nil {
		m.idle.Stop()
	}
	if m.proc != nil {
		m.proc.stop()
		m.proc = nil
	}
}

func (m *Manager) start(ctx context.Context) (*process, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(m.execPath()),
		// The headless shell hints text fully by default, which makes it
		// narrower than in a desktop Chrome and breaks lines elsewhere. With
		// slight hinting the pictures came out identical to the full Chrome
		// of the same version, pixel by pixel (measured on 25/11/2026).
		chromedp.Flag("font-render-hinting", "slight"),
	)
	// The browser outlives the request that started it: Close and the idle
	// clock stop it. chromedp makes the kernel kill it if the Mister dies.
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	p := &process{ctx: browserCtx, cancel: func() {
		cancelBrowser()
		cancelAlloc()
	}}
	err := m.within(ctx, m.StartTimeout, p.cancel, func() error { return chromedp.Do(browserCtx) })
	switch {
	case err == nil:
		return p, nil
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		err = fmt.Errorf("o navegador não abriu em %s", m.StartTimeout)
	}
	p.cancel()
	return nil, fmt.Errorf("abrindo o navegador %s: %w", m.execPath(), err)
}

// stop asks the browser to close and kills it if it does not.
func (p *process) stop() {
	ctx, cancel := context.WithTimeout(p.ctx, stopTimeout)
	_ = chromedp.Cancel(ctx)
	cancel()
	p.cancel()
}
