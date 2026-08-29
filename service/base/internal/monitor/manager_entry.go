package monitor

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (m *Manager) Probe(ctx context.Context, tag string) (time.Duration, error) {
	m.mu.RLock()
	e, ok := m.nodes[tag]
	generation := m.reloadGen
	probeEpoch := m.probeEpoch.Load()
	if !ok {
		m.mu.RUnlock()
		return 0, fmt.Errorf("node not found: %s", tag)
	}
	e.mu.RLock()
	probe := e.probe
	probeRevision := e.probeRevision
	entryGeneration := e.reloadGen
	e.mu.RUnlock()
	m.mu.RUnlock()
	if probe == nil {
		return 0, errors.New("probe not available for this node")
	}
	if entryGeneration != generation {
		return 0, ErrStaleGeneration
	}
	probeSeq := m.nextProbeSeq.Add(1)
	latency, probeErr := runProbeWithTimeout(ctx, 0, probe)
	if !m.applyProbeResult(generation, tag, e, 0, probeEpoch, probeSeq, probeRevision, latency, probeErr) {
		return 0, ErrStaleGeneration
	}
	return latency, probeErr
}

// Release clears blacklist state for the given node.
func (m *Manager) Release(tag string) error {
	e, err := m.entry(tag)
	if err != nil {
		return err
	}
	if e.release == nil {
		return errors.New("release not available for this node")
	}
	e.release()
	return nil
}

func (m *Manager) entry(tag string) (*entry, error) {
	m.mu.RLock()
	e, ok := m.nodes[tag]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("node %s not found", tag)
	}
	return e, nil
}

func (e *entry) snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.clearExpiredBlacklistLocked(now)

	latencyMs := int64(-1)
	if e.lastProbe > 0 {
		latencyMs = e.lastProbe.Milliseconds()
		if latencyMs == 0 {
			latencyMs = 1
		}
	}

	var timelineCopy []TimelineEvent
	if len(e.timeline) > 0 {
		timelineCopy = make([]TimelineEvent, len(e.timeline))
		copy(timelineCopy, e.timeline)
	}

	snap := Snapshot{
		NodeInfo:                  e.info,
		FailureCount:              e.failure,
		SuccessCount:              e.success,
		TrafficSuccessCount:       e.trafficSuccess,
		Blacklisted:               e.blacklist,
		BlacklistedUntil:          e.until,
		AvailabilityScore:         e.availabilityScoreLocked(),
		ReportedSuccessCount:      e.reportSuccess,
		ReportedFailureCount:      e.reportFailure,
		ConsecutiveReportFailures: e.reportFailures,
		ActiveConnections:         e.active.Load(),
		LastError:                 e.lastError,
		LastFailure:               e.lastFail,
		LastSuccess:               e.lastOK,
		LastTrafficSuccessAt:      e.lastTrafficOK,
		LastReportedAt:            e.lastReportedAt,
		LastReportedSuccess:       e.lastReportOK,
		LastProbeAt:               e.lastProbeAt,
		LastProbeSuccessAt:        e.lastProbeOK,
		LastProbeLatency:          e.lastProbe,
		LastLatencyMs:             latencyMs,
		Available:                 e.healthGen == e.reloadGen && e.available,
		InitialCheckDone:          e.healthGen == e.reloadGen && e.initialCheckDone,
		TotalUpload:               e.totalUpload.Load(),
		TotalDownload:             e.totalDownload.Load(),
		UploadSpeed:               e.uploadSpeed,
		DownloadSpeed:             e.downloadSpeed,
		TrafficSuccessSeq:         e.lastTrafficSeq,
		FailureSeq:                e.lastFailureSeq,
		Timeline:                  timelineCopy,
	}

	effectiveAvailable, trafficProvenUsable, availabilitySource := effectiveAvailabilityDetailsAt(snap, now)
	snap.EffectiveAvailable = effectiveAvailable
	snap.TrafficProvenUsable = trafficProvenUsable
	snap.AvailabilitySource = availabilitySource

	if !e.firstSeenAt.IsZero() {
		uptime := now.Sub(e.firstSeenAt)
		if uptime > 0 {
			snap.Uptime = uptime
			snap.UptimeSeconds = int64(uptime / time.Second)
		}
		minUptime := e.longLivedMinUptime
		if minUptime <= 0 {
			minUptime = defaultLongLivedMinUptime
		}
		minRate := e.longLivedMinRate
		if minRate <= 0 {
			minRate = defaultLongLivedMinSuccessRate
		}
		snap.LongLived = MeetsLongLivedPolicy(snap, minUptime, minRate)
	}
	return snap
}

// reportedSuccessRate returns the success ratio over reported probe/feedback
// outcomes. With no samples it returns 1.0 so a freshly-seen node is not
// penalized before any probe has run (it still fails the uptime gate anyway).
func reportedSuccessRate(success, failure int64) float64 {
	total := success + failure
	if total <= 0 {
		return 1.0
	}
	return float64(success) / float64(total)
}

func (e *entry) trafficProvenUsableLocked(now time.Time) bool {
	if e.blacklist || e.lastTrafficOK.IsZero() {
		return false
	}
	if e.lastFailureSeq > 0 && e.lastTrafficSeq > 0 {
		if e.lastFailureSeq > e.lastTrafficSeq {
			return false
		}
	} else if !e.lastFail.IsZero() && !e.lastFail.Before(e.lastTrafficOK) {
		return false
	}
	if now.Before(e.lastTrafficOK) {
		return true
	}
	return now.Sub(e.lastTrafficOK) <= trafficProvenSuccessWindow
}

func (e *entry) availabilityScoreLocked() int {
	const (
		baseScore            = 100
		maxPenalty           = 80
		unhealthyScoreCap    = 20
		blacklistedScoreCap  = 5
		minAvailabilityScore = 1
	)

	penalty := e.feedbackPenalty
	if penalty < 0 {
		penalty = 0
	} else if penalty > maxPenalty {
		penalty = maxPenalty
	}

	score := baseScore - penalty
	if e.initialCheckDone && !e.available && !e.trafficProvenUsableLocked(time.Now()) && score > unhealthyScoreCap {
		score = unhealthyScoreCap
	}
	if e.blacklist && score > blacklistedScoreCap {
		score = blacklistedScoreCap
	}
	if score < minAvailabilityScore {
		score = minAvailabilityScore
	}
	return score
}

func (e *entry) recordFailure(err error, destination string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	errStr := err.Error()
	e.eventSeq++
	e.lastFailureSeq = e.eventSeq
	e.failure++
	e.lastError = errStr
	e.lastFail = time.Now()
	// 注意：不修改 available/initialCheckDone
	// 流量失败不代表节点不可用（可能是目标网站的问题）
	// available 只由探测操作控制
	e.appendTimelineLocked(false, 0, errStr, destination)
}

func (e *entry) recordObservationFailure(err error, destination string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	errStr := err.Error()
	e.eventSeq++
	e.lastFailureSeq = e.eventSeq
	e.lastFail = time.Now()
	e.lastError = errStr
	e.appendTimelineLocked(false, 0, errStr, destination)
}

func (e *entry) recordSuccess(destination string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.eventSeq++
	e.lastTrafficSeq = e.eventSeq
	e.success++
	e.trafficSuccess++
	e.lastOK = now
	e.lastTrafficOK = now
	// 注意：不修改 available/initialCheckDone
	// 流量成功不代表需要更新探测状态
	// available 只由探测操作控制
	e.appendTimelineLocked(true, 0, "", destination)
}

func (e *entry) recordSuccessWithLatency(latency time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.success++
	e.lastError = ""
	e.lastOK = now
	e.lastProbeAt = now
	e.lastProbeOK = now
	e.lastProbe = latency
	e.available = true
	e.initialCheckDone = true
	e.healthGen = e.reloadGen
	latencyMs := latency.Milliseconds()
	if latencyMs == 0 && latency > 0 {
		latencyMs = 1
	}
	e.appendTimelineLocked(true, latencyMs, "", "")
}

func (e *entry) appendTimelineLocked(success bool, latencyMs int64, errStr string, destination string) {
	evt := TimelineEvent{
		Time:        time.Now(),
		Success:     success,
		LatencyMs:   latencyMs,
		Error:       errStr,
		Destination: destination,
	}
	if len(e.timeline) >= maxTimelineSize {
		copy(e.timeline, e.timeline[1:])
		e.timeline[len(e.timeline)-1] = evt
	} else {
		e.timeline = append(e.timeline, evt)
	}
}

func (e *entry) blacklistUntil(until time.Time) {
	e.mu.Lock()
	e.blacklist = true
	e.until = until
	e.mu.Unlock()
}

func (e *entry) clearExpiredBlacklistLocked(now time.Time) bool {
	if e.blacklist && !e.until.IsZero() && now.After(e.until) {
		e.blacklist = false
		e.until = time.Time{}
		return true
	}
	return false
}

func (e *entry) clearBlacklist() {
	e.mu.Lock()
	e.blacklist = false
	e.until = time.Time{}
	e.mu.Unlock()
}

func (e *entry) incActive() {
	e.active.Add(1)
}

func (e *entry) decActive() {
	e.active.Add(-1)
}

func (e *entry) setActiveConnections(active int32) {
	e.active.Store(active)
}

func (e *entry) setProbe(fn probeFunc) {
	e.mu.Lock()
	e.probe = fn
	e.probeRevision++
	owner := e.owner
	e.mu.Unlock()
	if owner != nil {
		owner.probeEpoch.Add(1)
	}
}

func (e *entry) setRelease(fn releaseFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.release = fn
}

func (e *entry) recordProbeLatency(d time.Duration) {
	e.mu.Lock()
	now := time.Now()
	e.lastProbeAt = now
	e.lastProbeOK = now
	e.lastProbe = d
	e.mu.Unlock()
}

func (e *entry) applyUsageReportSuccess() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.reportSuccess++
	e.reportFailures = 0
	e.lastReportedAt = time.Now()
	e.lastReportOK = true
	if e.feedbackPenalty >= 5 {
		e.feedbackPenalty -= 5
	} else {
		e.feedbackPenalty = 0
	}
}

func (e *entry) applyUsageReportFailure(penalty int, countAsRouteFailure bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if countAsRouteFailure {
		e.reportFailure++
		e.reportFailures++
	}
	e.lastReportedAt = time.Now()
	e.lastReportOK = false
	e.feedbackPenalty += penalty
	if e.feedbackPenalty > 80 {
		e.feedbackPenalty = 80
	}
}

func (e *entry) updateTrafficSpeed(now time.Time) {
	curUp := e.totalUpload.Load()
	curDown := e.totalDownload.Load()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.lastSpeedAt.IsZero() {
		e.lastSpeedAt = now
		e.lastSpeedUpload = curUp
		e.lastSpeedDown = curDown
		e.uploadSpeed = 0
		e.downloadSpeed = 0
		return
	}

	elapsed := now.Sub(e.lastSpeedAt).Seconds()
	if elapsed <= 0 {
		return
	}

	deltaUp := curUp - e.lastSpeedUpload
	deltaDown := curDown - e.lastSpeedDown
	if deltaUp < 0 {
		deltaUp = 0
	}
	if deltaDown < 0 {
		deltaDown = 0
	}

	e.uploadSpeed = int64(float64(deltaUp) / elapsed)
	e.downloadSpeed = int64(float64(deltaDown) / elapsed)
	e.lastSpeedUpload = curUp
	e.lastSpeedDown = curDown
	e.lastSpeedAt = now
}

func (e *entry) restorePersistedState(state PersistedState) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	blacklisted := state.Blacklisted
	until := state.BlacklistedUntil
	if blacklisted && !until.IsZero() && now.After(until) {
		blacklisted = false
		until = time.Time{}
	}

	lastTrafficOK := state.LastTrafficSuccessAt
	if lastTrafficOK.IsZero() && (state.TotalUpload > 0 || state.TotalDownload > 0) && !state.LastSuccessAt.IsZero() {
		lastTrafficOK = state.LastSuccessAt
	}

	e.failure = state.FailureCount
	e.success = state.SuccessCount
	e.trafficSuccess = state.TrafficSuccessCount
	e.blacklist = blacklisted
	e.until = until
	e.lastError = state.LastError
	e.lastFail = state.LastFailureAt
	e.lastOK = state.LastSuccessAt
	e.lastTrafficOK = lastTrafficOK
	e.eventSeq = 0
	e.lastFailureSeq = 0
	e.lastTrafficSeq = 0
	switch {
	case !e.lastFail.IsZero() && !e.lastTrafficOK.IsZero() && e.lastFail.After(e.lastTrafficOK):
		e.lastTrafficSeq = 1
		e.lastFailureSeq = 2
		e.eventSeq = 2
	case !e.lastFail.IsZero() && !e.lastTrafficOK.IsZero() && e.lastTrafficOK.After(e.lastFail):
		e.lastFailureSeq = 1
		e.lastTrafficSeq = 2
		e.eventSeq = 2
	case !e.lastTrafficOK.IsZero():
		e.lastTrafficSeq = 1
		e.eventSeq = 1
	case !e.lastFail.IsZero():
		e.lastFailureSeq = 1
		e.eventSeq = 1
	}
	e.lastProbeAt = state.LastProbeAt
	e.lastProbeOK = state.LastProbeSuccessAt
	e.available = state.Available
	e.initialCheckDone = state.InitialCheckDone
	e.healthGen = e.reloadGen
	if state.LastLatencyMs > 0 {
		e.lastProbe = time.Duration(state.LastLatencyMs) * time.Millisecond
	} else {
		e.lastProbe = 0
	}

	e.totalUpload.Store(state.TotalUpload)
	e.totalDownload.Store(state.TotalDownload)
	e.uploadSpeed = 0
	e.downloadSpeed = 0
	e.lastSpeedUpload = state.TotalUpload
	e.lastSpeedDown = state.TotalDownload
	e.lastSpeedAt = now
}

// RecordFailure updates failure counters with destination info.
