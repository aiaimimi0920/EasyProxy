package monitor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegisterPreservesHealthForStableURIOnReload(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	old := mgr.Register(NodeInfo{Tag: "stable-node", Name: "Stable", URI: "ss://stable.example:443"})
	old.RecordSuccessWithLatency(42 * time.Millisecond)

	mgr.BeginReload()
	candidate := mgr.Register(NodeInfo{Tag: "stable-node", Name: "Stable renamed", URI: "ss://stable.example:443"})
	snap := candidate.Snapshot()
	if !snap.InitialCheckDone || !snap.Available || !snap.EffectiveAvailable || snap.LastLatencyMs != 42 {
		t.Fatalf("stable node health was not preserved: %+v", snap)
	}
}

func TestRegisterResetsHealthWhenURIChanges(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	old := mgr.Register(NodeInfo{Tag: "rotated-node", Name: "Rotated", URI: "ss://old.example:443"})
	old.RecordSuccessWithLatency(42 * time.Millisecond)

	mgr.BeginReload()
	candidate := mgr.Register(NodeInfo{Tag: "rotated-node", Name: "Rotated", URI: "ss://new.example:443"})
	snap := candidate.Snapshot()
	if snap.InitialCheckDone || snap.Available || snap.EffectiveAvailable || snap.LastLatencyMs != -1 || snap.SuccessCount != 0 {
		t.Fatalf("changed node URI inherited stale health: %+v", snap)
	}
	old.RecordSuccessWithLatency(5 * time.Millisecond)
	if snap = candidate.Snapshot(); snap.InitialCheckDone || snap.LastLatencyMs != -1 {
		t.Fatalf("detached old handle mutated replacement node: %+v", snap)
	}
}

func TestLateProbeCompletionCannotMutateNewGeneration(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	mgr.probeReady = true
	started := make(chan struct{})
	release := make(chan struct{})
	generation := mgr.BeginReload()
	handle := mgr.Register(NodeInfo{Tag: "late-probe"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(started)
		<-release
		return time.Millisecond, nil
	})
	probeDone := make(chan struct{})
	go func() {
		_, _ = mgr.ProbeGeneration(context.Background(), generation, time.Second)
		close(probeDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	mgr.BeginReload()
	mgr.Register(NodeInfo{Tag: "late-probe"})
	close(release)
	select {
	case <-probeDone:
	case <-time.After(time.Second):
		t.Fatal("probe did not finish")
	}
	snap := handle.Snapshot()
	if snap.Available || snap.InitialCheckDone || snap.LastProbeSuccessAt.IsZero() == false {
		t.Fatalf("stale probe mutated the new generation: %+v", snap)
	}
}

func TestBeginReloadInvalidatesPeriodicRoundAndAllowsNewGenerationRequest(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	oldStarted := make(chan struct{})
	oldRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(oldRelease) }) })
	oldHandle := mgr.Register(NodeInfo{Tag: "periodic-generation"})
	oldHandle.SetProbe(func(context.Context) (time.Duration, error) {
		close(oldStarted)
		<-oldRelease // Deliberately ignore cancellation like a stuck network call.
		return time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old periodic probe did not start")
	}

	mgr.BeginReload()
	candidateCalled := make(chan struct{})
	candidateHandle := mgr.Register(NodeInfo{Tag: "periodic-generation"})
	candidateHandle.SetProbe(func(context.Context) (time.Duration, error) {
		close(candidateCalled)
		return 2 * time.Millisecond, nil
	})
	mgr.RequestProbeAllOnce(time.Second)

	select {
	case <-candidateCalled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("new-generation periodic request was swallowed by the old in-flight round")
	}
	releaseOnce.Do(func() { close(oldRelease) })
}

func TestRegisterNewGenerationDoesNotReusePreviousProbeBeforeReplacement(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	var oldProbeCalls atomic.Int32
	oldHandle := mgr.Register(NodeInfo{Tag: "probe-replacement-window"})
	oldHandle.SetProbe(func(context.Context) (time.Duration, error) {
		oldProbeCalls.Add(1)
		return time.Millisecond, nil
	})

	generation := mgr.BeginReload()
	candidateHandle := mgr.Register(NodeInfo{Tag: "probe-replacement-window"})
	summary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration() error = %v", err)
	}
	if got := oldProbeCalls.Load(); got != 0 {
		t.Fatalf("new generation reused the previous probe closure %d time(s)", got)
	}
	if summary.Total != 1 || summary.Completed != 1 || summary.Available != 0 || summary.Failed != 1 {
		t.Fatalf("candidate summary before probe replacement = %+v, want one unavailable candidate", summary)
	}
	if snap := candidateHandle.Snapshot(); snap.Available || !snap.InitialCheckDone {
		t.Fatalf("candidate health before probe replacement = %+v", snap)
	}
}

func TestReplacingProbeInvalidatesInFlightResultWithinGeneration(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	generation := mgr.BeginReload()
	handle := mgr.Register(NodeInfo{Tag: "same-generation-probe-replacement"})
	oldStarted := make(chan struct{})
	oldRelease := make(chan struct{})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(oldStarted)
		<-oldRelease
		return time.Millisecond, nil
	})

	type probeResult struct {
		summary ProbeSummary
		err     error
	}
	resultCh := make(chan probeResult, 1)
	go func() {
		summary, probeErr := mgr.ProbeGeneration(context.Background(), generation, time.Second)
		resultCh <- probeResult{summary: summary, err: probeErr}
	}()
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old probe did not start")
	}

	var replacementCalls atomic.Int32
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		replacementCalls.Add(1)
		return 2 * time.Millisecond, nil
	})
	close(oldRelease)
	result := <-resultCh
	if result.err != nil {
		t.Fatalf("ProbeGeneration(old probe) error = %v", result.err)
	}
	if result.summary.Available != 0 || result.summary.Completed != 0 {
		t.Fatalf("replaced probe result was committed: %+v", result.summary)
	}
	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("replaced probe mutated current health: %+v", snap)
	}

	replacementSummary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration(replacement) error = %v", err)
	}
	if replacementCalls.Load() != 1 || replacementSummary.Available != 1 {
		t.Fatalf("replacement probe did not become authoritative: calls=%d summary=%+v", replacementCalls.Load(), replacementSummary)
	}
}

func TestProbeGenerationSupersedesInFlightPeriodicRoundWithinSameGeneration(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	generation := mgr.BeginReload()
	handle := mgr.Register(NodeInfo{Tag: "exclusive-generation-probe"})
	periodicStarted := make(chan struct{})
	periodicRelease := make(chan struct{})
	periodicReturned := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(periodicRelease) }) })
	var calls atomic.Int32
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		if calls.Add(1) == 1 {
			close(periodicStarted)
			<-periodicRelease // Ignore cancellation to model a late network return.
			close(periodicReturned)
			return 0, errors.New("late periodic failure")
		}
		return 2 * time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-periodicStarted:
	case <-time.After(time.Second):
		t.Fatal("periodic probe did not start")
	}

	summary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("ProbeGeneration() error = %v", err)
	}
	if summary.Available != 1 || summary.Completed != 1 {
		t.Fatalf("candidate generation summary = %+v", summary)
	}
	if snap := handle.Snapshot(); !snap.Available {
		t.Fatalf("candidate probe did not publish availability: %+v", snap)
	}

	releaseOnce.Do(func() { close(periodicRelease) })
	select {
	case <-periodicReturned:
	case <-time.After(time.Second):
		t.Fatal("late periodic probe did not return")
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mgr.probeRoundMu.Lock()
		activeRound := mgr.probeRound
		mgr.probeRoundMu.Unlock()
		if activeRound == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.LastError != "" || snap.FailureCount != 0 || len(snap.Timeline) != 1 {
		t.Fatalf("late periodic result overwrote the exclusive candidate probe: %+v", snap)
	}
}
