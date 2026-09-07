package runnercatalog

import (
	"reflect"
	"testing"
)

func TestPublicTemplatesExposeOnlyManagedStableMetadata(t *testing.T) {
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

	if got := PublicTemplates(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PublicTemplates() = %#v, want %#v", got, want)
	}
}
