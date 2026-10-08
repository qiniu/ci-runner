package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *DBStore) ListProfiles() ([]RunnerProfile, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []runnerProfileRecord
	if err := db.Order("priority DESC, name ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	profiles := make([]RunnerProfile, 0, len(records))
	for _, record := range records {
		profile, err := recordToProfile(record)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (s *DBStore) GetProfile(name string) (RunnerProfile, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return RunnerProfile{}, err
	}
	var record runnerProfileRecord
	if err := db.First(&record, "name = ?", strings.TrimSpace(name)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunnerProfile{}, ErrNotFound
		}
		return RunnerProfile{}, err
	}
	return recordToProfile(record)
}

// ValidateProfile checks local write constraints without accessing a database
// or provider. Admin handlers reuse it before performing remote validation.
func ValidateProfile(profile RunnerProfile) error {
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if err := validateProfileName(name); err != nil {
		return err
	}
	if !labelsMatch(profile.RequiredLabels, profile.Labels) {
		return fmt.Errorf("required labels must be a subset of labels")
	}
	return ValidateProfilePolicy(NormalizeProfilePolicy(profile))
}

func (s *DBStore) UpsertProfile(profile RunnerProfile) (RunnerProfile, error) {
	return s.saveProfile(profile, false, nil)
}

// UpsertProfileIfUnchanged inserts only when absent (nil expectedUpdatedAt), or
// updates only the version read before validation. Deleted/changed rows conflict.
func (s *DBStore) UpsertProfileIfUnchanged(profile RunnerProfile, expectedUpdatedAt *time.Time) (RunnerProfile, error) {
	return s.saveProfile(profile, true, expectedUpdatedAt)
}

func (s *DBStore) saveProfile(profile RunnerProfile, conditional bool, expectedUpdatedAt *time.Time) (RunnerProfile, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return RunnerProfile{}, err
	}
	profile = NormalizeProfilePolicy(profile)
	profile.Name = strings.TrimSpace(profile.Name)
	if err := ValidateProfile(profile); err != nil {
		return RunnerProfile{}, err
	}
	labelsJSON, err := json.Marshal(profile.Labels)
	if err != nil {
		return RunnerProfile{}, err
	}
	if profile.RequiredLabels == nil {
		profile.RequiredLabels = []string{}
	}
	requiredLabelsJSON, err := json.Marshal(profile.RequiredLabels)
	if err != nil {
		return RunnerProfile{}, err
	}
	requiredLabelsJSONText := string(requiredLabelsJSON)
	now := time.Now().UTC()
	if conditional && expectedUpdatedAt != nil {
		// MySQL persists datetime(3). Advance beyond the stored revision even
		// for rapid saves or when the application clock moves backwards.
		if next := expectedUpdatedAt.Add(time.Millisecond); now.Before(next) {
			now = next
		}
	}
	record := runnerProfileRecord{
		Name:                profile.Name,
		LabelsJSON:          string(labelsJSON),
		RequiredLabelsJSON:  &requiredLabelsJSONText,
		TemplateID:          profile.TemplateID,
		DefaultTemplateName: profile.DefaultTemplateName,
		TemplateSource:      profile.TemplateSource,
		Published:           profile.Published,
		RunnerGroup:         profile.RunnerGroup,
		MaxConcurrency:      profile.MaxConcurrency,
		MinIdle:             profile.MinIdle,
		Priority:            profile.Priority,
		Enabled:             profile.Enabled,
		DefaultAvailable:    true,
		ManagedBy:           profile.ManagedBy,
		CatalogRevision:     profile.CatalogRevision,
		CreatedAt:           profile.CreatedAt,
		UpdatedAt:           now,
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	updates := map[string]any{
		"labels_json":           record.LabelsJSON,
		"required_labels_json":  record.RequiredLabelsJSON,
		"template_id":           record.TemplateID,
		"default_template_name": record.DefaultTemplateName,
		"template_source":       record.TemplateSource,
		"published":             record.Published,
		"runner_group":          record.RunnerGroup,
		"max_concurrency":       record.MaxConcurrency,
		"min_idle":              record.MinIdle,
		"priority":              record.Priority,
		"enabled":               record.Enabled,
		"managed_by":            record.ManagedBy,
		"catalog_revision":      record.CatalogRevision,
		"updated_at":            record.UpdatedAt,
	}
	var result *gorm.DB
	switch {
	case conditional && expectedUpdatedAt != nil:
		result = db.Model(&runnerProfileRecord{}).
			Where("name = ? AND updated_at = ?", record.Name, *expectedUpdatedAt).Updates(updates)
	case conditional:
		// A no-op ON CONFLICT reports success with MySQL clientFoundRows.
		// Use a real insert and translate duplicate errors only at this boundary.
		result = db.Create(&record)
		if translator, ok := db.Dialector.(gorm.ErrorTranslator); ok && result.Error != nil && errors.Is(translator.Translate(result.Error), gorm.ErrDuplicatedKey) {
			return RunnerProfile{}, ErrConflict
		}
	default:
		result = db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoUpdates: clause.Assignments(updates)}).Create(&record)
	}
	if result.Error != nil {
		return RunnerProfile{}, result.Error
	}
	if conditional && result.RowsAffected == 0 {
		return RunnerProfile{}, ErrConflict
	}

	return s.GetProfile(record.Name)
}

func validateProfileName(name string) error {
	if strings.Contains(name, "/") || name == "." || name == ".." {
		return fmt.Errorf("profile name must not contain '/' or be '.' or '..'")
	}
	return nil
}

func (s *DBStore) DeleteProfile(name string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	return db.Delete(&runnerProfileRecord{}, "name = ?", name).Error
}

func (s *DBStore) MatchProfile(repositoryFullName string, labels []string) (ProfileMatch, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return ProfileMatch{}, err
	}
	profiles, err := loadRunnerProfilesForMatch(db)
	if err != nil {
		return ProfileMatch{}, err
	}
	match := profileMatchFromCandidates(repositoryFullName, labels, profiles)
	match.Source = "global"
	return match, nil
}

func loadRunnerProfilesForMatch(tx *gorm.DB) ([]RunnerProfile, error) {
	var profileRecords []runnerProfileRecord
	if err := tx.Order("priority DESC, name ASC").Find(&profileRecords).Error; err != nil {
		return nil, err
	}
	profiles := make([]RunnerProfile, 0, len(profileRecords))
	for _, record := range profileRecords {
		profile, err := recordToProfile(record)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func profileMatchFromCandidates(repositoryFullName string, labels []string, profiles []RunnerProfile) ProfileMatch {
	match := ProfileMatch{RepositoryFullName: repositoryFullName, Labels: append([]string(nil), labels...)}
	match.Profile = selectMatchingProfile(profiles, labels)
	if match.Profile == nil {
		match.Reason = "profile_labels_not_matched"
	}
	return match
}

func selectMatchingProfile(profiles []RunnerProfile, labels []string) *RunnerProfile {
	var candidates []RunnerProfile
	for _, profile := range profiles {
		if profile.Enabled && labelsMatch(profile.RequiredLabels, labels) && labelsMatch(labels, profile.Labels) {
			candidates = append(candidates, profile)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		if len(candidates[i].Labels) != len(candidates[j].Labels) {
			return len(candidates[i].Labels) > len(candidates[j].Labels)
		}
		return candidates[i].Name < candidates[j].Name
	})
	selected := candidates[0]
	return &selected
}
