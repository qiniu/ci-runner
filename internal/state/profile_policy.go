package state

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	// Legacy values describe the reference/runtime path, not provider visibility.
	TemplateSourcePublic  = "public"
	TemplateSourcePrivate = "private"
)

// NormalizeProfilePolicy translates legacy rows once. Explicit policy fields
// take precedence; managed_by is retained only for historical compatibility.
func NormalizeProfilePolicy(p RunnerProfile) RunnerProfile {
	if p.TemplateSource == "" {
		p.TemplateSource = TemplateSourcePrivate
		if strings.TrimSpace(p.ManagedBy) != "" {
			p.TemplateSource = TemplateSourcePublic
			p.TemplateID = ""
			if strings.TrimSpace(p.DefaultTemplateName) != "" {
				p.Published = true
			}
		}
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
			return fmt.Errorf("ID references must use a physical template ID, not a public template name")
		}
	default:
		return fmt.Errorf("invalid template source")
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
				"template_source": p.TemplateSource,
				"published":       p.Published,
				"updated_at":      row.UpdatedAt,
			}
			if p.TemplateSource == TemplateSourcePublic {
				updates["template_id"] = ""
			} else {
				// Legacy custom specs used only the physical ID. A stray public
				// name was inert, but would now fail private-binding validation.
				updates["default_template_name"] = ""
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
