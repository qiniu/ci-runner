package runnercatalog

import (
	"sort"

	"github.com/qiniu/ci-runner/internal/state"
)

// PublicTemplate is the stable metadata for one published public
// Sandbox template. It intentionally excludes provider and scoped metadata.
type PublicTemplate struct {
	DefaultTemplateName string     `json:"default_template_name"`
	RunnerSpecNames     []string   `json:"runner_spec_names"`
	WorkflowLabels      [][]string `json:"workflow_labels"`
}

// PublicTemplates projects the database catalog without credentials or physical IDs.
func PublicTemplates(profiles []state.RunnerProfile) []PublicTemplate {
	byTemplateName := make(map[string]*PublicTemplate)
	profiles = append([]state.RunnerProfile(nil), profiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	for _, profile := range profiles {
		if !profile.Enabled || !profile.Published || profile.TemplateSource != state.TemplateSourcePublic || profile.DefaultTemplateName == "" {
			continue
		}
		template := byTemplateName[profile.DefaultTemplateName]
		if template == nil {
			template = &PublicTemplate{DefaultTemplateName: profile.DefaultTemplateName}
			byTemplateName[profile.DefaultTemplateName] = template
		}
		template.RunnerSpecNames = append(template.RunnerSpecNames, profile.Name)
		labels := profile.RequiredLabels
		if len(labels) == 0 {
			labels = profile.Labels
		}
		template.WorkflowLabels = append(template.WorkflowLabels, append([]string(nil), labels...))
	}

	templates := make([]PublicTemplate, 0, len(byTemplateName))
	for _, template := range byTemplateName {
		templates = append(templates, *template)
	}
	sort.Slice(templates, func(i, j int) bool {
		return templates[i].DefaultTemplateName < templates[j].DefaultTemplateName
	})
	return templates
}
