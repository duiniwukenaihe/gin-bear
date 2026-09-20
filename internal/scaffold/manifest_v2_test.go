package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifestFile(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".bear"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bear", "scaffold.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadManifestAcceptsV1AndV2 pins explicit version reading: v1 keeps
// working, v2 carries file digests, anything else is rejected clearly.
func TestReadManifestAcceptsV1AndV2(t *testing.T) {
	dir := t.TempDir()
	writeManifestFile(t, dir, `{"module":"example.com/m","framework_version":"v0.0.0","template_version":1,"apis":[]}`)
	if _, err := ReadManifest(dir); err != nil {
		t.Fatalf("read v1: %v", err)
	}

	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	dir2 := t.TempDir()
	writeManifestFile(t, dir2, `{"module":"example.com/m","framework_version":"v0.0.0","template_version":2,"apis":[{"name":"Invoice","package":"invoice","path":"internal/invoice","module_type":"invoice.Module","files":{"internal/invoice/model.go":"`+digest+`"}}]}`)
	manifest, err := ReadManifest(dir2)
	if err != nil {
		t.Fatalf("read v2: %v", err)
	}
	if manifest.APIs[0].Files["internal/invoice/model.go"] != digest {
		t.Fatalf("v2 digests lost: %+v", manifest.APIs[0].Files)
	}

	for _, version := range []string{"0", "3", "99"} {
		dirN := t.TempDir()
		writeManifestFile(t, dirN, `{"module":"example.com/m","framework_version":"v0.0.0","template_version":`+version+`,"apis":[]}`)
		if _, err := ReadManifest(dirN); err == nil || !strings.Contains(err.Error(), "unsupported template version") {
			t.Fatalf("version %s error = %v, want unsupported-version rejection", version, err)
		}
	}
}

// TestReadManifestRejectsBadDigests keeps v2 digests machine-checkable.
func TestReadManifestRejectsBadDigests(t *testing.T) {
	dir := t.TempDir()
	writeManifestFile(t, dir, `{"module":"example.com/m","framework_version":"v0.0.0","template_version":2,"apis":[{"name":"Invoice","package":"invoice","path":"internal/invoice","module_type":"invoice.Module","files":{"../escape.go":"zzzz"}}]}`)
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("bad v2 digests accepted")
	}
}
