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
	strict             bool
	state              lifecycleState
	registrationSealed bool
	operationDone      chan struct{}
	startErr           error
	startRetryable     bool
	stopErr            error
}

type lifecycleEntry struct {
	component     any
	registrations map[reflect.Type]struct{}
	active        bool
	started       bool
	stopped       bool
	stopState     lifecycleEntryStopState
	stopAttempt   *lifecycleStopAttempt
	terminalErr   error
	legacyStarted bool
}

type lifecycleEntryStopState uint8

const (
	lifecycleEntryPending lifecycleEntryStopState = iota
	lifecycleEntryStopping
	lifecycleEntryRetryPending
	lifecycleEntryStopped
	lifecycleEntryStoppedWithError
)

type lifecycleStopAttempt struct {
	done chan struct{}
	err  error
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
	return newLifecycleWithMode(false)
}

func newLifecycleWithMode(strict bool) *Lifecycle {
	return &Lifecycle{beanEntries: make(map[reflect.Type]*lifecycleEntry), strict: strict}
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

func (l *Lifecycle) registerBeans(registrations []beanRegistration, commit func()) error {
	if l == nil {
		commit()
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.registrationSealed || l.state != lifecycleNew {
		return ErrLifecycleRegistrationClosed
	}
	commit()
	for _, registration := range registrations {
		l.setBeanLocked(registration.beanType, registration.bean)
	}
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
	if l.strict {
		return l.startStrict(ctx)
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

func (l *Lifecycle) startStrict(ctx context.Context) error {
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
			l.startRetryable = false
			l.operationDone = make(chan struct{})
			components := append([]*lifecycleEntry(nil), l.components...)
			l.mu.Unlock()
			return l.startStrictComponents(ctx, components)
		}
	}
}

func (l *Lifecycle) startStrictComponents(ctx context.Context, components []*lifecycleEntry) error {
	for _, entry := range components {
		if err := ctx.Err(); err != nil {
			return l.rollbackStrictStart(fmt.Errorf("component initialization not started [%s]: %w", strictLifecycleComponentName(entry.component), err))
		}
		l.mu.Lock()
		active := entry.active
		if active {
			entry.started = true
			entry.stopped = false
			entry.stopState = lifecycleEntryPending
			entry.stopAttempt = nil
			entry.terminalErr = nil
			entry.legacyStarted = false
		}
		l.mu.Unlock()
		if !active {
			continue
		}
		initializer, ok := entry.component.(Initializer)
		if !ok {
			continue
		}
		if err := runLifecycleInitializer(ctx, initializer); err != nil {
			startErr := fmt.Errorf("component initialization failed [%s]: %w", strictLifecycleComponentName(entry.component), err)
			return l.rollbackStrictStart(startErr)
		}
	}
	l.mu.Lock()
	l.state = lifecycleStarted
	l.startErr = nil
	l.startRetryable = false
	close(l.operationDone)
	l.mu.Unlock()
	return nil
}

func (l *Lifecycle) rollbackStrictStart(startErr error) error {
	l.mu.Lock()
	l.state = lifecycleStopping
	l.startErr = startErr
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), lifecycleRollbackTimeout)
	defer cancel()
	rollbackErr, complete := l.stopStrictEntries(ctx)

	l.mu.Lock()
	if complete && rollbackErr == nil {
		l.resetStrictEntriesForRetryLocked()
		l.state = lifecycleNew
		l.startRetryable = true
		l.stopErr = nil
	} else {
		l.startRetryable = false
		l.stopErr = rollbackErr
		if complete {
			l.state = lifecycleStopped
		} else {
			l.state = lifecycleStarted
		}
	}
	close(l.operationDone)
	l.mu.Unlock()
	if rollbackErr != nil {
		return errors.Join(startErr, fmt.Errorf("lifecycle rollback failed: %w", rollbackErr))
	}
	return startErr
}

func (l *Lifecycle) resetStrictEntriesForRetryLocked() {
	for _, entry := range l.components {
		entry.started = false
		entry.stopped = false
		entry.stopState = lifecycleEntryPending
		entry.stopAttempt = nil
		entry.terminalErr = nil
		entry.legacyStarted = false
	}
}

func (l *Lifecycle) canRetryStart() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	retryable := l.strict && l.startRetryable && l.state == lifecycleNew
	l.mu.Unlock()
	return retryable
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
	if l.strict {
		return l.stopStrict(ctx)
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

func (l *Lifecycle) stopStrict(ctx context.Context) error {
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
			l.mu.Unlock()

			stopErr, complete := l.stopStrictEntries(ctx)
			terminalErr := l.strictTerminalStopError()
			l.mu.Lock()
			if complete {
				l.state = lifecycleStopped
				l.stopErr = terminalErr
				stopErr = l.stopErr
			} else {
				l.state = lifecycleStarted
				l.stopErr = nil
			}
			close(l.operationDone)
			l.mu.Unlock()
			return stopErr
		}
	}
}

func (l *Lifecycle) stopStrictEntries(ctx context.Context) (error, bool) {
	var shutdownErrors []error
	for index := len(l.components) - 1; index >= 0; index-- {
		entry := l.components[index]
		if !entry.active || !entry.started {
			continue
		}
		if entry.stopState == lifecycleEntryStopped || entry.stopState == lifecycleEntryStoppedWithError {
			continue
		}
		if err := ctx.Err(); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("component shutdown not started [%s]: %w", strictLifecycleComponentName(entry.component), err))
			return errors.Join(shutdownErrors...), false
		}
		err, complete := l.stopStrictEntry(ctx, entry)
		if err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("component shutdown failed [%s]: %w", strictLifecycleComponentName(entry.component), err))
		}
		if !complete {
			return errors.Join(shutdownErrors...), false
		}
	}
	return errors.Join(shutdownErrors...), true
}

func (l *Lifecycle) stopStrictEntry(ctx context.Context, entry *lifecycleEntry) (error, bool) {
	switch component := entry.component.(type) {
	case ContextShutdowner:
		startedHere := false
		for {
			if entry.stopState != lifecycleEntryStopping {
				if err := ctx.Err(); err != nil {
					return err, false
				}
				entry.stopAttempt = startLifecycleStopAttempt(func() error { return component.ShutdownContext(ctx) })
				entry.stopState = lifecycleEntryStopping
				startedHere = true
			}
			err, finished := waitLifecycleStopAttempt(ctx, entry.stopAttempt)
			if !finished {
				return err, false
			}
			entry.stopAttempt = nil
			switch {
			case err == nil:
				entry.stopState = lifecycleEntryStopped
				entry.stopped = true
				return nil, true
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				entry.stopState = lifecycleEntryRetryPending
				if startedHere || ctx.Err() != nil {
					return err, false
				}
				continue
			default:
				entry.stopState = lifecycleEntryStoppedWithError
				entry.stopped = true
				entry.terminalErr = err
				return err, true
			}
		}
	case Shutdowner:
		if !entry.legacyStarted {
			if err := ctx.Err(); err != nil {
				return err, false
			}
			entry.stopAttempt = startLifecycleStopAttempt(component.Shutdown)
			entry.stopState = lifecycleEntryStopping
			entry.legacyStarted = true
		}
		err, finished := waitLifecycleStopAttempt(ctx, entry.stopAttempt)
		if !finished {
			return err, false
		}
		if err == nil {
			entry.stopState = lifecycleEntryStopped
			entry.stopped = true
			return nil, true
		}
		entry.stopState = lifecycleEntryStoppedWithError
		entry.stopped = true
		entry.terminalErr = err
		return err, true
	default:
		entry.stopState = lifecycleEntryStopped
		entry.stopped = true
		return nil, true
	}
}

func startLifecycleStopAttempt(shutdown func() error) *lifecycleStopAttempt {
	attempt := &lifecycleStopAttempt{done: make(chan struct{})}
	go func() {
		defer func() {
			if recover() != nil {
				attempt.err = errors.New("component shutdown panic")
			}
			close(attempt.done)
		}()
		attempt.err = shutdown()
	}()
	return attempt
}

func waitLifecycleStopAttempt(ctx context.Context, attempt *lifecycleStopAttempt) (error, bool) {
	if attempt == nil {
		return nil, true
	}
	select {
	case <-attempt.done:
		return attempt.err, true
	case <-ctx.Done():
		return ctx.Err(), false
	}
}

func (l *Lifecycle) strictTerminalStopError() error {
	var terminalErrors []error
	for _, entry := range l.components {
		if entry.stopState == lifecycleEntryStoppedWithError && entry.terminalErr != nil {
			terminalErrors = append(terminalErrors, fmt.Errorf("component shutdown failed [%s]: %w", strictLifecycleComponentName(entry.component), entry.terminalErr))
		}
	}
	return errors.Join(terminalErrors...)
}

func strictLifecycleComponentName(component any) string {
	return fmt.Sprintf("%T", component)
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
