package monitor

import (
	"math"
	"time"
)

// ProbeMetricWindow bounds both memory and the age of samples used for routing.
const (
	ProbeMetricWindow           = 10 * time.Minute
	ProbeMetricSampleLimit      = 8
	ProbeMetricMinJitterSamples = 3
)

type probeLatencySample struct {
	at      time.Time
	latency time.Duration
}
type probeLatencyWindow struct {
	generation Generation
	samples    [ProbeMetricSampleLimit]probeLatencySample
	next       int
}

func (w *probeLatencyWindow) add(at time.Time, latency time.Duration) {
	if latency <= 0 {
		return
	}
	w.samples[w.next] = probeLatencySample{at, latency}
	w.next = (w.next + 1) % len(w.samples)
}

// ProbeMetrics is an atomic, generation-checked view of fresh successful probes.
// Jitter is the population standard deviation, in duration units, not a health score.
type ProbeMetrics struct {
	Latency      time.Duration
	Jitter       time.Duration
	Samples      int
	LastSampleAt time.Time
}

func (w *probeLatencyWindow) metrics(now time.Time, history, freshness time.Duration) ProbeMetrics {
	var out ProbeMetrics
	var mean, m2 float64
	for _, s := range w.samples {
		if s.latency <= 0 || s.at.After(now) || now.Sub(s.at) > history {
			continue
		}
		out.Samples++
		delta := float64(s.latency) - mean
		mean += delta / float64(out.Samples)
		m2 += delta * (float64(s.latency) - mean)
		if !s.at.Before(out.LastSampleAt) {
			out.Latency = s.latency
			out.LastSampleAt = s.at
		}
	}
	if out.LastSampleAt.IsZero() || now.Sub(out.LastSampleAt) > freshness {
		return ProbeMetrics{}
	}
	if out.Samples >= ProbeMetricMinJitterSamples {
		out.Jitter = time.Duration(math.Sqrt(m2 / float64(out.Samples)))
	}
	return out
}

// ProbeMetrics avoids copying timeline/counters on the metric selection hot path.
func (h *EntryHandle) ProbeMetrics(now time.Time) ProbeMetrics {
	if h == nil || h.ref == nil {
		return ProbeMetrics{}
	}
	e := h.ref
	history, freshness := ProbeMetricWindow, ProbeMetricWindow
	e.mu.RLock()
	owner := e.owner
	e.mu.RUnlock()
	if owner != nil {
		owner.healthMu.Lock()
		interval := owner.healthInterval
		owner.healthMu.Unlock()
		// Clamp arithmetic to a week; normal intervals are much shorter.
		interval = min(interval, 7*24*time.Hour)
		history = max(history, ProbeMetricSampleLimit*interval)
		freshness = max(freshness, 2*interval)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.healthGen != e.reloadGen || e.latencySamples.generation != e.reloadGen || !e.available || e.upstreamAuthFailed {
		return ProbeMetrics{}
	}
	return e.latencySamples.metrics(now, history, freshness)
}

func (e *entry) recordProbeMetricLocked(now time.Time, latency time.Duration) {
	if e.latencySamples.generation != e.reloadGen {
		e.latencySamples = probeLatencyWindow{generation: e.reloadGen}
	}
	e.latencySamples.add(now, latency)
}
