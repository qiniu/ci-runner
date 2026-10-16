// Package worker executes persisted Deploy plans with attempt fencing.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	deploy "github.com/miclle/deployd"
	deploygit "github.com/miclle/deployd/source/git"
	deployruntime "github.com/qiniu/ci-runner/internal/deployment/runtime"
	"github.com/qiniu/ci-runner/internal/state"
)

const (
	defaultCloneTimeout   = 5 * time.Minute
	defaultInstallTimeout = 15 * time.Minute
	defaultStartTimeout   = 2 * time.Minute
	defaultCleanupTimeout = 30 * time.Second
)

// RuntimeHandle combines the deployd process runtime with Sandbox cleanup.
type RuntimeHandle interface {
	deploy.Runtime
	Close(context.Context) error
}

// RuntimeFactory creates or reconnects one attempt. Implementations must bind
// the returned runtime to the selected Sandbox ID and never reuse it for an
// unrelated deployment.
type RuntimeFactory interface {
	Create(context.Context, state.Deployment, int64) (RuntimeHandle, string, error)
	Connect(context.Context, state.Deployment) (RuntimeHandle, error)
	Reconcile(context.Context, state.Deployment) (RuntimeHandle, string, error)
}

// SourceFactory creates a source adapter whose credentials are transient. It
// must use the deployment's immutable repository and commit evidence.
type SourceFactory func(context.Context, state.Deployment) (deploy.Source, error)

// SpecDecoder decrypts the persisted normalized deployd Spec in memory.
type SpecDecoder func(state.Deployment) (deploy.Spec, error)

// OutputSink receives already bounded and redacted deployd output.
type OutputSink func(state.Deployment, deploy.OutputEvent)

// EventSink persists bounded lifecycle evidence. It must never receive secrets
// or repository commands; deployd output is already redacted before delivery.
type EventSink func(state.Deployment, state.DeploymentEvent)

// Worker processes durable deployment records. It is intentionally independent
// of HTTP handlers so recovery and later queue scheduling share the same path.
type Worker struct {
	Store          state.DeploymentStore
	Runtime        RuntimeFactory
	Source         SourceFactory
	DecodeSpec     SpecDecoder
	WorkRoot       string
	Output         OutputSink
	Event          EventSink
	CloneTimeout   time.Duration
	InstallTimeout time.Duration
	StartTimeout   time.Duration
	CleanupTimeout time.Duration
	Retention      time.Duration
	TemplateID     string
	CreateTimeout  time.Duration
}

// New creates a worker with conservative execution bounds.
func New(store state.DeploymentStore, runtime RuntimeFactory, source SourceFactory, decode SpecDecoder, workRoot string) *Worker {
	return &Worker{
		Store:          store,
		Runtime:        runtime,
		Source:         source,
		DecodeSpec:     decode,
		WorkRoot:       filepath.Clean(workRoot),
		CloneTimeout:   defaultCloneTimeout,
		InstallTimeout: defaultInstallTimeout,
		StartTimeout:   defaultStartTimeout,
		CleanupTimeout: defaultCleanupTimeout,
		Retention:      7 * 24 * time.Hour,
		CreateTimeout:  2 * time.Minute,
	}
}

// RunOnce advances queued deployments and performs expiry cleanup. It is safe
// to call from a periodic loop and after startup recovery.
func (w *Worker) RunOnce(ctx context.Context) error {
	if w == nil || w.Store == nil {
		return nil
	}
	if w.Retention > 0 {
		if _, err := w.Store.PruneDeployments(time.Now().UTC().Add(-w.Retention), 100); err != nil {
			return err
		}
	}
	items, err := w.Store.ListActiveDeployments()
	if err != nil {
		return err
	}
	var firstErr error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		var itemErr error
		switch {
		case item.Status == state.DeploymentStatusCreating:
			itemErr = w.recoverCreating(ctx, item)
		case item.Status == state.DeploymentStatusPlanning:
			// Planning has no provider side effects. This single-worker loop can
			// safely resume an interrupted plan without changing the attempt.
			itemErr = w.transition(&item, state.DeploymentStatusQueued, "recovery", "")
		case !item.ExpiresAt.IsZero() && !time.Now().UTC().Before(item.ExpiresAt):
			itemErr = w.Stop(ctx, item.ID, true)
		case item.Status == state.DeploymentStatusQueued:
			itemErr = w.Process(ctx, item.ID)
		case item.Status == state.DeploymentStatusFailed && strings.TrimSpace(item.SandboxID) != "":
			itemErr = w.Stop(ctx, item.ID, false)
		case item.Status == state.DeploymentStatusStopping:
			itemErr = w.Stop(ctx, item.ID, false)
		case item.Status == state.DeploymentStatusRunning:
			itemErr = w.checkRunning(ctx, item)
		}
		if itemErr != nil && firstErr == nil {
			firstErr = itemErr
		}
	}
	return firstErr
}

// Process executes one queued deployment. Every provider side effect follows a
// durable state transition and all saved process evidence carries the attempt.
func (w *Worker) Process(ctx context.Context, id string) error {
	if w == nil || w.Store == nil || w.Runtime == nil || w.Source == nil || w.DecodeSpec == nil {
		return errors.New("deployment worker is not configured")
	}
	d, err := w.Store.GetDeployment(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if d.Status != state.DeploymentStatusQueued {
		return nil
	}
	if !time.Now().UTC().Before(d.ExpiresAt) {
		return w.Stop(ctx, d.ID, true)
	}
	if err := w.transition(&d, state.DeploymentStatusPlanning, "", ""); err != nil {
		return err
	}
	spec, err := w.DecodeSpec(d)
	if err != nil {
		return w.fail(&d, "plan_restore", "persisted deployment plan is invalid")
	}
	source, err := w.Source(ctx, d)
	if err != nil {
		return w.fail(&d, "source", "deployment source is unavailable")
	}
	snapshot := deploy.Snapshot{
		Version:   d.SnapshotVersion,
		SourceID:  githubSourceURL(d.RepositoryFullName),
		CommitSHA: d.CommitSHA,
		SpecHash:  d.SpecHash,
	}
	plan, err := deploy.Restore(snapshot, spec)
	if err != nil {
		return w.fail(&d, "plan_restore", "persisted deployment plan is invalid")
	}
	d.Attempt++
	if strings.TrimSpace(d.DeploymentTemplateID) == "" {
		d.DeploymentTemplateID = strings.TrimSpace(w.TemplateID)
	}
	if d.DeploymentTemplateID == "" {
		return w.fail(&d, "template", "no deployment template selected; create a deployment with an available Sandbox template")
	}
	if err := w.transition(&d, state.DeploymentStatusCreating, "", ""); err != nil {
		return err
	}
	createCtx, cancelCreate := context.WithTimeout(ctx, w.CreateTimeout)
	rt, sandboxID, err := w.Runtime.Create(createCtx, d, d.Attempt)
	cancelCreate()
	if err != nil {
		if rt == nil && strings.TrimSpace(sandboxID) == "" && errors.Is(err, deployruntime.ErrCreatePending) {
			w.emit(d, state.DeploymentEvent{EventType: "lifecycle", Stage: "sandbox_create", Message: "creation outcome is unknown; reconciling the saved attempt"})
			return err
		}
		if rt != nil || strings.TrimSpace(sandboxID) != "" {
			// A reconciled provider operation may return an exact Sandbox handle
			// together with an error (for example, still provisioning). Persist the
			// evidence before cleanup so a timeout cannot become an untracked bill.
			d.SandboxID = strings.TrimSpace(sandboxID)
			if rt != nil {
				d.RuntimeID = rt.ID()
			}
			if persistErr := w.persist(&d); persistErr != nil {
				return persistErr
			}
			cleanupErr := w.closeAttempt(ctx, rt)
			if cleanupErr == nil {
				d.SandboxID = ""
				d.RuntimeID = ""
			}
			if cleanupErr != nil {
				return w.fail(&d, "sandbox_create", "Sandbox creation failed; cleanup is incomplete")
			}
		}
		return w.fail(&d, "sandbox_create", "Sandbox creation failed")
	}
	if strings.TrimSpace(sandboxID) == "" || rt == nil || strings.TrimSpace(rt.ID()) == "" {
		_ = w.closeAttempt(ctx, rt)
		return w.fail(&d, "sandbox_create", "Sandbox identity was not confirmed")
	}
	keepAttempt := false
	cleanupDone := false
	defer func() {
		if !keepAttempt && !cleanupDone {
			_ = w.closeAttempt(ctx, rt)
		}
	}()
	d.SandboxID = strings.TrimSpace(sandboxID)
	d.RuntimeID = rt.ID()
	if evidence, ok := rt.(interface{ ActualExpiresAt() time.Time }); ok {
		if actual := evidence.ActualExpiresAt(); !actual.IsZero() {
			d.ExpiresAt = actual.UTC()
		}
	}
	if err := w.persist(&d); err != nil {
		return err
	}

	// Keep the workspace identity deterministic and within deployd's 64-character
	// limit including the attempt, independently of the persisted ID format.
	identity := sha256.Sum256([]byte(d.ID))
	operationID := fmt.Sprintf("deploy-%x-%d", identity[:16], d.Attempt)
	application := &applicationRuntime{RuntimeHandle: rt, port: plan.Spec().Port}
	result, applyErr := deploy.Apply(ctx, source, application, plan, deploy.Options{
		WorkRoot:       w.WorkRoot,
		OperationID:    operationID,
		CloneTimeout:   w.CloneTimeout,
		InstallTimeout: w.InstallTimeout,
		StartTimeout:   w.StartTimeout,
		CleanupTimeout: w.CleanupTimeout,
		OnStage: func(_ context.Context, stage deploy.Stage) error {
			if stage == deploy.Ready {
				w.emit(d, state.DeploymentEvent{Attempt: d.Attempt, EventType: "lifecycle", Stage: string(stage), Message: "deployment is ready"})
				return nil
			}
			w.emit(d, state.DeploymentEvent{Attempt: d.Attempt, EventType: "lifecycle", Stage: string(stage), Message: deploymentStageMessage(stage)})
			return w.transition(&d, state.DeploymentStatusCreating, string(stage), "")
		},
		OnCheckpoint: func(_ context.Context, checkpoint deploy.Checkpoint) error {
			if checkpoint.Result.Process.RuntimeID != "" {
				d.RuntimeID = checkpoint.Result.Process.RuntimeID
				d.ProcessID = checkpoint.Result.Process.ID
				d.ProcessTag = checkpoint.Result.Process.Tag
			}
			if checkpoint.StartIntent.Tag != "" {
				d.ProcessTag = checkpoint.StartIntent.Tag
			}
			return w.persist(&d)
		},
		OnOutput: func(event deploy.OutputEvent) {
			w.emit(d, state.DeploymentEvent{Attempt: d.Attempt, EventType: "output", Stage: string(event.Stage), Stream: string(event.Stream), Message: event.Message})
			if w.Output != nil {
				w.Output(d, event)
			}
		},
	})
	if applyErr != nil {
		cleanupErr := w.closeAttempt(ctx, rt)
		cleanupDone = true
		reason := "deployment execution failed"
		if cleanupErr == nil {
			d.SandboxID = ""
			d.RuntimeID = ""
			d.ProcessID = ""
			d.ProcessTag = ""
		} else {
			reason = "deployment execution failed; cleanup is incomplete"
		}
		return w.fail(&d, string(stageFromError(applyErr)), reason)
	}
	d.Endpoint = result.Endpoint
	d.ProcessID = result.Process.ID
	d.ProcessTag = result.Process.Tag
	d.RuntimeID = result.Process.RuntimeID
	if err := w.transition(&d, state.DeploymentStatusRunning, "ready", ""); err != nil {
		return err
	}
	keepAttempt = true
	return nil
}

// recoverCreating compensates interrupted execution instead of replaying
// uncheckpointed user commands. Never create a replacement for an unknown
// operation. A provider outage keeps the saved attempt unresolved.
func (w *Worker) recoverCreating(ctx context.Context, d state.Deployment) error {
	if d.SandboxID == "" {
		lookupCtx, cancel := context.WithTimeout(ctx, w.CleanupTimeout)
		rt, id, err := w.Runtime.Reconcile(lookupCtx, d)
		cancel()
		if rt == nil {
			if err != nil {
				return err
			}
			if time.Now().Before(d.ExpiresAt.Add(2 * time.Minute)) {
				return deployruntime.ErrCreatePending
			}
			return w.fail(&d, "recovery", "interrupted creation has no remaining Sandbox")
		}
		if strings.TrimSpace(id) == "" || strings.TrimSpace(rt.ID()) == "" {
			return deploy.ErrRuntimeMismatch
		}
		d.SandboxID, d.RuntimeID = id, rt.ID()
		if err := w.persist(&d); err != nil {
			return err
		}
	}
	if err := w.fail(&d, "recovery", "deployment execution was interrupted"); err != nil {
		return err
	}
	return w.Stop(ctx, d.ID, !time.Now().Before(d.ExpiresAt))
}

func (w *Worker) checkRunning(ctx context.Context, d state.Deployment) error {
	checkCtx, cancel := context.WithTimeout(ctx, w.CleanupTimeout)
	defer cancel()
	rt, err := w.Runtime.Connect(checkCtx, d)
	if err != nil {
		return err
	}
	var process deploy.ProcessState
	if rt != nil {
		ref := deploy.ProcessRef{RuntimeID: d.RuntimeID, ID: d.ProcessID, Tag: d.ProcessTag}
		var inspectErr error
		process, inspectErr = rt.Inspect(checkCtx, ref)
		if inspectErr != nil && !errors.Is(inspectErr, deploy.ErrProcessUnknown) {
			return inspectErr
		}
		if inspectErr != nil {
			process = deploy.ProcessState{}
		}
	}
	// Provider I/O may cross the saved expiry while the provider itself removes
	// the resource. Recheck the deadline before classifying a missing process.
	if !d.ExpiresAt.IsZero() && !time.Now().UTC().Before(d.ExpiresAt) {
		return w.stop(checkCtx, d.ID, true, &d)
	}
	if rt != nil && process.Running {
		return nil
	}
	if err := w.fail(&d, "process_exit", "deployment process is no longer running"); err != nil {
		return err
	}
	return w.Stop(checkCtx, d.ID, false)
}

func (w *Worker) closeAttempt(ctx context.Context, rt RuntimeHandle) error {
	if rt == nil {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.CleanupTimeout)
	defer cancel()
	return rt.Close(cleanup)
}

// Stop stops the exact persisted attempt and then deletes its Sandbox. A
// missing Sandbox is treated as already cleaned; a live replacement cannot be
// touched because RuntimeFactory.Connect must bind to the saved Sandbox ID.
func (w *Worker) Stop(ctx context.Context, id string, expired bool) error {
	return w.stop(ctx, id, expired, nil)
}

func (w *Worker) stop(ctx context.Context, id string, expired bool, observed *state.Deployment) error {
	ctx, cancel := context.WithTimeout(ctx, w.CleanupTimeout)
	defer cancel()
	d, err := w.Store.GetDeployment(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if observed != nil && d.Version != observed.Version {
		return state.ErrConflict
	}
	preserveFailed := d.Status == state.DeploymentStatusFailed ||
		(d.Status == state.DeploymentStatusStopping && d.FailureReason != "")
	if (d.Status == state.DeploymentStatusCompleted || d.Status == state.DeploymentStatusExpired || d.Status == state.DeploymentStatusFailed) && strings.TrimSpace(d.SandboxID) == "" {
		return nil
	}
	if d.SandboxID == "" && d.Attempt > 0 && (d.Status == state.DeploymentStatusCreating || d.Status == state.DeploymentStatusStopping) {
		rt, sandboxID, lookupErr := w.Runtime.Reconcile(ctx, d)
		if lookupErr != nil && rt == nil {
			return lookupErr
		}
		if rt == nil && time.Now().Before(d.ExpiresAt.Add(2*time.Minute)) {
			return deployruntime.ErrCreatePending
		}
		if rt != nil {
			if sandboxID == "" || rt.ID() == "" {
				return deploy.ErrRuntimeMismatch
			}
			d.SandboxID, d.RuntimeID = sandboxID, rt.ID()
			if err := w.persist(&d); err != nil {
				return err
			}
		}
	}
	if err := w.transition(&d, state.DeploymentStatusStopping, "cleanup", ""); err != nil {
		return err
	}
	if strings.TrimSpace(d.SandboxID) != "" {
		rt, connectErr := w.Runtime.Connect(ctx, d)
		if connectErr != nil {
			if persistErr := w.fail(&d, "cleanup", "deployment cleanup is incomplete"); persistErr != nil {
				return persistErr
			}
			return connectErr
		}
		if rt != nil {
			runtimeID := d.RuntimeID
			if runtimeID == "" {
				runtimeID = rt.ID()
			}
			if d.ProcessID != "" && d.ProcessTag != "" {
				if stopErr := deploy.Stop(ctx, rt, deploy.ProcessRef{RuntimeID: runtimeID, ID: d.ProcessID, Tag: d.ProcessTag}); stopErr != nil && !errors.Is(stopErr, deploy.ErrProcessUnknown) {
					if persistErr := w.fail(&d, "cleanup", "deployment cleanup is incomplete"); persistErr != nil {
						return persistErr
					}
					return stopErr
				}
			}
			if closeErr := rt.Close(ctx); closeErr != nil && err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		if persistErr := w.fail(&d, "cleanup", "deployment cleanup is incomplete"); persistErr != nil {
			return persistErr
		}
		return err
	}
	if expired {
		d.Status = state.DeploymentStatusExpired
	} else {
		d.Status = state.DeploymentStatusCompleted
	}
	if preserveFailed {
		d.Status = state.DeploymentStatusFailed
	}
	d.SandboxID = ""
	d.RuntimeID = ""
	d.ProcessID = ""
	d.ProcessTag = ""
	return w.persist(&d)
}

func (w *Worker) transition(d *state.Deployment, status, stage, reason string) error {
	// Cleanup emits its own lifecycle stage, but successful compensation must
	// retain the failure that caused it for the deployment and its revision.
	preserveFailure := d.Status == state.DeploymentStatusFailed ||
		(d.Status == state.DeploymentStatusStopping && d.FailureReason != "")
	if status != state.DeploymentStatusStopping || !preserveFailure {
		d.FailureStage = stage
		d.FailureReason = reason
	}
	d.Status = status
	err := w.persist(d)
	if err == nil {
		w.emit(*d, state.DeploymentEvent{Attempt: d.Attempt, EventType: "lifecycle", Stage: stage, Message: "status: " + status})
	}
	return err
}

func (w *Worker) fail(d *state.Deployment, stage, reason string) error {
	d.Status = state.DeploymentStatusFailed
	d.FailureStage = stage
	d.FailureReason = reason
	d.Error = reason
	err := w.persist(d)
	if err == nil {
		w.emit(*d, state.DeploymentEvent{Attempt: d.Attempt, EventType: "failure", Stage: stage, Message: reason})
	}
	return err
}

func (w *Worker) emit(d state.Deployment, event state.DeploymentEvent) {
	if w == nil || w.Event == nil {
		return
	}
	event.DeploymentID = d.ID
	event.RevisionNumber = d.RevisionNumber
	if event.Attempt == 0 {
		event.Attempt = d.Attempt
	}
	w.Event(d, event)
}

func (w *Worker) persist(d *state.Deployment) error {
	if err := w.Store.UpdateDeployment(*d); err != nil {
		return err
	}
	d.Version++
	return nil
}

// DecodeJSONSpec decrypts and normalizes a persisted JSON Spec.
func DecodeJSONSpec(d state.Deployment, decrypt func(string) (string, error)) (deploy.Spec, error) {
	if decrypt == nil {
		return deploy.Spec{}, errors.New("deployment spec decryptor is required")
	}
	value, err := decrypt(d.SpecJSONEncrypted)
	if err != nil {
		return deploy.Spec{}, err
	}
	var spec deploy.Spec
	if err := json.Unmarshal([]byte(value), &spec); err != nil {
		return deploy.Spec{}, err
	}
	return deploy.NormalizeSpec(spec)
}

func githubSourceURL(repository string) string {
	return "https://github.com/" + strings.TrimSuffix(strings.TrimSpace(repository), ".git") + ".git"
}

func stageFromError(err error) deploy.Stage {
	var stageErr *deploy.StageError
	if errors.As(err, &stageErr) {
		return stageErr.Stage
	}
	return deploy.Preparing
}

func deploymentStageMessage(stage deploy.Stage) string {
	switch stage {
	case deploy.Preparing:
		return "Preparing deployment workspace."
	case deploy.Cloning:
		return "Cloning repository at the pinned Commit."
	case deploy.Verifying:
		return "Verifying the checked-out Commit."
	case deploy.Installing:
		return "Running installation command."
	case deploy.Starting:
		return "Starting application."
	case deploy.Probing:
		return "Checking application readiness."
	default:
		return "Deployment stage: " + string(stage)
	}
}

// GitSourceFactory builds the standard GitHub HTTPS source. The token is held
// only in transient Git environment variables and is redacted by deployd.
func GitSourceFactory(token func(context.Context, int64) (string, error)) SourceFactory {
	return func(ctx context.Context, d state.Deployment) (deploy.Source, error) {
		if token == nil {
			return nil, errors.New("github token source is not configured")
		}
		accountID := d.ExecutionAccountID
		if accountID == 0 {
			accountID = d.AccountID
		}
		value, err := token(ctx, accountID)
		if err != nil {
			return nil, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("github token is unavailable")
		}
		encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + value))
		env := map[string]string{
			"GIT_CONFIG_COUNT":    "1",
			"GIT_CONFIG_KEY_0":    "http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0":  "AUTHORIZATION: basic " + encoded,
			"GIT_TERMINAL_PROMPT": "0",
			"GIT_CONFIG_NOSYSTEM": "1",
			"GIT_CONFIG_GLOBAL":   "/dev/null",
		}
		return deploygit.New(githubSourceURL(d.RepositoryFullName), deploygit.Options{Ref: d.CommitSHA, CheckoutEnv: env})
	}
}
