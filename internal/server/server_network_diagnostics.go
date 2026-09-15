package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

const (
	runnerNetworkDiagnosticTimeout      = 10 * time.Second
	runnerNetworkDiagnosticCooldown     = 30 * time.Second
	runnerNetworkDiagnosticStage        = "network_diagnostic"
	runnerNetworkDiagnosticBodyLimit    = 4 << 10
	runnerNetworkDiagnosticAddressLimit = 8
)

type networkDiagnosticRequest struct {
	Target string `json:"target"`
}

type networkDiagnosticAttempt struct {
	Running         bool
	LastCompletedAt time.Time
}

func (s *Server) handleRunnerNetworkDiagnostic(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}

	var input networkDiagnosticRequest
	r.Body = http.MaxBytesReader(w, r.Body, runnerNetworkDiagnosticBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid network diagnostic request")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid network diagnostic request")
		return
	}
	target, ok := runnerNetworkDiagnosticTarget(input.Target)
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported network diagnostic target")
		return
	}

	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		s.writeRunnerRequestLookupError(w, r.PathValue("id"), err)
		return
	}
	if !runnerNetworkDiagnosticAvailable(st) {
		writeError(w, http.StatusConflict, "network diagnostics require a running runner sandbox")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runnerNetworkDiagnosticTimeout)
	defer cancel()
	req, err := s.store.ReadRequest(st.ID)
	if err != nil {
		s.writeRunnerRequestLookupError(w, st.ID, err)
		return
	}
	service, err := s.sandboxServiceForRunnerRequest(ctx, req)
	if err != nil {
		s.logger.Warn("resolve Sandbox service for network diagnostic failed", "id", st.ID, "error_type", fmt.Sprintf("%T", err))
		writeError(w, http.StatusServiceUnavailable, "Sandbox service is unavailable for this runner request")
		return
	}
	diagnosticService, ok := service.(sandboxrunner.NetworkDiagnosticService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "Sandbox service does not support network diagnostics")
		return
	}

	attemptKey := runnerNetworkDiagnosticAttemptKey(st)
	release, retryAfter, ok := s.acquireRunnerNetworkDiagnostic(attemptKey, time.Now().UTC())
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "network diagnostic is already running or was requested recently")
		return
	}
	defer func() {
		release(time.Now().UTC())
	}()

	result, diagnosticErr := diagnosticService.RunNetworkDiagnostic(ctx, st.SandboxID, target)
	unlock := s.lockRunner(st.ID)
	current, readErr := s.store.ReadState(st.ID)
	if readErr != nil {
		unlock()
		s.logger.Error("reload runner request after network diagnostic", "id", st.ID, "error", readErr)
		writeError(w, http.StatusInternalServerError, "failed to reload runner request after network diagnostic")
		return
	}
	if !sameRunnerNetworkDiagnosticAttempt(st, current) {
		unlock()
		writeError(w, http.StatusConflict, "runner sandbox changed while the network diagnostic was running")
		return
	}
	if diagnosticErr != nil {
		host, _ := sandboxrunner.NetworkDiagnosticTargetHost(target)
		failure := map[string]any{
			"target":      target,
			"host":        host,
			"status":      "unavailable",
			"observed_at": time.Now().UTC(),
		}
		if errors.Is(diagnosticErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			failure["error"] = "probe_timed_out"
		} else {
			failure["error"] = "probe_unavailable"
		}
		s.appendRunnerNetworkDiagnosticEvent(st.ID, failure)
		unlock()
		s.logger.Warn("runner network diagnostic failed", "id", st.ID, "target", target, "timed_out", failure["error"] == "probe_timed_out", "error_type", fmt.Sprintf("%T", diagnosticErr))
		if failure["error"] == "probe_timed_out" {
			writeError(w, http.StatusGatewayTimeout, "network diagnostic timed out")
			return
		}
		writeError(w, http.StatusBadGateway, "network diagnostic failed")
		return
	}

	result = sanitizeRunnerNetworkDiagnosticResult(target, result, time.Now().UTC())
	s.appendRunnerNetworkDiagnosticEvent(st.ID, result)
	unlock()
	writeJSON(w, http.StatusOK, result)
}

func sanitizeRunnerNetworkDiagnosticResult(target sandboxrunner.NetworkDiagnosticTarget, result sandboxrunner.NetworkDiagnosticResult, observedAt time.Time) sandboxrunner.NetworkDiagnosticResult {
	result.Target = target
	result.Host, _ = sandboxrunner.NetworkDiagnosticTargetHost(target)

	addresses := make([]string, 0, min(len(result.DNSAddresses), runnerNetworkDiagnosticAddressLimit))
	seen := make(map[string]struct{}, len(result.DNSAddresses))
	for _, value := range result.DNSAddresses {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			continue
		}
		canonical := ip.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		addresses = append(addresses, canonical)
		if len(addresses) == runnerNetworkDiagnosticAddressLimit {
			break
		}
	}
	result.DNSAddresses = addresses
	if ip := net.ParseIP(strings.TrimSpace(result.ConnectedIP)); ip != nil {
		result.ConnectedIP = ip.String()
	} else {
		result.ConnectedIP = ""
	}
	if result.HTTPStatus < 100 || result.HTTPStatus > 599 {
		result.HTTPStatus = 0
	}
	result.Timings.DNSMS = boundedRunnerNetworkDiagnosticMilliseconds(result.Timings.DNSMS)
	result.Timings.ConnectMS = boundedRunnerNetworkDiagnosticMilliseconds(result.Timings.ConnectMS)
	result.Timings.TLSMS = boundedRunnerNetworkDiagnosticMilliseconds(result.Timings.TLSMS)
	result.Timings.FirstByteMS = boundedRunnerNetworkDiagnosticMilliseconds(result.Timings.FirstByteMS)
	result.Timings.TotalMS = boundedRunnerNetworkDiagnosticMilliseconds(result.Timings.TotalMS)
	result.Error = sandboxrunner.SanitizeNetworkDiagnosticError(result.Error)
	if result.ObservedAt.IsZero() {
		result.ObservedAt = observedAt.UTC()
	} else {
		result.ObservedAt = result.ObservedAt.UTC()
	}
	return result
}

func boundedRunnerNetworkDiagnosticMilliseconds(value int64) int64 {
	if value < 0 {
		return 0
	}
	limit := runnerNetworkDiagnosticTimeout.Milliseconds()
	if value > limit {
		return limit
	}
	return value
}

func runnerNetworkDiagnosticTarget(value string) (sandboxrunner.NetworkDiagnosticTarget, bool) {
	target := sandboxrunner.NetworkDiagnosticTarget(strings.TrimSpace(value))
	switch target {
	case sandboxrunner.NetworkDiagnosticTargetGitHubAPI,
		sandboxrunner.NetworkDiagnosticTargetUbuntuArchive,
		sandboxrunner.NetworkDiagnosticTargetLLVMAPT:
		return target, true
	default:
		return "", false
	}
}

func runnerNetworkDiagnosticAvailable(st state.RunnerState) bool {
	return st.Status == state.StatusRunning && strings.TrimSpace(st.SandboxID) != "" && st.ProcessPID != 0
}

func runnerNetworkDiagnosticAttemptKey(st state.RunnerState) string {
	return fmt.Sprintf("%s/%s/%d", st.ID, st.SandboxID, st.ProcessPID)
}

func sameRunnerNetworkDiagnosticAttempt(before, after state.RunnerState) bool {
	return runnerNetworkDiagnosticAvailable(after) &&
		before.ID == after.ID &&
		before.SandboxID == after.SandboxID &&
		before.ProcessPID == after.ProcessPID
}

func (s *Server) acquireRunnerNetworkDiagnostic(key string, now time.Time) (func(time.Time), time.Duration, bool) {
	s.networkDiagnosticMu.Lock()
	defer s.networkDiagnosticMu.Unlock()
	for existingKey, attempt := range s.networkDiagnosticAttempts {
		if !attempt.Running && !attempt.LastCompletedAt.IsZero() && now.Sub(attempt.LastCompletedAt) >= time.Minute {
			delete(s.networkDiagnosticAttempts, existingKey)
		}
	}
	if attempt, exists := s.networkDiagnosticAttempts[key]; exists {
		if attempt.Running {
			return nil, runnerNetworkDiagnosticCooldown, false
		}
		remaining := runnerNetworkDiagnosticCooldown - now.Sub(attempt.LastCompletedAt)
		if remaining > 0 {
			return nil, remaining, false
		}
	}
	s.networkDiagnosticAttempts[key] = networkDiagnosticAttempt{Running: true}
	return func(completedAt time.Time) {
		s.networkDiagnosticMu.Lock()
		attempt, exists := s.networkDiagnosticAttempts[key]
		if exists {
			attempt.Running = false
			attempt.LastCompletedAt = completedAt
			s.networkDiagnosticAttempts[key] = attempt
		}
		s.networkDiagnosticMu.Unlock()
	}, 0, true
}

func (s *Server) appendRunnerNetworkDiagnosticEvent(requestID string, result any) {
	data, err := json.Marshal(result)
	if err != nil {
		s.logger.Warn("marshal runner network diagnostic event failed", "id", requestID, "error", err)
		return
	}
	message := append([]byte("network diagnostic result="), data...)
	message = append(message, '\n')
	s.store.AppendStagedLog(requestID, "control.log", runnerNetworkDiagnosticStage, message)
}
