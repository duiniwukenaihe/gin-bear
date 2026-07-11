package bear

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// ContextShutdowner allows components to honor the application's shutdown deadline.
type ContextShutdowner interface {
	ShutdownContext(context.Context) error
}

// Lifecycle owns component startup and shutdown order for one application.
type Lifecycle struct {
	mu                 sync.Mutex
	components         []*lifecycleEntry
	beanEntries        map[reflect.Type]*lifecycleEntry
	state              lifecycleState
	registrationSealed bool
	operationDone      chan struct{}
	startErr           error
	stopErr            error
}

type lifecycleEntry struct {
	component     any
	registrations map[reflect.Type]struct{}
	active        bool
	started       bool
	stopped       bool
}

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleStarting
	lifecycleStarted
	lifecycleStopping
	lifecycleStopped
)

var (
	errLifecycleStopped = errors.New("lifecycle cannot start after stop")

	// ErrLifecycleRegistrationClosed reports registration after startup begins.
	ErrLifecycleRegistrationClosed = errors.New("lifecycle registration closed after startup begins")
)

const lifecycleRollbackTimeout = 5 * time.Second

func newLifecycle() *Lifecycle {
	return &Lifecycle{beanEntries: make(map[reflect.Type]*lifecycleEntry)}
}

func (l *Lifecycle) registrationClosed() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	closed := l.registrationSealed || l.state != lifecycleNew
	l.mu.Unlock()
	return closed
}

func (l *Lifecycle) sealRegistration() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.registrationSealed = true
	l.mu.Unlock()
}

func (l *Lifecycle) setBean(beanType reflect.Type, bean any) {
	if l == nil || bean == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.setBeanLocked(beanType, bean)
}

func (l *Lifecycle) registerBean(beanType reflect.Type, bean any, commit func()) error {
	if l == nil || bean == nil {
		commit()
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.registrationSealed || l.state != lifecycleNew {
		return ErrLifecycleRegistrationClosed
	}
	commit()
	l.setBeanLocked(beanType, bean)
	return nil
}

func (l *Lifecycle) setBeanLocked(beanType reflect.Type, bean any) {
	if l.state == lifecycleStopping || l.state == lifecycleStopped {
		return
	}
	if current := l.beanEntries[beanType]; current != nil && sameLifecycleComponent(current.component, bean) {
		return
	}
	if current := l.beanEntries[beanType]; current != nil {
		delete(current.registrations, beanType)
		delete(l.beanEntries, beanType)
		l.retireUnregisteredEntry(current)
	}
	entry := l.findComponentEntry(bean)
	if entry == nil {
		entry = &lifecycleEntry{
			component:     bean,
			registrations: make(map[reflect.Type]struct{}),
			active:        true,
		}
		l.components = append(l.components, entry)
	}
	entry.active = true
	entry.registrations[beanType] = struct{}{}
	l.beanEntries[beanType] = entry
}

func (l *Lifecycle) removeBean(beanType reflect.Type) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.beanEntries[beanType]
	if entry == nil {
		return
	}
	delete(l.beanEntries, beanType)
	delete(entry.registrations, beanType)
	l.retireUnregisteredEntry(entry)
}

func (l *Lifecycle) findComponentEntry(component any) *lifecycleEntry {
	for _, entry := range l.components {
		if sameLifecycleComponent(entry.component, component) {
			return entry
		}
	}
	return nil
}

func (l *Lifecycle) retireUnregisteredEntry(entry *lifecycleEntry) {
	if entry == nil || len(entry.registrations) > 0 {
		return
	}
	entry.active = false
	if entry.started {
		return
	}
	for i, candidate := range l.components {
		if candidate == entry {
			l.components = append(l.components[:i], l.components[i+1:]...)
			return
		}
	}
}

// Add appends a component in registration order. Registrations attempted after
// startup begins are ignored for compatibility; use TryAdd to observe rejection.
func (l *Lifecycle) Add(component any) {
	_ = l.TryAdd(component)
}

// TryAdd appends a component or reports that lifecycle registration is closed.
func (l *Lifecycle) TryAdd(component any) error {
	if l == nil || component == nil {
		return nil
	}
	return l.add(component)
}

func (l *Lifecycle) add(components ...any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.registrationSealed || l.state != lifecycleNew {
		return ErrLifecycleRegistrationClosed
	}
	for _, component := range components {
		if component != nil {
			l.components = append(l.components, &lifecycleEntry{component: component, active: true})
		}
	}
	return nil
}

// Start initializes components in registration order.
func (l *Lifecycle) Start(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		l.mu.Lock()
		switch l.state {
		case lifecycleStopped:
			err := l.startErr
			l.mu.Unlock()
			if err != nil {
				return err
			}
			return errLifecycleStopped
		case lifecycleStarted:
			err := l.startErr
			l.mu.Unlock()
			return err
		case lifecycleStarting, lifecycleStopping:
			done := l.operationDone
			l.mu.Unlock()
			if err := waitLifecycleOperation(ctx, done); err != nil {
				return err
			}
			continue
		default:
			l.state = lifecycleStarting
			l.operationDone = make(chan struct{})
			components := append([]*lifecycleEntry(nil), l.components...)
			l.mu.Unlock()
			return l.startComponents(ctx, components)
		}
	}
}

func (l *Lifecycle) startComponents(ctx context.Context, components []*lifecycleEntry) error {
	for _, entry := range components {
		l.mu.Lock()
		active := entry.active
		l.mu.Unlock()
		if !active {
			continue
		}
		initializer, ok := entry.component.(Initializer)
		if !ok {
			l.markStarted(entry)
			continue
		}
		if err := runLifecycleInitializer(ctx, initializer); err != nil {
			startErr := fmt.Errorf("component initialization failed [%s]: %w", lifecycleComponentName(entry.component), err)
			return l.rollbackStart(startErr)
		}
		l.markStarted(entry)
	}
	l.mu.Lock()
	l.state = lifecycleStarted
	close(l.operationDone)
	l.mu.Unlock()
	return nil
}

func runLifecycleInitializer(ctx context.Context, initializer Initializer) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("component initialization panic")
		}
	}()
	return initializer.Init(ctx)
}

func (l *Lifecycle) rollbackStart(startErr error) error {
	l.mu.Lock()
	components := make([]any, 0, len(l.components))
	for _, entry := range l.components {
		if entry.started && !entry.stopped {
			entry.stopped = true
			components = append(components, entry.component)
		}
	}
	l.state = lifecycleStopping
	l.startErr = startErr
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), lifecycleRollbackTimeout)
	defer cancel()
	rollbackErr := stopLifecycleComponents(ctx, components)
	l.mu.Lock()
	l.stopErr = rollbackErr
	l.state = lifecycleStopped
	close(l.operationDone)
	l.mu.Unlock()
	if rollbackErr != nil {
		return errors.Join(startErr, fmt.Errorf("lifecycle rollback failed: %w", rollbackErr))
	}
	return startErr
}

func (l *Lifecycle) markStarted(entry *lifecycleEntry) {
	l.mu.Lock()
	entry.started = true
	l.mu.Unlock()
}

// Stop shuts components down in reverse registration order and joins all errors.
func (l *Lifecycle) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		l.mu.Lock()
		switch l.state {
		case lifecycleStopped:
			err := l.stopErr
			l.mu.Unlock()
			return err
		case lifecycleStarting, lifecycleStopping:
			done := l.operationDone
			l.mu.Unlock()
			if err := waitLifecycleOperation(ctx, done); err != nil {
				return err
			}
			continue
		default:
			l.state = lifecycleStopping
			l.operationDone = make(chan struct{})
			components := make([]any, 0, len(l.components))
			for _, entry := range l.components {
				if entry.started && !entry.stopped {
					entry.stopped = true
					components = append(components, entry.component)
				}
			}
			l.mu.Unlock()
			return l.stopComponents(ctx, components)
		}
	}
}

func (l *Lifecycle) stopComponents(ctx context.Context, components []any) error {
	stopErr := stopLifecycleComponents(ctx, components)
	l.mu.Lock()
	l.stopErr = stopErr
	l.state = lifecycleStopped
	close(l.operationDone)
	l.mu.Unlock()
	return stopErr
}

func stopLifecycleComponents(ctx context.Context, components []any) error {
	var shutdownErrors []error
	for i := len(components) - 1; i >= 0; i-- {
		component := components[i]
		if err := ctx.Err(); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("component shutdown not started [%s]: %w", lifecycleComponentName(component), err))
			break
		}
		if err := stopLifecycleComponent(ctx, component); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("component shutdown failed [%s]: %w", lifecycleComponentName(component), err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(shutdownErrors...)
}

func waitLifecycleOperation(ctx context.Context, done <-chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func sameLifecycleComponent(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	aValue := reflect.ValueOf(a)
	bValue := reflect.ValueOf(b)
	if aValue.Type() != bValue.Type() {
		return false
	}
	if aValue.Comparable() {
		return aValue.Interface() == bValue.Interface()
	}
	switch aValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return aValue.Pointer() == bValue.Pointer()
	default:
		return false
	}
}

func stopLifecycleComponent(ctx context.Context, component any) error {
	switch component := component.(type) {
	case ContextShutdowner:
		if err := ctx.Err(); err != nil {
			return err
		}
		return runShutdownWorker(ctx, func() error { return component.ShutdownContext(ctx) })
	case Shutdowner:
		return runLegacyShutdown(ctx, component.Shutdown)
	default:
		return nil
	}
}

func runLegacyShutdown(ctx context.Context, shutdown func() error) error {
	return runShutdownWorker(ctx, shutdown)
}

func runShutdownWorker(ctx context.Context, shutdown func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- errors.New("component shutdown panic")
			}
		}()
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

func (h shutdownHook) Shutdown() error {
	h.fn()
	return nil
}
