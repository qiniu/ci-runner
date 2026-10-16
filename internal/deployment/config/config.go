// Package deployconfig parses the repository-owned Deploy configuration.
package deployconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	deploy "github.com/miclle/deployd"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultPath is the repository-relative path used by the first Deploy flow.
	DefaultPath = ".qiniu/deploy.yaml"
	// MaxBytes bounds the untrusted repository configuration read before parsing.
	MaxBytes      = 64 << 10
	configVersion = 1
)

var (
	// ErrInvalidConfig identifies a malformed or unsupported Deploy configuration.
	ErrInvalidConfig = errors.New("invalid deploy configuration")
	// ErrConfigTooLarge identifies a configuration that exceeds MaxBytes.
	ErrConfigTooLarge = errors.New("deploy configuration is too large")
)

// Config is the stable repository configuration for one HTTP service.
// Commands are trusted repository code and must never be copied into lifecycle
// events or API error responses.
type Config struct {
	Version          int               `yaml:"version"`
	WorkingDirectory string            `yaml:"working_directory"`
	InstallCommand   string            `yaml:"install_command"`
	StartCommand     string            `yaml:"start_command"`
	Port             int               `yaml:"port"`
	Healthcheck      HealthcheckConfig `yaml:"healthcheck"`
}

// HealthcheckConfig describes the origin-relative readiness probe.
type HealthcheckConfig struct {
	Path           string `yaml:"path"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// Parse decodes one strict YAML document and validates its execution parameters.
// The input is bounded before decoding and may not contain a second document.
func Parse(data []byte) (deploy.Spec, error) {
	if len(data) > MaxBytes {
		return deploy.Spec{}, ErrConfigTooLarge
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return deploy.Spec{}, fmt.Errorf("%w: document is empty", ErrInvalidConfig)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return deploy.Spec{}, fmt.Errorf("%w: %v", ErrInvalidConfig, sanitizeYAMLError(err))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return deploy.Spec{}, fmt.Errorf("%w: multiple documents are not supported", ErrInvalidConfig)
		}
		return deploy.Spec{}, fmt.Errorf("%w: %v", ErrInvalidConfig, sanitizeYAMLError(err))
	}
	// Existing repository configurations use camelCase. Normalize only known
	// aliases before strict decoding; both spellings become duplicate keys and
	// are rejected even if their values happen to match.
	if len(document.Content) == 1 && document.Content[0].Kind == yaml.MappingNode {
		root := document.Content[0]
		aliases := map[string]string{"workingDirectory": "working_directory", "installCommand": "install_command", "startCommand": "start_command"}
		for i := 0; i < len(root.Content); i += 2 {
			key, value := root.Content[i], root.Content[i+1]
			if alias, ok := aliases[key.Value]; ok {
				key.Value = alias
			}
			if key.Value == "healthcheck" && value.Kind == yaml.MappingNode {
				for j := 0; j < len(value.Content); j += 2 {
					if value.Content[j].Value == "timeoutSeconds" {
						value.Content[j].Value = "timeout_seconds"
					}
				}
			}
		}
	}
	normalized, err := yaml.Marshal(&document)
	if err != nil {
		return deploy.Spec{}, ErrInvalidConfig
	}
	strict := yaml.NewDecoder(bytes.NewReader(normalized))
	strict.KnownFields(true)
	var config Config
	if err := strict.Decode(&config); err != nil {
		return deploy.Spec{}, fmt.Errorf("%w: %v", ErrInvalidConfig, sanitizeYAMLError(err))
	}
	if config.Version != configVersion {
		return deploy.Spec{}, fmt.Errorf("%w: version must be %d", ErrInvalidConfig, configVersion)
	}

	spec, err := deploy.NormalizeSpec(deploy.Spec{
		WorkingDirectory: config.WorkingDirectory,
		InstallCommand:   config.InstallCommand,
		StartCommand:     config.StartCommand,
		Port:             config.Port,
		Healthcheck: deploy.Healthcheck{
			Path:           config.Healthcheck.Path,
			TimeoutSeconds: config.Healthcheck.TimeoutSeconds,
		},
	})
	if err != nil {
		return deploy.Spec{}, fmt.Errorf("%w: execution parameters are invalid", ErrInvalidConfig)
	}
	return spec, nil
}

func sanitizeYAMLError(err error) error {
	// yaml.v3 errors contain locations and field names, but not decoded command
	// values. Keep the useful parser context while avoiding raw repository data.
	if strings.TrimSpace(err.Error()) == "" {
		return ErrInvalidConfig
	}
	return err
}
