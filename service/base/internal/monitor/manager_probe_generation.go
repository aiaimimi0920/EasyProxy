package monitor

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type generationProbeTask struct {
	tag           string
	entry         *entry
	probe         probeFunc
	probeRevision uint64
}

// ProbeGeneration synchronously probes only entries registered in generation.
// It owns the full-probe scheduler while running: any periodic round is first
// invalidated, cancelled, and drained, and new periodic ticks are ignored until
// this authoritative generation probe completes.
func (m *Manager) ProbeGeneration(
	ctx context.Context,
	generation Generation,
	timeout time.Duration,
) (ProbeSummary, error) {
	m.exclusiveMu.Lock()
	defer m.exclusiveMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

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
	roundCtx, cancel := context.WithCancel(ctx)
	round := &periodicProbeRound{
		id:         m.nextRoundID.Add(1),
		generation: generation,
		probeEpoch: m.probeEpoch.Load(),
		probeSeq:   m.nextProbeSeq.Add(1),
		gateRev:    m.currentInitialProbeRev(),
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	m.exclusive = round
	m.setActiveRoundLocked(round)
	m.probeRoundMu.Unlock()

	defer func() {
		cancel()
		close(round.done)
		m.probeRoundMu.Lock()
		if m.exclusive == round {
			m.exclusive = nil
		}
		m.clearActiveRoundLocked(round)
		m.probeRoundMu.Unlock()
	}()
	summary, err := m.probeGeneration(
		roundCtx,
		generation,
		round.id,
		round.probeEpoch,
		round.probeSeq,
		timeout,
	)
	m.completeInitialProbeGateForRound(
		generation,
		round.id,
		round.probeEpoch,
		round.gateRev,
		err,
	)
	return summary, err
}

// cancelProbeRoundsLocked invalidates and drains any automatic probe
// coordinators. The caller must hold probeRoundMu. Results that were already
// in flight are rejected by activeRound before their network goroutines can
// publish health state.
func (m *Manager) cancelProbeRoundsLocked() {
	if active := m.probeRound; active != nil {
		active.cancel()
		<-active.done
		if m.probeRound == active {
			m.probeRound = nil
		}
	}
	if exclusive := m.exclusive; exclusive != nil {
		exclusive.cancel()
		<-exclusive.done
		if m.exclusive == exclusive {
			m.exclusive = nil
		}
	}
}

func (m *Manager) probeGeneration(
	ctx context.Context,
	generation Generation,
	roundID uint64,
	probeEpoch uint64,
	probeSeq uint64,
	timeout time.Duration,
) (ProbeSummary, error) {
	summary := ProbeSummary{Generation: generation}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	m.mu.RLock()
	if generation != m.reloadGen {
		m.mu.RUnlock()
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
	summary.Total = len(tasks)
	if len(tasks) == 0 {
		return summary, nil
	}

	workerLimit := probeAllWorkerLimit(len(tasks))
	sem := make(chan struct{}, workerLimit)
	var wg sync.WaitGroup
	var completed atomic.Int32
	var available atomic.Int32
	var failed atomic.Int32
	var scheduleErr error
scheduling:
	for _, task := range tasks {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			scheduleErr = ctx.Err()
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
				latency, probeErr = runProbeWithTimeout(ctx, timeout, task.probe)
			}
			// A round-level cancellation is not evidence that this node is
			// unavailable. Discard it instead of publishing a false negative;
			// per-node timeouts still have an active parent and are committed.
			if ctx.Err() != nil {
				return
			}
			if !m.applyProbeResult(
				generation,
				task.tag,
				task.entry,
				roundID,
				probeEpoch,
				probeSeq,
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
			} else {
				available.Add(1)
			}
		}(task)
	}
	wg.Wait()
	summary.Completed = int(completed.Load())
	summary.Available = int(available.Load())
	summary.Failed = int(failed.Load())

	m.mu.RLock()
	stale := generation != m.reloadGen
	m.mu.RUnlock()
	if stale {
		return summary, ErrStaleGeneration
	}
	if scheduleErr != nil {
		return summary, scheduleErr
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	return summary, nil
}

func (m *Manager) applyProbeResult(
	generation Generation,
	tag string,
	entry *entry,
	roundID uint64,
	probeEpoch uint64,
	probeSeq uint64,
	probeRevision uint64,
	latency time.Duration,
	probeErr error,
) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if generation != m.reloadGen || probeEpoch != m.probeEpoch.Load() || m.nodes[tag] != entry {
		return false
	}
	if roundID != 0 && m.activeRound != roundID {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	// Invocation order, not network completion order, decides which probe is
	// authoritative across manual, periodic, and exclusive generation probes.
	if entry.reloadGen != generation || entry.probeRevision != probeRevision || probeSeq <= entry.lastProbeSeq {
		return false
	}

	now := time.Now()
	entry.lastProbeSeq = probeSeq
	entry.healthGen = generation
	entry.lastProbeAt = now
	entry.initialCheckDone = true
	entry.eventSeq++
	if probeErr != nil {
		errText := probeErr.Error()
		entry.failure++
		entry.lastFailureSeq = entry.eventSeq
		entry.lastError = errText
		entry.lastFail = now
		entry.lastProbe = 0
		entry.available = false
		entry.appendTimelineLocked(false, 0, errText, "health-probe")
		return true
	}

	entry.success++
	entry.lastError = ""
	entry.lastOK = now
	entry.lastProbeOK = now
	entry.lastProbe = latency
	entry.available = true
	latencyMs := latency.Milliseconds()
	if latencyMs == 0 && latency > 0 {
		latencyMs = 1
	}
	entry.appendTimelineLocked(true, latencyMs, "", "health-probe")
	return true
}

func probeAllWorkerLimit(totalEntries int) int {
	if totalEntries <= 0 {
		return 0
	}

	workerLimit := runtime.NumCPU() * 4
	if workerLimit < 8 {
		workerLimit = 8
	}
	// Keep the full automatic probe round below the DNS / connector burst that
	// has repeatedly produced false zero-available snapshots on production-sized
	// node sets. The manual probe-all API already succeeds with this lower
	// ceiling, so startup and periodic probes should use the same safer range.
	if workerLimit > 16 {
		workerLimit = 16
	}
	if totalEntries < workerLimit {
		workerLimit = totalEntries
	}
	return workerLimit
}

const maxNodeProbeTimeout = 10 * time.Second

func normalizeNodeProbeTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 || timeout > maxNodeProbeTimeout {
		return maxNodeProbeTimeout
	}
	return timeout
}

func runProbeWithTimeout(parent context.Context, timeout time.Duration, probe probeFunc) (time.Duration, error) {
	timeout = normalizeNodeProbeTimeout(timeout)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	type probeResult struct {
		latency time.Duration
		err     error
	}
	resultCh := make(chan probeResult, 1)
	go func() {
		latency, err := probe(ctx)
		select {
		case resultCh <- probeResult{latency: latency, err: err}:
		case <-ctx.Done():
		}
	}()

	select {
	case result := <-resultCh:
		return result.latency, result.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Stop stops the periodic health check.
