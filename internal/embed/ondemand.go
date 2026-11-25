package embed

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// OnDemand is the rule every heavy dependency of the Mister follows: it is
// downloaded the first time it is needed. Callers that arrive during the
// download wait for it instead of starting another. The zero value is ready.
type OnDemand struct {
	// RetryAfter is how long a failed download is not tried again: the callers
	// in between get the same error at once. Zero tries on every call.
	RetryAfter time.Duration

	once     sync.Once
	turn     chan struct{}
	failed   error
	failedAt time.Time
}

// Ensure runs install when installed says the dependency is not on disk.
func (o *OnDemand) Ensure(ctx context.Context, installed func() bool, install func(context.Context) error) error {
	if installed() {
		return nil
	}
	o.once.Do(func() { o.turn = make(chan struct{}, 1) })
	select {
	case o.turn <- struct{}{}:
		defer func() { <-o.turn }()
	case <-ctx.Done():
		return fmt.Errorf("esperando um download que já estava em andamento: %w", ctx.Err())
	}
	if installed() {
		return nil
	}
	if o.failed != nil && time.Since(o.failedAt) < o.RetryAfter {
		return fmt.Errorf("o download falhou há pouco e só é tentado de novo depois de %s: %w", o.RetryAfter, o.failed)
	}
	err := install(ctx)
	// A caller that gave up says nothing about the next attempt.
	if err != nil && ctx.Err() == nil {
		o.failed, o.failedAt = err, time.Now()
	} else {
		o.failed = nil
	}
	return err
}
