package monitor

import (
	"fmt"
	"strconv"
	"time"
)

func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.healthMu.Lock()
	if m.healthTicker != nil {
		m.healthTicker.Stop()
		m.healthTicker = nil
	}
	m.healthStarted = false
	m.healthMu.Unlock()
	m.probeRoundMu.Lock()
	m.mu.Lock()
	m.activeRound = 0
	m.probeEpoch.Add(1)
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	m.probeRoundMu.Unlock()
}

// WaitForInitialProbe waits until the first full probe round completes.
// A zero or negative timeout falls back to the active health timeout or a
// conservative default.
func (m *Manager) WaitForInitialProbe(timeout time.Duration) error {
	if timeout <= 0 {
		m.healthMu.Lock()
		timeout = m.healthTimeout
		m.healthMu.Unlock()
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		m.mu.RLock()
		probeReady := m.probeReady
		m.mu.RUnlock()
		if !probeReady {
			return nil
		}

		m.initialProbeMu.Lock()
		if m.initialProbeDone {
			m.initialProbeMu.Unlock()
			return nil
		}
		ch := m.initialProbeCh
		m.initialProbeMu.Unlock()

		// A target can be cleared between the first readiness check and the
		// gate snapshot. Re-check so a disabled monitor does not wait on a
		// newly-created gate that can never be completed.
		m.mu.RLock()
		probeReady = m.probeReady
		m.mu.RUnlock()
		if !probeReady {
			return nil
		}

		select {
		case <-ch:
			// The channel may have been closed by a gate reset. Loop back to
			// observe the current revision/done state instead of treating every
			// close as successful completion.
			continue
		case <-timer.C:
			return fmt.Errorf("timeout waiting for initial probe completion after %s", timeout)
		case <-m.ctx.Done():
			return m.ctx.Err()
		}
	}
}

func (m *Manager) startTrafficSpeedSampler() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			m.sampleTrafficSpeeds(now)
		}
	}
}

func (m *Manager) sampleTrafficSpeeds(now time.Time) {
	m.mu.RLock()
	entries := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		entries = append(entries, e)
	}
	m.mu.RUnlock()

	for _, e := range entries {
		e.updateTrafficSpeed(now)
	}
}

func parsePort(value string) uint16 {
	p, err := strconv.Atoi(value)
	if err != nil || p <= 0 || p > 65535 {
		return 80
	}
	return uint16(p)
}

// BeginReload bumps the generation counter. Nodes registered after this call
// will be marked with the new generation. Call SweepStaleNodes after reload
// to remove nodes that were not re-registered (disabled/deleted nodes).
func (m *Manager) BeginReload() Generation {
	m.probeRoundMu.Lock()
	defer m.probeRoundMu.Unlock()

	m.mu.Lock()
	m.reloadGen++
	generation := m.reloadGen
	m.activeRound = 0
	m.resetInitialProbeGate()
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	return generation
}

// SweepStaleNodes removes nodes that were not re-registered during the current
// reload cycle. This preserves monitoring data (latency, success/failure counts)
// for nodes that are still active, while cleaning up disabled/removed nodes.
func (m *Manager) SweepStaleNodes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tag, e := range m.nodes {
		if e.reloadGen != m.reloadGen {
			delete(m.nodes, tag)
		}
	}
}

// ClearNodes removes all registered nodes. Use BeginReload + SweepStaleNodes
// for reload scenarios to preserve data for active nodes.
func (m *Manager) ClearNodes() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = make(map[string]*entry)
}

// Register ensures a node is tracked and returns its entry. Monitoring state is
// preserved across reloads only when both the tag and non-empty URI are stable.
