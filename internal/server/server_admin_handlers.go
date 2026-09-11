package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func (s *Server) handleCreateRunner(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	var input manualCreateRequest
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.logger.Warn("manual runner payload rejected", "error", err)
			writeError(w, http.StatusBadRequest, "invalid runner payload")
			return
		}
	}
	id := input.ID
	if id == "" {
		id = newID()
	}
	labels := input.Labels
	requestedLabels := append([]string(nil), labels...)
	repositoryFullName := strings.TrimSpace(input.RepositoryFullName)
	if repositoryFullName == "" || strings.Contains(repositoryFullName, "*") {
		s.logger.Info("manual runner repository missing", "id", id, "repository", repositoryFullName)
		writeError(w, http.StatusBadRequest, "repository_full_name is required for manual runner creation")
		return
	}
	if !s.cfg.RepositoryAllowed(repositoryFullName) {
		s.logger.Info("manual runner repository rejected by allowlist", "id", id, "repository", repositoryFullName)
		writeError(w, http.StatusForbidden, "repository is not allowed")
		return
	}
	profileName := strings.TrimSpace(input.ProfileName)
	runnerGroup := ""
	if profileName == "" {
		if len(labels) == 0 {
			s.logger.Info("manual runner labels missing", "id", id, "repository", repositoryFullName)
			writeError(w, http.StatusBadRequest, "labels are required when runner_spec_name is not provided")
			return
		}
		match, err := s.matchProfileForAdmission(repositoryFullName, 0, labels)
		if err != nil {
			s.logger.Error("match manual runner profile", "id", id, "repository", repositoryFullName, "labels", labels, "error", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if match.Profile == nil {
			s.logger.Info("manual runner admission rejected", "id", id, "repository", repositoryFullName, "labels", labels, "reason", match.Reason)
			writeError(w, http.StatusBadRequest, "no matching profile")
			return
		}
		profileName = match.Profile.Name
		runnerGroup = match.Profile.RunnerGroup
		labels = append([]string(nil), match.Profile.Labels...)
	} else {
		profile, err := s.store.GetProfile(profileName)
		if err != nil {
			s.logger.Info("manual runner profile not found", "id", id, "repository", repositoryFullName, "profile", profileName)
			writeError(w, http.StatusBadRequest, "profile not found")
			return
		}
		if err := validateRequestedProfile(profile, labels); err != nil {
			s.logger.Info("manual runner profile rejected", "id", id, "repository", repositoryFullName, "profile", profileName, "labels", labels, "error", err)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		runnerGroup = profile.RunnerGroup
		if len(labels) == 0 {
			labels = append([]string(nil), profile.Labels...)
		}
		if len(requestedLabels) == 0 {
			requestedLabels = append([]string(nil), labels...)
		}
	}
	req := state.RunnerRequest{
		ID:                 id,
		Source:             "manual_api",
		RepositoryFullName: repositoryFullName,
		Labels:             labels,
		RequestedLabels:    requestedLabels,
		ProfileName:        profileName,
		RunnerGroup:        runnerGroup,
		RunnerName:         "e2b-" + id,
	}
	s.logger.Info("manual runner create requested", "id", id, "repository", repositoryFullName, "profile", profileName, "labels", labels)
	s.createAndStart(w, r, req, nil)
}

func (s *Server) handleListRunners(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	limit, offset, err := parsePagination(r, defaultRunnerRequestListLimit, maxRunnerRequestListLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	states, total, err := s.store.ListStatesPage(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writePaginationHeaders(w, r, total, limit, offset)
	writeJSON(w, http.StatusOK, states)
}

func (s *Server) handleGetRunner(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "runner not found")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleResolveRunnerRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	identifier := strings.TrimSpace(r.PathValue("identifier"))
	if identifier == "" {
		writeError(w, http.StatusBadRequest, "runner request identifier is required")
		return
	}
	st, err := s.resolveRunnerRequestIdentifier(identifier)
	if err != nil {
		s.writeRunnerRequestLookupError(w, identifier, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleRetryRunner(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	st, err := s.store.RetryRequest(r.PathValue("id"), time.Now().UTC())
	if err != nil {
		if errors.Is(err, state.ErrRetryNotAllowed) {
			s.logger.Info("runner retry rejected", "id", r.PathValue("id"), "error", err)
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.logger.Error("retry runner request", "id", r.PathValue("id"), "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.AppendLog(st.ID, "control.log", []byte("runner request manually requeued\n"))
	s.logger.Info("runner request manually requeued", "id", st.ID, "repository", st.RepositoryFullName, "profile", st.ProfileName)
	s.recordAudit("admin_api", "runner.retry", "runner_request", st.ID, map[string]any{
		"status":           st.Status,
		"repository":       st.RepositoryFullName,
		"runner_spec_name": st.ProfileName,
		"requested_labels": st.RequestedLabels,
	})
	s.refreshMetrics()
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) handleGetRunnerLog(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	writeRunnerLog(w, s.store, r.PathValue("id"), r.PathValue("name"))
}

func writeRunnerLog(w http.ResponseWriter, store state.Store, id, name string) {
	switch name {
	case "control.log", "stdout.log", "stderr.log":
	default:
		writeError(w, http.StatusBadRequest, "unsupported log name")
		return
	}
	data, err := store.ReadLog(id, name, 256<<10)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			return
		}
		writeError(w, http.StatusNotFound, "log not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleDeleteRunner(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	id := r.PathValue("id")
	st, _, err := s.stopRunner(context.Background(), id, github.WorkflowJob{})
	if err != nil {
		s.logger.Error("delete runner request failed", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Info("delete runner request handled", "id", id, "status", st.Status, "sandbox_id", st.SandboxID)
	s.recordAudit("admin_api", "runner.stop", "runner_request", id, map[string]any{
		"status": st.Status,
	})
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	events, err := s.store.ListAuditEvents(100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	s.refreshMetrics()
	profiles, err := s.store.ListProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	payload, err := readProfileRequestPayload(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile payload")
		return
	}
	if payload.containsAny("managed_by", "catalog_revision", "default_template_name") {
		writeError(w, http.StatusBadRequest, "managed runner spec metadata cannot be set by clients")
		return
	}
	var input createProfileRequest
	if err := payload.decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile payload")
		return
	}
	input.TemplateID = strings.TrimSpace(input.TemplateID)
	if input.TemplateID == "" {
		writeError(w, http.StatusBadRequest, "template_id is required")
		return
	}
	existing, err := s.store.GetProfile(input.Name)
	if err == nil && strings.TrimSpace(existing.ManagedBy) != "" {
		writeErrorCode(
			w,
			http.StatusConflict,
			managedRunnerSpecErrorCode,
			"managed runner specs cannot be overwritten",
		)
		return
	}
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		s.logger.Error("load runner spec before create", "name", input.Name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var expectedUpdatedAt *time.Time
	if err == nil {
		expectedUpdatedAt = &existing.UpdatedAt
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	requestedProfile := state.RunnerProfile{
		Name:           input.Name,
		Labels:         input.Labels,
		RequiredLabels: input.RequiredLabels,
		TemplateID:     input.TemplateID,
		RunnerGroup:    input.RunnerGroup,
		MaxConcurrency: input.MaxConcurrency,
		MinIdle:        intValue(input.MinIdle),
		Priority:       intValue(input.Priority),
		Enabled:        enabled,
	}
	if err := state.ValidateProfile(requestedProfile); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.validateAdminProfileTemplate(w, r, requestedProfile.TemplateID) {
		return
	}
	var profile state.RunnerProfile
	s.admissionMu.Lock()
	err = s.applyMutationWithAudit("admin_api", "profile.create", "runner_profile", strings.TrimSpace(requestedProfile.Name), requestedProfile, func(tx state.Store) error {
		profile, err = tx.UpsertProfileIfUnchanged(requestedProfile, expectedUpdatedAt)
		return err
	})
	s.admissionMu.Unlock()
	if err != nil {
		if writeProfileConflict(w, err) || writeMutationAuditError(w, err) {
			return
		}
		s.logger.Info("profile create rejected", "name", input.Name, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("profile created", "name", profile.Name, "labels", profile.Labels, "template_id", profile.TemplateID, "max_concurrency", profile.MaxConcurrency, "enabled", profile.Enabled)
	s.refreshMetrics()
	writeJSON(w, http.StatusCreated, profile)
}

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	profile, err := s.store.GetProfile(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) handlePatchProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	current, err := s.store.GetProfile(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	payload, err := readProfileRequestPayload(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile payload")
		return
	}
	if strings.TrimSpace(current.ManagedBy) != "" {
		s.handlePatchManagedProfile(w, current, payload)
		return
	}
	if payload.containsAny("managed_by", "catalog_revision", "default_template_name") {
		writeError(w, http.StatusBadRequest, "managed runner spec metadata cannot be set by clients")
		return
	}
	var input patchProfileRequest
	if err := payload.decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile payload")
		return
	}
	if input.Labels != nil {
		current.Labels = *input.Labels
	}
	if input.RequiredLabels != nil {
		current.RequiredLabels = *input.RequiredLabels
	}
	previousTemplateID := strings.TrimSpace(current.TemplateID)
	if input.TemplateID != nil {
		current.TemplateID = *input.TemplateID
	}
	current.TemplateID = strings.TrimSpace(current.TemplateID)
	if current.TemplateID == "" {
		writeError(w, http.StatusBadRequest, "template_id is required")
		return
	}
	if input.RunnerGroup != nil {
		current.RunnerGroup = *input.RunnerGroup
	}
	if input.MaxConcurrency != nil {
		current.MaxConcurrency = *input.MaxConcurrency
	}
	if input.MinIdle != nil {
		current.MinIdle = *input.MinIdle
	}
	if input.Priority != nil {
		current.Priority = *input.Priority
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if err := state.ValidateProfile(current); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if current.TemplateID != previousTemplateID && !s.validateAdminProfileTemplate(w, r, current.TemplateID) {
		return
	}
	var profile state.RunnerProfile
	s.admissionMu.Lock()
	err = s.applyMutationWithAudit("admin_api", "profile.update", "runner_profile", current.Name, current, func(tx state.Store) error {
		profile, err = tx.UpsertProfileIfUnchanged(current, &current.UpdatedAt)
		return err
	})
	s.admissionMu.Unlock()
	if err != nil {
		if writeProfileConflict(w, err) || writeMutationAuditError(w, err) {
			return
		}
		s.logger.Info("profile update rejected", "name", current.Name, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("profile updated", "name", profile.Name, "labels", profile.Labels, "template_id", profile.TemplateID, "max_concurrency", profile.MaxConcurrency, "enabled", profile.Enabled)
	s.refreshMetrics()
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) handlePatchManagedProfile(w http.ResponseWriter, current state.RunnerProfile, payload profileRequestPayload) {
	if payload.containsAny(
		"labels",
		"required_labels",
		"template_id",
		"runner_group",
		"priority",
		"default_template_name",
		"managed_by",
		"catalog_revision",
	) {
		writeErrorCode(
			w,
			http.StatusConflict,
			managedRunnerSpecErrorCode,
			"managed runner spec catalog fields cannot be changed",
		)
		return
	}
	var input patchProfileRequest
	if err := payload.decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile payload")
		return
	}
	if input.MaxConcurrency != nil {
		current.MaxConcurrency = *input.MaxConcurrency
	}
	if input.MinIdle != nil {
		current.MinIdle = *input.MinIdle
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	var profile state.RunnerProfile
	s.admissionMu.Lock()
	err := s.applyMutationWithAudit("admin_api", "profile.update", "runner_profile", current.Name, current, func(tx state.Store) error {
		var mutationErr error
		profile, mutationErr = tx.UpsertProfileIfUnchanged(current, &current.UpdatedAt)
		return mutationErr
	})
	s.admissionMu.Unlock()
	if err != nil {
		if writeProfileConflict(w, err) || writeMutationAuditError(w, err) {
			return
		}
		s.logger.Info("managed profile update rejected", "name", current.Name, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("managed profile updated", "name", profile.Name, "max_concurrency", profile.MaxConcurrency, "min_idle", profile.MinIdle, "enabled", profile.Enabled)
	s.refreshMetrics()
	writeJSON(w, http.StatusOK, profile)
}

type profileRequestPayload struct {
	data   []byte
	fields map[string]json.RawMessage
}

func readProfileRequestPayload(body io.Reader) (profileRequestPayload, error) {
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return profileRequestPayload{}, err
	}
	payload := profileRequestPayload{data: data}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&payload.fields); err != nil {
		return profileRequestPayload{}, err
	}
	return payload, nil
}

func (p profileRequestPayload) decode(dst any) error {
	return json.NewDecoder(bytes.NewReader(p.data)).Decode(dst)
}

func (p profileRequestPayload) containsAny(names ...string) bool {
	for field := range p.fields {
		for _, name := range names {
			if strings.EqualFold(field, name) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	name := r.PathValue("name")
	profile, err := s.store.GetProfile(name)
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		s.logger.Error("load runner spec before delete", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err == nil && strings.TrimSpace(profile.ManagedBy) != "" {
		writeErrorCode(
			w,
			http.StatusConflict,
			managedRunnerSpecErrorCode,
			"managed runner specs cannot be deleted",
		)
		return
	}
	s.admissionMu.Lock()
	err = s.applyMutationWithAudit("admin_api", "profile.delete", "runner_profile", name, map[string]any{"status": "deleted"}, func(tx state.Store) error {
		return tx.DeleteProfile(name)
	})
	s.admissionMu.Unlock()
	if err != nil {
		if writeMutationAuditError(w, err) {
			return
		}
		s.logger.Error("delete profile", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Info("profile deleted", "name", name)
	s.refreshMetrics()
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleMatchProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	var input profileMatchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid match payload")
		return
	}
	match, err := s.matchProfileForAdmission(input.RepositoryFullName, 0, input.Labels)
	if err != nil {
		s.logger.Error("match profile request failed", "repository", input.RepositoryFullName, "labels", input.Labels, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if match.Profile == nil {
		s.logger.Info("match profile result", "repository", input.RepositoryFullName, "labels", input.Labels, "matched", false, "reason", match.Reason)
	} else {
		s.logger.Info("match profile result", "repository", input.RepositoryFullName, "labels", input.Labels, "matched", true, "profile", match.Profile.Name)
	}
	writeJSON(w, http.StatusOK, match)
}
