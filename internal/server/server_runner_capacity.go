package server

import (
	"context"
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

		if strings.HasPrefix(st.LastErrorCode, "runner_capacity_") {
			st.LastErrorCode, st.LastErrorMessage, st.LastErrorRetryable = "", "", false
			st.NextRetryAt = time.Time{}
		}
		st.Status = state.StatusCreating
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
	return profile, stage, err
}

func (s *Server) runnerCapacityLimit(tx state.Store, req state.RunnerRequest, profile state.RunnerProfile, timings *runnerTimings) (string, error) {
	repository := strings.ToLower(strings.TrimSpace(req.RepositoryFullName))
	owner, _, found := strings.Cut(repository, "/")
	if !found {
		owner = ""
	}
	for _, check := range []struct {
		stage string
		limit int
		count func() (int, error)
	}{
		{"global_capacity_check", s.cfg.MaxConcurrentRunners, tx.InFlightCount},
		{"profile_capacity_check", profile.MaxConcurrency, func() (int, error) { return inFlightForRunnerProfile(tx, req, profile.Name) }},
		{"account_capacity_check", s.cfg.RunnerAccountConcurrency[owner], func() (int, error) { return tx.InFlightCountForRepositoryOwner(owner) }},
		{"repository_capacity_check", s.cfg.RunnerRepositoryConcurrency[repository], func() (int, error) { return tx.InFlightCountForRepository(repository) }},
	} {
		if check.limit <= 0 && check.stage != "global_capacity_check" {
			continue
		}
		started := time.Now()
		count, err := check.count()
		timings.record(check.stage, started)
		if err != nil || count >= check.limit {
			return check.stage, err
		}
	}
	return "", nil
}
