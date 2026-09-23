package bear

import (
	"context"
	"sync"
	"testing"
)

type expressionMetadataConcurrencyBean struct{ name string }

func (b *expressionMetadataConcurrencyBean) Name() string { return b.name }

// TestExpressionMetadataSurvivesConcurrentRegistrationAndOpenAPIRead pins the
// lock discipline for Bear.exprData. Compatibility registration used to write
// that map without synchronization while the request-time OpenAPI path read it
// through authFairings, which the race detector reports as a concurrent map
// read/write between Beans and GenerateOpenAPI.
func TestExpressionMetadataSurvivesConcurrentRegistrationAndOpenAPIRead(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.DB.Enabled = false
	app := Ignite(config)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll: %v", err)
	}

	const registrations = 3000
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < registrations; i++ {
			app.Beans(&expressionMetadataConcurrencyBean{name: "concurrent"})
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = app.authFairings()
		}
	}()

	wg.Wait()

	if _, err := app.GenerateOpenAPI(); err != nil {
		t.Fatalf("GenerateOpenAPI after concurrent registration: %v", err)
	}
}
