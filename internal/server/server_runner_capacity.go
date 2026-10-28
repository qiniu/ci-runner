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
	mu       sync.Mutex
	started  uint64
	pending  int
	profiles map[runnerCapacityKey]*runnerCapacityCounter
}

type runnerCapacityObservation struct {
	capacity       *runnerAdmissionCapacity
	key            runnerCapacityKey
	profile        *runnerCapacityCounter
	started        uint64
	pending        int
	profileStarted uint64
	profilePending int
	reserved       bool
}

func (c *runnerAdmissionCapacity) observe(req state.RunnerRequest) *runnerCapacityObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := runnerCapacityProfileKey(req)
	if c.profiles == nil {
		c.profiles = make(map[runnerCapacityKey]*runnerCapacityCounter)
	}
	profile := c.profiles[key]
	if profile == nil {
		profile = &runnerCapacityCounter{}
		c.profiles[key] = profile
	}
	profile.readers++
	return &runnerCapacityObservation{
		capacity: c, key: key, profile: profile,
		started: c.started, pending: c.pending, profileStarted: profile.started, profilePending: profile.pending,
	}
}

func (o *runnerCapacityObservation) reserve(globalCount, globalLimit, profileCount, profileLimit int) string {
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
	c.started++
	c.pending++
	o.profile.started++
	o.profile.pending++
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
	}
	o.profile.readers--
	if o.profile.readers == 0 {
		delete(c.profiles, o.key)
	}
}

// The caller holds admissionMu.RLock from profile reload through creating CAS,
// excluding spec mutations while permitting independent Store operations.
// The capacity mutex protects only memory; no Store call runs under it.
func (s *Server) reserveRunnerCapacity(req state.RunnerRequest, profile state.RunnerProfile, timings *runnerTimings) (release func(), stage string, err error) {
	observation := s.runnerCapacity.observe(req)
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
	started = time.Now()
	stage = observation.reserve(globalCount, s.cfg.MaxConcurrentRunners, profileCount, profileLimit)
	timings.record("capacity_reservation", started)
	if stage != "" {
		return nil, stage, nil
	}
	return observation.finish, "", nil
}
