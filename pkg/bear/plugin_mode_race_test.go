package bear

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// pluginModeRaceModule is a bean-less module: its only observable effect is the
// pluginMode flag flip that registerModuleInRegistration performs around Build.
type pluginModeRaceModule struct{}

func (*pluginModeRaceModule) Name() string  { return "plugin-mode-race" }
func (*pluginModeRaceModule) Beans() []Bean { return nil }
func (*pluginModeRaceModule) Build(*Bear)   {}

// TestPluginModeConcurrentRegistrationRace reproduces the data race between
// PluginManager.registerModule writing b.pluginMode and route registration
// reading it in registerCompiledHandler. The two sides hold different locks
// (pluginBarrier.mu vs eRegistrationMu), so no happens-before edge exists.
func TestPluginModeConcurrentRegistrationRace(t *testing.T) {
	resetGinModeForTest(t)
	app := Ignite(NewSysConfig())

	var workers sync.WaitGroup
	workers.Add(2)

	go func() {
		defer workers.Done()
		for i := 0; i < 300; i++ {
			if err := app.pluginManager.registerModule(&pluginModeRaceModule{}); err != nil {
				t.Errorf("registerModule() error = %v", err)
				return
			}
		}
	}()

	go func() {
		defer workers.Done()
		for i := 0; i < 300; i++ {
			path := fmt.Sprintf("/plugin-mode-race/%d", i)
			if err := app.HandleE(http.MethodGet, path, func() string { return "ok" }); err != nil {
				t.Errorf("HandleE() error = %v", err)
				return
			}
		}
	}()

	workers.Wait()
}
