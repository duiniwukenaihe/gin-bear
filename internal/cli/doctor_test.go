package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoctorFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeDoctorProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/doctorcheck\n\ngo 1.25.14\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, contents := range files {
		writeDoctorFile(t, dir, name, contents)
	}
	return dir
}

func execDoctor(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute(append([]string{"doctor"}, args...), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func decodeDoctorJSON(t *testing.T, stdout string) doctorReport {
	t.Helper()
	var report doctorReport
	decoder := json.NewDecoder(strings.NewReader(stdout))
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("decode doctor JSON: %v\n%s", err, stdout)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		t.Fatalf("doctor stdout holds more than one document:\n%s", stdout)
	}
	return report
}

func TestDoctorJSONContractOnHealthyProject(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\ndatabase:\n  enabled: true\n  type: \"sqlite\"\n  dsn: \"doctor.db\"\n",
	})
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := execDoctor(t, dir, "--format", "json")
	if code != 0 {
		t.Fatalf("doctor exit = %d, want 0\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	if report.SchemaVersion != doctorSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", report.SchemaVersion, doctorSchemaVersion)
	}
	// A hand-made project without a manifest warns but does not block.
	if report.Status != "warn" {
		t.Fatalf("status = %q, want warn: %+v", report.Status, report.Checks)
	}
	ids := map[string]string{}
	for _, check := range report.Checks {
		ids[check.ID] = check.Status
		switch check.Status {
		case "pass", "fail", "warn", "skip":
		default:
			t.Fatalf("check %q has unknown status %q", check.ID, check.Status)
		}
	}
	for _, want := range []string{"project", "config", "secrets", "manifest", "framework", "database"} {
		if _, ok := ids[want]; !ok {
			t.Fatalf("checks missing %q: %v", want, ids)
		}
	}
	if ids["manifest"] != "warn" {
		t.Fatalf("manifest = %q, want warn for an unmanaged project", ids["manifest"])
	}
}

func TestDoctorRejectsBrokenManifest(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\n",
	})
	if err := os.MkdirAll(filepath.Join(dir, ".bear"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDoctorFile(t, filepath.Join(dir, ".bear"), "scaffold.json", "{broken\n")
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := execDoctor(t, dir, "--format", "json")
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	if report.Status != "fail" {
		t.Fatalf("status = %q, want fail", report.Status)
	}
	for _, check := range report.Checks {
		if check.ID == "manifest" && check.Status != "fail" {
			t.Fatalf("manifest = %q, want fail", check.Status)
		}
	}
}

func TestDoctorFailsWithoutProject(t *testing.T) {
	dir := t.TempDir()
	stdout, _, code := execDoctor(t, dir, "--format", "json")
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	if report.Status != "fail" {
		t.Fatalf("status = %q, want fail", report.Status)
	}
}

func TestDoctorRejectsBadFormat(t *testing.T) {
	dir := makeDoctorProject(t, nil)
	_, _, code := execDoctor(t, dir, "--format", "yaml")
	if code != 2 {
		t.Fatalf("doctor exit = %d, want 2", code)
	}
}

func TestDoctorNeverPrintsSecrets(t *testing.T) {
	const secret = "doctor-secret-value-abc123"
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\ndatabase:\n  enabled: true\n  type: \"postgres\"\n  host: \"db.internal\"\n  user: \"app\"\n  password: \"" + secret + "\"\n  dbname: \"app\"\nauth:\n  enabled: true\n  jwt_secret: \"" + secret + "\"\n",
	})
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")
	t.Setenv("BEAR_AUTH_JWT_SECRET", secret)
	stdout, stderr, _ := execDoctor(t, dir, "--format", "json")
	combined := stdout + stderr
	if strings.Contains(combined, secret) {
		t.Fatalf("doctor printed a secret value:\n%s", combined)
	}
	if strings.Contains(stdout, "postgres://") {
		t.Fatalf("doctor printed a DSN:\n%s", stdout)
	}
}

func TestDoctorStaticModeListsNoProbes(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\ndatabase:\n  enabled: true\n  type: \"sqlite\"\n  dsn: \"doctor.db\"\n",
	})
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := execDoctor(t, dir, "--format", "json")
	if code != 0 {
		t.Fatalf("doctor exit = %d\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	for _, check := range report.Checks {
		if strings.HasPrefix(check.ID, "probe-") {
			t.Fatalf("static mode ran %q", check.ID)
		}
	}
}

func TestDoctorProbeRefusesUnreachableDatabase(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\ndatabase:\n  enabled: true\n  type: \"postgres\"\n  host: \"127.0.0.1\"\n  port: \"1\"\n  user: \"nobody\"\n  dbname: \"nothing\"\n",
	})
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := execDoctor(t, dir, "--format", "json", "--probe", "--probe-timeout", "2s")
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	found := false
	for _, check := range report.Checks {
		if check.ID == "probe-database" {
			found = true
			if check.Status != "fail" {
				t.Fatalf("probe-database = %q, want fail", check.Status)
			}
			if check.Remediation == "" {
				t.Fatal("probe-database has no remediation")
			}
		}
	}
	if !found {
		t.Fatal("probe-database check missing")
	}
}

func TestDoctorWritesNoFiles(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\n",
	})
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	before := snapshotDoctorTree(t, dir)
	_, _, _ = execDoctor(t, dir, "--format", "json")
	_, _, _ = execDoctor(t, dir, "--format", "text")
	_, _, _ = execDoctor(t, dir, "--format", "json", "--probe")
	after := snapshotDoctorTree(t, dir)
	if len(before) != len(after) {
		t.Fatalf("doctor wrote files: before=%v after=%v", keys(before), keys(after))
	}
	for name := range before {
		if before[name] != after[name] {
			t.Fatalf("doctor modified %s", name)
		}
	}
}

func snapshotDoctorTree(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func keys(values map[string]int64) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	return names
}

func TestDoctorPassesManagedProject(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\ndatabase:\n  enabled: false\n",
	})
	if err := os.MkdirAll(filepath.Join(dir, ".bear"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDoctorFile(t, filepath.Join(dir, ".bear"), "scaffold.json", `{
  "module": "example.com/doctorcheck",
  "framework_version": "v0.0.0",
  "template_version": 1,
  "apis": []
}
`)
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := execDoctor(t, dir, "--format", "json")
	if code != 0 {
		t.Fatalf("doctor exit = %d, want 0\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	if report.Status != "pass" {
		t.Fatalf("status = %q, want pass: %+v", report.Status, report.Checks)
	}
}
