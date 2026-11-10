package monitor

import (
	"context"
	"testing"
	"time"
)

func TestProbeMetricsReloadPreservesOnlySameIdentity(t *testing.T) {
	m, err := NewManager(Config{})
	if err != nil {
		t.Fatal(err)
	}
	info := NodeInfo{Tag: "node", URI: "https://same.example:443"}
	h := m.Register(info)
	for i := 1; i <= 3; i++ {
		h.RecordSuccessWithLatency(time.Duration(i) * time.Millisecond)
	}
	before := h.ProbeMetrics(time.Now())
	if before.Samples != 3 {
		t.Fatalf("initial metrics=%+v", before)
	}
	for i := 0; i < 3; i++ {
		m.BeginReload()
		h = m.Register(info)
		got := h.ProbeMetrics(time.Now())
		if got != before {
			t.Fatalf("reload changed observations: before=%+v after=%+v", before, got)
		}
	}
	h.SetProbe(func(context.Context) (time.Duration, error) { return 4 * time.Millisecond, nil })
	if _, err = m.Probe(context.Background(), info.Tag); err != nil {
		t.Fatal(err)
	}
	if got := h.ProbeMetrics(time.Now()); got.Samples != 4 {
		t.Fatalf("first new probe erased history: %+v", got)
	}
	m.BeginReload()
	info.URI = "https://replacement.example:443"
	replacement := m.Register(info)
	replacement.RecordSuccessWithLatency(time.Millisecond)
	if got := replacement.ProbeMetrics(time.Now()); got.Samples != 1 {
		t.Fatalf("changed URI inherited observations: %+v", got)
	}
	// URI-less entries cannot establish identity across a reload.
	unknown := m.Register(NodeInfo{Tag: "unknown"})
	for i := 0; i < 3; i++ {
		unknown.RecordSuccessWithLatency(time.Millisecond)
	}
	m.BeginReload()
	unknown = m.Register(NodeInfo{Tag: "unknown"})
	unknown.RecordSuccessWithLatency(time.Millisecond)
	if got := unknown.ProbeMetrics(time.Now()); got.Samples != 1 {
		t.Fatalf("unknown identity inherited history: %+v", got)
	}
}

func TestProbeMetricsReloadRejectsInFlightOldProbe(t *testing.T) {
	m, err := NewManager(Config{})
	if err != nil {
		t.Fatal(err)
	}
	info := NodeInfo{Tag: "node", URI: "https://same.example:443"}
	h := m.Register(info)
	for i := 0; i < 3; i++ {
		h.RecordSuccessWithLatency(time.Millisecond)
	}
	started, release := make(chan struct{}), make(chan struct{})
	h.SetProbe(func(context.Context) (time.Duration, error) { close(started); <-release; return time.Second, nil })
	done := make(chan error, 1)
	go func() { _, err := m.Probe(context.Background(), info.Tag); done <- err }()
	<-started
	m.BeginReload()
	h = m.Register(info)
	close(release)
	if err := <-done; err != ErrStaleGeneration {
		t.Fatalf("late probe result=%v", err)
	}
	if got := h.ProbeMetrics(time.Now()); got.Samples != 3 || got.Latency != time.Millisecond {
		t.Fatalf("stale result modified preserved samples: %+v", got)
	}
}
