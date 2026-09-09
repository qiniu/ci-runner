package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	RunnerProfileScopeAccount            = "account"
	RunnerProfileScopeGitHubInstallation = "github_installation"
)

func ValidateRunnerProfileScope(scope RunnerProfileScope) error {
	if scope.Type != RunnerProfileScopeAccount && scope.Type != RunnerProfileScopeGitHubInstallation {
		return fmt.Errorf("invalid runner profile scope type")
	}
	if scope.ID <= 0 {
		return fmt.Errorf("invalid runner profile scope id")
	}
	return nil
}

func NormalizeWorkflowLabels(labels []string) ([]string, string, error) {
	seen := make(map[string]struct{}, len(labels))
	normalized := make([]string, 0, len(labels))
	for _, raw := range labels {
		label := strings.TrimSpace(raw)
		if label == "" {
			continue
		}
		canonical := strings.ToLower(label)
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, label)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return strings.ToLower(normalized[i]) < strings.ToLower(normalized[j])
	})
	if len(normalized) == 0 {
		return nil, "", fmt.Errorf("workflow labels are required")
	}
	canonical := make([]string, len(normalized))
	for i, label := range normalized {
		canonical[i] = strings.ToLower(label)
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return normalized, hex.EncodeToString(sum[:]), nil
}

func (s *DBStore) ListScopedProfiles(scope RunnerProfileScope) ([]ScopedRunnerProfile, error) {
	if err := ValidateRunnerProfileScope(scope); err != nil {
		return nil, err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []scopedRunnerProfileRecord
	if err := db.Where("scope_type = ? AND scope_id = ?", scope.Type, scope.ID).Order("name ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	profiles := make([]ScopedRunnerProfile, 0, len(records))
	for _, record := range records {
		profile, err := scopedProfileFromRecord(record)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (s *DBStore) GetScopedProfile(scope RunnerProfileScope, name string) (ScopedRunnerProfile, error) {
	if err := ValidateRunnerProfileScope(scope); err != nil {
		return ScopedRunnerProfile{}, err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return ScopedRunnerProfile{}, err
	}
	var record scopedRunnerProfileRecord
	if err := db.Where("scope_type = ? AND scope_id = ? AND name = ?", scope.Type, scope.ID, strings.TrimSpace(name)).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ScopedRunnerProfile{}, ErrNotFound
		}
		return ScopedRunnerProfile{}, err
	}
	return scopedProfileFromRecord(record)
}

func (s *DBStore) UpsertScopedProfileIfUnchanged(profile ScopedRunnerProfile, expectedUpdatedAt *time.Time) (ScopedRunnerProfile, error) {
	scope := RunnerProfileScope{Type: profile.ScopeType, ID: profile.ScopeID}
	if err := ValidateRunnerProfileScope(scope); err != nil {
		return ScopedRunnerProfile{}, err
	}
	profile.Name = strings.TrimSpace(profile.Name)
	if profile.Name == "" || strings.Contains(profile.Name, "/") || profile.Name == "." || profile.Name == ".." || profile.MaxConcurrency < 0 {
		return ScopedRunnerProfile{}, fmt.Errorf("invalid scoped runner profile")
	}
	if global, globalErr := s.GetProfile(profile.Name); globalErr == nil && global.Enabled {
		return ScopedRunnerProfile{}, ErrRunnerProfileNameConflict
	} else if globalErr != nil && !errors.Is(globalErr, ErrNotFound) {
		return ScopedRunnerProfile{}, globalErr
	}
	labels, labelKey, err := NormalizeWorkflowLabels(profile.WorkflowLabels)
	if err != nil {
		return ScopedRunnerProfile{}, err
	}
	profile.WorkflowLabels, profile.LabelKey = labels, labelKey
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return ScopedRunnerProfile{}, err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return ScopedRunnerProfile{}, err
	}
	now := time.Now().UTC()
	if expectedUpdatedAt != nil && now.Before(expectedUpdatedAt.Add(time.Millisecond)) {
		now = expectedUpdatedAt.Add(time.Millisecond)
	}
	record := scopedRunnerProfileRecord{ScopeType: scope.Type, ScopeID: scope.ID, Name: profile.Name, WorkflowLabelsJSON: string(labelsJSON), LabelKey: labelKey, TemplateID: strings.TrimSpace(profile.TemplateID), RunnerGroup: strings.TrimSpace(profile.RunnerGroup), MaxConcurrency: profile.MaxConcurrency, Enabled: profile.Enabled, CreatedAt: profile.CreatedAt, UpdatedAt: now}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	updates := map[string]any{"workflow_labels_json": record.WorkflowLabelsJSON, "label_key": record.LabelKey, "template_id": record.TemplateID, "runner_group": record.RunnerGroup, "max_concurrency": record.MaxConcurrency, "enabled": record.Enabled, "updated_at": record.UpdatedAt}
	var result *gorm.DB
	if expectedUpdatedAt != nil {
		result = db.Model(&scopedRunnerProfileRecord{}).Where("scope_type = ? AND scope_id = ? AND name = ? AND updated_at = ?", scope.Type, scope.ID, profile.Name, *expectedUpdatedAt).Updates(updates)
		if isDuplicatedKeyError(db, result.Error) {
			return ScopedRunnerProfile{}, ErrRunnerProfileLabelsConflict
		}
	} else {
		result = db.Create(&record)
		if result.Error != nil {
			if isDuplicatedKeyError(db, result.Error) {
				var existing scopedRunnerProfileRecord
				existingErr := db.Where("scope_type = ? AND scope_id = ? AND name = ?", scope.Type, scope.ID, profile.Name).First(&existing).Error
				if existingErr == nil {
					return ScopedRunnerProfile{}, ErrConflict
				}
				if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
					return ScopedRunnerProfile{}, existingErr
				}
				return ScopedRunnerProfile{}, ErrRunnerProfileLabelsConflict
			}
		}
	}
	if result.Error != nil {
		return ScopedRunnerProfile{}, result.Error
	}
	if expectedUpdatedAt != nil && result.RowsAffected == 0 {
		return ScopedRunnerProfile{}, ErrConflict
	}
	return s.GetScopedProfile(scope, profile.Name)
}

func (s *DBStore) DeleteScopedProfileIfUnchanged(scope RunnerProfileScope, name string, expectedUpdatedAt *time.Time) error {
	if err := ValidateRunnerProfileScope(scope); err != nil {
		return err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	query := db.Where("scope_type = ? AND scope_id = ? AND name = ?", scope.Type, scope.ID, strings.TrimSpace(name))
	if expectedUpdatedAt != nil {
		query = query.Where("updated_at = ?", *expectedUpdatedAt)
	}
	result := query.Delete(&scopedRunnerProfileRecord{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}

func (s *DBStore) ListEffectiveProfiles(scope RunnerProfileScope) ([]EffectiveRunnerProfile, error) {
	if err := ValidateRunnerProfileScope(scope); err != nil {
		return nil, err
	}
	globals, err := s.ListProfiles()
	if err != nil {
		return nil, err
	}
	items := make([]EffectiveRunnerProfile, 0, len(globals))
	globalLabelKeys := make(map[string]struct{}, len(globals))
	for _, profile := range globals {
		if _, labelKey, normalizeErr := NormalizeWorkflowLabels(profile.Labels); normalizeErr == nil {
			globalLabelKeys[labelKey] = struct{}{}
		}
		source := "platform_custom"
		if profile.ManagedBy != "" {
			source = "managed"
		}
		items = append(items, EffectiveRunnerProfile{Source: source, ScopeType: scope.Type, ScopeID: scope.ID, Profile: profile, WorkflowLabels: append([]string(nil), profile.Labels...)})
	}
	scoped, err := s.ListScopedProfiles(scope)
	if err != nil {
		return nil, err
	}
	for _, profile := range scoped {
		_, overridesGlobal := globalLabelKeys[profile.LabelKey]
		items = append(items, EffectiveRunnerProfile{Source: "scoped_custom", ScopeType: scope.Type, ScopeID: scope.ID, Profile: RunnerProfile{Name: profile.Name, Labels: append([]string(nil), profile.WorkflowLabels...), RequiredLabels: append([]string(nil), profile.WorkflowLabels...), TemplateID: profile.TemplateID, RunnerGroup: profile.RunnerGroup, MaxConcurrency: profile.MaxConcurrency, Enabled: profile.Enabled, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt}, WorkflowLabels: append([]string(nil), profile.WorkflowLabels...), OverridesGlobal: overridesGlobal})
	}
	return items, nil
}

func isDuplicatedKeyError(db *gorm.DB, err error) bool {
	if err == nil {
		return false
	}
	if translator, ok := db.Dialector.(gorm.ErrorTranslator); ok {
		return errors.Is(translator.Translate(err), gorm.ErrDuplicatedKey)
	}
	return errors.Is(err, gorm.ErrDuplicatedKey)
}

func (s *DBStore) MatchProfileForScope(scope RunnerProfileScope, repositoryFullName string, labels []string) (ProfileMatch, error) {
	if err := ValidateRunnerProfileScope(scope); err != nil {
		return ProfileMatch{}, err
	}
	match := ProfileMatch{RepositoryFullName: repositoryFullName, Labels: append([]string(nil), labels...), ScopeType: scope.Type, ScopeID: scope.ID}
	scoped, err := s.ListScopedProfiles(scope)
	if err != nil {
		return ProfileMatch{}, err
	}
	_, labelKey, normalizeErr := NormalizeWorkflowLabels(labels)
	if normalizeErr == nil {
		for _, profile := range scoped {
			if profile.LabelKey != labelKey {
				continue
			}
			match.Source = "scoped_custom"
			if !profile.Enabled {
				match.Reason = "profile_scope_disabled"
				return match, nil
			}
			runnerProfile := RunnerProfile{Name: profile.Name, Labels: append([]string(nil), profile.WorkflowLabels...), RequiredLabels: append([]string(nil), profile.WorkflowLabels...), TemplateID: profile.TemplateID, RunnerGroup: profile.RunnerGroup, MaxConcurrency: profile.MaxConcurrency, Enabled: true, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt}
			match.Profile = &runnerProfile
			return match, nil
		}
	}
	globals, err := s.ListProfiles()
	if err != nil {
		return ProfileMatch{}, err
	}
	match = profileMatchFromCandidates(repositoryFullName, labels, globals)
	match.Source, match.ScopeType, match.ScopeID = "global", scope.Type, scope.ID
	return match, nil
}

func scopedProfileFromRecord(record scopedRunnerProfileRecord) (ScopedRunnerProfile, error) {
	var labels []string
	if err := json.Unmarshal([]byte(record.WorkflowLabelsJSON), &labels); err != nil {
		return ScopedRunnerProfile{}, err
	}
	return ScopedRunnerProfile{ScopeType: record.ScopeType, ScopeID: record.ScopeID, Name: record.Name, WorkflowLabels: labels, LabelKey: record.LabelKey, TemplateID: record.TemplateID, RunnerGroup: record.RunnerGroup, MaxConcurrency: record.MaxConcurrency, Enabled: record.Enabled, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}, nil
}

func (s *DBStore) ActiveCountForProfileScope(source string, scope RunnerProfileScope, name string) (int, error) {
	return s.countStatesForProfileScope(source, scope, name, []string{StatusQueued, StatusCreating, StatusRunning, StatusStopping})
}

func (s *DBStore) InFlightCountForProfileScope(source string, scope RunnerProfileScope, name string) (int, error) {
	return s.countStatesForProfileScope(source, scope, name, []string{StatusCreating, StatusRunning, StatusStopping})
}

func (s *DBStore) countStatesForProfileScope(source string, scope RunnerProfileScope, name string, statuses []string) (int, error) {
	if err := ValidateRunnerProfileScope(scope); err != nil {
		return 0, err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	var count int64
	if err := db.Model(&runnerRequestRecord{}).Where("profile_source = ? AND profile_scope_type = ? AND profile_scope_id = ? AND profile_name = ? AND status IN ?", source, scope.Type, scope.ID, strings.TrimSpace(name), statuses).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}
