package runnercatalog

import (
	"reflect"
	"testing"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestPublicTemplatesExposeOnlyPublishedStableMetadata(t *testing.T) {
	want := []PublicTemplate{
		{
			DefaultTemplateName: "github-runner-ubuntu-22-04",
			RunnerSpecNames:     []string{"qiniu-ubuntu-22.04"},
			WorkflowLabels:      [][]string{{"qiniu", "ubuntu-22.04"}},
		},
		{
			DefaultTemplateName: "github-runner-ubuntu-24-04",
			RunnerSpecNames:     []string{"qiniu-ubuntu-24.04", "qiniu-ubuntu-latest"},
			WorkflowLabels:      [][]string{{"qiniu", "ubuntu-24.04"}, {"qiniu", "ubuntu-latest"}},
		},
		{
			DefaultTemplateName: "github-runner-ubuntu-26-04",
			RunnerSpecNames:     []string{"qiniu-ubuntu-26.04"},
			WorkflowLabels:      [][]string{{"qiniu", "ubuntu-26.04"}},
		},
		{
			DefaultTemplateName: "github-runner-ubuntu-slim",
			RunnerSpecNames:     []string{"qiniu-ubuntu-slim"},
			WorkflowLabels:      [][]string{{"qiniu", "ubuntu-slim"}},
		},
	}

	if got := PublicTemplates(publicTestProfiles()); !reflect.DeepEqual(got, want) {
		t.Fatalf("PublicTemplates(publicTestProfiles()) = %#v, want %#v", got, want)
	}
}

func publicTestProfiles() []state.RunnerProfile {
	profiles := DefaultProfiles()
	for i := range profiles {
		profiles[i] = state.NormalizeProfilePolicy(profiles[i])
	}
	return profiles
}

func TestPublicTemplatesUsesPublicationAndKeepsLabelPairs(t *testing.T) {
	base := state.RunnerProfile{TemplateSource: state.TemplateSourcePublic, DefaultTemplateName: "dynamic-name", Published: true, Enabled: true, Labels: []string{"full"}}
	a, b := base, base
	a.Name, a.RequiredLabels = "a", []string{"z-label"}
	b.Name, b.RequiredLabels = "b", []string{"a-label"}
	private, hidden, disabled := base, base, base
	private.Name, private.TemplateSource = "private", state.TemplateSourcePrivate
	hidden.Name, hidden.Published = "hidden", false
	disabled.Name, disabled.Enabled = "disabled", false
	profiles := []state.RunnerProfile{b, private, hidden, a, disabled}
	got := PublicTemplates(profiles)
	want := []PublicTemplate{{DefaultTemplateName: "dynamic-name", RunnerSpecNames: []string{"a", "b"}, WorkflowLabels: [][]string{{"z-label"}, {"a-label"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dynamic projection: %#v", got)
	}
	if profiles[0].Name != "b" {
		t.Fatal("projection sorted caller's catalog")
	}
	if empty := PublicTemplates(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("fresh directory = %#v", empty)
	}
}
