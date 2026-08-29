package monitor

import (
	"strings"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func (m *Manager) Register(info NodeInfo) *EntryHandle {
	m.mu.Lock()
	defer m.mu.Unlock()
	minUptime, minRate := m.longLivedThresholds()
	e, ok := m.nodes[info.Tag]
	if ok {
		e.mu.RLock()
		currentURI := strings.TrimSpace(e.info.URI)
		candidateURI := strings.TrimSpace(info.URI)
		sameIdentity := (candidateURI != "" && currentURI == candidateURI) ||
			(candidateURI == "" && currentURI == "" && e.reloadGen == m.reloadGen)
		e.mu.RUnlock()
		if !sameIdentity {
			ok = false
		}
	}
	if !ok {
		e = &entry{
			owner:              m,
			info:               info,
			timeline:           make([]TimelineEvent, 0, maxTimelineSize),
			reloadGen:          m.reloadGen,
			firstSeenAt:        time.Now(),
			longLivedMinUptime: minUptime,
			longLivedMinRate:   minRate,
		}
		m.nodes[info.Tag] = e
	} else {
		e.mu.Lock()
		e.owner = m
		e.info = info
		if e.reloadGen != m.reloadGen {
			// A probe closure belongs to the box generation that installed it.
			// Clear it before exposing the reused entry as part of the candidate
			// generation; SetProbe will publish the candidate closure separately.
			e.probe = nil
			e.probeRevision++
			// The node identity is unchanged, so its last known health remains
			// valid while the replacement probe closure is installed.
			e.healthGen = m.reloadGen
		}
		e.reloadGen = m.reloadGen
		if e.firstSeenAt.IsZero() {
			e.firstSeenAt = time.Now()
		}
		e.longLivedMinUptime = minUptime
		e.longLivedMinRate = minRate
		e.mu.Unlock()
	}
	m.probeEpoch.Add(1)
	return &EntryHandle{ref: e}
}

// longLivedThresholds resolves configured long-lived thresholds, falling back
// to defaults when unset.
func (m *Manager) longLivedThresholds() (time.Duration, float64) {
	return normalizeLongLivedThresholds(m.cfg.LongLivedMinUptime, m.cfg.LongLivedMinSuccessRate)
}

func normalizeLongLivedThresholds(minUptime time.Duration, minRate float64) (time.Duration, float64) {
	if minUptime <= 0 {
		minUptime = defaultLongLivedMinUptime
	}
	if minRate <= 0 || minRate > 1 {
		minRate = defaultLongLivedMinSuccessRate
	}
	return minUptime, minRate
}

// MeetsLongLivedPolicy reports whether a raw snapshot satisfies the supplied
// uptime / success-rate thresholds. Callers should pass the directive-specific
// thresholds they want to enforce; zero or invalid values fall back to the
// manager defaults.
func MeetsLongLivedPolicy(snapshot Snapshot, minUptime time.Duration, minRate float64) bool {
	if !snapshot.EffectiveAvailable {
		return false
	}
	minUptime, minRate = normalizeLongLivedThresholds(minUptime, minRate)
	rate := reportedSuccessRate(snapshot.ReportedSuccessCount, snapshot.ReportedFailureCount)
	uptime := snapshot.Uptime
	if uptime <= 0 {
		uptime = time.Duration(snapshot.UptimeSeconds) * time.Second
	}
	return uptime >= minUptime && rate >= minRate
}

// SetLongLivedThresholds updates both future registrations and all currently
// registered entries without rebuilding sing-box.
func (m *Manager) SetLongLivedThresholds(minUptime time.Duration, minRate float64) {
	minUptime, minRate = normalizeLongLivedThresholds(minUptime, minRate)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.LongLivedMinUptime = minUptime
	m.cfg.LongLivedMinSuccessRate = minRate
	for _, e := range m.nodes {
		e.mu.Lock()
		e.longLivedMinUptime = minUptime
		e.longLivedMinRate = minRate
		e.mu.Unlock()
	}
}

func (m *Manager) resetInitialProbeGate() {
	m.initialProbeMu.Lock()
	defer m.initialProbeMu.Unlock()
	m.initialProbeRev++
	if !m.initialProbeDone && m.initialProbeCh != nil {
		close(m.initialProbeCh)
	}
	m.initialProbeDone = false
	m.initialProbeCh = make(chan struct{})
}

func (m *Manager) currentInitialProbeRev() uint64 {
	m.initialProbeMu.Lock()
	defer m.initialProbeMu.Unlock()
	return m.initialProbeRev
}

func (m *Manager) completeInitialProbeGate(revision uint64) {
	m.initialProbeMu.Lock()
	defer m.initialProbeMu.Unlock()
	if m.initialProbeDone || revision != m.initialProbeRev {
		return
	}
	m.initialProbeDone = true
	if m.initialProbeCh != nil {
		close(m.initialProbeCh)
	}
}

func (m *Manager) completeInitialProbeGateForRound(
	generation Generation,
	roundID uint64,
	probeEpoch uint64,
	gateRev uint64,
	err error,
) {
	if err != nil {
		return
	}
	// A reset publishes a new gate revision before cancelling the old round.
	// Re-check round ownership so a late completion cannot close the new gate.
	m.mu.RLock()
	authoritative := generation == m.reloadGen &&
		probeEpoch == m.probeEpoch.Load() &&
		m.activeRound == roundID
	m.mu.RUnlock()
	if !authoritative {
		return
	}
	m.completeInitialProbeGate(gateRev)
}

// RestorePersistedState hydrates a registered node with runtime stats loaded
// from durable storage. Matching prefers URI and falls back to name.
func (m *Manager) RestorePersistedState(uri, name string, state PersistedState) bool {
	normalizedURI := strings.TrimSpace(uri)
	normalizedName := strings.TrimSpace(name)
	if normalizedURI == "" && normalizedName == "" {
		return false
	}

	m.mu.RLock()
	var target *entry
	if normalizedURI != "" {
		for _, candidate := range m.nodes {
			if strings.TrimSpace(candidate.info.URI) == normalizedURI {
				target = candidate
				break
			}
		}
	}
	if target == nil && normalizedName != "" {
		for _, candidate := range m.nodes {
			if strings.TrimSpace(candidate.info.Name) == normalizedName {
				target = candidate
				break
			}
		}
	}
	m.mu.RUnlock()

	if target == nil {
		return false
	}
	target.restorePersistedState(state)
	return true
}

// DestinationForProbe exposes the configured destination for health checks.
func (m *Manager) DestinationForProbe() (M.Socksaddr, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.probeReady {
		return M.Socksaddr{}, false
	}
	return m.probeDst, true
}

func (m *Manager) ProbeTargets() ([]ProbeTargetSpec, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.probeReady || len(m.probeSpecs) == 0 {
		return nil, false
	}
	specs := make([]ProbeTargetSpec, len(m.probeSpecs))
	copy(specs, m.probeSpecs)
	return specs, true
}

// SkipCertVerify reports whether HTTPS probe TLS verification is disabled.
func (m *Manager) SkipCertVerify() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.SkipCertVerify
}

// SetSkipCertVerify updates the HTTPS probe TLS verification policy at runtime.
func (m *Manager) SetSkipCertVerify(skip bool) {
	m.probeRoundMu.Lock()
	m.mu.Lock()
	if m.cfg.SkipCertVerify == skip {
		m.mu.Unlock()
		m.probeRoundMu.Unlock()
		return
	}
	m.cfg.SkipCertVerify = skip
	m.activeRound = 0
	m.probeEpoch.Add(1)
	m.resetInitialProbeGate()
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	m.probeRoundMu.Unlock()
}

// UpdateProbeTarget dynamically updates the probe destination at runtime.
func (m *Manager) UpdateProbeTarget(target string) error {
	return m.UpdateProbeTargets(nil, target)
}

func (m *Manager) UpdateProbeTargets(targets []string, single string) error {
	specs, err := parseProbeTargets(targets, single)
	if err != nil {
		return err
	}
	m.probeRoundMu.Lock()
	m.mu.Lock()
	if stringSlicesEqual(m.cfg.ProbeTargets, targets) && strings.TrimSpace(m.cfg.ProbeTarget) == strings.TrimSpace(single) {
		m.mu.Unlock()
		m.probeRoundMu.Unlock()
		return nil
	}
	probeReady := len(specs) > 0
	if !probeReady {
		m.probeSpecs = nil
		m.probeDst = M.Socksaddr{}
		m.probeReady = false
	} else {
		m.probeSpecs = specs
		m.probeDst = specs[0].Dst
		m.probeReady = true
	}
	m.cfg.ProbeTargets = append([]string(nil), targets...)
	m.cfg.ProbeTarget = strings.TrimSpace(single)
	m.activeRound = 0
	m.probeEpoch.Add(1)
	m.resetInitialProbeGate()
	m.mu.Unlock()
	m.cancelProbeRoundsLocked()
	m.probeRoundMu.Unlock()
	if probeReady {
		m.healthMu.Lock()
		healthStarted := m.healthStarted
		healthTimeout := m.healthTimeout
		m.healthMu.Unlock()
		if healthStarted {
			m.RequestProbeAllOnce(healthTimeout)
		}
	}
	return nil
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Snapshot returns a sorted copy of current node states.
