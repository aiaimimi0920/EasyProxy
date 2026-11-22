package monitor

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type probeTaskPriority struct {
	tier      int
	latencyNs int64
}

func classifyProbeTaskPriority(e *entry) probeTaskPriority {
	const unknownLatency = int64(1<<62 - 1)
	if e == nil {
		return probeTaskPriority{tier: 3, latencyNs: unknownLatency}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()

	latencyNs := unknownLatency
	if e.lastProbe > 0 {
		latencyNs = e.lastProbe.Nanoseconds()
	}

	switch {
	case e.upstreamAuthFailed || e.blacklist:
		return probeTaskPriority{tier: 3, latencyNs: latencyNs}
	case e.initialCheckDone && e.available:
		return probeTaskPriority{tier: 0, latencyNs: latencyNs}
	case !e.initialCheckDone:
		return probeTaskPriority{tier: 1, latencyNs: latencyNs}
	default:
		return probeTaskPriority{tier: 2, latencyNs: latencyNs}
	}
}

func sortGenerationProbeTasks(tasks []generationProbeTask) {
	if len(tasks) <= 1 {
		return
	}
	priorities := make(map[string]probeTaskPriority, len(tasks))
	for _, task := range tasks {
		priorities[task.tag] = classifyProbeTaskPriority(task.entry)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		left := priorities[tasks[i].tag]
		right := priorities[tasks[j].tag]
		if left.tier != right.tier {
			return left.tier < right.tier
		}
		if left.latencyNs != right.latencyNs {
			return left.latencyNs < right.latencyNs
		}
		return tasks[i].tag < tasks[j].tag
	})
}

// ProbeGenerationAtLeast probes entries registered in generation, returning as
// soon as minAvailable nodes succeed while continuing remaining tail probes in
// the background under m.probeRound. If quorum is not met before all probes
// finish or ctx expires, any remaining workers are cancelled and drained
// synchronously before returning.
func (m *Manager) ProbeGenerationAtLeast(
	ctx context.Context,
	generation Generation,
	minAvailable int,
	timeout time.Duration,
) (ProbeSummary, error) {
	summary := ProbeSummary{Generation: generation}
	if m == nil {
		return summary, errors.New("monitor manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	m.exclusiveMu.Lock()
	defer m.exclusiveMu.Unlock()

	m.probeRoundMu.Lock()
	if active := m.probeRound; active != nil {
		select {
		case <-active.done:
			m.probeRound = nil
			m.clearActiveRoundLocked(active)
		default:
			m.invalidateActiveRoundLocked(active)
			active.cancel()
			<-active.done
			if m.probeRound == active {
				m.probeRound = nil
			}
		}
	}

	m.mu.RLock()
	if generation != m.reloadGen {
		m.mu.RUnlock()
		m.probeRoundMu.Unlock()
		return summary, ErrStaleGeneration
	}
	tasks := make([]generationProbeTask, 0, len(m.nodes))
	for tag, entry := range m.nodes {
		entry.mu.RLock()
		if entry.reloadGen == generation {
			tasks = append(tasks, generationProbeTask{
				tag:           tag,
				entry:         entry,
				probe:         entry.probe,
				probeRevision: entry.probeRevision,
			})
		}
		entry.mu.RUnlock()
	}
	m.mu.RUnlock()

	sortGenerationProbeTasks(tasks)
	summary.Total = len(tasks)
	if len(tasks) == 0 || minAvailable <= 0 {
		m.probeRoundMu.Unlock()
		if len(tasks) > 0 && minAvailable <= 0 {
			m.RequestProbeAllOnce(timeout)
		}
		return summary, nil
	}

	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	roundCtx, roundCancel := context.WithCancel(parent)
	round := &periodicProbeRound{
		id:         m.nextRoundID.Add(1),
		generation: generation,
		probeEpoch: m.probeEpoch.Load(),
		probeSeq:   m.nextProbeSeq.Add(1),
		gateRev:    m.currentInitialProbeRev(),
		cancel:     roundCancel,
		done:       make(chan struct{}),
	}
	m.setActiveRoundLocked(round)
	m.probeRound = round
	m.probeRoundMu.Unlock()

	var completed atomic.Int32
	var available atomic.Int32
	var failed atomic.Int32
	quorumCh := make(chan struct{})
	var quorumOnce sync.Once
	signalQuorum := func() {
		quorumOnce.Do(func() { close(quorumCh) })
	}

	go func() {
		defer func() {
			roundCancel()
			close(round.done)
			m.probeRoundMu.Lock()
			if m.probeRound == round {
				m.probeRound = nil
			}
			m.clearActiveRoundLocked(round)
			m.probeRoundMu.Unlock()
		}()

		workerLimit := probeAllWorkerLimit(len(tasks))
		sem := make(chan struct{}, workerLimit)
		var wg sync.WaitGroup
		var scheduleErr error

	scheduling:
		for _, task := range tasks {
			select {
			case sem <- struct{}{}:
			case <-roundCtx.Done():
				scheduleErr = roundCtx.Err()
				break scheduling
			}
			wg.Add(1)
			go func(task generationProbeTask) {
				defer wg.Done()
				defer func() { <-sem }()

				var latency time.Duration
				var probeErr error
				if task.probe == nil {
					probeErr = errors.New("probe not available for this node")
				} else {
					latency, probeErr = runProbeWithTimeout(roundCtx, timeout, task.probe)
				}
				if roundCtx.Err() != nil {
					return
				}
				if !m.applyProbeResult(
					generation,
					task.tag,
					task.entry,
					round.id,
					round.probeEpoch,
					round.probeSeq,
					task.probeRevision,
					latency,
					probeErr,
				) {
					return
				}
				completed.Add(1)
				if probeErr != nil {
					failed.Add(1)
					if m.logger != nil {
						m.logger.Warn("probe failed for ", task.tag, ": ", probeErr)
					}
					return
				}
				if int(available.Add(1)) >= minAvailable {
					signalQuorum()
				}
			}(task)
		}

		wg.Wait()
		if roundErr := roundCtx.Err(); roundErr != nil && scheduleErr == nil {
			scheduleErr = roundErr
		}
		m.completeInitialProbeGateForRound(
			generation,
			round.id,
			round.probeEpoch,
			round.gateRev,
			scheduleErr,
		)
	}()

	abortRound := func() {
		m.probeRoundMu.Lock()
		m.invalidateActiveRoundLocked(round)
		roundCancel()
		<-round.done
		if m.probeRound == round {
			m.probeRound = nil
		}
		m.probeRoundMu.Unlock()
	}

	snapshotSummary := func() ProbeSummary {
		return ProbeSummary{
			Generation: generation,
			Total:      len(tasks),
			Completed:  int(completed.Load()),
			Available:  int(available.Load()),
			Failed:     int(failed.Load()),
		}
	}

	checkStale := func() error {
		m.mu.RLock()
		stale := generation != m.reloadGen
		m.mu.RUnlock()
		if stale {
			return ErrStaleGeneration
		}
		return nil
	}

	select {
	case <-quorumCh:
		if err := checkStale(); err != nil {
			abortRound()
			return snapshotSummary(), err
		}
		return snapshotSummary(), nil
	case <-round.done:
		if err := checkStale(); err != nil {
			return snapshotSummary(), err
		}
		if err := ctx.Err(); err != nil && int(available.Load()) < minAvailable {
			return snapshotSummary(), err
		}
		if err := parent.Err(); err != nil && int(available.Load()) < minAvailable {
			return snapshotSummary(), err
		}
		return snapshotSummary(), nil
	case <-ctx.Done():
		abortRound()
		if err := checkStale(); err != nil {
			return snapshotSummary(), err
		}
		if int(available.Load()) >= minAvailable {
			return snapshotSummary(), nil
		}
		return snapshotSummary(), ctx.Err()
	}
}
