package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func generationLockPath(t *testing.T, project string) string {
	t.Helper()
	path := filepath.Join(project, filepath.FromSlash(generateLockPath))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPrepareManagedGenerationReportsHeldLock covers the recovery path for a
// lock left behind by a crashed run. Reporting "file exists" leaves the operator
// with no way forward, so the failure has to name the lock, quote the recorded
// owner, and print the command that clears it.
func TestPrepareManagedGenerationReportsHeldLock(t *testing.T) {
	project := newManagedGenerationProject(t)
	lockPath := generationLockPath(t, project)
	if err := os.WriteFile(lockPath, []byte("pid=424242 started=2026-01-02T03:04:05Z\n"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := prepareManagedGeneration(project, "api", "invoice")
	if err == nil {
		t.Fatal("prepareManagedGeneration took a lock that was already held")
	}
	message := err.Error()
	for _, want := range []string{lockPath, "pid=424242", "delete the lock and retry", "rm "} {
		if !strings.Contains(message, want) {
			t.Fatalf("held-lock failure %q does not contain %q", message, want)
		}
	}
}

// TestPrepareManagedGenerationDescribesLockWithoutOwner keeps the message usable
// for a lock that carries no owner record, which is what an interrupted write or
// a lock from an older CLI leaves behind.
func TestPrepareManagedGenerationDescribesLockWithoutOwner(t *testing.T) {
	project := newManagedGenerationProject(t)
	lockPath := generationLockPath(t, project)
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := prepareManagedGeneration(project, "api", "invoice")
	if err == nil {
		t.Fatal("prepareManagedGeneration took a lock with no owner record")
	}
	if !strings.Contains(err.Error(), "owner record empty") {
		t.Fatalf("ownerless-lock failure is not actionable: %v", err)
	}
}

// TestPrepareManagedGenerationRecordsAndReleasesLock checks that a successful
// acquisition is described on disk and that releasing it lets the next
// generation through, so the lock cannot leak on the happy path.
func TestPrepareManagedGenerationRecordsAndReleasesLock(t *testing.T) {
	project := newManagedGenerationProject(t)

	first, err := prepareManagedGeneration(project, "api", "invoice")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(project, filepath.FromSlash(generateLockPath))
	contents, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("generation lock was not published: %v", err)
	}
	if !strings.Contains(string(contents), "pid=") || !strings.Contains(string(contents), "started=") {
		t.Fatalf("generation lock does not record its owner: %q", contents)
	}
	if _, err := prepareManagedGeneration(project, "api", "order"); err == nil {
		t.Fatal("a second generation acquired the lock while the first still held it")
	}

	first.release()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("generation lock survived release: %v", err)
	}
	second, err := prepareManagedGeneration(project, "api", "order")
	if err != nil {
		t.Fatalf("generation could not proceed after release: %v", err)
	}
	second.release()
}
