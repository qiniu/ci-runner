package sandboxrunner

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

type StartInput struct {
	RequestID           string
	RunnerName          string
	RepositoryURL       string
	RegistrationToken   string
	Labels              []string
	RunnerGroup         string
	TemplateID          string
	RequireDocker       bool
	Timeout             time.Duration
	CommandContext      context.Context
	CacheS3Region       string
	CacheS3Bucket       string
	CacheS3Endpoint     string
	CacheS3ReadPrefixes string
	CacheS3WritePrefix  string
	CacheS3AccessKeyID  string
	CacheS3SecretKey    string
	CacheS3SessionToken string
	OnStdout            func([]byte)
	OnStderr            func([]byte)
	OnExit              func(ExitResult, error)
}

type StartResult struct {
	SandboxID          string
	PID                uint32
	ResolvedTemplateID string
	TemplateVersion    string
	RunnerVersion      string
}

type runtimeEnvironment struct {
	TemplateVersion string
	RunnerVersion   string
}

type NetworkDiagnosticTarget string

const (
	NetworkDiagnosticTargetGitHubAPI     NetworkDiagnosticTarget = "github_api"
	NetworkDiagnosticTargetUbuntuArchive NetworkDiagnosticTarget = "ubuntu_archive"
	NetworkDiagnosticTargetLLVMAPT       NetworkDiagnosticTarget = "llvm_apt"
)

type NetworkDiagnosticTimings struct {
	DNSMS       int64 `json:"dns_ms"`
	ConnectMS   int64 `json:"connect_ms"`
	TLSMS       int64 `json:"tls_ms"`
	FirstByteMS int64 `json:"first_byte_ms"`
	TotalMS     int64 `json:"total_ms"`
}

type NetworkDiagnosticResult struct {
	Target       NetworkDiagnosticTarget  `json:"target"`
	Host         string                   `json:"host"`
	DNSAddresses []string                 `json:"dns_addresses"`
	ConnectedIP  string                   `json:"connected_ip,omitempty"`
	HTTPStatus   int                      `json:"http_status,omitempty"`
	ExitCode     int                      `json:"exit_code"`
	Timings      NetworkDiagnosticTimings `json:"timings_ms"`
	Error        string                   `json:"error,omitempty"`
	ObservedAt   time.Time                `json:"observed_at"`
}

type NetworkDiagnosticService interface {
	RunNetworkDiagnostic(ctx context.Context, sandboxID string, target NetworkDiagnosticTarget) (NetworkDiagnosticResult, error)
}

const (
	runtimeEnvironmentValueLimit = 256
	runtimeEnvironmentTimeout    = 5 * time.Second
	networkDiagnosticErrorLimit  = 512
	networkDiagnosticOutputLimit = 64 << 10
	runtimeEnvironmentCommand    = `set +u
runtime_environment_file="${RUNNER_ENVIRONMENT_FILE:-/etc/environment}"
actions_runner_root="${ACTIONS_RUNNER_ROOT:-/opt/actions-runner}"
template_version="${IMAGE_VERSION:-${ImageVersion:-}}"
if [ -z "$template_version" ] && [ -r "$runtime_environment_file" ]; then
  template_version="$(awk -F= '
    $1 == "IMAGE_VERSION" || $1 == "ImageVersion" {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
      exit
    }
  ' "$runtime_environment_file")"
  case "$template_version" in
    \"*\")
      template_version="${template_version#\"}"
      template_version="${template_version%\"}"
      ;;
    \'*\')
      template_version="${template_version#\'}"
      template_version="${template_version%\'}"
      ;;
  esac
fi
runner_version="$("$actions_runner_root/bin/Runner.Listener" --version 2>/dev/null || true)"
if [ "${#template_version}" -gt 256 ]; then template_version=""; fi
if [ "${#runner_version}" -gt 256 ]; then runner_version=""; fi
printf 'template_version=%s\nrunner_version=%s\n' \
  "$(printf '%s' "$template_version" | base64 | tr -d '\n')" \
  "$(printf '%s' "$runner_version" | base64 | tr -d '\n')"`
)

type RecoverInput struct {
	RequestID      string
	SandboxID      string
	PID            uint32
	Timeout        time.Duration
	CommandContext context.Context
	OnExit         func(ExitResult, error)
}

type PtySize struct {
	Cols uint32
	Rows uint32
}

type ExitResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Error    string
}

type CatalogTemplate struct {
	TemplateID  string    `json:"template_id"`
	Aliases     []string  `json:"aliases"`
	Names       []string  `json:"names"`
	BuildStatus string    `json:"build_status"`
	CPUCount    int32     `json:"cpu_count"`
	MemoryMB    int32     `json:"memory_mb"`
	DiskSizeMB  int32     `json:"disk_size_mb"`
	Public      bool      `json:"public"`
	SpawnCount  int64     `json:"spawn_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CatalogSandbox struct {
	SandboxID  string    `json:"sandbox_id"`
	TemplateID string    `json:"template_id"`
	Alias      string    `json:"alias,omitempty"`
	State      string    `json:"state"`
	CPUCount   int32     `json:"cpu_count"`
	MemoryMB   int32     `json:"memory_mb"`
	DiskSizeMB int32     `json:"disk_size_mb"`
	StartedAt  time.Time `json:"started_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Catalog interface {
	ListTemplates(ctx context.Context) ([]CatalogTemplate, error)
	ListRunnerSandboxes(ctx context.Context) ([]CatalogSandbox, error)
}

// DefaultTemplateCatalog lists public templates available on the scoped Sandbox endpoint.
type DefaultTemplateCatalog interface {
	ListDefaultTemplates(ctx context.Context) ([]CatalogTemplate, error)
}

type TerminalSession interface {
	PID() uint32
	SendInput(ctx context.Context, data []byte) error
	Resize(ctx context.Context, size PtySize) error
	Close(ctx context.Context) error
}

type Service interface {
	ValidateTemplate(ctx context.Context, templateID string) error
	StartRunner(ctx context.Context, input StartInput) (StartResult, error)
	RecoverRunner(ctx context.Context, input RecoverInput) (StartResult, error)
	StopRunner(ctx context.Context, sandboxID string, pid uint32) error
	StartTerminal(ctx context.Context, sandboxID string, size PtySize, onData func([]byte)) (TerminalSession, error)
}

var (
	ErrTemplateRequired             = errors.New("template_id is required")
	ErrTemplateNotFound             = errors.New("template not found")
	ErrTemplateNotReady             = errors.New("template is not ready")
	ErrTemplateStateUnavailable     = errors.New("template default build state cannot be confirmed")
	ErrSandboxNotFound              = errors.New("sandbox not found")
	ErrRunnerNotFound               = errors.New("runner process not found")
	ErrNetworkDiagnosticTarget      = errors.New("unsupported network diagnostic target")
	ErrNetworkDiagnosticExecution   = errors.New("network diagnostic command failed")
	ErrNetworkDiagnosticOutputLimit = errors.New("network diagnostic output limit exceeded")
)

type E2BService struct {
	client *qnsandbox.Client
}

const runnerBootstrapUser = "root"

//go:embed scripts/start-github-runner.sh
var startRunnerScriptTemplate string

func NewE2BService(apiKey, endpoint string, httpClient *http.Client) (*E2BService, error) {
	cfg := &qnsandbox.Config{APIKey: apiKey, Endpoint: endpoint, HTTPClient: httpClient}
	client, err := qnsandbox.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &E2BService{client: client}, nil
}

func (s *E2BService) ValidateTemplate(ctx context.Context, templateID string) error {
	templateID = strings.TrimSpace(templateID)
	if templateID == "" {
		return ErrTemplateRequired
	}
	template, err := s.client.GetTemplate(ctx, templateID, nil)
	if err != nil {
		var apiErr *qnsandbox.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return ErrTemplateNotFound
		}
		return err
	}
	// Detail builds are paginated history (including other tags) and hidden
	// from non-owners. The catalog's BuildID instead identifies the uploaded
	// default build actually selected for creation. BuildStatus describes the
	// latest attempt, which may be rebuilding while the old default is usable.
	var templates []qnsandbox.Template
	switch {
	case template.IsOwner:
		templates, err = s.client.ListTemplates(ctx, nil)
	case template.Public:
		templates, err = s.client.ListDefaultTemplates(ctx)
	default:
		return ErrTemplateStateUnavailable
	}
	if err != nil {
		return err
	}
	for _, item := range templates {
		if item.TemplateID != templateID {
			continue
		}
		buildID := strings.TrimSpace(item.BuildID)
		if buildID == "" || buildID == "00000000-0000-0000-0000-000000000000" {
			return ErrTemplateNotReady
		}
		return nil
	}
	return ErrTemplateStateUnavailable
}

func (s *E2BService) ListTemplates(ctx context.Context) ([]CatalogTemplate, error) {
	items, err := s.client.ListTemplates(ctx, nil)
	if err != nil {
		return nil, err
	}
	result := make([]CatalogTemplate, 0, len(items))
	for _, item := range items {
		result = append(result, CatalogTemplate{
			TemplateID:  item.TemplateID,
			Aliases:     item.Aliases,
			BuildStatus: string(item.BuildStatus),
			CPUCount:    item.CPUCount,
			MemoryMB:    item.MemoryMB,
			DiskSizeMB:  item.DiskSizeMB,
			Public:      item.Public,
			SpawnCount:  item.SpawnCount,
			UpdatedAt:   item.UpdatedAt,
		})
	}
	return result, nil
}

// ListDefaultTemplates lists the public template catalog from the scoped Sandbox endpoint.
func (s *E2BService) ListDefaultTemplates(ctx context.Context) ([]CatalogTemplate, error) {
	items, err := s.client.ListDefaultTemplates(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]CatalogTemplate, 0, len(items))
	for _, item := range items {
		result = append(result, CatalogTemplate{
			TemplateID:  item.TemplateID,
			Names:       item.Names,
			BuildStatus: string(item.BuildStatus),
			CPUCount:    item.CPUCount,
			MemoryMB:    item.MemoryMB,
			DiskSizeMB:  item.DiskSizeMB,
			Public:      item.Public,
			SpawnCount:  item.SpawnCount,
			UpdatedAt:   item.UpdatedAt,
		})
	}
	return result, nil
}

func (s *E2BService) ListRunnerSandboxes(ctx context.Context) ([]CatalogSandbox, error) {
	metadata := "app=e2b-github-runner"
	items, err := s.client.List(ctx, &qnsandbox.ListParams{Metadata: &metadata})
	if err != nil {
		return nil, err
	}
	result := make([]CatalogSandbox, 0, len(items))
	for _, item := range items {
		alias := ""
		if item.Alias != nil {
			alias = *item.Alias
		}
		result = append(result, CatalogSandbox{
			SandboxID:  item.SandboxID,
			TemplateID: item.TemplateID,
			Alias:      alias,
			State:      string(item.State),
			CPUCount:   item.CPUCount,
			MemoryMB:   item.MemoryMB,
			DiskSizeMB: item.DiskSizeMB,
			StartedAt:  item.StartedAt,
			ExpiresAt:  item.EndAt,
		})
	}
	return result, nil
}

func (s *E2BService) StartRunner(ctx context.Context, input StartInput) (StartResult, error) {
	timeout := sandboxTimeoutSeconds(input.Timeout)
	allowInternet := true
	metadata := qnsandbox.Metadata{
		"app":        "e2b-github-runner",
		"request_id": input.RequestID,
	}
	sb, _, err := s.client.CreateAndWait(ctx, qnsandbox.CreateParams{
		TemplateID:          input.TemplateID,
		Timeout:             &timeout,
		AllowInternetAccess: &allowInternet,
		Metadata:            &metadata,
	}, qnsandbox.WithPollInterval(500*time.Millisecond))
	if err != nil {
		return StartResult{}, err
	}
	runtimeEnvironment := readRuntimeEnvironment(ctx, sb)

	if _, err := sb.Files().Write(ctx, "/tmp/start-github-runner.sh", []byte(startScript(input, sb.ID()))); err != nil {
		_ = sb.Kill(ctx)
		return StartResult{}, fmt.Errorf("write runner script: %w", err)
	}
	commandCtx := input.CommandContext
	if commandCtx == nil {
		commandCtx = context.Background()
	}
	cmd := "chmod +x /tmp/start-github-runner.sh && /tmp/start-github-runner.sh"
	handle, err := sb.Commands().Start(
		commandCtx, cmd,
		qnsandbox.WithCommandUser(runnerBootstrapUser),
		qnsandbox.WithTag("github-runner"),
		qnsandbox.WithOnStdout(input.OnStdout),
		qnsandbox.WithOnStderr(input.OnStderr),
	)
	if err != nil {
		_ = sb.Kill(ctx)
		return StartResult{}, fmt.Errorf("start runner command: %w", err)
	}
	pid, err := handle.WaitPID(ctx)
	if err != nil {
		_ = sb.Kill(ctx)
		return StartResult{}, fmt.Errorf("wait runner pid: %w", err)
	}
	if input.OnExit != nil {
		go func() {
			result, err := handle.Wait()
			if result == nil {
				input.OnExit(ExitResult{}, err)
				return
			}
			input.OnExit(ExitResult{
				ExitCode: result.ExitCode,
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
				Error:    result.Error,
			}, err)
		}()
	}
	return StartResult{
		SandboxID:          sb.ID(),
		PID:                pid,
		ResolvedTemplateID: sb.TemplateID(),
		TemplateVersion:    runtimeEnvironment.TemplateVersion,
		RunnerVersion:      runtimeEnvironment.RunnerVersion,
	}, nil
}

func (s *E2BService) RecoverRunner(ctx context.Context, input RecoverInput) (StartResult, error) {
	sandboxID := strings.TrimSpace(input.SandboxID)
	if sandboxID == "" {
		var err error
		sandboxID, err = s.findRunnerSandbox(ctx, input.RequestID)
		if err != nil {
			return StartResult{}, err
		}
	}

	sb, err := s.client.Connect(ctx, sandboxID, qnsandbox.ConnectParams{
		Timeout: sandboxTimeoutSeconds(input.Timeout),
	})
	if err != nil {
		if isSandboxNotFound(err) {
			return StartResult{}, fmt.Errorf("%w: %s", ErrSandboxNotFound, sandboxID)
		}
		return StartResult{}, fmt.Errorf("connect sandbox %s: %w", sandboxID, err)
	}
	runtimeEnvironment := readRuntimeEnvironment(ctx, sb)
	processes, err := sb.Commands().List(ctx)
	if err != nil {
		return StartResult{}, fmt.Errorf("list sandbox %s processes: %w", sandboxID, err)
	}
	pid, ok := recoveredRunnerPID(processes, input.PID)
	if !ok {
		return StartResult{SandboxID: sandboxID}, fmt.Errorf("%w in sandbox %s", ErrRunnerNotFound, sandboxID)
	}

	commandCtx := input.CommandContext
	if commandCtx == nil {
		commandCtx = context.Background()
	}
	handle, err := sb.Commands().Connect(commandCtx, pid)
	if err != nil {
		return StartResult{}, fmt.Errorf("connect runner process %d in sandbox %s: %w", pid, sandboxID, err)
	}
	if input.OnExit != nil {
		go func() {
			result, err := handle.Wait()
			if result == nil {
				input.OnExit(ExitResult{}, err)
				return
			}
			input.OnExit(ExitResult{
				ExitCode: result.ExitCode,
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
				Error:    result.Error,
			}, err)
		}()
	}
	return StartResult{
		SandboxID:          sandboxID,
		PID:                pid,
		ResolvedTemplateID: sb.TemplateID(),
		TemplateVersion:    runtimeEnvironment.TemplateVersion,
		RunnerVersion:      runtimeEnvironment.RunnerVersion,
	}, nil
}

func readRuntimeEnvironment(ctx context.Context, sb *qnsandbox.Sandbox) runtimeEnvironment {
	result, err := sb.Commands().Run(
		ctx,
		runtimeEnvironmentCommand,
		qnsandbox.WithCommandUser(runnerBootstrapUser),
		qnsandbox.WithTimeout(runtimeEnvironmentTimeout),
	)
	if err != nil || result == nil || result.ExitCode != 0 {
		return runtimeEnvironment{}
	}
	return parseRuntimeEnvironment(result.Stdout)
}

func (s *E2BService) RunNetworkDiagnostic(ctx context.Context, sandboxID string, target NetworkDiagnosticTarget) (NetworkDiagnosticResult, error) {
	command, host, err := networkDiagnosticCommand(target)
	if err != nil {
		return NetworkDiagnosticResult{}, err
	}
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return NetworkDiagnosticResult{}, ErrSandboxNotFound
	}
	sb, err := s.client.Connect(ctx, sandboxID, qnsandbox.ConnectParams{Timeout: 10})
	if err != nil {
		return NetworkDiagnosticResult{}, fmt.Errorf("connect sandbox for network diagnostic: %w", err)
	}
	result, err := runBoundedNetworkDiagnosticCommand(ctx, func(commandCtx context.Context, onStdout, onStderr func([]byte)) (*qnsandbox.CommandResult, error) {
		handle, startErr := sb.Commands().Start(
			commandCtx,
			command,
			qnsandbox.WithCommandUser(runnerBootstrapUser),
			qnsandbox.WithTimeout(10*time.Second),
			qnsandbox.WithOnStdout(onStdout),
			qnsandbox.WithOnStderr(onStderr),
		)
		if startErr != nil {
			return nil, startErr
		}
		return handle.Wait()
	})
	if err != nil {
		return NetworkDiagnosticResult{}, fmt.Errorf("run network diagnostic: %w", err)
	}
	return parseNetworkDiagnosticCommandResult(target, host, result, time.Now().UTC())
}

func runBoundedNetworkDiagnosticCommand(
	ctx context.Context,
	execute func(context.Context, func([]byte), func([]byte)) (*qnsandbox.CommandResult, error),
) (*qnsandbox.CommandResult, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var outputBytes atomic.Int64
	var outputLimitExceeded atomic.Bool
	observeOutput := func(data []byte) {
		if outputBytes.Add(int64(len(data))) > networkDiagnosticOutputLimit && outputLimitExceeded.CompareAndSwap(false, true) {
			cancel()
		}
	}
	result, err := execute(commandCtx, observeOutput, observeOutput)
	if outputLimitExceeded.Load() {
		return nil, ErrNetworkDiagnosticOutputLimit
	}
	return result, err
}

func parseNetworkDiagnosticCommandResult(target NetworkDiagnosticTarget, host string, result *qnsandbox.CommandResult, observedAt time.Time) (NetworkDiagnosticResult, error) {
	if result == nil || strings.TrimSpace(result.Error) != "" {
		return NetworkDiagnosticResult{}, ErrNetworkDiagnosticExecution
	}
	parsed := parseNetworkDiagnosticOutput(target, result.Stdout, result.Stderr, result.ExitCode, observedAt)
	parsed.Host = host
	return parsed, nil
}

func networkDiagnosticCommand(target NetworkDiagnosticTarget) (string, string, error) {
	host, ok := NetworkDiagnosticTargetHost(target)
	if !ok {
		return "", "", ErrNetworkDiagnosticTarget
	}
	var targetURL string
	switch target {
	case NetworkDiagnosticTargetGitHubAPI:
		targetURL = "https://api.github.com/"
	case NetworkDiagnosticTargetUbuntuArchive:
		targetURL = "https://archive.ubuntu.com/ubuntu/dists/noble/InRelease"
	case NetworkDiagnosticTargetLLVMAPT:
		targetURL = "https://apt.llvm.org/llvm.sh"
	}

	return fmt.Sprintf(`host=%q
target_url=%q
getent ahosts "$host" 2>/dev/null | awk '{print $1}' | awk '!seen[$0]++' | head -n 8 | while IFS= read -r ip; do
  if [ -n "$ip" ]; then printf 'dns_ip=%%s\n' "$ip"; fi
done
curl --silent \
  --proto '=https' \
  --connect-timeout 3 \
  --max-time 8 \
  --range 0-65535 \
  --max-filesize 65536 \
  --output /dev/null \
  --write-out 'remote_ip=%%{remote_ip}\nhttp_status=%%{http_code}\ndns_seconds=%%{time_namelookup}\nconnect_seconds=%%{time_connect}\ntls_seconds=%%{time_appconnect}\nfirst_byte_seconds=%%{time_starttransfer}\ntotal_seconds=%%{time_total}\n' \
  "$target_url" 2>/dev/null
probe_exit=$?
probe_error=
case "$probe_exit" in
  0) ;;
  5) probe_error=proxy_resolution_failed ;;
  6) probe_error=dns_resolution_failed ;;
  7) probe_error=connection_failed ;;
  28) probe_error=operation_timed_out ;;
  35|51|53|58|59|60|64|66|77|80|82|83|90|91) probe_error=tls_failed ;;
  63) probe_error=download_limit_exceeded ;;
  *) probe_error="curl_exit_$probe_exit" ;;
esac
if [ -n "$probe_error" ]; then printf 'probe_error=%%s\n' "$probe_error"; fi
exit "$probe_exit"`, host, targetURL), host, nil
}

func NetworkDiagnosticTargetHost(target NetworkDiagnosticTarget) (string, bool) {
	switch target {
	case NetworkDiagnosticTargetGitHubAPI:
		return "api.github.com", true
	case NetworkDiagnosticTargetUbuntuArchive:
		return "archive.ubuntu.com", true
	case NetworkDiagnosticTargetLLVMAPT:
		return "apt.llvm.org", true
	default:
		return "", false
	}
}

func parseNetworkDiagnosticOutput(target NetworkDiagnosticTarget, stdout, _ string, exitCode int, observedAt time.Time) NetworkDiagnosticResult {
	result := NetworkDiagnosticResult{
		Target:       target,
		DNSAddresses: []string{},
		ExitCode:     exitCode,
		ObservedAt:   observedAt.UTC(),
	}
	var dnsSeconds, connectSeconds, tlsSeconds, firstByteSeconds, totalSeconds float64
	seenAddresses := make(map[string]struct{})
	for _, line := range strings.Split(stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "dns_ip":
			ip := net.ParseIP(value)
			if ip == nil || len(result.DNSAddresses) >= 8 {
				continue
			}
			canonical := ip.String()
			if _, exists := seenAddresses[canonical]; exists {
				continue
			}
			seenAddresses[canonical] = struct{}{}
			result.DNSAddresses = append(result.DNSAddresses, canonical)
		case "remote_ip":
			if ip := net.ParseIP(value); ip != nil {
				result.ConnectedIP = ip.String()
			}
		case "http_status":
			status, err := strconv.Atoi(value)
			if err == nil && status >= 100 && status <= 599 {
				result.HTTPStatus = status
			}
		case "dns_seconds":
			dnsSeconds = diagnosticSeconds(value)
		case "connect_seconds":
			connectSeconds = diagnosticSeconds(value)
		case "tls_seconds":
			tlsSeconds = diagnosticSeconds(value)
		case "first_byte_seconds":
			firstByteSeconds = diagnosticSeconds(value)
		case "total_seconds":
			totalSeconds = diagnosticSeconds(value)
		case "probe_error":
			result.Error = SanitizeNetworkDiagnosticError(value)
		}
	}
	connectionEnd := math.Max(connectSeconds, dnsSeconds)
	tlsEnd := math.Max(tlsSeconds, connectionEnd)
	result.Timings = NetworkDiagnosticTimings{
		DNSMS:       diagnosticMilliseconds(dnsSeconds),
		ConnectMS:   diagnosticMilliseconds(connectionEnd - dnsSeconds),
		TLSMS:       diagnosticMilliseconds(tlsEnd - connectionEnd),
		FirstByteMS: diagnosticMilliseconds(math.Max(firstByteSeconds, tlsEnd) - tlsEnd),
		TotalMS:     diagnosticMilliseconds(totalSeconds),
	}
	if len(result.Error) > networkDiagnosticErrorLimit {
		result.Error = result.Error[:networkDiagnosticErrorLimit]
	}
	return result
}

func SanitizeNetworkDiagnosticError(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "proxy_resolution_failed", "dns_resolution_failed", "connection_failed", "operation_timed_out", "tls_failed", "download_limit_exceeded":
		return value
	}
	codeText, ok := strings.CutPrefix(value, "curl_exit_")
	if !ok {
		return ""
	}
	code, err := strconv.Atoi(codeText)
	if err != nil || code < 1 || code > 255 {
		return ""
	}
	return "curl_exit_" + strconv.Itoa(code)
}

func diagnosticSeconds(value string) float64 {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	return seconds
}

func diagnosticMilliseconds(seconds float64) int64 {
	if seconds <= 0 {
		return 0
	}
	return int64(math.Round(seconds * 1000))
}

func parseRuntimeEnvironment(output string) runtimeEnvironment {
	var environment runtimeEnvironment
	for _, line := range strings.Split(output, "\n") {
		key, encoded, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (key != "template_version" && key != "runner_version") {
			continue
		}
		value, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(value) > runtimeEnvironmentValueLimit {
			continue
		}
		decoded := strings.TrimSpace(string(value))
		switch key {
		case "template_version":
			environment.TemplateVersion = decoded
		case "runner_version":
			environment.RunnerVersion = decoded
		}
	}
	return environment
}

func (s *E2BService) findRunnerSandbox(ctx context.Context, requestID string) (string, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", ErrSandboxNotFound
	}
	metadata := url.Values{
		"app":        {"e2b-github-runner"},
		"request_id": {requestID},
	}.Encode()
	items, err := s.client.List(ctx, &qnsandbox.ListParams{Metadata: &metadata})
	if err != nil {
		return "", fmt.Errorf("find runner sandbox for request %s: %w", requestID, err)
	}
	if len(items) == 0 {
		return "", fmt.Errorf("%w for request %s", ErrSandboxNotFound, requestID)
	}
	if len(items) > 1 {
		return "", fmt.Errorf("multiple sandboxes found for request %s", requestID)
	}
	return items[0].SandboxID, nil
}

func recoveredRunnerPID(processes []qnsandbox.ProcessInfo, expectedPID uint32) (uint32, bool) {
	var recoveredPID uint32
	taggedCount := 0
	for _, process := range processes {
		if process.Tag == nil || *process.Tag != "github-runner" {
			continue
		}
		if expectedPID != 0 {
			if process.PID == expectedPID {
				return process.PID, true
			}
			continue
		}
		taggedCount++
		recoveredPID = process.PID
	}
	if expectedPID == 0 && taggedCount == 1 {
		return recoveredPID, true
	}
	// A persisted PID identifies the exact process started for this request.
	// If it disappeared, a differently numbered tagged process is a replacement,
	// not the runner that runnerd owned before restarting.
	return 0, false
}

func sandboxTimeoutSeconds(timeout time.Duration) int32 {
	seconds := math.Ceil(timeout.Seconds())
	if seconds < 1 {
		return 1
	}
	if seconds > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(seconds)
}

func isSandboxNotFound(err error) bool {
	var apiErr *qnsandbox.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func (s *E2BService) StopRunner(ctx context.Context, sandboxID string, pid uint32) error {
	sb, err := s.client.Connect(ctx, sandboxID, qnsandbox.ConnectParams{Timeout: 30})
	if err != nil {
		return err
	}
	if pid != 0 {
		_ = sb.Commands().Kill(ctx, pid)
	}
	return sb.Kill(ctx)
}

type e2bTerminalSession struct {
	sandboxID string
	pty       interface {
		SendInput(context.Context, uint32, []byte) error
		Resize(context.Context, uint32, qnsandbox.PtySize) error
		Kill(context.Context, uint32) error
	}
	pid uint32
}

func (s *E2BService) StartTerminal(ctx context.Context, sandboxID string, size PtySize, onData func([]byte)) (TerminalSession, error) {
	sb, err := s.client.Connect(ctx, sandboxID, qnsandbox.ConnectParams{Timeout: 30})
	if err != nil {
		return nil, err
	}
	if size.Cols == 0 {
		size.Cols = 100
	}
	if size.Rows == 0 {
		size.Rows = 28
	}
	handle, err := sb.Pty().Create(
		ctx,
		qnsandbox.PtySize{Cols: size.Cols, Rows: size.Rows},
		qnsandbox.WithTag("runnerd-web-terminal"),
		qnsandbox.WithOnPtyData(onData),
	)
	if err != nil {
		return nil, err
	}
	pid, err := handle.WaitPID(ctx)
	if err != nil {
		return nil, err
	}
	return &e2bTerminalSession{sandboxID: sandboxID, pty: sb.Pty(), pid: pid}, nil
}

func (s *e2bTerminalSession) PID() uint32 {
	return s.pid
}

func (s *e2bTerminalSession) SendInput(ctx context.Context, data []byte) error {
	return s.pty.SendInput(ctx, s.pid, data)
}

func (s *e2bTerminalSession) Resize(ctx context.Context, size PtySize) error {
	if size.Cols == 0 || size.Rows == 0 {
		return nil
	}
	return s.pty.Resize(ctx, s.pid, qnsandbox.PtySize{Cols: size.Cols, Rows: size.Rows})
}

func (s *e2bTerminalSession) Close(ctx context.Context) error {
	return s.pty.Kill(ctx, s.pid)
}

func startScript(input StartInput, sandboxID string) string {
	labels := strings.Join(input.Labels, ",")
	requireDocker := "0"
	if input.RequireDocker {
		requireDocker = "1"
	}
	return fmt.Sprintf(
		startRunnerScriptTemplate,
		base64.StdEncoding.EncodeToString([]byte(input.RepositoryURL)),
		base64.StdEncoding.EncodeToString([]byte(input.RegistrationToken)),
		base64.StdEncoding.EncodeToString([]byte(input.RunnerName)),
		base64.StdEncoding.EncodeToString([]byte(labels)),
		base64.StdEncoding.EncodeToString([]byte(input.RunnerGroup)),
		base64.StdEncoding.EncodeToString([]byte(input.RequestID)),
		base64.StdEncoding.EncodeToString([]byte(sandboxID)),
		requireDocker,
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3Region)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3Bucket)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3Endpoint)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3ReadPrefixes)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3WritePrefix)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3AccessKeyID)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3SecretKey)),
		base64.StdEncoding.EncodeToString([]byte(input.CacheS3SessionToken)),
	)
}
