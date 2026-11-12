package server

import (
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestDeploymentTerminalRejectsChangedAttempt(t *testing.T) {
	original := state.Deployment{ID: "d", AccountID: 1, RevisionNumber: 2, Attempt: 3, Status: state.DeploymentStatusRunning, SandboxID: "sb", ExpiresAt: time.Now().Add(time.Hour)}
	if !sameDeploymentTerminal(original, original) {
		t.Fatal("current attempt rejected")
	}
	for _, mutate := range []func(*state.Deployment){
		func(d *state.Deployment) { d.RevisionNumber++ }, func(d *state.Deployment) { d.Attempt++ }, func(d *state.Deployment) { d.SandboxID = "new" }, func(d *state.Deployment) { d.Status = state.DeploymentStatusStopping }, func(d *state.Deployment) { d.ExpiresAt = time.Now().Add(-time.Second) }, func(d *state.Deployment) { d.AccountID++ },
	} {
		changed := original
		mutate(&changed)
		if sameDeploymentTerminal(original, changed) {
			t.Fatalf("accepted stale attempt %#v", changed)
		}
	}
}
