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
