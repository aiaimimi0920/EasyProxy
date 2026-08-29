package monitor

import (
	"context"
	"time"
)

func (h *EntryHandle) RecordFailure(err error, destination string) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordFailure(err, destination)
}

// RecordObservationFailure appends a failed timeline event without degrading
// hard route-failure counters. This is used for business failures that do not
// prove the proxy route itself is unhealthy.
func (h *EntryHandle) RecordObservationFailure(err error, destination string) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordObservationFailure(err, destination)
}

// RecordSuccess updates the last success timestamp with destination info.
func (h *EntryHandle) RecordSuccess(destination string) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordSuccess(destination)
}

// RecordSuccessWithLatency updates the last success timestamp and latency.
func (h *EntryHandle) RecordSuccessWithLatency(latency time.Duration) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.recordSuccessWithLatency(latency)
}

// Blacklist marks the node unavailable until the given deadline.
func (h *EntryHandle) Blacklist(until time.Time) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.blacklistUntil(until)
}

// ClearBlacklist removes the blacklist flag.
func (h *EntryHandle) ClearBlacklist() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.clearBlacklist()
}

// IncActive increments the active connection counter.
func (h *EntryHandle) IncActive() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.incActive()
}

// DecActive decrements the active connection counter.
func (h *EntryHandle) DecActive() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.decActive()
}

// SetActiveConnections replaces the active connection counter with an exact
// shared-state snapshot during monitor-entry reattachment.
func (h *EntryHandle) SetActiveConnections(active int32) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.setActiveConnections(active)
}

// SetProbe assigns a probe function.
func (h *EntryHandle) SetProbe(fn func(ctx context.Context) (time.Duration, error)) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.setProbe(fn)
}

// SetRelease assigns a release function.
func (h *EntryHandle) SetRelease(fn func()) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.setRelease(fn)
}

// MarkInitialCheckDone marks the initial health check as completed.
func (h *EntryHandle) MarkInitialCheckDone(available bool) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.mu.Lock()
	h.ref.initialCheckDone = true
	h.ref.available = available
	h.ref.healthGen = h.ref.reloadGen
	h.ref.mu.Unlock()
}

// MarkAvailable updates the availability status.
func (h *EntryHandle) MarkAvailable(available bool) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.mu.Lock()
	h.ref.available = available
	h.ref.healthGen = h.ref.reloadGen
	h.ref.mu.Unlock()
}

// AddTraffic adds upload and download byte counts to the node's traffic counters.
func (h *EntryHandle) AddTraffic(upload, download int64) {
	if h == nil || h.ref == nil {
		return
	}
	if upload > 0 {
		h.ref.totalUpload.Add(upload)
	}
	if download > 0 {
		h.ref.totalDownload.Add(download)
	}
}

// SetTraffic sets the traffic counters to specific values (used for restoring from store).
func (h *EntryHandle) SetTraffic(upload, download int64) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.totalUpload.Store(upload)
	h.ref.totalDownload.Store(download)
	h.ref.mu.Lock()
	h.ref.uploadSpeed = 0
	h.ref.downloadSpeed = 0
	h.ref.lastSpeedUpload = upload
	h.ref.lastSpeedDown = download
	h.ref.lastSpeedAt = time.Now()
	h.ref.mu.Unlock()
}

// ApplyUsageReportSuccess updates the external task-feedback score after a
// successful business request routed through this node.
func (h *EntryHandle) ApplyUsageReportSuccess() {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.applyUsageReportSuccess()
}

// ApplyUsageReportFailure updates the external task-feedback score after a
// failed business request routed through this node.
func (h *EntryHandle) ApplyUsageReportFailure(penalty int, countAsRouteFailure bool) {
	if h == nil || h.ref == nil {
		return
	}
	h.ref.applyUsageReportFailure(penalty, countAsRouteFailure)
}

// Snapshot returns a point-in-time copy of the node state.
func (h *EntryHandle) Snapshot() Snapshot {
	if h == nil || h.ref == nil {
		return Snapshot{}
	}
	return h.ref.snapshot()
}
