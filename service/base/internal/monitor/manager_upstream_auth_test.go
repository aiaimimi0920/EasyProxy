package monitor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUpstreamAuthFailureRejectsEarlierProbeCompletion(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	entry := mgr.Register(NodeInfo{Tag: "auth-race"})
	entry.MarkInitialCheckDone(true)
	started, finish := make(chan struct{}), make(chan struct{})
	entry.SetProbe(func(context.Context) (time.Duration, error) {
		close(started)
		<-finish
		return time.Millisecond, nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := mgr.Probe(context.Background(), "auth-race")
		done <- err
	}()
	<-started
	entry.RecordFailure(ErrUpstreamAuthenticationRequired, "www.google.com:443")
	close(finish)
	if err := <-done; !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("earlier probe result = %v, want stale result", err)
	}
	if snap := entry.Snapshot(); snap.Available || snap.EffectiveAvailable || !snap.UpstreamAuthFailed {
		t.Fatalf("earlier probe revived authentication-rejected node: %+v", snap)
	}
	// A failed fresh probe must not drop the quarantine merely because its
	// diagnostic text differs from the original authentication rejection.
	entry.SetProbe(func(context.Context) (time.Duration, error) { return 0, context.DeadlineExceeded })
	_, _ = mgr.Probe(context.Background(), "auth-race")
	if !entry.Snapshot().UpstreamAuthFailed {
		t.Fatal("failed probe cleared authentication quarantine")
	}
	entry.SetProbe(func(context.Context) (time.Duration, error) { return time.Millisecond, nil })
	if _, err := mgr.Probe(context.Background(), "auth-race"); err != nil {
		t.Fatal(err)
	}
	if snap := entry.Snapshot(); !snap.EffectiveAvailable || snap.UpstreamAuthFailed {
		t.Fatalf("fresh successful probe did not recover node: %+v", snap)
	}
}

func TestPeriodicProbeSkipsRecentAuthFailedNodeUntilCooldownOrExplicitProbe(t *testing.T) {
	mgr, err := NewManager(Config{
		ProbeTarget:               "http://127.0.0.1:1",
		UpstreamAuthProbeCooldown: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	generation := mgr.BeginReload()
	handle := mgr.Register(NodeInfo{Tag: "auth-cooldown", URI: "http://1.2.3.4:8080"})
	var calls int32
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		calls++
		if calls == 1 {
			return 0, ErrUpstreamAuthenticationRequired
		}
		return time.Millisecond, nil
	})

	// Initial synchronous generation probe records 407 failure.
	if _, err := mgr.ProbeGeneration(context.Background(), generation, time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !handle.Snapshot().UpstreamAuthFailed {
		t.Fatalf("expected 1 probe and upstream auth failure, calls=%d", calls)
	}

	// Background periodic probe within cooldown skips network dial.
	mgr.RequestProbeAllOnce(time.Second)
	mgr.probeRoundMu.Lock()
	round := mgr.probeRound
	mgr.probeRoundMu.Unlock()
	if round != nil {
		<-round.done
	}
	if calls != 1 || !handle.Snapshot().UpstreamAuthFailed {
		t.Fatalf("periodic round did not respect auth cooldown: calls=%d", calls)
	}

	// Explicit single-node probe still bypasses cooldown immediately.
	if _, err := mgr.Probe(context.Background(), "auth-cooldown"); err != nil {
		t.Fatalf("explicit Probe() error = %v", err)
	}
	if calls != 2 || handle.Snapshot().UpstreamAuthFailed || !handle.Snapshot().EffectiveAvailable {
		t.Fatalf("explicit probe did not recover node: calls=%d snap=%+v", calls, handle.Snapshot())
	}
}

func TestAuthCooldownRequiresProbeInThisGeneration(t *testing.T) {
	m, err := NewManager(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	node := NodeInfo{Tag: "same", URI: "http://example.invalid:8080"}
	g := m.BeginReload()
	handle := m.Register(node)
	handle.SetProbe(func(context.Context) (time.Duration, error) { return 0, ErrUpstreamAuthenticationRequired })
	if s, err := m.ProbeGeneration(context.Background(), g, time.Second); err != nil || s.Failed != 1 {
		t.Fatalf("setup: %+v %v", s, err)
	}
	at := handle.ref.authProbeAt
	handle.SetProbe(func(context.Context) (time.Duration, error) { return 0, context.DeadlineExceeded })
	_, _ = m.Probe(context.Background(), node.Tag)
	if !handle.ref.authProbeAt.Equal(at) || shouldSkipAuthFailedBackgroundProbe(handle.ref, g, at.Add(time.Hour), time.Hour) {
		t.Fatal("generic failure extended auth cooldown")
	}
	g = m.BeginReload()
	m.Register(node).SetProbe(func(context.Context) (time.Duration, error) { return time.Millisecond, nil })
	s, err := m.ProbeGenerationAtLeast(context.Background(), g, 1, time.Second)
	if err != nil || s.Available != 1 || s.Completed != s.Failed+s.Available {
		t.Fatalf("new generation: %+v %v", s, err)
	}
}
