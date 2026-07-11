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
	env := configEnvironment()
	production := isProductionEnvironment(env)
	if len(paths) == 0 {
		paths = existingDefaultConfigPaths(env)
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		strict, err := strictPolicy(data, path, config, production)
		if err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		if err := decodeConfig(data, path, config, strict); err != nil {
			return nil, err
		}
	}

	applyEnvOverrides(config)
	config.PostProcess()
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := validateProductionSecurity(config); err != nil {
		return nil, fmt.Errorf("invalid production configuration: %w", err)
	}
	return config, nil
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
	env := strings.ToLower(strings.TrimSpace(os.Getenv("BEAR_ENV")))
	if env != "" {
		return env
	}
	if strings.EqualFold(os.Getenv("GIN_MODE"), "release") {
		return "prod"
	}
	return "dev"
}

func isProductionEnvironment(env string) bool {
	return env == "prod" || env == "production"
}

func existingDefaultConfigPaths(env string) []string {
	candidates := []string{"application.yaml", fmt.Sprintf("application-%s.yaml", env), "config.json"}
	paths := make([]string, 0, len(candidates))
	for _, path := range candidates {
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
