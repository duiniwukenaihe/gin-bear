//go:build !darwin && !linux && !windows

package atomicdir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishIsUnsupportedWithoutAtomicNoReplacePrimitive(t *testing.T) {
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

	err := Publish(staged, destination)
	if !errors.Is(err, errAtomicNoReplaceUnsupported) {
		t.Fatalf("Publish error = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported publication touched destination: %v", statErr)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "generated" {
		t.Fatalf("unsupported publication changed staged directory: contents=%q err=%v", contents, readErr)
	}
}
