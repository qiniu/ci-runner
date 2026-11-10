package worker

import (
	"errors"
	"fmt"
	"testing"
	"time"

	deploy "github.com/miclle/deployd"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestFailureCleanupPreservesFirstFailureTime(t *testing.T) {
	for _, status := range []string{state.DeploymentStatusCreating, state.DeploymentStatusRunning} {
		for _, cleanupFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cleanup_failure=%t", status, cleanupFails), func(t *testing.T) {
				store := state.New(t.TempDir())
				d := deploymentFixture(t)
				d.Status, d.Attempt, d.SandboxID = status, 1, "sandbox-1"
				if _, err := store.CreateDeployment(d); err != nil {
					t.Fatal(err)
				}
				runtime := &fakeRuntime{id: d.SandboxID, inspectErr: deploy.ErrProcessUnknown}
				if cleanupFails {
					runtime.closeErr = errors.New("delete unavailable")
				}
				w := New(store, &fakeFactory{runtime: runtime}, nil, nil, t.TempDir())
				var firstFailure, stopping time.Time
				w.Event = func(item state.Deployment, event state.DeploymentEvent) {
					saved, err := store.GetDeployment(item.ID)
					if err != nil {
						t.Fatal(err)
					}
					if event.EventType == "failure" && firstFailure.IsZero() {
						firstFailure = saved.FailedAt
					}
					if event.EventType == "lifecycle" && item.Status == state.DeploymentStatusStopping {
						stopping = saved.StoppingAt
					}
				}
				if err := w.RunOnce(t.Context()); (err != nil) != cleanupFails {
					t.Fatalf("cleanup error=%v", err)
				}
				got, err := store.GetDeployment(d.ID)
				if err != nil {
					t.Fatal(err)
				}
				if firstFailure.IsZero() || !got.FailedAt.Equal(firstFailure) || stopping.IsZero() || !got.StoppingAt.Equal(stopping) {
					t.Fatalf("cleanup rewrote timestamps: first_failure=%s final_failure=%s stopping=%s final_stopping=%s", firstFailure, got.FailedAt, stopping, got.StoppingAt)
				}
				if got.Status != state.DeploymentStatusFailed || got.Attempt != d.Attempt || !runtime.closed || (got.SandboxID != "") != cleanupFails {
					t.Fatalf("unexpected cleanup result: %#v", got)
				}
			})
		}
	}
}

type deploymentReadbackStore struct {
	state.Store
	mode    string
	updates int
}

func (s *deploymentReadbackStore) UpdateClaimedDeployment(d state.Deployment, token string) error {
	if err := s.Store.UpdateClaimedDeployment(d, token); err != nil {
		return err
	}
	s.updates++
	return nil
}

func (s *deploymentReadbackStore) GetDeployment(id string) (state.Deployment, error) {
	d, err := s.Store.GetDeployment(id)
	if err != nil || s.updates == 0 {
		return d, err
	}
	switch s.mode {
	case "version":
		d.Version++
	case "attempt":
		d.Attempt++
	case "revision":
		d.RevisionNumber++
	case "read error":
		return state.Deployment{}, errors.New("read unavailable")
	}
	return d, nil
}

func TestFailureCleanupRejectsChangedPersistenceReadback(t *testing.T) {
	for _, mode := range []string{"version", "attempt", "revision", "read error"} {
		t.Run(mode, func(t *testing.T) {
			store := &deploymentReadbackStore{Store: state.New(t.TempDir()), mode: mode}
			d := deploymentFixture(t)
			d.Status, d.Attempt, d.SandboxID = state.DeploymentStatusCreating, 1, "sandbox-1"
			if _, err := store.CreateDeployment(d); err != nil {
				t.Fatal(err)
			}
			runtime := &fakeRuntime{id: d.SandboxID}
			w := New(store, &fakeFactory{runtime: runtime}, nil, nil, t.TempDir())
			err := w.RunOnce(t.Context())
			if err == nil || (mode != "read error" && !errors.Is(err, state.ErrConflict)) {
				t.Fatalf("accepted changed or unavailable readback: %v", err)
			}
			if store.updates != 1 || runtime.closed {
				t.Fatalf("cleanup continued after readback: writes=%d closed=%t", store.updates, runtime.closed)
			}
			got, err := store.Store.GetDeployment(d.ID)
			if err != nil || got.Status != state.DeploymentStatusFailed || got.SandboxID != d.SandboxID || !got.StoppingAt.IsZero() {
				t.Fatalf("readback failure changed cleanup evidence: %#v err=%v", got, err)
			}
		})
	}
}
