// Package deployruntime adapts the Qiniu Sandbox process API to deployd.Runtime.
package deployruntime

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	deploy "github.com/miclle/deployd"
	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

const (
	defaultCreateTimeout = 2 * time.Minute
	defaultStopTimeout   = 30 * time.Second
	maxCommandOutput     = 64 << 10
	rootUser             = "root"
)

var (
	ErrOutputLimit     = errors.New("sandbox command output limit exceeded")
	ErrProcessLost     = errors.New("sandbox process result is unknown")
	ErrCreatePending   = errors.New("sandbox creation is still pending")
	ErrSandboxNotFound = errors.New("deployment Sandbox not found")
)

type Factory struct{ client *qnsandbox.Client }

func NewFactory(apiKey, endpoint string, httpClient *http.Client) (*Factory, error) {
	client, err := qnsandbox.NewClient(&qnsandbox.Config{APIKey: apiKey, Endpoint: strings.TrimSpace(endpoint), HTTPClient: httpClient})
	if err != nil {
		return nil, err
	}
	return &Factory{client: client}, nil
}

type Handle struct {
	Runtime   deploy.Runtime
	SandboxID string
	box       *qnsandbox.Sandbox
	expiresAt time.Time
}

func (h *Handle) ID() string                 { return h.Runtime.ID() }
func (h *Handle) ActualExpiresAt() time.Time { return h.expiresAt }
func (h *Handle) Run(ctx context.Context, command deploy.Command, output deploy.Output) (deploy.Exit, error) {
	return h.Runtime.Run(ctx, command, output)
}

func (h *Handle) Start(ctx context.Context, command deploy.Command, output deploy.Output) (deploy.ProcessRef, error) {
	return h.Runtime.Start(ctx, command, output)
}

func (h *Handle) Inspect(ctx context.Context, ref deploy.ProcessRef) (deploy.ProcessState, error) {
	return h.Runtime.Inspect(ctx, ref)
}

func (h *Handle) Stop(ctx context.Context, ref deploy.ProcessRef) error {
	return h.Runtime.Stop(ctx, ref)
}

func (h *Handle) Endpoint(ctx context.Context, port int) (string, error) {
	return h.Runtime.Endpoint(ctx, port)
}

func (h *Handle) Close(ctx context.Context) error {
	if h == nil || h.box == nil {
		return nil
	}
	if err := h.box.Kill(ctx); err != nil {
		var apiErr *qnsandbox.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil
		}
		return sanitizeProviderError(ctx, err)
	}
	return nil
}

func (f *Factory) Create(ctx context.Context, templateID string, timeout time.Duration, operationID string) (*Handle, error) {
	if f == nil || f.client == nil || strings.TrimSpace(templateID) == "" || strings.TrimSpace(operationID) == "" {
		return nil, deploy.ErrInvalidInput
	}
	if timeout == 0 {
		timeout = defaultCreateTimeout
	}
	if timeout < time.Second {
		return nil, deploy.ErrInvalidInput
	}
	seconds := int32(timeout / time.Second)
	if seconds <= 0 {
		return nil, deploy.ErrInvalidInput
	}
	allowInternet := true
	metadata := qnsandbox.Metadata{"app": "qiniu-deployd", "deployment_id": operationID}
	box, info, err := f.client.CreateAndWait(ctx, qnsandbox.CreateParams{
		TemplateID: strings.TrimSpace(templateID), Timeout: &seconds, AllowInternetAccess: &allowInternet,
		Metadata: &metadata, IdempotencyKey: "deployd-" + operationID,
	}, qnsandbox.WithPollInterval(500*time.Millisecond))
	if err != nil {
		// CreateAndWait can lose the response after the provider has accepted the
		// request. Reconcile by the server-owned operation metadata before the
		// caller considers another attempt. This keeps the idempotency boundary
		// useful even when the original request timed out.
		reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		recovered, recoverErr := f.ReconcileCreate(reconcileCtx, templateID, operationID, timeout)
		cancel()
		if recovered != nil {
			return recovered, recoverErr
		}
		// An empty list immediately after a timeout does not prove the provider
		// rejected creation. Keep the attempt unresolved for later reconciliation.
		return nil, ErrCreatePending
	}
	handle := &Handle{Runtime: newRuntime(box), SandboxID: box.ID(), box: box}
	if info != nil {
		handle.expiresAt = info.EndAt
	}
	return handle, nil
}

// ReconcileCreate finds an existing exact operation without creating a resource.
func (f *Factory) ReconcileCreate(ctx context.Context, templateID, operationID string, timeout time.Duration) (*Handle, error) {
	metadataQuery := url.Values{}
	metadataQuery.Set("app", "qiniu-deployd")
	metadataQuery.Set("deployment_id", operationID)
	templates := []string{strings.TrimSpace(templateID)}
	limit := int32(100)
	items, err := f.client.List(ctx, &qnsandbox.ListParams{Metadata: ptr(metadataQuery.Encode()), Template: &templates, Limit: &limit})
	if err != nil {
		return nil, ErrCreatePending
	}
	if len(items) >= int(limit) {
		return nil, ErrCreatePending
	}
	var match *qnsandbox.ListedSandbox
	for _, item := range items {
		if item.Metadata == nil || (*item.Metadata)["app"] != "qiniu-deployd" || (*item.Metadata)["deployment_id"] != operationID {
			continue
		}
		if match != nil {
			return nil, deploy.ErrConflict
		}
		copy := item
		match = &copy
	}
	if match != nil {
		box, connectErr := f.client.Connect(ctx, match.SandboxID, qnsandbox.ConnectParams{Timeout: connectTimeout(timeout)})
		if connectErr != nil {
			return nil, ErrCreatePending
		}
		info, infoErr := box.GetInfo(ctx)
		if infoErr != nil {
			return &Handle{Runtime: newRuntime(box), SandboxID: match.SandboxID, box: box}, ErrCreatePending
		}
		if info.State != qnsandbox.StateRunning {
			return &Handle{Runtime: newRuntime(box), SandboxID: match.SandboxID, box: box}, ErrCreatePending
		}
		return &Handle{Runtime: newRuntime(box), SandboxID: match.SandboxID, box: box}, nil
	}
	return nil, nil
}

func ptr[T any](value T) *T { return &value }

func (f *Factory) Connect(ctx context.Context, sandboxID string, remaining time.Duration) (*Handle, error) {
	if f == nil || f.client == nil || strings.TrimSpace(sandboxID) == "" {
		return nil, deploy.ErrInvalidInput
	}
	box, err := f.client.Connect(ctx, strings.TrimSpace(sandboxID), qnsandbox.ConnectParams{Timeout: connectTimeout(remaining)})
	if err != nil {
		var apiErr *qnsandbox.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, ErrSandboxNotFound
		}
		return nil, sanitizeProviderError(ctx, err)
	}
	return &Handle{Runtime: newRuntime(box), SandboxID: box.ID(), box: box}, nil
}

func connectTimeout(remaining time.Duration) int32 {
	// The provider Connect call resets lifetime. Preserve the saved expiry
	// rather than shrinking a healthy service to the cleanup timeout.
	seconds := int64((remaining + time.Second - 1) / time.Second)
	return int32(min(max(seconds, 1), 86400))
}

type sandboxRuntime struct{ box *qnsandbox.Sandbox }

func newRuntime(box *qnsandbox.Sandbox) deploy.Runtime { return &sandboxRuntime{box: box} }
func (r *sandboxRuntime) ID() string {
	if r == nil || r.box == nil {
		return ""
	}
	return r.box.ID()
}

func (r *sandboxRuntime) Run(ctx context.Context, command deploy.Command, output deploy.Output) (deploy.Exit, error) {
	if err := validateCommand(command, false); err != nil {
		return deploy.Exit{}, err
	}
	limiter := newOutputLimiter(output, nil)
	handle, err := r.box.Commands().Start(ctx, command.Script, qnsandbox.WithCommandUser(rootUser), qnsandbox.WithCwd(command.Directory), qnsandbox.WithEnvs(command.Env), qnsandbox.WithOnStdout(func(data []byte) { limiter.write(deploy.Stdout, data) }), qnsandbox.WithOnStderr(func(data []byte) { limiter.write(deploy.Stderr, data) }))
	if err != nil {
		return deploy.Exit{}, sanitizeProviderError(ctx, err)
	}
	result, waitErr := handle.Wait()
	if limiter.Exceeded() {
		return deploy.Exit{}, ErrOutputLimit
	}
	if waitErr != nil || result == nil || strings.TrimSpace(result.Error) != "" {
		return deploy.Exit{}, sanitizeProviderError(ctx, waitErr)
	}
	return deploy.Exit{Code: result.ExitCode}, nil
}

func (r *sandboxRuntime) Start(ctx context.Context, command deploy.Command, output deploy.Output) (deploy.ProcessRef, error) {
	if err := validateCommand(command, true); err != nil {
		return deploy.ProcessRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return deploy.ProcessRef{}, err
	}
	// A Connect RPC deadline becomes a remote process timeout. Bound startup
	// confirmation locally, without sending that deadline as workload lifetime.
	streamCtx, cancelStream := context.WithCancel(context.WithoutCancel(ctx))
	confirmed, watcherDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			cancelStream()
		case <-confirmed:
		}
	}()
	defer func() {
		close(confirmed)
		<-watcherDone
		cancelStream()
	}()
	limiter := newOutputLimiter(output, cancelStream)
	handle, err := r.box.Commands().Start(streamCtx, command.Script, qnsandbox.WithCommandUser(rootUser), qnsandbox.WithCwd(command.Directory), qnsandbox.WithEnvs(command.Env), qnsandbox.WithTag(command.Tag), qnsandbox.WithOnStdout(func(data []byte) { limiter.write(deploy.Stdout, data) }), qnsandbox.WithOnStderr(func(data []byte) { limiter.write(deploy.Stderr, data) }))
	if err != nil {
		return deploy.ProcessRef{}, sanitizeProviderError(ctx, err)
	}
	pid, err := handle.WaitPID(ctx)
	if err != nil || pid == 0 || ctx.Err() != nil || limiter.Exceeded() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultStopTimeout)
		if handle.PID() != 0 {
			_ = handle.Kill(cleanup)
		}
		cancel()
		if limiter.Exceeded() {
			return deploy.ProcessRef{}, ErrOutputLimit
		}
		if ctx.Err() != nil {
			return deploy.ProcessRef{}, ctx.Err()
		}
		if err != nil {
			return deploy.ProcessRef{}, sanitizeProviderError(ctx, err)
		}
		return deploy.ProcessRef{}, ErrProcessLost
	}
	return deploy.ProcessRef{RuntimeID: r.ID(), ID: strconv.FormatUint(uint64(pid), 10), Tag: command.Tag}, nil
}

func (r *sandboxRuntime) Inspect(ctx context.Context, ref deploy.ProcessRef) (deploy.ProcessState, error) {
	if r == nil || r.box == nil || ref.RuntimeID != r.ID() || strings.TrimSpace(ref.Tag) == "" {
		return deploy.ProcessState{}, deploy.ErrRuntimeMismatch
	}
	if ref.ID != "" {
		if pid, err := strconv.ParseUint(ref.ID, 10, 32); err != nil || pid == 0 {
			return deploy.ProcessState{}, deploy.ErrRuntimeMismatch
		}
	}
	items, err := r.box.Commands().List(ctx)
	if err != nil {
		return deploy.ProcessState{}, sanitizeProviderError(ctx, err)
	}
	var found *qnsandbox.ProcessInfo
	for i := range items {
		item := &items[i]
		if ref.ID != "" && strconv.FormatUint(uint64(item.PID), 10) == ref.ID && item.Tag != nil && *item.Tag != ref.Tag {
			return deploy.ProcessState{}, deploy.ErrRuntimeMismatch
		}
		if item.PID == 0 || item.Tag == nil || *item.Tag != ref.Tag || (ref.ID != "" && strconv.FormatUint(uint64(item.PID), 10) != ref.ID) {
			continue
		}
		if found != nil {
			return deploy.ProcessState{}, deploy.ErrConflict
		}
		found = item
	}
	if found == nil {
		return deploy.ProcessState{}, deploy.ErrProcessUnknown
	}
	return deploy.ProcessState{Running: true}, nil
}

func (r *sandboxRuntime) Stop(ctx context.Context, ref deploy.ProcessRef) error {
	if _, err := r.Inspect(ctx, ref); errors.Is(err, deploy.ErrProcessUnknown) {
		return nil
	} else if err != nil {
		return err
	}
	pid, err := strconv.ParseUint(ref.ID, 10, 32)
	if err != nil || pid == 0 {
		return deploy.ErrRuntimeMismatch
	}
	if err := r.box.Commands().Kill(ctx, uint32(pid)); err != nil {
		return sanitizeProviderError(ctx, err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, inspectErr := r.Inspect(ctx, ref)
		if errors.Is(inspectErr, deploy.ErrProcessUnknown) {
			return nil
		}
		if inspectErr != nil {
			return inspectErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *sandboxRuntime) Endpoint(ctx context.Context, port int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if port < 1 || port > 65535 || r == nil || r.box == nil {
		return "", deploy.ErrInvalidInput
	}
	host := strings.TrimSpace(r.box.GetHost(port))
	if host == "" {
		return "", errors.New("sandbox endpoint is invalid")
	}
	endpoint := "https://" + host
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" {
		return "", errors.New("sandbox endpoint is invalid")
	}
	return endpoint, nil
}

func validateCommand(command deploy.Command, requireTag bool) error {
	if strings.TrimSpace(command.Script) == "" || (command.Directory != "" && !strings.HasPrefix(command.Directory, "/")) || strings.ContainsAny(command.Directory, "\r\n\x00") {
		return deploy.ErrInvalidInput
	}
	if requireTag && strings.TrimSpace(command.Tag) == "" {
		return deploy.ErrConflict
	}
	return nil
}

func sanitizeProviderError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return errors.New("sandbox operation failed")
}

type outputLimiter struct {
	mu       sync.Mutex
	output   deploy.Output
	bytes    int
	exceeded bool
	onLimit  func()
}

func newOutputLimiter(output deploy.Output, onLimit func()) *outputLimiter {
	return &outputLimiter{output: output, onLimit: onLimit}
}

func (l *outputLimiter) write(stream deploy.Stream, data []byte) {
	l.mu.Lock()
	if l.exceeded {
		l.mu.Unlock()
		return
	}
	remaining := maxCommandOutput - l.bytes
	if len(data) > remaining {
		output, chunk, onLimit := l.output, data, l.onLimit
		if remaining > 0 && len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		l.bytes, l.exceeded = maxCommandOutput, true
		l.mu.Unlock()
		if output != nil && len(chunk) > 0 {
			output(stream, chunk)
		}
		if onLimit != nil {
			onLimit()
		}
		return
	}
	l.bytes += len(data)
	output := l.output
	l.mu.Unlock()
	if output != nil && len(data) > 0 {
		output(stream, data)
	}
}
func (l *outputLimiter) Exceeded() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.exceeded }

var _ deploy.Runtime = (*sandboxRuntime)(nil)
