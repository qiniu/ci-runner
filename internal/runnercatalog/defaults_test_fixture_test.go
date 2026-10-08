package runnercatalog

import "github.com/qiniu/ci-runner/internal/state"

const (
	// ManagerName identifies historical rows from the retired built-in catalog.
	ManagerName = "qiniu/ci-runner"
	// CurrentRevision is the historical revision of the retired catalog fixture.
	CurrentRevision = 1
)

// DefaultProfiles returns historical catalog fixtures for template-contract tests only.
func DefaultProfiles() []state.RunnerProfile {
	return []state.RunnerProfile{
		defaultProfile("qiniu-ubuntu-slim", "ubuntu-slim", "github-runner-ubuntu-slim"),
		defaultProfile("qiniu-ubuntu-22.04", "ubuntu-22.04", "github-runner-ubuntu-22-04"),
		defaultProfile("qiniu-ubuntu-24.04", "ubuntu-24.04", "github-runner-ubuntu-24-04"),
		defaultProfile("qiniu-ubuntu-26.04", "ubuntu-26.04", "github-runner-ubuntu-26-04"),
		defaultProfile("qiniu-ubuntu-latest", "ubuntu-latest", "github-runner-ubuntu-24-04"),
	}
}

func defaultProfile(name, osLabel, templateName string) state.RunnerProfile {
	return state.RunnerProfile{
		Name:                name,
		Labels:              []string{"self-hosted", "linux", "x64", "qiniu", osLabel},
		RequiredLabels:      []string{"qiniu", osLabel},
		DefaultTemplateName: templateName,
		MaxConcurrency:      10,
		Priority:            100,
		Enabled:             true,
		ManagedBy:           ManagerName,
		CatalogRevision:     CurrentRevision,
	}
}
