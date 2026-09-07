package runnercatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestDefaultProfilesMatchTargetCatalog(t *testing.T) {
	want := []state.RunnerProfile{
		{
			Name:                "qiniu-ubuntu-slim",
			Labels:              []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-slim"},
			RequiredLabels:      []string{"qiniu", "ubuntu-slim"},
			DefaultTemplateName: "github-runner-ubuntu-slim",
			MaxConcurrency:      10,
			Priority:            100,
			Enabled:             true,
			ManagedBy:           "qiniu/ci-runner",
			CatalogRevision:     1,
		},
		{
			Name:                "qiniu-ubuntu-22.04",
			Labels:              []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-22.04"},
			RequiredLabels:      []string{"qiniu", "ubuntu-22.04"},
			DefaultTemplateName: "github-runner-ubuntu-22-04",
			MaxConcurrency:      10,
			Priority:            100,
			Enabled:             true,
			ManagedBy:           "qiniu/ci-runner",
			CatalogRevision:     1,
		},
		{
			Name:                "qiniu-ubuntu-24.04",
			Labels:              []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-24.04"},
			RequiredLabels:      []string{"qiniu", "ubuntu-24.04"},
			DefaultTemplateName: "github-runner-ubuntu-24-04",
			MaxConcurrency:      10,
			Priority:            100,
			Enabled:             true,
			ManagedBy:           "qiniu/ci-runner",
			CatalogRevision:     1,
		},
		{
			Name:                "qiniu-ubuntu-26.04",
			Labels:              []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-26.04"},
			RequiredLabels:      []string{"qiniu", "ubuntu-26.04"},
			DefaultTemplateName: "github-runner-ubuntu-26-04",
			MaxConcurrency:      10,
			Priority:            100,
			Enabled:             true,
			ManagedBy:           "qiniu/ci-runner",
			CatalogRevision:     1,
		},
		{
			Name:                "qiniu-ubuntu-latest",
			Labels:              []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-latest"},
			RequiredLabels:      []string{"qiniu", "ubuntu-latest"},
			DefaultTemplateName: "github-runner-ubuntu-24-04",
			MaxConcurrency:      10,
			Priority:            100,
			Enabled:             true,
			ManagedBy:           "qiniu/ci-runner",
			CatalogRevision:     1,
		},
	}

	got := DefaultProfiles()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultProfiles() = %#v, want %#v", got, want)
	}
}

func TestDefaultProfilesHaveUniqueRoutingKeys(t *testing.T) {
	profiles := DefaultProfiles()
	names := make(map[string]struct{}, len(profiles))
	requiredLabelPairs := make(map[string]struct{}, len(profiles))
	revision := 0
	for _, profile := range profiles {
		if _, exists := names[profile.Name]; exists {
			t.Fatalf("duplicate profile name %q", profile.Name)
		}
		names[profile.Name] = struct{}{}

		if len(profile.RequiredLabels) != 2 {
			t.Fatalf("%s required labels = %#v, want one unique pair", profile.Name, profile.RequiredLabels)
		}
		pair := profile.RequiredLabels[0] + "\x00" + profile.RequiredLabels[1]
		if _, exists := requiredLabelPairs[pair]; exists {
			t.Fatalf("duplicate required-label pair %#v", profile.RequiredLabels)
		}
		requiredLabelPairs[pair] = struct{}{}

		if profile.DefaultTemplateName == "" {
			t.Fatalf("%s has empty default template name", profile.Name)
		}
		if profile.ManagedBy != "qiniu/ci-runner" {
			t.Fatalf("%s ManagedBy = %q", profile.Name, profile.ManagedBy)
		}
		if profile.CatalogRevision <= 0 {
			t.Fatalf("%s CatalogRevision = %d, want positive", profile.Name, profile.CatalogRevision)
		}
		if revision == 0 {
			revision = profile.CatalogRevision
		} else if profile.CatalogRevision != revision {
			t.Fatalf("%s CatalogRevision = %d, want shared revision %d", profile.Name, profile.CatalogRevision, revision)
		}
	}
}

func TestDefaultProfilesReferenceTrackedPublicTemplates(t *testing.T) {
	templateDirectories := map[string]string{
		"ubuntu-slim":  "github-runner-ubuntu-slim",
		"ubuntu-22.04": "github-runner-ubuntu-22.04",
		"ubuntu-24.04": "github-runner-ubuntu-24.04",
		"ubuntu-26.04": "github-runner-ubuntu-26.04",
	}
	profilesByLabel := make(map[string]state.RunnerProfile)
	for _, profile := range DefaultProfiles() {
		if len(profile.RequiredLabels) != 2 {
			continue
		}
		profilesByLabel[profile.RequiredLabels[1]] = profile
	}

	for label, directory := range templateDirectories {
		profile, ok := profilesByLabel[label]
		if !ok {
			t.Fatalf("missing managed profile for public template label %q", label)
		}
		configPath := filepath.Join("..", "..", "templates", directory, "qshell.sandbox.toml")
		config, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read public template config %s: %v", configPath, err)
		}
		nameDeclaration := `name = "` + profile.DefaultTemplateName + `"`
		if !strings.Contains(string(config), nameDeclaration) {
			t.Fatalf("%s does not declare managed template name %q", configPath, profile.DefaultTemplateName)
		}
	}

	latest, ok := profilesByLabel["ubuntu-latest"]
	if !ok {
		t.Fatal("missing managed profile for ubuntu-latest")
	}
	if latest.DefaultTemplateName != profilesByLabel["ubuntu-24.04"].DefaultTemplateName {
		t.Fatalf(
			"ubuntu-latest template = %q, want ubuntu-24.04 template %q",
			latest.DefaultTemplateName,
			profilesByLabel["ubuntu-24.04"].DefaultTemplateName,
		)
	}
}
