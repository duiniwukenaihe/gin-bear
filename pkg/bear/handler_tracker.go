package bear

import (
	"context"
	"sync"

	"github.com/gin-gonic/gin"
)

type activeHandlerTracker struct {
	mu     sync.Mutex
	active int
	idle   chan struct{}
	closed bool
}

func newActiveHandlerTracker() *activeHandlerTracker {
	idle := make(chan struct{})
	close(idle)
	return &activeHandlerTracker{idle: idle}
}

func (t *activeHandlerTracker) begin() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	if t.active == 0 {
		t.idle = make(chan struct{})
	}
	t.active++
	return true
}

func (t *activeHandlerTracker) end() {
	t.mu.Lock()
	if t.active > 0 {
		t.active--
		if t.active == 0 {
			close(t.idle)
		}
	}
	t.mu.Unlock()
}

func (t *activeHandlerTracker) wait(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	idle := t.idle
	active := t.active
	t.mu.Unlock()
	if active == 0 {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *activeHandlerTracker) count() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

func (t *activeHandlerTracker) stopAccepting() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
}

func (t *activeHandlerTracker) middleware() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if !t.begin() {
			WriteError(ctx, NewStatusError(503, 503, "server is shutting down", nil))
			return
		}
		defer t.end()
		ctx.Next()
	}
}
