//go:build darwin || linux

package atomicdir

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPublishFailsSafelyWhenNativeNoReplaceIsUnsupported(t *testing.T) {
	parent := t.TempDir()
	staged := filepath.Join(parent, "staged")
	destination := filepath.Join(parent, "destination")
	if err := os.Mkdir(staged, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(staged, "generated.txt")
	if err := os.WriteFile(marker, []byte("generated"), 0644); err != nil {
		t.Fatal(err)
	}

	original := nativeRenameNoReplace
	nativeRenameNoReplace = func(_, _ string) error { return syscall.ENOTSUP }
	t.Cleanup(func() { nativeRenameNoReplace = original })

	err := Publish(staged, destination)
	if !errors.Is(err, syscall.ENOTSUP) {
		t.Fatalf("Publish error = %v, want ENOTSUP", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported native publication touched destination: %v", statErr)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "generated" {
		t.Fatalf("unsupported native publication changed staged directory: contents=%q err=%v", contents, readErr)
	}
}
