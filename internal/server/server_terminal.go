package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type terminalCreateRequest struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

type terminalInputRequest struct {
	Data string `json:"data"`
}

type terminalResizeRequest struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

type userRunnerSiblingsResponse struct {
	Group string              `json:"group"`
	Jobs  []state.RunnerState `json:"jobs"`
}

type userRunnerJobGroupResponse struct {
	Key                   string              `json:"key"`
	Group                 string              `json:"group"`
	Repository            string              `json:"repository"`
	Title                 string              `json:"title"`
	Subtitle              string              `json:"subtitle"`
	UpdatedAt             time.Time           `json:"updated_at"`
	Jobs                  []state.RunnerState `json:"jobs"`
	CurrentJobs           []state.RunnerState `json:"current_jobs"`
	PreviousJobs          []state.RunnerState `json:"previous_jobs"`
	WorkflowRunIDs        []int64             `json:"workflow_run_ids"`
	HeadSHA               string              `json:"head_sha,omitempty"`
	HeadBranch            string              `json:"head_branch,omitempty"`
	PullRequestNumber     int64               `json:"pull_request_number,omitempty"`
	PullRequestTitle      string              `json:"pull_request_title,omitempty"`
	PullRequestTitleError string              `json:"pull_request_title_error,omitempty"`
}

type pullTitleResult struct {
	title     string
	errorText string
}

const maxPullTitleCacheItems = 512

func (s *Server) userRunnerState(w http.ResponseWriter, r *http.Request) (state.RunnerState, bool) {
	st, _, ok := s.userRunnerStateAndAccount(w, r)
	return st, ok
}

func (s *Server) userRunnerStateAndAccount(w http.ResponseWriter, r *http.Request) (state.RunnerState, int64, bool) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return state.RunnerState{}, 0, false
	}
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "runner not found")
		return state.RunnerState{}, 0, false
	}
	access, err := s.userAuthorizedRepositoryAccess(r.Context(), account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return state.RunnerState{}, 0, false
	}
	if hasRepositoryAccess(access, st.GitHubInstallationID, st.RepositoryFullName) {
		return userVisibleRunnerState(st), account.ID, true
	}
	writeError(w, http.StatusNotFound, "runner not found")
	return state.RunnerState{}, 0, false
}

func (s *Server) handleUserGetRunner(w http.ResponseWriter, r *http.Request) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleUserGetRunnerJobGroup(w http.ResponseWriter, r *http.Request) {
	st, states, ok := s.userRunnerStates(w, r)
	if !ok {
		return
	}
	group, ok := runnerJobGroupForRunner(st, states)
	if !ok {
		writeError(w, http.StatusNotFound, "job group not found")
		return
	}
	s.enrichUserRunnerJobGroup(r.Context(), &group)
	writeJSON(w, http.StatusOK, group)
}

func (s *Server) handleUserGetGitHubPullJobGroup(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number <= 0 {
		writeError(w, http.StatusBadRequest, "invalid pull request number")
		return
	}
	s.handleUserGetRunnerJobGroupByKey(w, r, fmt.Sprintf("pr:%s:%d", repositoryFromPath(r), number))
}

func (s *Server) handleUserGetGitHubRunJobGroup(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(r.PathValue("runID"), 10, 64)
	if err != nil || runID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid workflow run id")
		return
	}
	s.handleUserGetRunnerJobGroupByKey(w, r, fmt.Sprintf("run:%s:%d", repositoryFromPath(r), runID))
}

func (s *Server) handleUserGetGitHubBranchJobGroup(w http.ResponseWriter, r *http.Request) {
	branch := strings.TrimSpace(r.PathValue("branch"))
	if branch == "" {
		branch = strings.TrimSpace(r.URL.Query().Get("branch"))
	}
	sha := strings.TrimSpace(r.PathValue("sha"))
	if branch == "" || sha == "" {
		writeError(w, http.StatusBadRequest, "branch and sha are required")
		return
	}
	s.handleUserGetRunnerJobGroupByKey(w, r, fmt.Sprintf("branch:%s:%s:%s", repositoryFromPath(r), branch, sha))
}

func (s *Server) handleUserGetRunnerJobGroupByKey(w http.ResponseWriter, r *http.Request, key string) {
	states, ok := s.userRunnerStatesForRequest(w, r)
	if !ok {
		return
	}
	for _, group := range runnerJobGroups(states) {
		if group.Key == key {
			s.enrichUserRunnerJobGroup(r.Context(), &group)
			writeJSON(w, http.StatusOK, group)
			return
		}
	}
	writeError(w, http.StatusNotFound, "job group not found")
}

func (s *Server) enrichUserRunnerJobGroup(ctx context.Context, group *userRunnerJobGroupResponse) {
	if group == nil || group.Group != "pull_request" || group.Repository == "" || group.PullRequestNumber == 0 {
		return
	}
	cacheKey := fmt.Sprintf("%s#%d", group.Repository, group.PullRequestNumber)
	if title, errorText, ok := s.cachedPullTitle(cacheKey); ok {
		group.PullRequestTitle = title
		group.PullRequestTitleError = errorText
		return
	}
	value, _, _ := s.pullTitleGroup.Do(cacheKey, func() (any, error) {
		if title, errorText, ok := s.cachedPullTitle(cacheKey); ok {
			return pullTitleResult{title: title, errorText: errorText}, nil
		}
		if s.gh == nil {
			errorText := "github client is not configured"
			s.cachePullTitle(cacheKey, "", errorText)
			return pullTitleResult{errorText: errorText}, nil
		}
		apiCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		pull, err := s.gh.GetPullRequest(apiCtx, group.Repository, group.PullRequestNumber)
		if err != nil {
			issue, issueErr := s.gh.GetIssue(apiCtx, group.Repository, group.PullRequestNumber)
			if issueErr != nil {
				s.logger.DebugContext(apiCtx, "load github pull request title", "repository", group.Repository, "number", group.PullRequestNumber, "pull_error", err, "issue_error", issueErr)
				errorText := fmt.Sprintf("pull request title unavailable: %v; issue fallback: %v", err, issueErr)
				if !isContextCanceled(err) && !isContextCanceled(issueErr) {
					s.cachePullTitle(cacheKey, "", errorText)
				}
				return pullTitleResult{errorText: errorText}, nil
			}
			title := strings.TrimSpace(issue.Title)
			s.cachePullTitle(cacheKey, title, "")
			return pullTitleResult{title: title}, nil
		}
		title := strings.TrimSpace(pull.Title)
		s.cachePullTitle(cacheKey, title, "")
		return pullTitleResult{title: title}, nil
	})
	result, ok := value.(pullTitleResult)
	if !ok {
		group.PullRequestTitleError = "pull request title unavailable"
		return
	}
	group.PullRequestTitle = result.title
	group.PullRequestTitleError = result.errorText
}

func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Server) cachedPullTitle(key string) (string, string, bool) {
	s.pullTitleMu.Lock()
	defer s.pullTitleMu.Unlock()
	if s.pullTitleCache == nil {
		return "", "", false
	}
	cached, ok := s.pullTitleCache[key]
	if !ok || time.Now().UTC().After(cached.expiresAt) {
		if ok {
			delete(s.pullTitleCache, key)
		}
		return "", "", false
	}
	return cached.title, cached.errorText, true
}

func (s *Server) cachePullTitle(key, title, errorText string) {
	s.pullTitleMu.Lock()
	defer s.pullTitleMu.Unlock()
	if s.pullTitleCache == nil {
		s.pullTitleCache = map[string]cachedPullTitle{}
	}
	now := time.Now().UTC()
	for cacheKey, cached := range s.pullTitleCache {
		if now.After(cached.expiresAt) {
			delete(s.pullTitleCache, cacheKey)
		}
	}
	for len(s.pullTitleCache) >= maxPullTitleCacheItems {
		var oldestKey string
		var oldestExpiresAt time.Time
		for cacheKey, cached := range s.pullTitleCache {
			if oldestKey == "" || cached.expiresAt.Before(oldestExpiresAt) {
				oldestKey = cacheKey
				oldestExpiresAt = cached.expiresAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.pullTitleCache, oldestKey)
	}
	s.pullTitleCache[key] = cachedPullTitle{
		title:     title,
		errorText: errorText,
		expiresAt: now.Add(5 * time.Minute),
	}
}

func (s *Server) handleUserListRunnerSiblings(w http.ResponseWriter, r *http.Request) {
	st, states, ok := s.userRunnerStates(w, r)
	if !ok {
		return
	}
	group, siblings := runnerSiblings(st, states)
	writeJSON(w, http.StatusOK, userRunnerSiblingsResponse{Group: group, Jobs: siblings})
}

func (s *Server) userRunnerStates(w http.ResponseWriter, r *http.Request) (state.RunnerState, []state.RunnerState, bool) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return state.RunnerState{}, nil, false
	}
	states, ok := s.userRunnerStatesForRequest(w, r)
	if !ok {
		return state.RunnerState{}, nil, false
	}
	return st, states, true
}

func (s *Server) userRunnerStatesForRequest(w http.ResponseWriter, r *http.Request) ([]state.RunnerState, bool) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return nil, false
	}
	access, err := s.userAuthorizedRepositoryAccess(r.Context(), account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return nil, false
	}
	states, _, err := s.store.ListStatesForGitHubInstallationRepositories(access, 500, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	for i := range states {
		states[i] = userVisibleRunnerState(states[i])
	}
	return states, true
}

func userVisibleRunnerState(st state.RunnerState) state.RunnerState {
	st.ResolvedTemplateID = ""
	return st
}

func (s *Server) handleUserGetRunnerLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.userRunnerState(w, r); !ok {
		return
	}
	writeRunnerLog(w, s.store, r.PathValue("id"), r.PathValue("name"))
}

func (s *Server) handleUserCreateRunnerTerminal(w http.ResponseWriter, r *http.Request) {
	st, accountID, ok := s.userRunnerStateAndAccount(w, r)
	if !ok {
		return
	}
	if !runnerTerminalAvailable(st) {
		writeError(w, http.StatusConflict, "terminal is only available while a sandbox job is active")
		return
	}
	service, snapshot, err := s.sandboxServiceAndConfigForRunnerRequestContext(r.Context(), state.RunnerRequest{ID: st.ID, SandboxAPIURL: st.SandboxAPIURL, SandboxAPIKeyEncrypted: st.SandboxAPIKeyEncrypted, GitHubInstallationID: st.GitHubInstallationID})
	if err != nil {
		writeError(w, http.StatusBadGateway, "terminal provider unavailable")
		return
	}
	shared, ok := service.(sandboxrunner.SharedTerminalService)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "terminal provider does not support shared sessions")
		return
	}
	session := state.TerminalSession{ID: newID(), AccountID: accountID, Kind: "runner", ResourceID: st.ID, BindingKey: runnerTerminalKey(st, accountID), SandboxID: st.SandboxID, SandboxAPIURL: snapshot.APIURL, SandboxAPIKeyEncrypted: snapshot.EncryptedAPIKey}
	s.createSharedTerminal(w, r, session, shared)
}

func (s *Server) handleUserRunnerTerminalEvents(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedTerminalSession(w, r, true)
	if !ok {
		return
	}
	s.streamTerminalSession(w, r, session)
}

// HTTP Server.WriteTimeout is absolute, including for an idle SSE connection.
// Renew it before headers and each write, while still bounding blocked writes.
func prepareTerminalStreamWrite(controller *http.ResponseController) bool {
	err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return err == nil || errors.Is(err, http.ErrNotSupported)
}

func (s *Server) handleUserRunnerTerminalInput(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedTerminalSession(w, r, true)
	if !ok {
		return
	}
	s.sendTerminalInput(w, r, session)
}

func (s *Server) sendTerminalInput(w http.ResponseWriter, r *http.Request, session *state.TerminalSession) {
	var input terminalInputRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid terminal input")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.store.RenewTerminal(ctx, session.ID); err != nil {
		writeError(w, http.StatusConflict, "terminal is no longer available")
		return
	}
	service, err := s.sharedTerminalService(*session)
	if err == nil {
		err = service.InputPTY(ctx, session.SandboxID, terminalReference(*session), []byte(input.Data))
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "terminal operation failed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

func (s *Server) handleUserRunnerTerminalResize(w http.ResponseWriter, r *http.Request) {
	session, ok := s.authorizedTerminalSession(w, r, true)
	if !ok {
		return
	}
	s.resizeTerminalSession(w, r, session)
}

func (s *Server) resizeTerminalSession(w http.ResponseWriter, r *http.Request, session *state.TerminalSession) {
	var input terminalResizeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid terminal size")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.store.RenewTerminal(ctx, session.ID); err != nil {
		writeError(w, http.StatusConflict, "terminal is no longer available")
		return
	}
	service, err := s.sharedTerminalService(*session)
	if err == nil {
		err = service.ResizePTY(ctx, session.SandboxID, terminalReference(*session), sandboxrunner.PtySize{Cols: input.Cols, Rows: input.Rows})
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "terminal operation failed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

func (s *Server) handleUserCloseRunnerTerminal(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizedTerminalSession(w, r, false); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.closeSharedTerminal(ctx, r.PathValue("sessionID")); err != nil && !errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Bind to the persisted attempt timestamp, also available on older requests.
func runnerTerminalKey(st state.RunnerState, accountID int64) string {
	return fmt.Sprintf("account:%d:runner:%q:attempt:%s:installation:%d:repository:%q",
		accountID, st.ID, st.LastAttemptAt.UTC().Format(time.RFC3339Nano), st.GitHubInstallationID, st.RepositoryFullName)
}

func (s *Server) authorizedTerminalSession(w http.ResponseWriter, r *http.Request, requireAvailable bool) (*state.TerminalSession, bool) {
	st, accountID, ok := s.userRunnerStateAndAccount(w, r)
	if !ok {
		return nil, false
	}
	session, err := s.store.GetTerminal(r.Context(), r.PathValue("sessionID"))
	if err != nil || session.Kind != "runner" || session.AccountID != accountID || session.ResourceID != st.ID || session.BindingKey != runnerTerminalKey(st, accountID) || session.SandboxID != st.SandboxID {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return nil, false
	}
	if requireAvailable && (!runnerTerminalAvailable(st) || session.Status != "open") {
		_ = s.closeSharedTerminal(r.Context(), session.ID)
		writeError(w, http.StatusConflict, "terminal is no longer available")
		return nil, false
	}
	return &session, true
}

func runnerTerminalAvailable(st state.RunnerState) bool {
	if strings.TrimSpace(st.SandboxID) == "" {
		return false
	}
	switch st.Status {
	case state.StatusCreating, state.StatusRunning, state.StatusStopping:
		return true
	default:
		return false
	}
}

func runnerJobGroups(states []state.RunnerState) []userRunnerJobGroupResponse {
	prByRepositoryAndSHA := map[string]int64{}
	for _, st := range states {
		if st.RepositoryFullName != "" && st.HeadSHA != "" && st.PullRequestNumber != 0 {
			prByRepositoryAndSHA[st.RepositoryFullName+":"+st.HeadSHA] = st.PullRequestNumber
		}
	}

	groupsByKey := map[string]*userRunnerJobGroupResponse{}
	for _, st := range states {
		if !isUserVisibleRunnerJob(st) {
			continue
		}
		repository := st.RepositoryFullName
		if repository == "" {
			repository = "unknown/repository"
		}
		pullRequestNumber := st.PullRequestNumber
		if pullRequestNumber == 0 && st.HeadSHA != "" {
			pullRequestNumber = prByRepositoryAndSHA[repository+":"+st.HeadSHA]
		}
		seed := runnerJobGroupSeed(st, repository, pullRequestNumber)
		current, ok := groupsByKey[seed.Key]
		if !ok {
			group := seed
			group.Jobs = append([]state.RunnerState(nil), seed.Jobs...)
			groupsByKey[group.Key] = &group
			continue
		}
		current.Jobs = append(current.Jobs, st)
		if st.UpdatedAt.After(current.UpdatedAt) {
			current.UpdatedAt = st.UpdatedAt
			current.Subtitle = seed.Subtitle
			if st.HeadSHA != "" {
				current.HeadSHA = st.HeadSHA
			}
			if st.HeadBranch != "" {
				current.HeadBranch = st.HeadBranch
			}
		}
		if st.HeadSHA != "" && current.HeadSHA == "" {
			current.HeadSHA = st.HeadSHA
		}
		if st.HeadBranch != "" && current.HeadBranch == "" {
			current.HeadBranch = st.HeadBranch
		}
		if st.WorkflowRunID != 0 && !hasInt64(current.WorkflowRunIDs, st.WorkflowRunID) {
			current.WorkflowRunIDs = append(current.WorkflowRunIDs, st.WorkflowRunID)
		}
	}

	groups := make([]userRunnerJobGroupResponse, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		sort.Slice(group.WorkflowRunIDs, func(i, j int) bool {
			return group.WorkflowRunIDs[i] > group.WorkflowRunIDs[j]
		})
		group.Jobs = orderRunnerJobs(group.Jobs)
		group.CurrentJobs = currentRunnerGroupJobs(*group)
		group.PreviousJobs = previousRunnerGroupJobs(group.Jobs, group.CurrentJobs)
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].UpdatedAt.After(groups[j].UpdatedAt)
	})
	return groups
}

func runnerJobGroupForRunner(current state.RunnerState, states []state.RunnerState) (userRunnerJobGroupResponse, bool) {
	for _, group := range runnerJobGroups(states) {
		for _, job := range group.Jobs {
			if job.ID == current.ID {
				return group, true
			}
		}
	}
	return userRunnerJobGroupResponse{}, false
}

func runnerJobGroupSeed(st state.RunnerState, repository string, pullRequestNumber int64) userRunnerJobGroupResponse {
	workflowRunIDs := []int64{}
	if st.WorkflowRunID != 0 {
		workflowRunIDs = append(workflowRunIDs, st.WorkflowRunID)
	}
	if pullRequestNumber != 0 {
		return userRunnerJobGroupResponse{
			Key:               fmt.Sprintf("pr:%s:%d", repository, pullRequestNumber),
			Group:             "pull_request",
			Repository:        repository,
			Title:             fmt.Sprintf("PR #%d", pullRequestNumber),
			Subtitle:          firstNonEmpty(st.HeadBranch, shortSHA(st.HeadSHA), st.WorkflowName, "Pull request checks"),
			UpdatedAt:         st.UpdatedAt,
			Jobs:              []state.RunnerState{st},
			WorkflowRunIDs:    workflowRunIDs,
			HeadSHA:           st.HeadSHA,
			HeadBranch:        st.HeadBranch,
			PullRequestNumber: pullRequestNumber,
		}
	}
	if st.HeadSHA != "" {
		branch := firstNonEmpty(st.HeadBranch, "detached")
		return userRunnerJobGroupResponse{
			Key:            fmt.Sprintf("branch:%s:%s:%s", repository, branch, st.HeadSHA),
			Group:          "branch",
			Repository:     repository,
			Title:          firstNonEmpty(st.HeadBranch, "Commit "+shortSHA(st.HeadSHA)),
			Subtitle:       firstNonEmpty(shortSHA(st.HeadSHA), st.WorkflowName, "Branch checks"),
			UpdatedAt:      st.UpdatedAt,
			Jobs:           []state.RunnerState{st},
			WorkflowRunIDs: workflowRunIDs,
			HeadSHA:        st.HeadSHA,
			HeadBranch:     st.HeadBranch,
		}
	}
	if st.WorkflowRunID != 0 {
		return userRunnerJobGroupResponse{
			Key:            fmt.Sprintf("run:%s:%d", repository, st.WorkflowRunID),
			Group:          "workflow_run",
			Repository:     repository,
			Title:          firstNonEmpty(st.WorkflowName, "Workflow run"),
			Subtitle:       fmt.Sprintf("Run %d", st.WorkflowRunID),
			UpdatedAt:      st.UpdatedAt,
			Jobs:           []state.RunnerState{st},
			WorkflowRunIDs: workflowRunIDs,
		}
	}
	return userRunnerJobGroupResponse{
		Key:            fmt.Sprintf("manual:%s:%s", repository, st.ID),
		Group:          "manual",
		Repository:     repository,
		Title:          "Manual runner",
		Subtitle:       firstNonEmpty(st.ProfileName, st.RunnerName, st.ID),
		UpdatedAt:      st.UpdatedAt,
		Jobs:           []state.RunnerState{st},
		WorkflowRunIDs: workflowRunIDs,
	}
}

func isUserVisibleRunnerJob(st state.RunnerState) bool {
	return !(st.FailureStage == "admission" && st.FailureReason == "profile_labels_not_matched")
}

func orderRunnerJobs(jobs []state.RunnerState) []state.RunnerState {
	ordered := append([]state.RunnerState(nil), jobs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].WorkflowRunID != ordered[j].WorkflowRunID {
			return ordered[i].WorkflowRunID > ordered[j].WorkflowRunID
		}
		return ordered[i].UpdatedAt.After(ordered[j].UpdatedAt)
	})
	return ordered
}

func currentRunnerGroupJobs(group userRunnerJobGroupResponse) []state.RunnerState {
	if group.HeadSHA != "" {
		current := make([]state.RunnerState, 0, len(group.Jobs))
		for _, job := range group.Jobs {
			if job.HeadSHA == group.HeadSHA {
				current = append(current, job)
			}
		}
		if len(current) > 0 {
			return current
		}
	}
	if len(group.WorkflowRunIDs) > 0 {
		latestRunID := group.WorkflowRunIDs[0]
		current := make([]state.RunnerState, 0, len(group.Jobs))
		for _, job := range group.Jobs {
			if job.WorkflowRunID == latestRunID {
				current = append(current, job)
			}
		}
		if len(current) > 0 {
			return current
		}
	}
	return group.Jobs
}

func previousRunnerGroupJobs(jobs, currentJobs []state.RunnerState) []state.RunnerState {
	currentIDs := map[string]struct{}{}
	for _, job := range currentJobs {
		currentIDs[job.ID] = struct{}{}
	}
	previous := make([]state.RunnerState, 0, len(jobs))
	for _, job := range jobs {
		if _, ok := currentIDs[job.ID]; !ok {
			previous = append(previous, job)
		}
	}
	return previous
}

func runnerSiblings(current state.RunnerState, states []state.RunnerState) (string, []state.RunnerState) {
	group := "repository"
	if current.PullRequestNumber != 0 {
		group = "pull_request"
	} else if current.WorkflowRunID != 0 {
		group = "workflow_run"
	}
	matches := func(candidate state.RunnerState) bool {
		if candidate.RepositoryFullName != current.RepositoryFullName {
			return false
		}
		if group == "pull_request" {
			return candidate.PullRequestNumber == current.PullRequestNumber
		}
		if group == "workflow_run" {
			return candidate.WorkflowRunID == current.WorkflowRunID
		}
		return true
	}
	siblings := make([]state.RunnerState, 0, len(states))
	for _, candidate := range states {
		if matches(candidate) {
			siblings = append(siblings, candidate)
		}
	}
	if !hasRunnerStateID(siblings, current.ID) {
		siblings = append(siblings, current)
	}
	sort.SliceStable(siblings, func(i, j int) bool {
		if group == "repository" {
			return siblings[i].CreatedAt.After(siblings[j].CreatedAt)
		}
		if siblings[i].WorkflowRunID != siblings[j].WorkflowRunID {
			return siblings[i].WorkflowRunID > siblings[j].WorkflowRunID
		}
		return siblings[i].CreatedAt.Before(siblings[j].CreatedAt)
	})
	return group, siblings
}

func repositoryFromPath(r *http.Request) string {
	return strings.TrimSpace(r.PathValue("owner")) + "/" + strings.TrimSpace(r.PathValue("repo"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func shortSHA(value string) string {
	if len(value) > 7 {
		return value[:7]
	}
	return value
}

func hasRunnerStateID(states []state.RunnerState, id string) bool {
	for _, st := range states {
		if st.ID == id {
			return true
		}
	}
	return false
}

func hasInt64(values []int64, needle int64) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func writeTerminalEvent(w io.Writer, data []byte, offset uint64) error {
	payload, _ := json.Marshal(string(data))
	_, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", offset, payload)
	return err
}
