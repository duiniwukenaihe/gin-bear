package bear

import "testing"

// TestEnterPluginModeRestoresPreviousValue locks the contract
// registerModuleInRegistration depends on: the flag returns to whatever it was,
// so a nested or back-to-back plugin registration cannot clear a mode that an
// outer registration still holds.
func TestEnterPluginModeRestoresPreviousValue(t *testing.T) {
	app := &Bear{}
	if app.inPluginMode() {
		t.Fatal("a fresh Bear starts in plugin mode")
	}

	outer := app.enterPluginMode()
	if !app.inPluginMode() {
		t.Fatal("enterPluginMode did not mark plugin mode")
	}

	inner := app.enterPluginMode()
	inner()
	if !app.inPluginMode() {
		t.Fatal("restoring the inner registration cleared the outer plugin mode")
	}

	outer()
	if app.inPluginMode() {
		t.Fatal("restoring the outer registration left plugin mode set")
	}
}

// TestEnterPluginModeOnNilReceiverIsANoOp covers the nil guard. A nil *Bear must
// not panic on either call, and the returned restore function stays callable.
func TestEnterPluginModeOnNilReceiverIsANoOp(t *testing.T) {
	var app *Bear
	restore := app.enterPluginMode()
	if restore == nil {
		t.Fatal("enterPluginMode returned a nil restore function")
	}
	restore()
}
