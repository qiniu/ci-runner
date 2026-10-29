package server

import (
	"strings"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

type runnerCapacityKey struct {
	source, scopeType, name string
	scopeID                 int64
}

func normalizeRunnerRepository(repository string) string {
	return strings.ToLower(strings.TrimSpace(repository))
}

func runnerRepositoryOwner(repository string) string {
	owner, _, found := strings.Cut(normalizeRunnerRepository(repository), "/")
	if !found {
		return ""
	}
	return owner
}

func runnerCapacityProfileKey(req state.RunnerRequest) runnerCapacityKey {
	key := runnerCapacityKey{source: "global", name: strings.TrimSpace(req.ProfileName)}
	if req.ProfileSource != "" && req.ProfileSource != "global" && req.ProfileScopeType != "" && req.ProfileScopeID > 0 {
		key.source, key.scopeType, key.scopeID = req.ProfileSource, req.ProfileScopeType, req.ProfileScopeID
	}
	return key
}

type runnerCapacityCounter struct {
	started uint64
	pending int
	readers int
}

// Only creating transitions increase in-flight capacity. Reserve them before
// WriteState and release after it returns; successful writes are then covered
// by Store counts. Recovery uses those durable counts, not process-local state.
// Like admissionMu, this coordinates a single runnerd process, not replicas.
type runnerAdmissionCapacity struct {
	mu           sync.Mutex
	started      uint64
	pending      int
	profiles     map[runnerCapacityKey]*runnerCapacityCounter
	accounts     map[string]*runnerCapacityCounter
	repositories map[string]*runnerCapacityCounter
}

type runnerCapacityObservation struct {
	capacity          *runnerAdmissionCapacity
	key               runnerCapacityKey
	profile           *runnerCapacityCounter
	started           uint64
	pending           int
	profileStarted    uint64
	profilePending    int
	account           *runnerCapacityCounter
	repository        *runnerCapacityCounter
	accountStarted    uint64
	accountPending    int
	repositoryStarted uint64
	repositoryPending int
	accountKey        string
	repositoryKey     string
	reserved          bool
}

func (c *runnerAdmissionCapacity) observe(req state.RunnerRequest, profileName string) *runnerCapacityObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := runnerCapacityProfileKey(req)
	// Requests can retain an alias spelling under a case-insensitive database
	// collation. Use the reloaded row's name, not Go case folding, so aliases
	// share capacity while distinct names on case-sensitive backends do not.
	key.name = profileName
	if c.profiles == nil {
		c.profiles = make(map[runnerCapacityKey]*runnerCapacityCounter)
	}
	if c.accounts == nil {
		c.accounts = make(map[string]*runnerCapacityCounter)
	}
	if c.repositories == nil {
		c.repositories = make(map[string]*runnerCapacityCounter)
	}
	profile := c.profiles[key]
	if profile == nil {
		profile = &runnerCapacityCounter{}
		c.profiles[key] = profile
	}
	profile.readers++
	accountKey := runnerRepositoryOwner(req.RepositoryFullName)
	var account *runnerCapacityCounter
	if accountKey != "" {
		account = c.accounts[accountKey]
		if account == nil {
			account = &runnerCapacityCounter{}
			c.accounts[accountKey] = account
		}
		account.readers++
	}
	repositoryKey := normalizeRunnerRepository(req.RepositoryFullName)
	var repository *runnerCapacityCounter
	if repositoryKey != "" {
		repository = c.repositories[repositoryKey]
		if repository == nil {
			repository = &runnerCapacityCounter{}
			c.repositories[repositoryKey] = repository
		}
		repository.readers++
	}
	return &runnerCapacityObservation{
		capacity: c, key: key, profile: profile,
		started: c.started, pending: c.pending, profileStarted: profile.started, profilePending: profile.pending,
		account: account, repository: repository,
		accountStarted: counterStarted(account), accountPending: counterPending(account),
		repositoryStarted: counterStarted(repository), repositoryPending: counterPending(repository),
		accountKey: accountKey, repositoryKey: repositoryKey,
	}
}

func counterStarted(counter *runnerCapacityCounter) uint64 {
	if counter == nil {
		return 0
	}
	return counter.started
}

func counterPending(counter *runnerCapacityCounter) int {
	if counter == nil {
		return 0
	}
	return counter.pending
}

func (o *runnerCapacityObservation) reserve(globalCount, globalLimit, profileCount, profileLimit, accountCount, accountLimit, repositoryCount, repositoryLimit int) string {
	c := o.capacity
	c.mu.Lock()
	defer c.mu.Unlock()
	// A count can precede a concurrent creating write. Include reservations
	// pending before the read AND all admissions since it began, even if they
	// have since finished writing. Never subtract a reservation on this read's
	// behalf: its result may still exclude that write. Double counting is safe
	// and lasts only until a fresh queued attempt; undercounting oversubscribes.
	if globalCount+o.pending+int(c.started-o.started) >= globalLimit {
		return "global_capacity_check"
	}
	if profileLimit > 0 && profileCount+o.profilePending+int(o.profile.started-o.profileStarted) >= profileLimit {
		return "profile_capacity_check"
	}
	if accountLimit > 0 && accountCount+o.accountPending+int(counterStarted(o.account)-o.accountStarted) >= accountLimit {
		return "account_capacity_check"
	}
	if repositoryLimit > 0 && repositoryCount+o.repositoryPending+int(counterStarted(o.repository)-o.repositoryStarted) >= repositoryLimit {
		return "repository_capacity_check"
	}
	c.started++
	c.pending++
	o.profile.started++
	o.profile.pending++
	if o.account != nil {
		o.account.started++
		o.account.pending++
	}
	if o.repository != nil {
		o.repository.started++
		o.repository.pending++
	}
	o.reserved = true
	return ""
}

func (o *runnerCapacityObservation) finish() {
	c := o.capacity
	c.mu.Lock()
	defer c.mu.Unlock()
	if o.reserved {
		c.pending--
		o.profile.pending--
		if o.account != nil {
			o.account.pending--
		}
		if o.repository != nil {
			o.repository.pending--
		}
	}
	o.profile.readers--
	if o.account != nil {
		o.account.readers--
	}
	if o.repository != nil {
		o.repository.readers--
	}
	if o.profile.readers == 0 {
		delete(c.profiles, o.key)
	}
	if o.account != nil && o.account.readers == 0 {
		delete(c.accounts, o.accountKey)
	}
	if o.repository != nil && o.repository.readers == 0 {
		delete(c.repositories, o.repositoryKey)
	}
}

// The caller holds admissionMu.RLock from profile reload through creating CAS,
// excluding spec mutations while permitting independent Store operations.
// The capacity mutex protects only memory; no Store call runs under it.
func (s *Server) reserveRunnerCapacity(req state.RunnerRequest, profile state.RunnerProfile, timings *runnerTimings) (release func(), stage string, err error) {
	observation := s.runnerCapacity.observe(req, profile.Name)
	defer func() {
		if release == nil {
			observation.finish()
		}
	}()
	stage = "global_capacity_check"
	started := time.Now()
	globalCount, err := s.store.InFlightCount()
	timings.record(stage, started)
	if err != nil || globalCount >= s.cfg.MaxConcurrentRunners {
		return nil, stage, err
	}
	profileCount, profileLimit := 0, 0
	if req.ProfileName != "" {
		stage = "profile_capacity_check"
		started = time.Now()
		profileLimit = profile.MaxConcurrency
		if profileLimit > 0 {
			profileCount, err = s.inFlightForRunnerProfile(req)
		}
		timings.record(stage, started)
		if err != nil {
			return nil, stage, err
		}
	}
	accountCount, accountLimit := 0, 0
	accountKey := runnerRepositoryOwner(req.RepositoryFullName)
	if accountKey != "" {
		accountLimit = s.cfg.RunnerAccountConcurrency[accountKey]
		if accountLimit > 0 {
			stage = "account_capacity_check"
			started = time.Now()
			accountCount, err = s.store.InFlightCountForRepositoryOwner(accountKey)
			timings.record(stage, started)
			if err != nil {
				return nil, stage, err
			}
		}
	}
	repositoryCount, repositoryLimit := 0, 0
	repositoryKey := normalizeRunnerRepository(req.RepositoryFullName)
	if repositoryKey != "" {
		repositoryLimit = s.cfg.RunnerRepositoryConcurrency[repositoryKey]
		if repositoryLimit > 0 {
			stage = "repository_capacity_check"
			started = time.Now()
			repositoryCount, err = s.store.InFlightCountForRepository(req.RepositoryFullName)
			timings.record(stage, started)
			if err != nil {
				return nil, stage, err
			}
		}
	}
	started = time.Now()
	stage = observation.reserve(globalCount, s.cfg.MaxConcurrentRunners, profileCount, profileLimit, accountCount, accountLimit, repositoryCount, repositoryLimit)
	timings.record("capacity_reservation", started)
	if stage != "" {
		return nil, stage, nil
	}
	return observation.finish, "", nil
}
