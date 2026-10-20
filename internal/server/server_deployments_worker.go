package server

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	deploy "github.com/miclle/deployd"
	deployruntime "github.com/qiniu/ci-runner/internal/deployment/runtime"
	deploymentworker "github.com/qiniu/ci-runner/internal/deployment/worker"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type deploymentSandboxService interface {
	sandboxrunner.Service
	CreateDeploymentRuntime(context.Context, string, time.Duration, string) (*deployruntime.Handle, error)
	ConnectDeploymentRuntime(context.Context, string, time.Duration) (*deployruntime.Handle, error)
	ReconcileDeploymentRuntime(context.Context, string, string, time.Duration) (*deployruntime.Handle, error)
}

type serverDeploymentRuntimeFactory struct {
	server     *Server
	templateID string
}

func (f *serverDeploymentRuntimeFactory) service(ctx context.Context, d state.Deployment) (deploymentSandboxService, sandboxServiceConfigSnapshot, error) {
	snapshot := sandboxServiceConfigSnapshot{
		APIURL: d.SandboxAPIURL, EncryptedAPIKey: d.SandboxAPIKeyEncrypted,
		Source: d.SandboxConfigSource,
	}
	if f == nil || f.server == nil {
		return nil, snapshot, errors.New("deployment Sandbox service is not configured")
	}
	var service sandboxrunner.Service
	if strings.TrimSpace(d.SandboxAPIURL) != "" && strings.TrimSpace(d.SandboxAPIKeyEncrypted) != "" {
		resolved, err := f.server.sandboxServiceForConfig(snapshot)
		if err != nil {
			return nil, snapshot, err
		}
		service = resolved
	} else {
		scope := accountPreferenceScope{Type: d.ScopeType, ID: d.ScopeID}
		resolved, current, err := f.server.sandboxServiceForDeployment(ctx, scope, d.GitHubInstallationID)
		if err != nil {
			return nil, current, err
		}
		service = resolved
		snapshot = current
	}
	resolved, ok := service.(deploymentSandboxService)
	if !ok {
		return nil, snapshot, errors.New("configured Sandbox service does not support Deploy runtimes")
	}
	return resolved, snapshot, nil
}

func (s *Server) sandboxServiceForDeployment(ctx context.Context, scope accountPreferenceScope, installationID int64) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	service, snapshot, err := s.sandboxServiceForScope(scope)
	if err == nil || !errors.Is(err, errSandboxServiceNotConfigured) {
		return service, snapshot, err
	}
	// A personal repository collaborator uses their own credential scope, but
	// platform eligibility belongs to the repository's Installation owner.
	return s.sandboxServiceForAdminDefault(func() (state.GitHubInstallationAccount, error) {
		return s.githubInstallationOwner(ctx, installationID)
	})
}

func (f *serverDeploymentRuntimeFactory) Create(ctx context.Context, d state.Deployment, attempt int64) (deploymentworker.RuntimeHandle, string, error) {
	service, snapshot, err := f.service(ctx, d)
	if err != nil {
		return nil, "", err
	}
	operationID := d.ID + "-" + formatAttempt(attempt)
	templateID := f.template(d)
	// Legacy queued records without credentials must not gain access to the
	// platform's private templates when resolving a service for the first time.
	if snapshot.Source == sandboxConfigSourceAdminDefault && (strings.TrimSpace(d.SandboxAPIURL) == "" || strings.TrimSpace(d.SandboxAPIKeyEncrypted) == "") {
		allowed, err := publicDeploymentTemplateAllowed(ctx, service, templateID)
		if err != nil {
			// Factory errors may reach server logs; discard raw provider details.
			return nil, "", errors.New("public deployment template catalog is unavailable")
		}
		if !allowed {
			return nil, "", errors.New("deployment template must be public when using the platform Sandbox service")
		}
	} else if err := service.ValidateTemplate(ctx, templateID); err != nil {
		return nil, "", errors.New("deployment template is unavailable")
	}
	handle, err := service.CreateDeploymentRuntime(ctx, templateID, time.Until(d.ExpiresAt), operationID)
	if err != nil {
		if handle != nil {
			return handle, handle.SandboxID, err
		}
		return nil, "", err
	}
	return handle, handle.SandboxID, nil
}

func (f *serverDeploymentRuntimeFactory) Connect(ctx context.Context, d state.Deployment) (deploymentworker.RuntimeHandle, error) {
	service, _, err := f.service(ctx, d)
	if err != nil {
		return nil, err
	}
	handle, err := service.ConnectDeploymentRuntime(ctx, d.SandboxID, time.Until(d.ExpiresAt))
	if err != nil {
		if errors.Is(err, deployruntime.ErrSandboxNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return handle, nil
}

func (f *serverDeploymentRuntimeFactory) template(d state.Deployment) string {
	if value := strings.TrimSpace(d.DeploymentTemplateID); value != "" {
		return value
	}
	return strings.TrimSpace(f.templateID)
}

func (f *serverDeploymentRuntimeFactory) Reconcile(ctx context.Context, d state.Deployment) (deploymentworker.RuntimeHandle, string, error) {
	service, _, err := f.service(ctx, d)
	if err != nil {
		return nil, "", err
	}
	handle, err := service.ReconcileDeploymentRuntime(ctx, f.template(d), d.ID+"-"+formatAttempt(d.Attempt), time.Until(d.ExpiresAt))
	if handle != nil {
		return handle, handle.SandboxID, err
	}
	return nil, "", err
}

func formatAttempt(attempt int64) string {
	if attempt < 1 {
		return "1"
	}
	return strconv.FormatInt(attempt, 10)
}

func (s *Server) newDeploymentWorker() *deploymentworker.Worker {
	factory := &serverDeploymentRuntimeFactory{server: s, templateID: s.cfg.DeploymentTemplateID}
	decode := func(d state.Deployment) (deploy.Spec, error) {
		return deploymentworker.DecodeJSONSpec(d, func(value string) (string, error) {
			return decryptSecret(value, s.cfg.AuthEncryptionKey.Value())
		})
	}
	worker := deploymentworker.New(s.store, factory, s.deploymentSource, decode, s.cfg.DeploymentWorkRoot)
	worker.Retention = s.cfg.DeploymentRetention
	worker.TemplateID = s.cfg.DeploymentTemplateID
	worker.CreateTimeout = s.cfg.SandboxCreateTimeout
	worker.Event = func(_ state.Deployment, event state.DeploymentEvent) {
		if err := s.store.AppendDeploymentEvent(event); err != nil {
			s.logger.Error("append deployment event", "deployment_id", event.DeploymentID, "error", err)
		}
	}
	return worker
}

// A queued revision executes with its submitter's current repository access.
// Creator identity remains stable for auditing and request-key replay.
func (s *Server) deploymentSource(ctx context.Context, d state.Deployment) (deploy.Source, error) {
	accountID := d.ExecutionAccountID
	if accountID == 0 {
		accountID = d.AccountID
	}
	access, err := s.refreshUserRepositoryAccess(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !hasRepositoryAccess(access, d.GitHubInstallationID, d.RepositoryFullName) {
		return nil, errors.New("deployment repository access is unavailable")
	}
	return deploymentworker.GitSourceFactory(func(_ context.Context, actorID int64) (string, error) {
		return s.githubUserAccessToken(actorID)
	})(ctx, d)
}
