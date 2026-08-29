package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForInitialProbeBlocksUntilFirstProbeRoundCompletes(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	handle := manager.Register(NodeInfo{
		Tag:           "blocking-node",
		Name:          "Blocking Node",
		ListenAddress: "127.0.0.1",
		Port:          32001,
	})
	handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
		close(started)
		select {
		case <-release:
			return 25 * time.Millisecond, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})

	done := make(chan struct{})
	go func() {
		manager.StartPeriodicHealthCheck(time.Hour, time.Second)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected initial probe to start")
	}

	if err := manager.WaitForInitialProbe(100 * time.Millisecond); err == nil {
		t.Fatal("expected wait to time out while first probe is still blocked")
	}

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected StartPeriodicHealthCheck to return after initial probe completed")
	}

	if err := manager.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("expected initial probe wait to pass after completion, got %v", err)
	}

	manager.Stop()
}

func TestProbeAllNodesCompletesWhenProbeIgnoresContext(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	handle := manager.Register(NodeInfo{
		Tag:           "ignores-context",
		Name:          "Ignores Context",
		ListenAddress: "127.0.0.1",
		Port:          32003,
	})
	handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
		close(started)
		<-release
		return 0, errors.New("released after timeout")
	})

	done := make(chan struct{})
	go func() {
		manager.probeAllNodes(25 * time.Millisecond)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("expected probe to start")
	}

	timedOut := false
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		timedOut = true
	}
	close(release)
	if timedOut {
		t.Fatal("expected full probe round to complete after timeout even when a probe ignores context")
	}

	snap := handle.Snapshot()
	if !snap.InitialCheckDone || snap.Available {
		t.Fatalf("expected timed-out probe to mark node checked but unavailable, got %+v", snap)
	}
}

func TestManualProbeCompletesWhenProbeIgnoresContext(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer manager.Stop()

	release := make(chan struct{})
	handle := manager.Register(NodeInfo{Tag: "manual-ignores-context"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		<-release
		return time.Millisecond, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, probeErr := manager.Probe(ctx, "manual-ignores-context")
	close(release)
	if !errors.Is(probeErr, context.DeadlineExceeded) {
		t.Fatalf("Probe() error = %v, want context deadline exceeded", probeErr)
	}
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("manual probe returned after %v, want bounded context handling", elapsed)
	}
	if snap := handle.Snapshot(); !snap.InitialCheckDone || snap.Available {
		t.Fatalf("manual timeout snapshot = %+v, want checked and unavailable", snap)
	}
}

func TestNormalizeNodeProbeTimeoutCapsHealthGateTimeout(t *testing.T) {
	if got := normalizeNodeProbeTimeout(time.Minute); got != 10*time.Second {
		t.Fatalf("minute timeout normalized to %v, want 10s", got)
	}
	if got := normalizeNodeProbeTimeout(3 * time.Second); got != 3*time.Second {
		t.Fatalf("short timeout normalized to %v, want 3s", got)
	}
	if got := normalizeNodeProbeTimeout(0); got != 10*time.Second {
		t.Fatalf("zero timeout normalized to %v, want 10s", got)
	}
}

func TestProbeGenerationRoundDeadlineDoesNotPublishFalseFailures(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer manager.Stop()

	const totalNodes = 40
	handles := make([]*EntryHandle, 0, totalNodes)
	for idx := 0; idx < totalNodes; idx++ {
		handle := manager.Register(NodeInfo{Tag: fmt.Sprintf("round-deadline-%02d", idx)})
		handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		})
		handles = append(handles, handle)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	summary, probeErr := manager.ProbeGeneration(ctx, 0, time.Second)
	if !errors.Is(probeErr, context.DeadlineExceeded) {
		t.Fatalf("ProbeGeneration() error = %v, want context deadline exceeded", probeErr)
	}
	if summary.Completed != 0 || summary.Available != 0 || summary.Failed != 0 {
		t.Fatalf("round deadline summary = %+v, want no published node results", summary)
	}
	for _, handle := range handles {
		if snap := handle.Snapshot(); snap.InitialCheckDone || snap.Available || snap.LastError != "" {
			t.Fatalf("round deadline published a false node result: %+v", snap)
		}
	}
}

func TestProbeAllNodesAvoidsBurstConcurrencyThatOverwhelmsResolvers(t *testing.T) {
	manager, err := NewManager(Config{
		ProbeTargets: []string{"https://platform.openai.com/login"},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	const (
		totalNodes         = 40
		maxSafeConcurrency = 16
	)

	var inFlight atomic.Int32
	var maxSeen atomic.Int32

	for idx := 0; idx < totalNodes; idx++ {
		handle := manager.Register(NodeInfo{
			Tag:           fmt.Sprintf("burst-%02d", idx),
			Name:          fmt.Sprintf("Burst %02d", idx),
			ListenAddress: "127.0.0.1",
			Port:          uint16(32100 + idx),
		})
		handle.SetProbe(func(ctx context.Context) (time.Duration, error) {
			current := inFlight.Add(1)
			for {
				prev := maxSeen.Load()
				if current <= prev || maxSeen.CompareAndSwap(prev, current) {
					break
				}
			}
			defer inFlight.Add(-1)

			select {
			case <-time.After(30 * time.Millisecond):
			case <-ctx.Done():
				return 0, ctx.Err()
			}

			if current > maxSafeConcurrency {
				return 0, fmt.Errorf("simulated resolver overload at concurrency=%d", current)
			}
			return 10 * time.Millisecond, nil
		})
	}

	manager.probeAllNodes(500 * time.Millisecond)

	snapshots := manager.Snapshot()
	available := 0
	for _, snap := range snapshots {
		if snap.Available {
			available++
		}
	}

	if available != totalNodes {
		t.Fatalf(
			"expected all probes to stay below resolver burst limit and succeed, got available=%d/%d max_concurrency=%d",
			available,
			totalNodes,
			maxSeen.Load(),
		)
	}
	if maxSeen.Load() > maxSafeConcurrency {
		t.Fatalf("expected probe concurrency <= %d, got %d", maxSafeConcurrency, maxSeen.Load())
	}
}
