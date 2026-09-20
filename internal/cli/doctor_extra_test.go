package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/spf13/cobra"
)

func TestMentionsSecretAndRedaction(t *testing.T) {
	if !mentionsSecret("weak jwt secret is not allowed") {
		t.Fatal("jwt failure not recognized")
	}
	if !mentionsSecret("set BEAR_AUTH_JWT_SECRET now") {
		t.Fatal("secret failure not recognized")
	}
	if mentionsSecret("connection refused") {
		t.Fatal("plain failure flagged as secret")
	}
	redacted := redactDoctorDetail(`dial postgres password=hunter2 token=abc dsn=postgres://u:p@h/db`)
	for _, leaked := range []string{"hunter2", "abc", "u:p@h"} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("redaction leaked %q: %q", leaked, redacted)
		}
	}
}

func TestDoctorEnvSelection(t *testing.T) {
	t.Setenv("BEAR_ENV", "staging")
	t.Setenv("GIN_MODE", "")
	if got := doctorEnv(); got != "staging" {
		t.Fatalf("env = %q", got)
	}
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "release")
	if got := doctorEnv(); got != "prod" {
		t.Fatalf("gin release env = %q", got)
	}
}

func TestDoctorProbeSkipsDisabledDatabase(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\ndatabase:\n  enabled: false\n",
	})
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := execDoctor(t, dir, "--format", "json", "--probe")
	if code != 0 {
		t.Fatalf("probe exit = %d\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	for _, check := range report.Checks {
		if check.ID == "probe-database" && check.Status != "skip" {
			t.Fatalf("probe-database = %q, want skip", check.Status)
		}
		if check.ID == "probe-redis" && check.Status != "skip" {
			t.Fatalf("probe-redis = %q, want skip", check.Status)
		}
	}
}

func TestDoctorProbeHitsSqlite(t *testing.T) {
	dir := makeDoctorProject(t, map[string]string{
		"application.yaml": "server:\n  port: 8081\n  name: doctorcheck\ndatabase:\n  enabled: true\n  type: \"sqlite\"\n  dsn: \"probe.db\"\n",
	})
	t.Setenv("BEAR_ENV", "")
	t.Setenv("GIN_MODE", "")
	stdout, _, code := execDoctor(t, dir, "--format", "json", "--probe", "--probe-timeout", "5s")
	if code != 0 {
		t.Fatalf("probe exit = %d\n%s", code, stdout)
	}
	report := decodeDoctorJSON(t, stdout)
	for _, check := range report.Checks {
		if check.ID == "probe-database" && check.Status != "pass" {
			t.Fatalf("probe-database = %q (%s), want pass", check.Status, check.Message)
		}
	}
}

func TestRedisProbeWantedOnlyOnIntent(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("REDIS_PASSWORD", "")
	if redisProbeWanted(nil) {
		t.Fatal("nil config wanted a probe")
	}
	if redisProbeWanted(&bear.SysConfig{Redis: &bear.RedisConfig{Addr: "localhost:6379"}}) {
		t.Fatal("default loopback wanted a probe")
	}
	if !redisProbeWanted(&bear.SysConfig{Redis: &bear.RedisConfig{Addr: "redis.internal:6379"}}) {
		t.Fatal("explicit redis refused a probe")
	}
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	if !redisProbeWanted(&bear.SysConfig{Redis: &bear.RedisConfig{Addr: "localhost:6379"}}) {
		t.Fatal("REDIS_ADDR override refused a probe")
	}
}

func TestGoModHelpersRejectBadInput(t *testing.T) {
	dir := t.TempDir()
	if _, err := goModModule(filepath.Join(dir, "go.mod")); err == nil {
		t.Fatal("missing go.mod accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("not a module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := goModModule(filepath.Join(dir, "go.mod")); err == nil {
		t.Fatal("broken go.mod accepted")
	}
	if got := goModRequirement(dir, "example.com/none"); got != "" {
		t.Fatalf("missing requirement = %q", got)
	}
}

func TestDisplayDBTypeDefaults(t *testing.T) {
	if displayDBType("") != "mysql" || displayDBType("POSTGRES") != "postgres" {
		t.Fatal("display type wrong")
	}
}

func TestAgentEntryDiffGuidance(t *testing.T) {
	dir := t.TempDir()
	generated := renderAgentEntry("example.com/m")
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	command := &cobra.Command{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	suggestAgentEntryDiff(command, filepath.Join(dir, "AGENTS.md"), generated)
	if !strings.Contains(stdout.String(), "matches the generated entry") {
		t.Fatalf("match guidance missing: %q", stdout.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	suggestAgentEntryDiff(command, filepath.Join(dir, "AGENTS.md"), generated)
	if !strings.Contains(stdout.String(), "differs from the generated entry") {
		t.Fatalf("drift guidance missing: %q", stdout.String())
	}
}

func TestExecGitReportsExitCodes(t *testing.T) {
	dir := t.TempDir()
	if code := execGit(dir, "definitely-not-a-git-subcommand-xyz"); code == 0 {
		t.Fatal("bogus git subcommand exited 0")
	}
}
