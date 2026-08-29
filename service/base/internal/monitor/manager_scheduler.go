package monitor

import (
	"context"
	"errors"
	"time"
)

func (m *Manager) StartPeriodicHealthCheck(interval, timeout time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Hour
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	m.healthMu.Lock()
	if m.healthIntervalC == nil {
		m.healthIntervalC = make(chan time.Duration, 1)
	}
	alreadyStarted := m.healthStarted
	m.healthInterval = interval
	m.healthTimeout = timeout
	intervalC := m.healthIntervalC
	if alreadyStarted {
		m.healthMu.Unlock()
		select {
		case intervalC <- interval:
		default:
		}
		m.mu.RLock()
		probeReady := m.probeReady
		m.mu.RUnlock()
		if probeReady {
			m.RequestProbeAllOnce(timeout)
		}
		return
	}
	if m.healthTicker != nil {
		m.healthTicker.Stop()
	}
	m.healthTicker = time.NewTicker(interval)
	ticker := m.healthTicker
	m.healthStarted = true
	m.healthMu.Unlock()

	m.mu.RLock()
	probeReady := m.probeReady
	m.mu.RUnlock()

	// 启动阶段先同步完成首轮 probe，避免 compat checkout 或 available-only
	// 视图在 effective 节点尚未建立时抢先拿到空结果。
	if probeReady {
		m.probeAllNodes(timeout)
	} else if m.logger != nil {
		m.logger.Warn("probe target not configured, periodic health check is waiting")
	}

	go func() {
		for {
			select {
			case <-m.ctx.Done():
				return
			case newInterval := <-intervalC:
				if newInterval <= 0 {
					newInterval = 2 * time.Hour
				}
				// 重置 ticker
				m.healthMu.Lock()
				m.healthInterval = newInterval
				if m.healthTicker != nil {
					m.healthTicker.Stop()
				}
				m.healthTicker = time.NewTicker(newInterval)
				ticker = m.healthTicker
				m.healthMu.Unlock()
				if m.logger != nil {
					m.logger.Info("periodic health check interval updated: ", newInterval)
				}
			case <-ticker.C:
				m.RequestProbeAllOnce(timeout)
			}
		}
	}()

	if m.logger != nil {
		m.logger.Info("periodic health check started, interval: ", interval)
	}
}

// SetHealthCheckInterval updates the periodic health check interval at runtime.
// It is safe to call before StartPeriodicHealthCheck; it will be applied on start.
func (m *Manager) SetHealthCheckInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	m.healthMu.Lock()
	m.healthInterval = d
	intervalC := m.healthIntervalC
	m.healthMu.Unlock()

	if intervalC != nil {
		select {
		case intervalC <- d:
		default:
			// drop if a newer update is already queued
		}
	}
}

// RequestProbeAllOnce triggers a full periodic probe round. Requests for the
// same generation and probe topology are de-duplicated; a newer generation or
// probe replacement supersedes and drains the older coordinator first.
func (m *Manager) RequestProbeAllOnce(timeout time.Duration) {
	m.probeRoundMu.Lock()
	defer m.probeRoundMu.Unlock()
	if exclusive := m.exclusive; exclusive != nil {
		select {
		case <-exclusive.done:
			m.exclusive = nil
			m.clearActiveRoundLocked(exclusive)
		default:
			// A synchronous generation probe owns the scheduler. The caller will
			// run its own fresh round, so a periodic tick must not compete with it.
			return
		}
	}

	m.mu.RLock()
	ready := m.probeReady
	generation := m.reloadGen
	m.mu.RUnlock()
	if !ready {
		return
	}
	probeEpoch := m.probeEpoch.Load()

	if active := m.probeRound; active != nil {
		select {
		case <-active.done:
			m.probeRound = nil
			m.clearActiveRoundLocked(active)
		default:
			if active.generation == generation && active.probeEpoch == probeEpoch {
				return
			}
			m.invalidateActiveRoundLocked(active)
			active.cancel()
			<-active.done
			if m.probeRound == active {
				m.probeRound = nil
			}
		}
	}

	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	roundCtx, cancel := context.WithCancel(parent)
	round := &periodicProbeRound{
		id:         m.nextRoundID.Add(1),
		generation: generation,
		probeEpoch: probeEpoch,
		probeSeq:   m.nextProbeSeq.Add(1),
		gateRev:    m.currentInitialProbeRev(),
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	m.setActiveRoundLocked(round)
	m.probeRound = round
	go m.runPeriodicProbeRound(roundCtx, round, timeout)
}

// probeAllNodes checks all registered nodes concurrently.
func (m *Manager) probeAllNodes(timeout time.Duration) {
	m.mu.RLock()
	generation := m.reloadGen
	m.mu.RUnlock()
	summary, err := m.ProbeGeneration(m.ctx, generation, timeout)
	if err != nil && !errors.Is(err, ErrStaleGeneration) && m.logger != nil {
		m.logger.Warn("health check failed: ", err)
	}
	if m.logger != nil && !errors.Is(err, ErrStaleGeneration) {
		m.logger.Info("health check completed: ", summary.Available, " available, ", summary.Failed, " failed")
	}
}

func (m *Manager) runPeriodicProbeRound(
	ctx context.Context,
	round *periodicProbeRound,
	timeout time.Duration,
) {
	defer func() {
		round.cancel()
		close(round.done)
		m.probeRoundMu.Lock()
		if m.probeRound == round {
			m.probeRound = nil
		}
		m.clearActiveRoundLocked(round)
		m.probeRoundMu.Unlock()
	}()
	m.probeAllNodesForGeneration(
		ctx,
		round.generation,
		round.id,
		round.probeEpoch,
		round.probeSeq,
		round.gateRev,
		timeout,
	)
}

func (m *Manager) setActiveRoundLocked(round *periodicProbeRound) {
	if round == nil {
		return
	}
	m.mu.Lock()
	m.activeRound = round.id
	m.mu.Unlock()
}

func (m *Manager) invalidateActiveRoundLocked(round *periodicProbeRound) {
	if round == nil {
		return
	}
	m.mu.Lock()
	if m.activeRound == round.id {
		m.activeRound = 0
	}
	m.mu.Unlock()
}

func (m *Manager) clearActiveRoundLocked(round *periodicProbeRound) {
	m.invalidateActiveRoundLocked(round)
}

func (m *Manager) probeAllNodesForGeneration(
	ctx context.Context,
	generation Generation,
	roundID uint64,
	probeEpoch uint64,
	probeSeq uint64,
	gateRev uint64,
	timeout time.Duration,
) {
	summary, err := m.probeGeneration(ctx, generation, roundID, probeEpoch, probeSeq, timeout)
	m.completeInitialProbeGateForRound(generation, roundID, probeEpoch, gateRev, err)
	if err != nil && !errors.Is(err, ErrStaleGeneration) && m.logger != nil {
		m.logger.Warn("health check failed: ", err)
	}
	if m.logger != nil && !errors.Is(err, ErrStaleGeneration) {
		m.logger.Info("health check completed: ", summary.Available, " available, ", summary.Failed, " failed")
	}
}
