package bear

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// ContextShutdowner allows components to honor the application's shutdown deadline.
type ContextShutdowner interface {
	ShutdownContext(context.Context) error
}

// Lifecycle owns component startup and shutdown order for one application.
type Lifecycle struct {
	mu          sync.Mutex
	components  []any
	beanIndexes map[reflect.Type]int
	started     bool
	stopped     bool
}

func newLifecycle() *Lifecycle {
	return &Lifecycle{beanIndexes: make(map[reflect.Type]int)}
}

func (l *Lifecycle) setBean(beanType reflect.Type, bean any) {
	if l == nil || bean == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	if index, exists := l.beanIndexes[beanType]; exists {
		l.components[index] = bean
		return
	}
	l.beanIndexes[beanType] = len(l.components)
	l.components = append(l.components, bean)
}

func (l *Lifecycle) removeBean(beanType reflect.Type) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	index, exists := l.beanIndexes[beanType]
	if !exists {
		return
	}
	delete(l.beanIndexes, beanType)
	l.components = append(l.components[:index], l.components[index+1:]...)
	for registeredType, registeredIndex := range l.beanIndexes {
		if registeredIndex > index {
			l.beanIndexes[registeredType] = registeredIndex - 1
		}
	}
}

// Add appends a component to the lifecycle in registration order.
func (l *Lifecycle) Add(component any) {
	if l == nil || component == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.components = append(l.components, component)
}

// Start initializes components in registration order.
func (l *Lifecycle) Start(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return nil
	}
	l.started = true
	components := append([]any(nil), l.components...)
	l.mu.Unlock()

	for _, component := range components {
		initializer, ok := component.(Initializer)
		if !ok {
			continue
		}
		if err := initializer.Init(ctx); err != nil {
			return fmt.Errorf("component initialization failed [%s]: %w", lifecycleComponentName(component), err)
		}
	}
	return nil
}

// Stop shuts components down in reverse registration order and joins all errors.
func (l *Lifecycle) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return nil
	}
	l.stopped = true
	components := append([]any(nil), l.components...)
	l.mu.Unlock()

	var shutdownErrors []error
	for i := len(components) - 1; i >= 0; i-- {
		component := components[i]
		if err := stopLifecycleComponent(ctx, component); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("component shutdown failed [%s]: %w", lifecycleComponentName(component), err))
		}
	}
	return errors.Join(shutdownErrors...)
}

func stopLifecycleComponent(ctx context.Context, component any) error {
	var shutdown func() error
	switch component := component.(type) {
	case ContextShutdowner:
		shutdown = func() error { return component.ShutdownContext(ctx) }
	case Shutdowner:
		shutdown = component.Shutdown
	default:
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- shutdown()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func lifecycleComponentName(component any) string {
	if bean, ok := component.(Bean); ok {
		return bean.Name()
	}
	return fmt.Sprintf("%T", component)
}

type shutdownHook struct {
	fn func()
}

func (h shutdownHook) ShutdownContext(context.Context) error {
	h.fn()
	return nil
}
