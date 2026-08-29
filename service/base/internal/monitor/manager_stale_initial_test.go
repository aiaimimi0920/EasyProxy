package monitor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpdateProbeTargetsSupersedesInFlightPeriodicRound(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	handle := mgr.Register(NodeInfo{Tag: "probe-target-reload"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(started)
		<-release // model a network call that ignores cancellation
		return time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("periodic probe did not start")
	}

	updateDone := make(chan error, 1)
	go func() {
		updateDone <- mgr.UpdateProbeTarget("http://new.example/")
	}()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("UpdateProbeTarget() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UpdateProbeTarget did not return while the old probe ignored cancellation")
	}
	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("old probe remained authoritative after target update: %+v", snap)
	}

	releaseOnce.Do(func() { close(release) })

	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("stale probe result mutated health after target update: %+v", snap)
	}
	targets, ok := mgr.ProbeTargets()
	if !ok || len(targets) != 1 || targets[0].Original != "http://new.example/" {
		t.Fatalf("probe targets after update = %+v (ok=%v)", targets, ok)
	}
}

func TestManualProbeCompletionCannotMutateAfterProbeTargetUpdate(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	started := make(chan struct{})
	release := make(chan struct{})
	handle := mgr.Register(NodeInfo{Tag: "manual-probe-target-reload"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(started)
		<-release
		return time.Millisecond, nil
	})

	probeDone := make(chan error, 1)
	go func() {
		_, probeErr := mgr.Probe(context.Background(), "manual-probe-target-reload")
		probeDone <- probeErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("manual probe did not start")
	}
	if err := mgr.UpdateProbeTarget("http://new.example/"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}
	close(release)
	select {
	case err := <-probeDone:
		if !errors.Is(err, ErrStaleGeneration) {
			t.Fatalf("manual probe error = %v, want stale-generation rejection", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual probe did not finish after release")
	}
	if snap := handle.Snapshot(); snap.Available || snap.InitialCheckDone {
		t.Fatalf("stale manual probe mutated health: %+v", snap)
	}
}

func TestLateManualProbeCannotOverwriteNewerManualProbe(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(firstRelease) }) })
	var calls atomic.Int32
	handle := mgr.Register(NodeInfo{Tag: "manual-order"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-firstRelease
			return 0, errors.New("late manual failure")
		}
		return 2 * time.Millisecond, nil
	})

	firstDone := make(chan error, 1)
	go func() {
		_, probeErr := mgr.Probe(context.Background(), "manual-order")
		firstDone <- probeErr
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first manual probe did not start")
	}

	if _, err := mgr.Probe(context.Background(), "manual-order"); err != nil {
		t.Fatalf("newer manual probe error = %v", err)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 {
		t.Fatalf("newer manual probe did not establish health: %+v", snap)
	}

	releaseOnce.Do(func() { close(firstRelease) })
	select {
	case err := <-firstDone:
		if !errors.Is(err, ErrStaleGeneration) {
			t.Fatalf("late manual probe error = %v, want stale-result rejection", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late manual probe did not finish")
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 || len(snap.Timeline) != 1 {
		t.Fatalf("late manual probe overwrote newer health: %+v", snap)
	}
}

func TestLatePeriodicProbeCannotOverwriteNewerManualProbe(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	periodicStarted := make(chan struct{})
	periodicRelease := make(chan struct{})
	periodicReturned := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(periodicRelease) }) })
	var calls atomic.Int32
	handle := mgr.Register(NodeInfo{Tag: "periodic-manual-order"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		if calls.Add(1) == 1 {
			close(periodicStarted)
			<-periodicRelease
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
	if _, err := mgr.Probe(context.Background(), "periodic-manual-order"); err != nil {
		t.Fatalf("newer manual probe error = %v", err)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 {
		t.Fatalf("newer manual probe did not establish health: %+v", snap)
	}

	releaseOnce.Do(func() { close(periodicRelease) })
	select {
	case <-periodicReturned:
	case <-time.After(time.Second):
		t.Fatal("periodic probe did not return")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mgr.probeRoundMu.Lock()
		active := mgr.probeRound
		mgr.probeRoundMu.Unlock()
		if active == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snap := handle.Snapshot(); !snap.Available || snap.FailureCount != 0 || len(snap.Timeline) != 1 {
		t.Fatalf("late periodic probe overwrote newer manual health: %+v", snap)
	}
}

func TestBeginReloadResetsInitialProbeGateAndRejectsOldRoundCompletion(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	oldStarted := make(chan struct{})
	oldRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(oldRelease) }) })
	handle := mgr.Register(NodeInfo{Tag: "reload-gate"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		close(oldStarted)
		<-oldRelease
		return time.Millisecond, nil
	})

	mgr.RequestProbeAllOnce(5 * time.Second)
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old periodic probe did not start")
	}

	generation := mgr.BeginReload()
	if err := mgr.WaitForInitialProbe(75 * time.Millisecond); err == nil {
		t.Fatal("reload should reset the initial probe gate until the new round completes")
	}

	candidate := mgr.Register(NodeInfo{Tag: "reload-gate"})
	candidate.SetProbe(func(context.Context) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	})
	summary, err := mgr.ProbeGeneration(context.Background(), generation, time.Second)
	if err != nil {
		t.Fatalf("new generation probe error = %v", err)
	}
	if summary.Available != 1 || summary.Completed != 1 {
		t.Fatalf("new generation probe summary = %+v", summary)
	}
	if err := mgr.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("matching generation probe should complete the gate: %v", err)
	}

	releaseOnce.Do(func() { close(oldRelease) })
}

func TestUpdateProbeTargetResetsInitialProbeGateUntilDirectGenerationProbe(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(NodeInfo{Tag: "target-gate"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		return 2 * time.Millisecond, nil
	})
	mgr.probeAllNodes(time.Second)
	if err := mgr.WaitForInitialProbe(100 * time.Millisecond); err != nil {
		t.Fatalf("initial probe should complete the initial gate: %v", err)
	}

	if err := mgr.UpdateProbeTarget("http://new.example/"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}
	if err := mgr.WaitForInitialProbe(75 * time.Millisecond); err == nil {
		t.Fatal("probe target update should reset the initial probe gate")
	}

	mgr.mu.RLock()
	generation := mgr.reloadGen
	mgr.mu.RUnlock()
	if _, err := mgr.ProbeGeneration(context.Background(), generation, time.Second); err != nil {
		t.Fatalf("direct generation probe error = %v", err)
	}
	if err := mgr.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("direct matching generation probe should complete the gate: %v", err)
	}
}

func TestPeriodicHealthCheckStartsAfterLateProbeTarget(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	probed := make(chan struct{}, 1)
	handle := mgr.Register(NodeInfo{Tag: "late-periodic-target"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		select {
		case probed <- struct{}{}:
		default:
		}
		return time.Millisecond, nil
	})

	mgr.StartPeriodicHealthCheck(20*time.Millisecond, 100*time.Millisecond)
	select {
	case <-probed:
		t.Fatal("periodic probe ran before a target was configured")
	case <-time.After(40 * time.Millisecond):
	}

	if err := mgr.UpdateProbeTarget("http://late.example/"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}
	select {
	case <-probed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("periodic health check did not resume after adding a probe target")
	}
	if err := mgr.WaitForInitialProbe(200 * time.Millisecond); err != nil {
		t.Fatalf("late periodic probe did not complete the initial gate: %v", err)
	}
}

func TestInitialProbeWaiterFollowsGateResetToNewGeneration(t *testing.T) {
	mgr, err := NewManager(Config{ProbeTarget: "http://old.example/"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	waitDone := make(chan error, 1)
	mgr.initialProbeMu.Lock()
	go func() {
		waitDone <- mgr.WaitForInitialProbe(250 * time.Millisecond)
	}()
	mgr.initialProbeMu.Unlock()
	// Give the waiter time to capture the old gate before the reload replaces it.
	time.Sleep(25 * time.Millisecond)

	generation := mgr.BeginReload()
	select {
	case err := <-waitDone:
		t.Fatalf("waiter returned before the replacement probe: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	handle := mgr.Register(NodeInfo{Tag: "waiter-gate"})
	handle.SetProbe(func(context.Context) (time.Duration, error) {
		return time.Millisecond, nil
	})
	if _, err := mgr.ProbeGeneration(context.Background(), generation, time.Second); err != nil {
		t.Fatalf("replacement generation probe error = %v", err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("waiter did not follow the replacement gate: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not finish after replacement probe")
	}
}

func TestUpdateProbeTargetsCanBeCleared(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if err := manager.UpdateProbeTargets([]string{"https://platform.openai.com/login"}, ""); err != nil {
		t.Fatalf("UpdateProbeTargets() error = %v", err)
	}
	if _, ok := manager.ProbeTargets(); !ok {
		t.Fatal("expected probe targets to be ready before clear")
	}

	if err := manager.UpdateProbeTargets(nil, ""); err != nil {
		t.Fatalf("UpdateProbeTargets(clear) error = %v", err)
	}
	if _, ok := manager.ProbeTargets(); ok {
		t.Fatal("expected probe targets to be cleared")
	}
	if destination, ok := manager.DestinationForProbe(); ok || destination.Fqdn != "" {
		t.Fatalf("expected probe destination to be cleared, got %+v", destination)
	}
}
