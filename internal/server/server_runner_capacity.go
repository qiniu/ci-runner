package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// admitQueuedRunner reloads the spec, checks every applicable capacity limit,
// and consumes the queued claim while the transaction owns the admission guard.
// A nonempty stage identifies a deferral or the operation which failed.
func (s *Server) admitQueuedRunner(ctx context.Context, req state.RunnerRequest, st *state.RunnerState, timings *runnerTimings, started time.Time) (state.RunnerProfile, string, error) {
	stage := "admission_lock_wait"
	var profile state.RunnerProfile
	err := s.store.WithRunnerAdmission(ctx, func(tx state.Store) error {
		timings.record(stage, started)
		stage = "profile_lookup"
		started = time.Now()
		var err error
		profile, err = profileForRunnerRequest(tx, req)
		timings.record(stage, started)
		if err != nil {
			return fmt.Errorf("load profile %q: %w", req.ProfileName, err)
		}
		stage = "profile_validation"
		if err := validateRequestedProfile(profile, req.RequestedLabels); err != nil {
			return err
		}

		stage, err = s.runnerCapacityLimit(tx, req, profile, timings)
		if stage != "" || err != nil {
			return err
		}

		if strings.HasPrefix(st.LastErrorCode, "runner_capacity_") || st.LastErrorCode == "runner_admission_unavailable" {
			st.LastErrorCode, st.LastErrorMessage, st.LastErrorRetryable = "", "", false
			st.NextRetryAt = time.Time{}
		}
		st.Status = state.StatusCreating
		st.ProvisionAttemptID = rand.Text()
		if sandboxrunner.PlatformForLabels(req.Labels) == sandboxrunner.PlatformWindows {
			attemptAt := time.Now().UTC().Truncate(time.Millisecond)
			if !attemptAt.After(st.LastAttemptAt) {
				attemptAt = st.LastAttemptAt.Truncate(time.Millisecond).Add(time.Millisecond)
			}
			st.LastAttemptAt = attemptAt
		}
		stage = "creating_state_persist"
		started = time.Now()
		err = tx.WriteQueuedClaim(*st, st.LeaseOwner)
		timings.record(stage, started)
		if err == nil {
			stage = ""
		}
		return err
	})
	if err != nil && stage == "admission_lock_wait" {
		timings.record(stage, started)
	}
	if stage == "" {
		if err != nil {
			stage = "admission_commit"
		} else {
			st.Version++ // WriteQueuedClaim committed the creating row.
		}
	}
	return profile, stage, err
}

func (s *Server) runnerCapacityLimit(tx state.Store, req state.RunnerRequest, profile state.RunnerProfile, timings *runnerTimings) (string, error) {
	repository := strings.ToLower(strings.TrimSpace(req.RepositoryFullName))
	owner, _, found := strings.Cut(repository, "/")
	if !found {
		owner = ""
	}
	req.ProfileName = profile.Name
	started := time.Now()
	counts, err := tx.RunnerCapacityCounts(req)
	timings.record("capacity_count", started)
	if err != nil {
		return "capacity_count", err
	}
	for _, check := range []struct {
		stage string
		limit int
		count int
	}{
		{"global_capacity_check", s.cfg.MaxConcurrentRunners, counts.Global},
		{"profile_capacity_check", profile.MaxConcurrency, counts.Profile},
		{"account_capacity_check", s.cfg.RunnerAccountConcurrency[owner], counts.Account},
		{"repository_capacity_check", s.cfg.RunnerRepositoryConcurrency[repository], counts.Repository},
	} {
		if check.limit <= 0 && check.stage != "global_capacity_check" {
			continue
		}
		if check.count >= check.limit {
			return check.stage, nil
		}
	}
	return "", nil
}
