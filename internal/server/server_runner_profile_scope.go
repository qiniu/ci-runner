package server

import (
	"errors"

	"github.com/qiniu/ci-runner/internal/state"
)

// runnerProfileScopeForInstallation maps a webhook installation to a local
// account scope for personal installations or an installation scope for
// organizations. Unknown installations deliberately fall back to global.
func runnerProfileScopeForInstallation(store state.Store, installationID int64) (state.RunnerProfileScope, bool, error) {
	if installationID <= 0 {
		return state.RunnerProfileScope{}, false, nil
	}
	if accountID, ok, err := store.AccountScopeForPersonalGitHubInstallation(installationID); err != nil {
		return state.RunnerProfileScope{}, false, err
	} else if ok && accountID > 0 {
		return state.RunnerProfileScope{Type: state.RunnerProfileScopeAccount, ID: accountID}, true, nil
	}
	owner, err := store.GetGitHubInstallationOwner(installationID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			return state.RunnerProfileScope{}, false, err
		}
		return state.RunnerProfileScope{}, false, nil
	}
	if owner.AccountType == "Organization" || owner.AccountType == "organization" {
		return state.RunnerProfileScope{Type: state.RunnerProfileScopeGitHubInstallation, ID: installationID}, true, nil
	}
	return state.RunnerProfileScope{}, false, nil
}
