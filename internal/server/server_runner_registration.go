package server

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type runnerRegistrationLookup struct {
	LookupStatus       string                   `json:"lookup_status"`
	RepositoryFullName string                   `json:"repository_full_name"`
	RunnerName         string                   `json:"runner_name"`
	Scope              string                   `json:"scope"`
	Target             string                   `json:"target"`
	RunnerGroup        string                   `json:"runner_group,omitempty"`
	ObservedAt         time.Time                `json:"observed_at"`
	Runner             *runnerRegistrationState `json:"runner,omitempty"`
}

type runnerRegistrationState struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
}

func (s *Server) handleRunnerRegistrationLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.requireAdminAuth(w, r) {
		return
	}
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		s.writeRunnerRequestLookupError(w, r.PathValue("id"), err)
		return
	}
	if st.RepositoryFullName == "" || strings.TrimSpace(st.RunnerName) == "" {
		writeError(w, http.StatusConflict, "request has no GitHub Runner registration target")
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub Runner registration lookup is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// This path must remain independent of cleanup: no lease, state, audit or
	// event writes, and no Sandbox calls. Reuse the server's installation auth.
	runner, err := s.gh.FindRunnerByName(ctx, st.RepositoryFullName, st.RunnerGroup, st.RunnerName)
	if err != nil {
		// Provider errors can contain URLs or credentials. Do not expose or log them.
		writeError(w, http.StatusBadGateway, "GitHub Runner registration lookup failed; registration is unknown")
		return
	}
	out := runnerRegistrationLookup{
		LookupStatus: "not_found", RepositoryFullName: st.RepositoryFullName,
		RunnerName: st.RunnerName, Scope: "repository", Target: st.RepositoryFullName,
		RunnerGroup: st.RunnerGroup, ObservedAt: time.Now().UTC(),
	}
	if strings.TrimSpace(st.RunnerGroup) != "" {
		out.Scope = "organization"
		out.Target, _, _ = strings.Cut(st.RepositoryFullName, "/")
	}
	if runner != nil {
		out.LookupStatus = "found"
		status := "unknown"
		if runner.Status == "online" || runner.Status == "offline" {
			status = runner.Status
		}
		out.Runner = &runnerRegistrationState{ID: runner.ID, Status: status, Busy: runner.Busy}
	}
	writeJSON(w, http.StatusOK, out)
}
