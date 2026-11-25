package embed

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOnDemand_ManyCallersInstallOnce(t *testing.T) {
	t.Parallel()
	var o OnDemand
	var installed atomic.Bool
	var installs atomic.Int32
	install := func(context.Context) error {
		installs.Add(1)
		// Long enough for every caller to arrive during the install.
		time.Sleep(100 * time.Millisecond)
		installed.Store(true)
		return nil
	}
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Go(func() { errs[i] = o.Ensure(context.Background(), installed.Load, install) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("chamada %d: %v", i, err)
		}
	}
	if got := installs.Load(); got != 1 {
		t.Errorf("houve %d instalações para 10 chamadas ao mesmo tempo, quero 1", got)
	}
	if err := o.Ensure(context.Background(), installed.Load, install); err != nil || installs.Load() != 1 {
		t.Errorf("o que já está instalado não se instala de novo: %v, %d instalações", err, installs.Load())
	}
}

func TestOnDemand_CallerThatGivesUpWaiting(t *testing.T) {
	t.Parallel()
	var o OnDemand
	started, release := make(chan struct{}), make(chan struct{})
	never := func() bool { return false }
	done := make(chan error, 1)
	go func() {
		done <- o.Ensure(context.Background(), never, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := o.Ensure(ctx, never, func(context.Context) error {
		t.Error("quem chega durante um download não começa outro")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "esperando um download") {
		t.Errorf("erro = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("o download em andamento não deveria ser afetado por quem desistiu: %v", err)
	}
}

func TestOnDemand_FailedDownloadWaitsBeforeTryingAgain(t *testing.T) {
	t.Parallel()
	never := func() bool { return false }
	boom := errors.New("sem rede")
	attempts := 0
	fail := func(context.Context) error {
		attempts++
		return boom
	}

	spaced := OnDemand{RetryAfter: time.Hour}
	if err := spaced.Ensure(context.Background(), never, fail); !errors.Is(err, boom) {
		t.Fatalf("erro = %v", err)
	}
	err := spaced.Ensure(context.Background(), never, fail)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "falhou há pouco") || attempts != 1 {
		t.Errorf("dentro do intervalo não se tenta de novo: %v, %d tentativas", err, attempts)
	}

	attempts = 0
	eager := OnDemand{}
	for range 3 {
		_ = eager.Ensure(context.Background(), never, fail)
	}
	if attempts != 3 {
		t.Errorf("sem intervalo, cada chamada tenta de novo: %d tentativas", attempts)
	}

	// A caller that cancelled says nothing about the network: the next one tries.
	attempts = 0
	cancelled := OnDemand{RetryAfter: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	_ = cancelled.Ensure(ctx, never, func(ctx context.Context) error {
		attempts++
		cancel()
		return ctx.Err()
	})
	if err := cancelled.Ensure(context.Background(), never, fail); !errors.Is(err, boom) || attempts != 2 {
		t.Errorf("depois de um cancelamento a próxima chamada tenta: %v, %d tentativas", err, attempts)
	}
}
