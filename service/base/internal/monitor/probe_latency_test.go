package monitor

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestProbeMetricWindow(t *testing.T) {
	now := time.Now()
	var w probeLatencyWindow
	for _, d := range []time.Duration{10, 20, 30} {
		w.add(now, d*time.Millisecond)
	}
	got := w.metrics(now, ProbeMetricWindow, ProbeMetricWindow)
	if got.Samples != 3 || math.Abs(float64(got.Jitter)/float64(time.Millisecond)-math.Sqrt(200.0/3)) > 0.001 {
		t.Fatalf("metrics = %+v", got)
	}
	if w.metrics(now.Add(ProbeMetricWindow+time.Nanosecond), ProbeMetricWindow, ProbeMetricWindow).Samples != 0 {
		t.Fatal("stale samples used")
	}
	w.add(now, 0)
	w.add(now, -1)
	if w.metrics(now, ProbeMetricWindow, ProbeMetricWindow).Samples != 3 {
		t.Fatal("invalid samples recorded")
	}
	for i := 0; i < 20; i++ {
		w.add(now, time.Millisecond)
	}
	if got := w.metrics(now, ProbeMetricWindow, ProbeMetricWindow); got.Samples != ProbeMetricSampleLimit || got.Jitter != 0 {
		t.Fatalf("window not bounded: %+v", got)
	}
}

func TestProbeMetricGenerationAndConcurrency(t *testing.T) {
	e := &entry{}
	h := &EntryHandle{ref: e}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.RecordSuccessWithLatency(time.Millisecond)
				_ = h.ProbeMetrics(time.Now())
			}
		}()
	}
	wg.Wait()
	if got := h.ProbeMetrics(time.Now()); got.Samples != 8 {
		t.Fatalf("samples = %+v", got)
	}
	e.mu.Lock()
	e.reloadGen++
	e.mu.Unlock()
	if h.ProbeMetrics(time.Now()).Samples != 0 {
		t.Fatal("old generation escaped")
	}
	h.RecordSuccessWithLatency(2 * time.Millisecond)
	if h.ProbeMetrics(time.Now()).Samples != 1 {
		t.Fatal("old samples crossed generation")
	}
	e.mu.Lock()
	e.available = false
	e.mu.Unlock()
	if h.ProbeMetrics(time.Now()).Samples != 0 {
		t.Fatal("failed probe metrics used")
	}
}

func TestProbeMetricsUsesConfiguredCadence(t *testing.T) {
	now := time.Now()
	mgr := &Manager{healthInterval: 2 * time.Hour}
	e := &entry{owner: mgr, available: true}
	for i := 0; i < 3; i++ {
		e.latencySamples.add(now.Add(-time.Duration(i)*2*time.Hour), time.Duration(i+1)*time.Millisecond)
	}
	h := &EntryHandle{ref: e}
	if got := h.ProbeMetrics(now); got.Samples != 3 {
		t.Fatalf("default cadence lost history: %+v", got)
	}
	if got := h.ProbeMetrics(now.Add(4*time.Hour + time.Second)); got.Samples != 0 {
		t.Fatalf("stale last sample accepted: %+v", got)
	}
}
