package runnercatalog

import "github.com/qiniu/ci-runner/internal/state"

const (
	// ManagerName identifies runner specs owned by the built-in catalog.
	ManagerName = "qiniu/ci-runner"
	// CurrentRevision is the schema revision of the built-in catalog rows.
	CurrentRevision = 1
)

// DefaultProfiles returns the managed runner specs reconciled at startup.
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
