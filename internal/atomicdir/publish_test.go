package atomicdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsupportedPublishFailsBeforeTouchingEitherDirectory(t *testing.T) {
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

	err := unsupportedRenameNoReplace(staged, destination)
	if !errors.Is(err, errAtomicNoReplaceUnsupported) {
		t.Fatalf("unsupported publication error = %v", err)
	}
	if !strings.Contains(err.Error(), "atomic no-replace directory publication is unsupported") {
		t.Fatalf("unsupported publication error is unclear: %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported publication touched destination: %v", statErr)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "generated" {
		t.Fatalf("unsupported publication changed staged directory: contents=%q err=%v", contents, readErr)
	}
}

func TestPublishDoesNotReplaceDestinationCreatedBeforePublication(t *testing.T) {
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

	created := make(chan error, 1)
	go func() {
		created <- os.Mkdir(destination, 0755)
	}()
	if err := <-created; err != nil {
		t.Fatal(err)
	}

	if err := Publish(staged, destination); err == nil {
		t.Fatal("publication replaced a concurrently-created destination")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("concurrently-created destination changed: %v", entries)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "generated" {
		t.Fatalf("staged directory changed: contents=%q err=%v", contents, err)
	}
}

func TestPublishMovesStagedDirectory(t *testing.T) {
	parent := t.TempDir()
	staged := filepath.Join(parent, "staged")
	destination := filepath.Join(parent, "destination")
	if err := os.Mkdir(staged, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "generated.txt"), []byte("generated"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Publish(staged, destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "generated.txt"))
	if err != nil || string(contents) != "generated" {
		t.Fatalf("published contents=%q err=%v", contents, err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged directory still exists: %v", err)
	}
}
