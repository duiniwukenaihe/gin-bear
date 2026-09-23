package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duiniwukenaihe/gin-bear/internal/scaffold"
	"github.com/duiniwukenaihe/gin-bear/pkg/bear"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
)

// Doctor JSON contract version. Bump only with an incompatible change and
// document the migration in docs.
const doctorSchemaVersion = 1

const (
	doctorPass = "pass"
	doctorFail = "fail"
	doctorWarn = "warn"
	doctorSkip = "skip"
)

type doctorCheck struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

type doctorProject struct {
	Root   string `json:"root"`
	Module string `json:"module,omitempty"`
}

type doctorFramework struct {
	CLI     string `json:"cli"`
	Project string `json:"project,omitempty"`
}

type doctorReport struct {
	SchemaVersion int             `json:"schema_version"`
	Status        string          `json:"status"`
	Checks        []doctorCheck   `json:"checks"`
	Project       doctorProject   `json:"project"`
	ConfigSources []string        `json:"config_sources"`
	Framework     doctorFramework `json:"framework"`
}

func doctorCommand() *cobra.Command {
	var format string
	var probe bool
	var probeTimeout time.Duration
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose project configuration without side effects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "json" && format != "text" {
				return &exitError{code: 2, err: fmt.Errorf("invalid --format %q (supported: json, text)", format)}
			}
			if probe && probeTimeout <= 0 {
				return &exitError{code: 2, err: fmt.Errorf("invalid --probe-timeout %s: must be positive", probeTimeout)}
			}
			report := runDoctor(cmd.Context(), probe, probeTimeout)
			if format == "json" {
				encoded, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				// Stdout carries exactly one document; diagnostics go to stderr.
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", encoded)
			} else {
				for _, check := range report.Checks {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", check.Status, check.ID, check.Message)
					if check.Remediation != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  fix: %s\n", check.Remediation)
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "status: %s\n", report.Status)
			}
			if report.Status == doctorFail {
				return &exitError{code: 1, err: fmt.Errorf("doctor found blocking failures")}
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "text", "output format: json or text")
	command.Flags().BoolVar(&probe, "probe", false, "dial dependencies with a bounded timeout (default is static and read-only)")
	command.Flags().DurationVar(&probeTimeout, "probe-timeout", 5*time.Second, "per-probe timeout, e.g. 5s")
	return command
}

// exitError carries a process exit code through Execute without changing the
// 0/1 contract of existing commands, which never return this type.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// doctorEnv mirrors the runtime environment selection (BEAR_ENV, then
// GIN_MODE=release) without importing unexported framework helpers.
func doctorEnv() string {
	if env := strings.ToLower(strings.TrimSpace(os.Getenv("BEAR_ENV"))); env != "" {
		return env
	}
	if strings.EqualFold(os.Getenv("GIN_MODE"), "release") {
		return "prod"
	}
	return "dev"
}

func runDoctor(ctx context.Context, probe bool, probeTimeout time.Duration) doctorReport {
	report := doctorReport{
		SchemaVersion: doctorSchemaVersion,
		Checks:        []doctorCheck{},
		Framework:     doctorFramework{CLI: bear.Version},
	}
	add := func(check doctorCheck) {
		report.Checks = append(report.Checks, check)
	}

	currentDirectory, err := os.Getwd()
	if err != nil {
		add(doctorCheck{ID: "project", Status: doctorFail, Message: fmt.Sprintf("resolve working directory: %v", err), Remediation: "run doctor inside a project directory"})
		report.Status = overallDoctorStatus(report.Checks)
		return report
	}
	root, err := nearestGoModRoot(currentDirectory)
	if err != nil {
		add(doctorCheck{ID: "project", Status: doctorFail, Message: fmt.Sprintf("no Go module found: %v", err), Remediation: "run doctor inside a Go project (bear new to scaffold one)"})
		report.Status = overallDoctorStatus(report.Checks)
		return report
	}
	report.Project.Root = root
	if module, err := goModModule(filepath.Join(root, "go.mod")); err != nil {
		add(doctorCheck{ID: "project", Status: doctorFail, Message: fmt.Sprintf("read go.mod: %v", err), Remediation: "repair go.mod (go mod tidy)"})
	} else {
		report.Project.Module = module
		add(doctorCheck{ID: "project", Status: doctorPass, Message: fmt.Sprintf("module %s at %s", module, root)})
	}

	env := doctorEnv()
	candidates := []string{"application.yaml", fmt.Sprintf("application-%s.yaml", env), "config.json"}
	var sources []string
	for _, name := range candidates {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			sources = append(sources, name)
		}
	}
	report.ConfigSources = append([]string(nil), sources...)

	var config *bear.SysConfig
	if len(sources) == 0 {
		add(doctorCheck{ID: "config", Status: doctorSkip, Message: "no configuration files; framework defaults apply"})
	} else {
		absolute := make([]string, 0, len(sources))
		for _, name := range sources {
			absolute = append(absolute, filepath.Join(root, name))
		}
		loaded, err := bear.LoadConfig(absolute...)
		if err != nil {
			remediation := "fix the reported configuration error and re-run doctor"
			if mentionsSecret(err.Error()) {
				remediation = "set BEAR_AUTH_JWT_SECRET (JWT_SECRET also works) and re-run doctor; values are never printed"
			}
			add(doctorCheck{ID: "config", Status: doctorFail, Message: fmt.Sprintf("load %s: %v", strings.Join(sources, ", "), err), Remediation: remediation})
		} else {
			config = loaded
			summary := fmt.Sprintf("loaded %s", strings.Join(sources, ", "))
			if loaded.DB != nil {
				summary += fmt.Sprintf("; database %s enabled=%v", loaded.DB.Type, loaded.DB.Enabled)
			}
			if loaded.Server != nil {
				summary += fmt.Sprintf("; server port %d", loaded.Server.Port)
			}
			add(doctorCheck{ID: "config", Status: doctorPass, Message: summary})
		}
	}

	if config == nil {
		add(doctorCheck{ID: "secrets", Status: doctorSkip, Message: "skipped without a loaded configuration"})
	} else if config.Auth != nil && config.Auth.Enabled {
		if _, hasSecret := os.LookupEnv("BEAR_AUTH_JWT_SECRET"); hasSecret {
			add(doctorCheck{ID: "secrets", Status: doctorPass, Message: "BEAR_AUTH_JWT_SECRET is set (value never printed)"})
		} else if _, hasFallback := os.LookupEnv("JWT_SECRET"); hasFallback {
			add(doctorCheck{ID: "secrets", Status: doctorPass, Message: "JWT_SECRET fallback is set (value never printed)"})
		} else {
			add(doctorCheck{ID: "secrets", Status: doctorWarn, Message: "auth is enabled but no JWT secret environment is set", Remediation: "set BEAR_AUTH_JWT_SECRET (value never printed)"})
		}
	} else {
		add(doctorCheck{ID: "secrets", Status: doctorPass, Message: "auth is disabled; no secret required"})
	}

	manifest, manifestErr := scaffold.ReadManifest(root)
	if manifestErr != nil {
		if os.IsNotExist(manifestErr) || strings.Contains(manifestErr.Error(), "no such file") {
			// ReadManifest wraps os errors; detect absence via a direct stat.
			if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(scaffold.ManifestPath))); os.IsNotExist(statErr) {
				add(doctorCheck{ID: "manifest", Status: doctorWarn, Message: "no .bear/scaffold.json; unmanaged project", Remediation: "register generated modules manually, or scaffold with bear new"})
			} else {
				add(doctorCheck{ID: "manifest", Status: doctorFail, Message: fmt.Sprintf("read manifest: %v", manifestErr), Remediation: "restore .bear/scaffold.json from version control"})
			}
		} else {
			add(doctorCheck{ID: "manifest", Status: doctorFail, Message: fmt.Sprintf("read manifest: %v", manifestErr), Remediation: "restore .bear/scaffold.json from version control"})
		}
	} else {
		report.Framework.Project = manifest.FrameworkVersion
		add(doctorCheck{ID: "manifest", Status: doctorPass, Message: fmt.Sprintf("module %s, framework %s, template v%d, %d api(s)", manifest.Module, manifest.FrameworkVersion, manifest.TemplateVersion, len(manifest.APIs))})
	}

	projectFramework := report.Framework.Project
	if projectFramework == "" {
		projectFramework = goModRequirement(root, "github.com/duiniwukenaihe/gin-bear")
		report.Framework.Project = projectFramework
	}
	switch {
	case projectFramework == "":
		add(doctorCheck{ID: "framework", Status: doctorSkip, Message: "no framework version pinned by the project"})
	case bear.Version == "dev" || bear.Version == "":
		add(doctorCheck{ID: "framework", Status: doctorPass, Message: fmt.Sprintf("development CLI against project framework %s", projectFramework)})
	case projectFramework == bear.Version:
		add(doctorCheck{ID: "framework", Status: doctorPass, Message: fmt.Sprintf("CLI and project agree on %s", bear.Version)})
	default:
		add(doctorCheck{ID: "framework", Status: doctorWarn, Message: fmt.Sprintf("CLI %s differs from project %s", bear.Version, projectFramework), Remediation: "align the CLI with the pinned framework version before generating or upgrading"})
	}

	if config == nil {
		add(doctorCheck{ID: "database", Status: doctorSkip, Message: "skipped without a loaded configuration"})
	} else if config.DB == nil || !config.DB.Enabled {
		add(doctorCheck{ID: "database", Status: doctorPass, Message: "database is disabled"})
	} else {
		switch strings.ToLower(strings.TrimSpace(config.DB.Type)) {
		case "mysql", "", "postgres", "postgresql", "sqlite", "sqlite3":
			dbType := config.DB.Type
			if dbType == "" {
				dbType = "mysql"
			}
			add(doctorCheck{ID: "database", Status: doctorPass, Message: fmt.Sprintf("database %s is enabled (static; no connection attempted)", dbType)})
		default:
			add(doctorCheck{ID: "database", Status: doctorFail, Message: fmt.Sprintf("unsupported database type %q", config.DB.Type), Remediation: "set database.type to mysql, postgres, or sqlite"})
		}
	}

	if probe {
		runDoctorProbes(ctx, config, probeTimeout, add)
	}

	report.Status = overallDoctorStatus(report.Checks)
	return report
}

func runDoctorProbes(ctx context.Context, config *bear.SysConfig, timeout time.Duration, add func(doctorCheck)) {
	if config != nil && config.DB != nil && config.DB.Enabled {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		adapter, err := bear.OpenGormAdapter(probeCtx, config.DB)
		cancel()
		if err != nil {
			add(doctorCheck{ID: "probe-database", Status: doctorFail, Message: fmt.Sprintf("dial %s database: %v", displayDBType(config.DB.Type), redactDoctorDetail(err.Error())), Remediation: "start the database, then re-run doctor --probe"})
		} else {
			_ = adapter.Shutdown()
			add(doctorCheck{ID: "probe-database", Status: doctorPass, Message: fmt.Sprintf("%s database answered within %s", displayDBType(config.DB.Type), timeout)})
		}
	} else {
		add(doctorCheck{ID: "probe-database", Status: doctorSkip, Message: "database is disabled"})
	}

	if redisProbeWanted(config) {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		adapter, err := bear.OpenRedisAdapterContext(probeCtx, config.Redis)
		cancel()
		if err != nil {
			add(doctorCheck{ID: "probe-redis", Status: doctorFail, Message: fmt.Sprintf("dial redis: %v", redactDoctorDetail(err.Error())), Remediation: "start redis, then re-run doctor --probe"})
		} else {
			_ = adapter.Shutdown()
			add(doctorCheck{ID: "probe-redis", Status: doctorPass, Message: fmt.Sprintf("redis answered within %s", timeout)})
		}
	} else {
		add(doctorCheck{ID: "probe-redis", Status: doctorSkip, Message: "redis is not explicitly configured"})
	}
}

// redisProbeWanted probes only on explicit intent: environment overrides or a
// non-default address in the file. The scaffold default points at loopback and
// must not fail a probe when nothing runs there.
func redisProbeWanted(config *bear.SysConfig) bool {
	if addr, ok := os.LookupEnv("REDIS_ADDR"); ok && strings.TrimSpace(addr) != "" {
		return true
	}
	if password, ok := os.LookupEnv("REDIS_PASSWORD"); ok && strings.TrimSpace(password) != "" {
		return true
	}
	if config == nil || config.Redis == nil {
		return false
	}
	addr := strings.TrimSpace(config.Redis.Addr)
	return addr != "" && addr != "localhost:6379" && addr != "127.0.0.1:6379"
}

func displayDBType(dbType string) string {
	dbType = strings.ToLower(strings.TrimSpace(dbType))
	if dbType == "" {
		return "mysql"
	}
	return dbType
}

func mentionsSecret(message string) bool {
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "jwt") || strings.Contains(lowered, "secret")
}

// redactDoctorDetail strips anything shaped like a credential from an error
// before it reaches stdout. A forward cursor guarantees termination: masked
// output is never rescanned.
func redactDoctorDetail(message string) string {
	redacted := message
	for _, key := range []string{"password", "passwd", "secret", "token", "dsn"} {
		var out strings.Builder
		lower := strings.ToLower(redacted)
		pos := 0
		for {
			rel := strings.Index(lower[pos:], key)
			if rel < 0 {
				out.WriteString(redacted[pos:])
				break
			}
			index := pos + rel
			out.WriteString(redacted[pos:index])
			rest := redacted[index+len(key):]
			i := 0
			for i < len(rest) && strings.ContainsRune("=: \"'", rune(rest[i])) {
				i++
			}
			j := i
			for j < len(rest) && !strings.ContainsRune(" \t\n\r\"',;", rune(rest[j])) {
				j++
			}
			out.WriteString(key + "=***")
			pos = index + len(key) + j
		}
		redacted = out.String()
	}
	return redacted
}

func overallDoctorStatus(checks []doctorCheck) string {
	status := doctorPass
	for _, check := range checks {
		switch check.Status {
		case doctorFail:
			return doctorFail
		case doctorWarn:
			status = doctorWarn
		}
	}
	return status
}

func goModModule(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	file, err := modfile.Parse(path, contents, nil)
	if err != nil {
		return "", err
	}
	if file.Module == nil {
		return "", fmt.Errorf("go.mod has no module line")
	}
	return file.Module.Mod.Path, nil
}

func goModRequirement(root, module string) string {
	contents, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	file, err := modfile.Parse(filepath.Join(root, "go.mod"), contents, nil)
	if err != nil {
		return ""
	}
	for _, requirement := range file.Require {
		if requirement.Mod.Path == module {
			return requirement.Mod.Version
		}
	}
	return ""
}
