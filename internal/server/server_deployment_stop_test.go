package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	deploymentworker "github.com/qiniu/ci-runner/internal/deployment/worker"
	"github.com/qiniu/ci-runner/internal/state"
)

type deploymentStopSnapshotFactory struct {
	deploymentworker.RuntimeFactory
	observed state.Deployment
	err      error
}

func (f *deploymentStopSnapshotFactory) Connect(_ context.Context, d state.Deployment) (deploymentworker.RuntimeHandle, error) {
	f.observed = d
	return nil, f.err
}

func TestManualDeploymentStopPreservesPersistedTimestamp(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup_failure=%t", cleanupFails), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveActiveInstallationFixture(w, r, 987) {
					return
				}
				if r.URL.Path == "/user/installations/987/repositories" {
					fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"acme/project"}]}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer api.Close()
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, api.URL, &fakeSandbox{})
			srv.Close()
			srv.loopCtx = context.Background()
			account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
			if err != nil {
				t.Fatal(err)
			}
			saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
			if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "organization", AccountLogin: "acme", GitHubAccountID: 600}); err != nil {
				t.Fatal(err)
			}
			initial, err := store.CreateDeployment(state.Deployment{ID: "manual-stop", AccountID: account.ID, ScopeType: state.AccountScopeTypeGitHubInstall, ScopeID: 987, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "acme/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusRunning, Attempt: 1, SandboxID: "sandbox-1", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			initialRevision, err := store.GetDeploymentRevision(initial.ID, initial.RevisionNumber)
			if err != nil {
				t.Fatal(err)
			}
			factory := &deploymentStopSnapshotFactory{}
			if cleanupFails {
				factory.err = errors.New("provider unavailable")
			}
			srv.deploymentWorker.Runtime = factory
			rec := deploymentAuthCall(srv, http.MethodPost, "/user/deployments/"+initial.ID+"/stop", "")
			wantCode, wantStatus := http.StatusOK, state.DeploymentStatusCompleted
			if cleanupFails {
				wantCode, wantStatus = http.StatusBadGateway, state.DeploymentStatusFailed
			}
			if rec.Code != wantCode {
				t.Fatalf("stop: status=%d body=%s", rec.Code, rec.Body.String())
			}
			final, err := store.GetDeployment(initial.ID)
			if err != nil {
				t.Fatal(err)
			}
			if factory.observed.StoppingAt.IsZero() || !final.StoppingAt.Equal(factory.observed.StoppingAt) {
				t.Fatalf("stop timestamp was not carried through cleanup: observed=%s final=%s", factory.observed.StoppingAt, final.StoppingAt)
			}
			finalRevision, err := store.GetDeploymentRevision(initial.ID, initial.RevisionNumber)
			if err != nil {
				t.Fatal(err)
			}
			if finalRevision.Status != wantStatus || !finalRevision.RunningAt.Equal(initialRevision.RunningAt) || !finalRevision.CreatedAt.Equal(initialRevision.CreatedAt) || finalRevision.UpdatedAt.Before(initialRevision.UpdatedAt) {
				t.Fatalf("cleanup rewrote revision history: %#v", finalRevision)
			}
			if final.Status != wantStatus || final.RevisionNumber != initial.RevisionNumber || final.Attempt != initial.Attempt || final.Version <= initial.Version || !final.RunningAt.Equal(initial.RunningAt) {
				t.Fatalf("unexpected cleanup result: %#v", final)
			}
		})
	}
}
