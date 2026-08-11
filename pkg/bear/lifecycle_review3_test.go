package bear

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type review3CanceledShutdowner struct {
	calls atomic.Int32
}

func (c *review3CanceledShutdowner) ShutdownContext(context.Context) error {
	c.calls.Add(1)
	return context.Canceled
}

func TestStrictStopCachesComponentContextCancellationAsTerminal(t *testing.T) {
	component := &review3CanceledShutdowner{}
	lifecycle := newLifecycleWithMode(true)
	lifecycle.Add(component)
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	firstErr := lifecycle.Stop(context.Background())
	if !errors.Is(firstErr, context.Canceled) {
		t.Fatalf("first Stop() error = %v, want context.Canceled", firstErr)
	}
	lifecycle.mu.Lock()
	state := lifecycle.state
	cachedErr := lifecycle.stopErr
	lifecycle.mu.Unlock()
	if state != lifecycleStopped {
		t.Fatalf("lifecycle state = %v, want lifecycleStopped", state)
	}
	if cachedErr != firstErr {
		t.Fatalf("cached Stop() error = %v, want first error %v", cachedErr, firstErr)
	}

	if secondErr := lifecycle.Stop(context.Background()); secondErr != firstErr {
		t.Fatalf("second Stop() error = %v, want cached %v", secondErr, firstErr)
	}
	if got := component.calls.Load(); got != 1 {
		t.Fatalf("ShutdownContext calls = %d, want 1", got)
	}
}
