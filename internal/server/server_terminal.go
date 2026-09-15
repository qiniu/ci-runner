package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
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

const (
	terminalIdleCloseDelay  = 30 * time.Second
	maxGitHubLogFileBytes   = 8 << 20
	maxGitHubLogOutputBytes = 16 << 20
	maxPullTitleCacheItems  = 512
)

type terminalHub struct {
	mu       sync.Mutex
	sessions map[string]*terminalSession
	logger   *slog.Logger
}

type terminalSession struct {
	id        string
	requestID string
	sandboxID string
	terminal  sandboxrunner.TerminalSession
	createdAt time.Time

	mu       sync.Mutex
	buffer   []byte
	watchers map[chan []byte]struct{}
	closed   bool
}

func newTerminalHub(logger *slog.Logger) *terminalHub {
	return &terminalHub{
		sessions: map[string]*terminalSession{},
		logger:   logger,
	}
}

func (h *terminalHub) Add(requestID, sandboxID string, terminal sandboxrunner.TerminalSession) *terminalSession {
	session := &terminalSession{
		id:        newID(),
		requestID: requestID,
		sandboxID: sandboxID,
		terminal:  terminal,
		createdAt: time.Now().UTC(),
		watchers:  map[chan []byte]struct{}{},
	}
	h.mu.Lock()
	h.sessions[session.id] = session
	h.mu.Unlock()
	return session
}

func (h *terminalHub) Get(sessionID string) (*terminalSession, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[sessionID]
	return session, ok
}

func (h *terminalHub) CloseSession(ctx context.Context, sessionID string) error {
	h.mu.Lock()
	session, ok := h.sessions[sessionID]
	if ok {
		delete(h.sessions, sessionID)
	}
	h.mu.Unlock()
	if !ok {
		return state.ErrNotFound
	}
	session.closeWatchers()
	return session.terminal.Close(ctx)
}

func (h *terminalHub) CloseSessionWhenIdle(sessionID string, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		session, closed, err := h.closeSessionIfIdle(ctx, sessionID)
		if !closed || session == nil {
			return
		}
		if err != nil && h.logger != nil {
			h.logger.Warn("close idle terminal session", "session_id", sessionID, "request_id", session.requestID, "error", err)
		}
	}()
}

func (h *terminalHub) closeSessionIfIdle(ctx context.Context, sessionID string) (*terminalSession, bool, error) {
	h.mu.Lock()
	session, ok := h.sessions[sessionID]
	if !ok {
		h.mu.Unlock()
		return nil, false, state.ErrNotFound
	}
	session.mu.Lock()
	if session.closed || len(session.watchers) > 0 {
		session.mu.Unlock()
		h.mu.Unlock()
		return session, false, nil
	}
	delete(h.sessions, sessionID)
	session.closed = true
	session.mu.Unlock()
	h.mu.Unlock()
	return session, true, session.terminal.Close(ctx)
}

func (h *terminalHub) Close() {
	h.mu.Lock()
	sessions := make([]*terminalSession, 0, len(h.sessions))
	for id, session := range h.sessions {
		sessions = append(sessions, session)
		delete(h.sessions, id)
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, session := range sessions {
		session.closeWatchers()
		wg.Add(1)
		go func(session *terminalSession) {
			defer wg.Done()
			if err := session.terminal.Close(ctx); err != nil && h.logger != nil {
				h.logger.Warn("close terminal session", "session_id", session.id, "request_id", session.requestID, "error", err)
			}
		}(session)
	}
	wg.Wait()
}

func (s *terminalSession) append(data []byte) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	s.buffer = append(s.buffer, data...)
	const maxBuffer = 64 << 10
	if len(s.buffer) > maxBuffer*2 {
		s.buffer = append([]byte(nil), s.buffer[len(s.buffer)-maxBuffer:]...)
	}
	for watcher := range s.watchers {
		select {
		case watcher <- append([]byte(nil), data...):
		default:
		}
	}
	s.mu.Unlock()
}

func (s *terminalSession) Subscribe() ([]byte, <-chan []byte, func()) {
	ch := make(chan []byte, 32)
	s.mu.Lock()
	initial := append([]byte(nil), s.buffer...)
	if s.closed {
		close(ch)
		s.mu.Unlock()
		return initial, ch, func() {}
	}
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		if _, ok := s.watchers[ch]; ok {
			delete(s.watchers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
	return initial, ch, cancel
}

func (s *terminalSession) closeWatchers() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for watcher := range s.watchers {
		close(watcher)
		delete(s.watchers, watcher)
	}
	s.mu.Unlock()
}

func (s *Server) userRunnerState(w http.ResponseWriter, r *http.Request) (state.RunnerState, bool) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return state.RunnerState{}, false
	}
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "runner not found")
		return state.RunnerState{}, false
	}
	access, err := s.userAuthorizedRepositoryAccess(r.Context(), account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return state.RunnerState{}, false
	}
	if hasRepositoryAccess(access, st.GitHubInstallationID, st.RepositoryFullName) {
		return userVisibleRunnerState(st), true
	}
	writeError(w, http.StatusNotFound, "runner not found")
	return state.RunnerState{}, false
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

func (s *Server) handleUserGetRunnerGitHubLog(w http.ResponseWriter, r *http.Request) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusBadGateway, "github client is not configured")
		return
	}
	if strings.TrimSpace(st.RepositoryFullName) == "" {
		writeError(w, http.StatusConflict, "runner has no github repository")
		return
	}
	jobID := st.AssignedJobID
	if jobID == 0 {
		jobID = st.WorkflowJobID
	}
	var (
		data        []byte
		contentType string
		err         error
	)
	if jobID != 0 {
		data, contentType, err = s.gh.DownloadWorkflowJobLogs(r.Context(), st.RepositoryFullName, jobID)
	} else if st.WorkflowRunID != 0 {
		data, contentType, err = s.gh.DownloadWorkflowRunLogs(r.Context(), st.RepositoryFullName, st.WorkflowRunID)
	} else {
		writeError(w, http.StatusConflict, "runner has no github workflow job or run id")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	text, err := formatGitHubActionsLog(data)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if strings.TrimSpace(text) == "" {
		text = "GitHub log is empty"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-GitHub-Log-Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, text)
}

func (s *Server) handleUserCreateRunnerTerminal(w http.ResponseWriter, r *http.Request) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return
	}
	if !runnerTerminalAvailable(st) {
		writeError(w, http.StatusConflict, "terminal is only available while a sandbox job is active")
		return
	}
	var input terminalCreateRequest
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid terminal size")
			return
		}
	}
	req := state.RunnerRequest{
		ID:                     st.ID,
		SandboxAPIURL:          st.SandboxAPIURL,
		SandboxAPIKeyEncrypted: st.SandboxAPIKeyEncrypted,
		GitHubInstallationID:   st.GitHubInstallationID,
	}
	svc, err := s.sandboxServiceForRunnerRequest(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var session *terminalSession
	var sessionMu sync.Mutex
	var pending [][]byte
	terminalCtx := s.loopCtx
	if terminalCtx == nil {
		terminalCtx = context.Background()
	}
	terminal, err := svc.StartTerminal(terminalCtx, st.SandboxID, sandboxrunner.PtySize{Cols: input.Cols, Rows: input.Rows}, func(data []byte) {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		if session != nil {
			session.append(data)
			return
		}
		pending = append(pending, append([]byte(nil), data...))
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	sessionMu.Lock()
	session = s.terminals.Add(st.ID, st.SandboxID, terminal)
	s.terminals.CloseSessionWhenIdle(session.id, terminalIdleCloseDelay)
	for _, data := range pending {
		session.append(data)
	}
	pending = nil
	sessionMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id": session.id,
		"pid":        terminal.PID(),
		"sandbox_id": st.SandboxID,
	})
}

func (s *Server) handleUserRunnerTerminalEvents(w http.ResponseWriter, r *http.Request) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return
	}
	session, ok := s.authorizedTerminalSession(w, r, st, true)
	if !ok {
		return
	}
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return
	}
	initial, ch, cancel := session.Subscribe()
	defer func() {
		cancel()
		s.terminals.CloseSessionWhenIdle(session.id, terminalIdleCloseDelay)
	}()
	if len(initial) > 0 {
		writeTerminalEvent(w, initial)
		if err := controller.Flush(); err != nil {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			writeTerminalEvent(w, data)
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleUserRunnerTerminalInput(w http.ResponseWriter, r *http.Request) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return
	}
	session, ok := s.authorizedTerminalSession(w, r, st, true)
	if !ok {
		return
	}
	var input terminalInputRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid terminal input")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := session.terminal.SendInput(ctx, []byte(input.Data)); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

func (s *Server) handleUserRunnerTerminalResize(w http.ResponseWriter, r *http.Request) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return
	}
	session, ok := s.authorizedTerminalSession(w, r, st, true)
	if !ok {
		return
	}
	var input terminalResizeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid terminal size")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := session.terminal.Resize(ctx, sandboxrunner.PtySize{Cols: input.Cols, Rows: input.Rows}); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

func (s *Server) handleUserCloseRunnerTerminal(w http.ResponseWriter, r *http.Request) {
	st, ok := s.userRunnerState(w, r)
	if !ok {
		return
	}
	if _, ok := s.authorizedTerminalSession(w, r, st, false); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.terminals.CloseSession(ctx, r.PathValue("sessionID")); err != nil && !errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) authorizedTerminalSession(w http.ResponseWriter, r *http.Request, st state.RunnerState, requireAvailable bool) (*terminalSession, bool) {
	sessionID := r.PathValue("sessionID")
	session, ok := s.terminals.Get(sessionID)
	if !ok || session.requestID != st.ID || session.sandboxID != st.SandboxID {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return nil, false
	}
	if requireAvailable && !runnerTerminalAvailable(st) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		if err := s.terminals.CloseSession(ctx, sessionID); err != nil && !errors.Is(err, state.ErrNotFound) {
			s.logger.DebugContext(ctx, "close inactive terminal session", "session", sessionID, "request", st.ID, "error", err)
		}
		writeError(w, http.StatusConflict, "terminal is no longer available")
		return nil, false
	}
	return session, true
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

func formatGitHubActionsLog(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if len(data) > maxGitHubLogOutputBytes {
			return string(data[:maxGitHubLogOutputBytes]) + fmt.Sprintf("\n[runnerd] GitHub log output truncated after %d bytes.\n", maxGitHubLogOutputBytes), nil
		}
		return string(data), nil
	}
	files := append([]*zip.File(nil), reader.File...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})
	var out strings.Builder
	for _, file := range files {
		if out.Len() >= maxGitHubLogOutputBytes {
			break
		}
		if file.FileInfo().IsDir() {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("open github log %s: %w", file.Name, err)
		}
		remainingLimit := int64(maxGitHubLogOutputBytes-out.Len()) + 1
		readLimit := int64(maxGitHubLogFileBytes) + 1
		if remainingLimit < readLimit {
			readLimit = remainingLimit
		}
		chunk, readErr := io.ReadAll(io.LimitReader(rc, readLimit))
		closeErr := rc.Close()
		if readErr != nil {
			return "", fmt.Errorf("read github log %s: %w", file.Name, readErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close github log %s: %w", file.Name, closeErr)
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString("===== ")
		out.WriteString(file.Name)
		out.WriteString(" =====\n")
		fileTruncated := len(chunk) > maxGitHubLogFileBytes
		if fileTruncated {
			chunk = chunk[:maxGitHubLogFileBytes]
		}
		outputTruncated := false
		remaining := maxGitHubLogOutputBytes - out.Len()
		if remaining <= 0 {
			out.WriteString(fmt.Sprintf("[runnerd] GitHub log output truncated after %d bytes.\n", maxGitHubLogOutputBytes))
			break
		}
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
			outputTruncated = true
		}
		out.Write(chunk)
		if len(chunk) == 0 || chunk[len(chunk)-1] != '\n' {
			out.WriteByte('\n')
		}
		if fileTruncated {
			out.WriteString(fmt.Sprintf("[runnerd] GitHub log file truncated after %d bytes.\n", maxGitHubLogFileBytes))
		}
		if outputTruncated || out.Len() >= maxGitHubLogOutputBytes {
			out.WriteString(fmt.Sprintf("[runnerd] GitHub log output truncated after %d bytes.\n", maxGitHubLogOutputBytes))
			break
		}
	}
	return out.String(), nil
}

func writeTerminalEvent(w io.Writer, data []byte) {
	payload, _ := json.Marshal(string(data))
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}
