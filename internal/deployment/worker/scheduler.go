package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

// Run keeps provisioning, health probes and cleanup on independent bounded
// lanes. A slow build cannot occupy cleanup or monitor capacity. Each lane
// walks one keyset cycle per tick; completion fills available slots without
// probing the same running deployment repeatedly in the same cycle.
func (w *Worker) Run(ctx context.Context, notify <-chan struct{}, report func(error)) {
	if w == nil || w.Store == nil {
		return
	}
	type lane struct {
		kind      string
		capacity  int
		active    int
		afterID   string
		exhausted bool
	}
	lanes := []*lane{{kind: "provision", capacity: 4}, {kind: "monitor", capacity: 8}, {kind: "cleanup", capacity: 4}}
	type completion struct {
		id   string
		lane *lane
		err  error
	}
	completed := make(chan completion, 16)
	inFlight := make(map[string]context.CancelFunc)
	var workers sync.WaitGroup
	defer func() {
		for _, cancel := range inFlight {
			cancel()
		}
		workers.Wait()
	}()
	reportError := func(err error) {
		if err != nil && !errors.Is(err, context.Canceled) && report != nil {
			report(err)
		}
	}
	schedule := func(l *lane) {
		for !l.exhausted && l.active < l.capacity && ctx.Err() == nil {
			// Include room to skip the bounded set of in-flight identities when
			// a deployment has changed lanes while an earlier operation exits.
			limit := l.capacity - l.active + len(inFlight)
			items, more, err := w.Store.ListDeploymentWork(l.kind, l.afterID, limit, time.Now().UTC())
			if err != nil {
				reportError(err)
				return
			}
			consumed := true
			for _, item := range items {
				if l.active == l.capacity {
					consumed = false
					break
				}
				l.afterID = item.ID
				if _, busy := inFlight[item.ID]; busy {
					continue
				}
				jobCtx, cancel := context.WithCancel(ctx)
				inFlight[item.ID] = cancel
				l.active++
				workers.Add(1)
				go func(item state.Deployment) {
					defer workers.Done()
					err := w.advance(jobCtx, item)
					select {
					case completed <- completion{id: item.ID, lane: l, err: err}:
					case <-ctx.Done():
					}
				}(item)
			}
			if !more && consumed {
				l.exhausted, l.afterID = true, ""
			}
		}
	}
	tick := func() {
		if w.Retention > 0 {
			_, err := w.Store.PruneDeployments(time.Now().UTC().Add(-w.Retention), 100)
			reportError(err)
		}
		for _, l := range lanes {
			l.exhausted = false
			schedule(l)
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		case <-notify:
			// New admission need not reset or amplify health-probe cycles.
			lanes[0].exhausted = false
			for _, l := range lanes {
				schedule(l)
			}
		case result := <-completed:
			inFlight[result.id]()
			delete(inFlight, result.id)
			result.lane.active--
			reportError(result.err)
			schedule(result.lane)
		}
	}
}
