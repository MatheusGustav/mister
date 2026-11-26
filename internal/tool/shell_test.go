package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShell(t *testing.T) {
	t.Parallel()
	limits := DefaultShellLimits()
	limits.MaxOutput = 600
	shell := find(t, ShellTools(limits), "rodar_comando")

	tests := []struct {
		name     string
		input    map[string]any
		wantErr  bool
		contains []string
	}{
		{name: "saída e código zero", input: map[string]any{"comando": "echo olá"}, contains: []string{"[código 0", "olá"}},
		{name: "stderr junto", input: map[string]any{"comando": "echo erro >&2"}, contains: []string{"erro"}},
		{name: "código de saída", input: map[string]any{"comando": "echo antes; exit 3"}, wantErr: true, contains: []string{"[código 3", "antes"}},
		{name: "sem saída", input: map[string]any{"comando": "true"}, contains: []string{"(sem saída)"}},
		{name: "roda na pasta de trabalho", input: map[string]any{"comando": "touch marca && ls"}, contains: []string{"marca"}},
		{name: "timeout", input: map[string]any{"comando": "echo começou; sleep 5", "timeout_segundos": 1}, wantErr: true, contains: []string{"interrompido", "começou", "timeout_segundos"}},
		{name: "saída cortada no meio", input: map[string]any{"comando": "seq 1 2000"}, contains: []string{"cortados do meio", "\n1\n", "2000"}},
		{name: "comando vazio", input: map[string]any{"comando": "  "}, wantErr: true, contains: []string{"vazio"}},
		{name: "timeout negativo", input: map[string]any{"comando": "true", "timeout_segundos": -1}, wantErr: true, contains: []string{"negativo"}},
		{name: "campo inventado", input: map[string]any{"comando": "true", "pasta": "/"}, wantErr: true, contains: []string{"inválidos"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := call(t, shell, newEnv(t), tt.input)
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError = %v, quero %v: %s", res.IsError, tt.wantErr, res.Content)
			}
			for _, want := range tt.contains {
				if !strings.Contains(res.Content, want) {
					t.Errorf("resultado não contém %q:\n%s", want, res.Content)
				}
			}
		})
	}
}

func TestShell_TimeoutKillsChildren(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	shell := find(t, ShellTools(DefaultShellLimits()), "rodar_comando")
	start := time.Now()
	res := call(t, shell, env, map[string]any{"comando": "(sleep 30; touch tarde) & sleep 30", "timeout_segundos": 1})
	if !res.IsError {
		t.Fatalf("deveria estourar o timeout: %s", res.Content)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("o comando levou %s para ser interrompido", elapsed)
	}
}

func TestShell_ReportsChildMemory(t *testing.T) {
	t.Parallel()
	env := newEnv(t)
	var peak atomic.Int64
	env.ChildMem = func(kb int64) { peak.Store(kb) }
	res := call(t, find(t, ShellTools(DefaultShellLimits()), "rodar_comando"), env, map[string]any{"comando": "true"})
	if res.IsError {
		t.Fatal(res.Content)
	}
	if peak.Load() <= 0 {
		t.Errorf("pico de memória do processo filho = %d KB, quero maior que zero", peak.Load())
	}
}

func TestCappedBuffer(t *testing.T) {
	t.Parallel()
	b := newCappedBuffer(300)
	for i := range 100 {
		fmt.Fprintf(b, "linha %03d\n", i)
	}
	out := b.String()
	if !strings.HasPrefix(out, "linha 000\n") {
		t.Errorf("perdeu o começo: %q", out[:20])
	}
	if !strings.HasSuffix(out, "linha 099\n") {
		t.Errorf("perdeu o fim: %q", out[len(out)-20:])
	}
	if !strings.Contains(out, "caracteres cortados do meio") {
		t.Error("faltou o aviso de corte")
	}
	if len(out) > 300+100 {
		t.Errorf("a saída ficou com %d caracteres, acima do teto", len(out))
	}

	small := newCappedBuffer(300)
	fmt.Fprint(small, "curto")
	if got := small.String(); got != "curto" {
		t.Errorf("saída pequena = %q, quero sem corte", got)
	}
}

func TestHeavyQueue_OneAtATime(t *testing.T) {
	t.Parallel()
	q := NewHeavyQueue(1)
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			_, err := q.Do(context.Background(), func() {
				now := running.Add(1)
				for {
					old := peak.Load()
					if now <= old || peak.CompareAndSwap(old, now) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				running.Add(-1)
			})
			if err != nil {
				t.Errorf("Do: %v", err)
			}
		})
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Errorf("rodaram %d comandos pesados ao mesmo tempo, quero 1", peak.Load())
	}
	if q.Running() != 0 || q.Waiting() != 0 {
		t.Errorf("a fila não esvaziou: rodando %d, esperando %d", q.Running(), q.Waiting())
	}
}

// Two queues over the same folder are two Misters open on the same machine.
func TestHeavyQueue_SharedByEveryMister(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "fila")
	queues := []*HeavyQueue{NewSharedHeavyQueue(1, dir), NewSharedHeavyQueue(1, dir), NewSharedHeavyQueue(1, dir)}
	var running, peak, ran atomic.Int32
	var wg sync.WaitGroup
	for _, q := range queues {
		for range 2 {
			wg.Go(func() {
				_, err := q.Do(context.Background(), func() {
					now := running.Add(1)
					for {
						old := peak.Load()
						if now <= old || peak.CompareAndSwap(old, now) {
							break
						}
					}
					time.Sleep(20 * time.Millisecond)
					running.Add(-1)
					ran.Add(1)
				})
				if err != nil {
					t.Errorf("Do: %v", err)
				}
			})
		}
	}
	wg.Wait()
	if peak.Load() != 1 || ran.Load() != 6 {
		t.Errorf("rodaram %d ao mesmo tempo (quero 1) e %d no total (quero 6)", peak.Load(), ran.Load())
	}
}

func TestHeavyQueue_SharedCancelWhileAnotherMisterRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	busy, waiting := NewSharedHeavyQueue(1, dir), NewSharedHeavyQueue(1, dir)
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = busy.Do(context.Background(), func() {
			close(started)
			<-release
		})
	})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	ran := false
	waited, err := waiting.Do(ctx, func() { ran = true })
	if err == nil || ran || waited < 100*time.Millisecond {
		t.Errorf("quem desistiu da fila não deveria rodar: err=%v ran=%v esperou=%s", err, ran, waited)
	}
	close(release)
	wg.Wait()
	if _, err := waiting.Do(context.Background(), func() { ran = true }); err != nil || !ran {
		t.Errorf("com a vaga livre, deveria rodar: err=%v ran=%v", err, ran)
	}
	if waiting.Waiting() != 0 || waiting.Running() != 0 {
		t.Errorf("a fila não esvaziou: rodando %d, esperando %d", waiting.Running(), waiting.Waiting())
	}
}

// A folder that cannot be created leaves each Mister with its own limit.
func TestHeavyQueue_SharedFolderThatCannotBeUsed(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "arquivo")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ran := false
	if _, err := NewSharedHeavyQueue(1, filepath.Join(file, "fila")).Do(context.Background(), func() { ran = true }); err != nil || !ran {
		t.Errorf("o comando deveria rodar mesmo sem a pasta: err=%v ran=%v", err, ran)
	}
}

func TestHeavyQueue_CancelWhileWaiting(t *testing.T) {
	t.Parallel()
	q := NewHeavyQueue(1)
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = q.Do(context.Background(), func() {
			close(started)
			<-release
		})
	})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ran := false
	if _, err := q.Do(ctx, func() { ran = true }); err == nil || ran {
		t.Errorf("quem desistiu da fila não deveria rodar: err=%v ran=%v", err, ran)
	}
	close(release)
	wg.Wait()
	if q.Waiting() != 0 {
		t.Errorf("sobrou %d esperando", q.Waiting())
	}
}

func TestHeavyTool_WaitsForSlot(t *testing.T) {
	t.Parallel()
	heavy := find(t, ShellTools(DefaultShellLimits()), "rodar_pesado")
	env := newEnv(t)
	env.Heavy = NewHeavyQueue(1)

	var wg sync.WaitGroup
	results := make([]Result, 2)
	for i := range results {
		wg.Go(func() {
			results[i] = call(t, heavy, env, map[string]any{"comando": "test ! -e trava && touch trava && sleep 0.3 && rm trava"})
		})
	}
	wg.Wait()
	for i, res := range results {
		if res.IsError {
			t.Errorf("comando %d viu o outro rodando ao mesmo tempo: %s", i, res.Content)
		}
	}
}
