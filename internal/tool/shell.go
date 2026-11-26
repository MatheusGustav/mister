package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ShellLimits bounds what a command may take.
type ShellLimits struct {
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	HeavyTimeout   time.Duration
	HeavyMax       time.Duration
	// MaxOutput is the most characters of output handed back to the model.
	MaxOutput int
}

func DefaultShellLimits() ShellLimits {
	return ShellLimits{
		DefaultTimeout: 2 * time.Minute,
		MaxTimeout:     10 * time.Minute,
		HeavyTimeout:   10 * time.Minute,
		HeavyMax:       30 * time.Minute,
		MaxOutput:      30_000,
	}
}

// HeavyQueue lets only a few heavy commands run at a time, across all agents.
// With a folder, the limit holds for every Mister that uses the same folder:
// each command in progress keeps one of the lock files there.
type HeavyQueue struct {
	slots   chan struct{}
	dir     string
	waiting atomic.Int32
	running atomic.Int32
}

func NewHeavyQueue(limit int) *HeavyQueue {
	if limit < 1 {
		limit = 1
	}
	return &HeavyQueue{slots: make(chan struct{}, limit)}
}

// NewSharedHeavyQueue is NewHeavyQueue with the limit shared by every Mister
// that points to dir.
func NewSharedHeavyQueue(limit int, dir string) *HeavyQueue {
	q := NewHeavyQueue(limit)
	q.dir = dir
	return q
}

// Do runs fn when a slot is free and reports how long it waited in line.
func (q *HeavyQueue) Do(ctx context.Context, fn func()) (time.Duration, error) {
	start := time.Now()
	q.waiting.Add(1)
	select {
	case q.slots <- struct{}{}:
	case <-ctx.Done():
		q.waiting.Add(-1)
		return time.Since(start), ctx.Err()
	}
	leave, err := q.shareSlot(ctx)
	q.waiting.Add(-1)
	if err != nil {
		<-q.slots
		return time.Since(start), err
	}
	waited := time.Since(start)
	q.running.Add(1)
	defer func() {
		q.running.Add(-1)
		leave()
		<-q.slots
	}()
	fn()
	return waited, nil
}

// shareSlot waits for one of the lock files of the folder, which the other
// Misters also line up for, and returns how to give it back. The system drops
// the lock of a Mister that dies. A folder that cannot be used leaves the
// limit to this Mister alone: the command must still run.
func (q *HeavyQueue) shareSlot(ctx context.Context) (leave func(), err error) {
	if q.dir == "" || os.MkdirAll(q.dir, 0o755) != nil {
		return func() {}, nil
	}
	for {
		for i := range cap(q.slots) {
			f, err := os.OpenFile(filepath.Join(q.dir, fmt.Sprintf("pesado-%d.lock", i)), os.O_RDWR|os.O_CREATE, 0o644)
			if err != nil {
				return func() {}, nil
			}
			if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				return func() { f.Close() }, nil
			}
			f.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (q *HeavyQueue) Limit() int   { return cap(q.slots) }
func (q *HeavyQueue) Running() int { return int(q.running.Load()) }
func (q *HeavyQueue) Waiting() int { return int(q.waiting.Load()) }

type shellInput struct {
	Comando string `json:"comando"`
	Timeout int    `json:"timeout_segundos"`
}

// ShellTools returns the plain shell tool and the queued one for heavy work.
func ShellTools(limits ShellLimits) []Tool {
	schema := func(defaultTimeout, maxTimeout time.Duration) map[string]any {
		return object([]string{"comando"}, map[string]any{
			"comando": str("linha de shell (bash), do jeito que se digitaria no terminal"),
			"timeout_segundos": integer(fmt.Sprintf("tempo máximo em segundos (opcional, padrão %d, máximo %d)",
				int(defaultTimeout.Seconds()), int(maxTimeout.Seconds()))),
		})
	}
	return []Tool{
		{
			Name: "rodar_comando",
			Description: "Roda um comando de shell na pasta de trabalho e devolve a saída (stdout e stderr juntos) e o código de saída. " +
				"O comando roda até terminar ou até o timeout. Saída grande volta cortada no meio. " +
				"Não use para o que é pesado (servidor do projeto, navegador, Playwright, suíte completa, build): isso é com rodar_pesado.",
			Schema: schema(limits.DefaultTimeout, limits.MaxTimeout),
			Run: func(ctx context.Context, env *Env, raw json.RawMessage) Result {
				in, err := decode[shellInput](raw)
				if err != nil {
					return Fail("%v", err)
				}
				return runShell(ctx, env, in, limits.DefaultTimeout, limits.MaxTimeout, limits.MaxOutput)
			},
		},
		{
			Name: "rodar_pesado",
			Description: "Roda um comando pesado (subir o servidor do projeto, navegador, Playwright, suíte completa de testes, build) pela fila: " +
				"só um número limitado roda por vez na máquina, os outros esperam a vez. " +
				"Servidor: suba, teste e derrube no mesmo comando, porque o comando precisa terminar para liberar a fila. " +
				"Só para tirar print com tirar_print o servidor fica em segundo plano entre as chamadas: suba com & e a saída num arquivo, numa porta livre, guarde o PID, " +
				"deixe no ar enquanto a tarefa usar e derrube pelo PID no fim.",
			Schema: schema(limits.HeavyTimeout, limits.HeavyMax),
			Run: func(ctx context.Context, env *Env, raw json.RawMessage) Result {
				in, err := decode[shellInput](raw)
				if err != nil {
					return Fail("%v", err)
				}
				if env.Heavy == nil {
					return runShell(ctx, env, in, limits.HeavyTimeout, limits.HeavyMax, limits.MaxOutput)
				}
				var res Result
				waited, err := env.Heavy.Do(ctx, func() {
					res = runShell(ctx, env, in, limits.HeavyTimeout, limits.HeavyMax, limits.MaxOutput)
				})
				if err != nil {
					return Fail("o comando saiu da fila sem rodar (esperou %s): %v", waited.Round(time.Second), err)
				}
				if waited > time.Second {
					res.Content = fmt.Sprintf("[esperou %s na fila]\n%s", waited.Round(time.Second), res.Content)
				}
				return res
			},
		},
	}
}

func runShell(ctx context.Context, env *Env, in shellInput, defaultTimeout, maxTimeout time.Duration, maxOutput int) Result {
	if strings.TrimSpace(in.Comando) == "" {
		return Fail("'comando' veio vazio. Informe a linha de shell a rodar.")
	}
	if in.Timeout < 0 {
		return Fail("'timeout_segundos' não pode ser negativo.")
	}
	timeout := defaultTimeout
	if in.Timeout > 0 {
		timeout = time.Duration(in.Timeout) * time.Second
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := "bash"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "sh"
	}
	output := newCappedBuffer(maxOutput)
	cmd := exec.CommandContext(runCtx, shell, "-c", in.Comando)
	cmd.Dir = env.Workspace.Root()
	cmd.Stdout, cmd.Stderr = output, output
	cmd.Stdin = nil
	// Own process group, so the timeout kills the children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// A child left in the background keeps the output pipe open; do not wait for it.
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start).Round(100 * time.Millisecond)
	if cmd.ProcessState != nil && env.ChildMem != nil {
		if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			env.ChildMem(usage.Maxrss)
		}
	}
	text := output.String()
	if strings.TrimSpace(text) == "" {
		text = "(sem saída)"
	}

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return Fail("o comando passou de %s e foi interrompido. Saída até ali:\n%s\nSe ele precisa de mais tempo, aumente timeout_segundos (máximo %d).",
			timeout, text, int(maxTimeout.Seconds()))
	case ctx.Err() != nil:
		return Fail("o comando foi cancelado. Saída até ali:\n%s", text)
	case err == nil:
		return OK("[código 0, %s]\n%s", elapsed, text)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return Fail("[código %d, %s]\n%s", exit.ExitCode(), elapsed, text)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return OK("[código 0, %s, deixou processo em segundo plano]\n%s", elapsed, text)
	}
	if errors.Is(err, os.ErrNotExist) {
		return Fail("não foi possível rodar o comando: a pasta de trabalho %s não existe mais.", env.Workspace.Root())
	}
	return Fail("não foi possível rodar o comando: %v", err)
}

// cappedBuffer keeps the beginning and the end of a stream and drops the
// middle, so a huge output never grows in memory.
type cappedBuffer struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	headMax int
	tailMax int
	dropped int
}

func newCappedBuffer(max int) *cappedBuffer {
	if max < 200 {
		max = 200
	}
	return &cappedBuffer{headMax: max / 3, tailMax: max - max/3}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if room := b.headMax - len(b.head); room > 0 {
		take := min(room, len(p))
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - b.tailMax; over > 0 {
		b.dropped += over
		b.tail = append(b.tail[:0], b.tail[over:]...)
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped == 0 {
		return string(b.head) + string(b.tail)
	}
	return fmt.Sprintf("%s\n[... %d caracteres cortados do meio da saída ...]\n%s",
		strings.ToValidUTF8(string(b.head), ""), b.dropped, strings.ToValidUTF8(string(b.tail), ""))
}
