package state

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	TemplateSourcePublic     = "public"
	TemplateSourcePrivate    = "private"
	RunnerUpdateOfficial     = "official"
	RunnerUpdatePreinstalled = "preinstalled"
)

// NormalizeProfilePolicy translates legacy rows once. Explicit policy fields
// take precedence; managed_by is retained only for historical compatibility.
func NormalizeProfilePolicy(p RunnerProfile) RunnerProfile {
	if p.TemplateSource == "" {
		p.TemplateSource = TemplateSourcePrivate
		if strings.TrimSpace(p.ManagedBy) != "" {
			if p.RunnerUpdatePolicy == "" {
				p.RunnerUpdatePolicy = RunnerUpdatePreinstalled
			}
			p.RequireDocker = true
			if strings.TrimSpace(p.DefaultTemplateName) != "" {
				p.TemplateSource = TemplateSourcePublic
				p.TemplateID = ""
				p.Published = true
				p.ForkSponsorship = true
			}
		}
	}
	if p.RunnerUpdatePolicy == "" {
		p.RunnerUpdatePolicy = RunnerUpdateOfficial
	}
	return p
}

func ValidateProfilePolicy(p RunnerProfile) error {
	switch p.TemplateSource {
	case TemplateSourcePublic:
		if len(p.Labels) == 0 {
			return fmt.Errorf("public template specs require workflow labels")
		}
		if strings.TrimSpace(p.DefaultTemplateName) == "" || strings.TrimSpace(p.TemplateID) != "" {
			return fmt.Errorf("public templates require a stable template name and no physical template ID")
		}
	case TemplateSourcePrivate:
		if strings.TrimSpace(p.DefaultTemplateName) != "" {
			return fmt.Errorf("private templates must use a physical template ID, not a public template name")
		}
		if p.Published || p.ForkSponsorship {
			return fmt.Errorf("private templates cannot be published or use fork sponsorship")
		}
	default:
		return fmt.Errorf("invalid template source")
	}
	if p.RunnerUpdatePolicy != RunnerUpdateOfficial && p.RunnerUpdatePolicy != RunnerUpdatePreinstalled {
		return fmt.Errorf("invalid runner update policy")
	}
	if p.MaxConcurrency < 0 || p.MinIdle < 0 {
		return fmt.Errorf("capacity values must not be negative")
	}
	return nil
}

// migrateRunnerProfilePolicies updates only rows without policy fields. It
// preserves timestamps, names, labels and operator controls, including unsafe
// historical names. Deleted profiles are never seeded or recreated.
func migrateRunnerProfilePolicies(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var rows []runnerProfileRecord
		if err := tx.Where("template_source = ? OR template_source IS NULL", "").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			p := NormalizeProfilePolicy(RunnerProfile{ManagedBy: row.ManagedBy, DefaultTemplateName: row.DefaultTemplateName})
			updates := map[string]any{
				"template_source":      p.TemplateSource,
				"published":            p.Published,
				"runner_update_policy": p.RunnerUpdatePolicy,
				"require_docker":       p.RequireDocker,
				"fork_sponsorship":     p.ForkSponsorship,
				"updated_at":           row.UpdatedAt,
			}
			if p.TemplateSource == TemplateSourcePublic {
				updates["template_id"] = ""
			}
			result := tx.Model(&runnerProfileRecord{}).Where("name = ? AND (template_source = ? OR template_source IS NULL)", row.Name, "").Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if err := tx.Create(&auditEventRecord{Actor: "runnerd_migration", Action: "profile.policy_migrate", ResourceType: "runner_profile", ResourceID: row.Name, PayloadJSON: `{"version":1}`, CreatedAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
