package bear

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadConfig loads configuration files in order and returns all parse and
// validation failures to the caller.
func LoadConfig(paths ...string) (*SysConfig, error) {
	config := NewSysConfig()
	rawEnvironment := os.Getenv("BEAR_ENV")
	env := configEnvironment()
	production := isProductionEnvironment(env)
	if len(paths) == 0 {
		paths = existingDefaultConfigPaths(configFilenameEnvironment(rawEnvironment, env), rawEnvironment)
	}

	if err := decodeConfigFiles(config, paths, production); err != nil {
		return nil, err
	}

	if err := applyEnvOverrides(config); err != nil {
		return nil, fmt.Errorf("invalid environment override: %w", err)
	}
	config.PostProcess()
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := validateProductionSecurity(config); err != nil {
		return nil, fmt.Errorf("invalid production configuration: %w", err)
	}
	return config, nil
}

// LoadDatabaseConfigForGeneration selects only the database contract for code
// generation. It follows the same file discovery, strict decoding, environment
// override and post-processing rules as LoadConfig, but it never requires
// unrelated runtime secrets and never validates production startup policy.
//
// dir is the project root used to resolve default files and relative explicit
// paths. Explicit paths replace the default file chain and are resolved
// against dir unless absolute. With no explicit paths and no configuration
// files it returns nil, nil to preserve generation for config-free projects.
// The returned snapshot must not be used to start the application.
func LoadDatabaseConfigForGeneration(dir string, paths ...string) (*DBConfig, error) {
	if dir == "" {
		dir = "."
	}
	env := configEnvironment()
	production := isProductionEnvironment(env)
	selected, err := GenerationConfigPaths(dir, paths...)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, nil
	}
	config := NewSysConfig()
	if err := decodeConfigFiles(config, selected, production); err != nil {
		return nil, err
	}
	if err := applyEnvOverrides(config); err != nil {
		return nil, fmt.Errorf("invalid environment override: %w", err)
	}
	config.PostProcess()
	if config.DB == nil {
		disabled := &DBConfig{Enabled: false, Type: "mysql"}
		return disabled, nil
	}
	snapshot := *config.DB
	return &snapshot, nil
}

// GenerationConfigPaths reports which files generation selection would read:
// explicit paths (absolute as-is, relative against dir) replace the default
// chain; otherwise the dir-rooted default chain. It reads nothing.
func GenerationConfigPaths(dir string, paths ...string) ([]string, error) {
	if dir == "" {
		dir = "."
	}
	if len(paths) > 0 {
		selected := make([]string, 0, len(paths))
		for _, path := range paths {
			if path == "" {
				return nil, fmt.Errorf("generation config path is empty")
			}
			if filepath.IsAbs(path) {
				selected = append(selected, path)
			} else {
				selected = append(selected, filepath.Join(dir, path))
			}
		}
		return selected, nil
	}
	rawEnvironment := os.Getenv("BEAR_ENV")
	env := configEnvironment()
	return discoverDefaultConfigPaths(dir, configFilenameEnvironment(rawEnvironment, env), rawEnvironment), nil
}

// decodeConfigFiles applies ordered strict decoding shared by runtime loading
// and generation selection. It connects to nothing and changes no process
// state.
func decodeConfigFiles(config *SysConfig, paths []string, production bool) error {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read config %s: %w", path, err)
		}
		strict, err := strictPolicy(data, path, config, production)
		if err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
		if err := decodeConfig(data, path, config, strict); err != nil {
			return err
		}
	}
	return nil
}

// InitConfig preserves the v0 panic-on-error signature.
func InitConfig() *SysConfig {
	config, err := LoadConfig()
	if err != nil {
		panic(fmt.Sprintf("Invalid configuration: %v", err))
	}
	return config
}

func configEnvironment() string {
	env := normalizeEnvironment(os.Getenv("BEAR_ENV"))
	if env != "" {
		return env
	}
	if strings.EqualFold(os.Getenv("GIN_MODE"), "release") {
		return "prod"
	}
	return "dev"
}

func isProductionEnvironment(env string) bool {
	switch normalizeEnvironment(env) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

func normalizeEnvironment(env string) string {
	return strings.ToLower(strings.TrimSpace(env))
}

func configFilenameEnvironment(rawEnvironment, normalizedEnvironment string) string {
	if normalized := normalizeEnvironment(normalizedEnvironment); normalized != "" {
		return normalized
	}
	return normalizeEnvironment(rawEnvironment)
}

func existingDefaultConfigPaths(env string, compatibilityEnvironments ...string) []string {
	return discoverDefaultConfigPaths(".", env, compatibilityEnvironments...)
}

// discoverDefaultConfigPaths builds the default file chain rooted at dir,
// mirroring the runtime selection order: base application.yaml, the
// BEAR_ENV/GIN_MODE overlay with the existing raw-environment compatibility
// rule, then config.json. Only existing files are returned. It changes no
// process state and never changes the working directory.
func discoverDefaultConfigPaths(dir, env string, compatibilityEnvironments ...string) []string {
	if dir == "" {
		dir = "."
	}
	join := func(name string) string {
		if filepath.IsAbs(name) {
			return name
		}
		return filepath.Join(dir, name)
	}
	overlayName := fmt.Sprintf("application-%s.yaml", env)
	candidates := []string{"application.yaml", overlayName}
	if _, err := os.Stat(join(overlayName)); err != nil {
		for _, compatibilityEnvironment := range compatibilityEnvironments {
			compatibilityEnvironment = strings.TrimSpace(compatibilityEnvironment)
			if compatibilityEnvironment == "" || compatibilityEnvironment == env {
				continue
			}
			compatibilityName := fmt.Sprintf("application-%s.yaml", compatibilityEnvironment)
			if _, err := os.Stat(join(compatibilityName)); err == nil {
				candidates = append(candidates, compatibilityName)
				break
			}
		}
	}
	candidates = append(candidates, "config.json")
	paths := make([]string, 0, len(candidates))
	for _, name := range candidates {
		path := join(name)
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
		}
	}
	return paths
}

func strictPolicy(data []byte, path string, current *SysConfig, production bool) (bool, error) {
	var raw map[string]any
	if err := decodeConfig(data, path, &raw, false); err != nil {
		return false, err
	}
	strict := true
	if current != nil {
		if configured, ok := configStrictValue(current.Config); ok {
			strict = configured
		}
	}
	if section, ok := raw["config"].(map[string]any); ok {
		if configured, ok := configStrictValue(section); ok {
			strict = configured
		}
	}
	mode := ""
	if current != nil && current.Server != nil {
		mode = current.Server.Mode
	}
	if server, ok := raw["server"].(map[string]any); ok {
		if configuredMode, ok := server["mode"].(string); ok {
			mode = configuredMode
		}
	}
	if !production {
		if mode != "" {
			production = strings.EqualFold(mode, "release")
		} else {
			production = strings.EqualFold(os.Getenv("GIN_MODE"), "release")
		}
	}
	if production && !strict {
		return false, fmt.Errorf("config.strict cannot be false in production")
	}
	return production || strict, nil
}

func configStrictValue(config map[string]any) (bool, bool) {
	if config == nil {
		return false, false
	}
	strict, ok := config["strict"].(bool)
	return strict, ok
}

func decodeConfig(data []byte, path string, target any, strict bool) error {
	ext := strings.ToLower(filepath.Ext(path))
	trimmed := bytes.TrimSpace(data)
	isJSON := ext == ".json" || (ext != ".yaml" && ext != ".yml" && bytes.HasPrefix(trimmed, []byte("{")))
	if isJSON {
		decoder := json.NewDecoder(bytes.NewReader(data))
		if strict {
			decoder.DisallowUnknownFields()
		}
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("failed to parse JSON config (%s): %w", path, err)
		}
		if err := requireDecoderEOF(decoder.Decode); err != nil {
			return fmt.Errorf("failed to parse JSON config (%s): %w", path, err)
		}
		return nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(strict)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("failed to parse YAML config (%s): %w", path, err)
	}
	if err := requireDecoderEOF(decoder.Decode); err != nil {
		return fmt.Errorf("failed to parse YAML config (%s): %w", path, err)
	}
	return nil
}

func requireDecoderEOF(decode func(any) error) error {
	var extra any
	err := decode(&extra)
	if err == nil {
		return fmt.Errorf("configuration must contain exactly one document")
	}
	if err != io.EOF {
		return err
	}
	return nil
}
