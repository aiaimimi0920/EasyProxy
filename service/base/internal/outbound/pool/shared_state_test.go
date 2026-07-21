package pool

import (
	"errors"
	"testing"
	"time"

	"easy_proxies/internal/monitor"
)

func TestSharedStateTransactionRollbackRestoresExactOldRegistry(t *testing.T) {
	ResetSharedStateStore()
	oldState := acquireSharedState("node")
	oldState.mu.Lock()
	oldState.failures = 2
	oldState.blacklisted = true
	oldState.blacklistedUntil = time.Now().Add(time.Hour)
	oldState.mu.Unlock()
	oldState.active.Store(3)
	oldState.totalUpload.Store(11)
	oldState.totalDownload.Store(22)

	txn := BeginSharedStateTransaction()
	candidateState := acquireSharedState("node")
	if candidateState == oldState {
		t.Fatal("candidate reused the old shared state registry")
	}
	candidateState.recordFailure(errors.New("candidate failure"), 1, time.Hour, "candidate.example:443")
	candidateState.addTraffic(100, 200)

	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	restored := acquireSharedState("node")
	if restored != oldState {
		t.Fatal("rollback did not restore the exact old shared state object")
	}
	restored.mu.Lock()
	failures := restored.failures
	blacklisted := restored.blacklisted
	restored.mu.Unlock()
	if failures != 2 || !blacklisted {
		t.Fatalf("restored failure state = failures:%d blacklisted:%v", failures, blacklisted)
	}
	if got := restored.active.Load(); got != 3 {
		t.Fatalf("restored active count = %d, want 3", got)
	}
	if got := restored.totalUpload.Load(); got != 11 {
		t.Fatalf("restored upload = %d, want 11", got)
	}
	if got := restored.totalDownload.Load(); got != 22 {
		t.Fatalf("restored download = %d, want 22", got)
	}
}

func TestSharedStateTransactionCommitKeepsFreshCandidateRegistry(t *testing.T) {
	ResetSharedStateStore()
	oldState := acquireSharedState("node")
	oldEntry := &monitor.EntryHandle{}
	oldState.attachEntry(oldEntry)
	oldState.recordFailure(errors.New("old failure"), 1, time.Hour, "old.example:443")

	txn := BeginSharedStateTransaction()
	candidateState := acquireSharedState("node")
	if err := txn.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if got := acquireSharedState("node"); got != candidateState || got == oldState {
		t.Fatal("commit did not retain the fresh candidate registry")
	}
	if oldState.entryHandle() != nil {
		t.Fatal("committed transaction left the detached old registry attached to monitor state")
	}
}

func TestSharedStateTransactionResetCandidatePreservesRollbackSnapshot(t *testing.T) {
	ResetSharedStateStore()
	oldState := acquireSharedState("node")

	txn := BeginSharedStateTransaction()
	firstCandidate := acquireSharedState("node")
	firstCandidate.attachEntry(&monitor.EntryHandle{})
	if err := txn.ResetCandidate(); err != nil {
		t.Fatalf("ResetCandidate() error = %v", err)
	}
	secondCandidate := acquireSharedState("node")
	if secondCandidate == firstCandidate || secondCandidate == oldState {
		t.Fatal("candidate reset did not install a fresh registry")
	}
	if firstCandidate.entryHandle() != nil {
		t.Fatal("candidate reset left the rejected registry attached to monitor state")
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if got := acquireSharedState("node"); got != oldState {
		t.Fatal("candidate reset replaced the transaction rollback snapshot")
	}
}

func TestDetachedReleaseDoesNotMutateCandidateRegistry(t *testing.T) {
	ResetSharedStateStore()
	oldState := acquireSharedState("node")
	oldState.recordFailure(errors.New("old failure"), 1, time.Hour, "old.example:443")
	releaseOld := releaseSharedState(oldState)

	txn := BeginSharedStateTransaction()
	candidateState := acquireSharedState("node")
	candidateState.recordFailure(errors.New("candidate failure"), 1, time.Hour, "candidate.example:443")

	releaseOld()
	if !candidateState.isBlacklisted(time.Now()) {
		t.Fatal("detached old release cleared the candidate registry")
	}
	if err := txn.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

func TestSharedStateTransactionDetachesOldRegistryFromCandidateMonitorEntry(t *testing.T) {
	ResetSharedStateStore()
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	oldHandle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	oldState := acquireSharedState("node")
	oldState.attachEntry(oldHandle)

	txn := BeginSharedStateTransaction()
	defer func() {
		_ = txn.Rollback()
		ResetSharedStateStore()
	}()
	mgr.BeginReload()
	candidateHandle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	candidateState := acquireSharedState("node")
	candidateState.attachEntry(candidateHandle)

	oldState.recordFailure(errors.New("late old connection failure"), 3, time.Hour, "old.example:443")
	if snap := candidateHandle.Snapshot(); snap.FailureCount != 0 {
		t.Fatalf("detached old registry mutated candidate monitor entry: %+v", snap)
	}
}

func TestResetSharedStateStoreDetachesPreviousMonitorEntry(t *testing.T) {
	ResetSharedStateStore()
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	oldState := acquireSharedState("node")
	oldState.attachEntry(handle)

	ResetSharedStateStore()
	oldState.recordFailure(errors.New("late reset connection failure"), 3, time.Hour, "old.example:443")
	if snap := handle.Snapshot(); snap.FailureCount != 0 {
		t.Fatalf("reset registry remained attached to monitor entry: %+v", snap)
	}
}

func TestSharedStateAttachEntryReconcilesActiveCount(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	for i := 0; i < 5; i++ {
		handle.IncActive()
	}
	state := acquireSharedState("node")
	state.active.Store(2)

	state.attachEntry(handle)
	if got := handle.Snapshot().ActiveConnections; got != 2 {
		t.Fatalf("attached monitor active count = %d, want shared state count 2", got)
	}
}

func TestSharedStateAttachEntryReconcilesBlacklist(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	state := acquireSharedState("node")
	handle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	until := time.Now().Add(time.Hour)
	state.mu.Lock()
	state.blacklisted = true
	state.blacklistedUntil = until
	state.mu.Unlock()
	state.attachEntry(handle)
	if snap := handle.Snapshot(); !snap.Blacklisted || snap.BlacklistedUntil.IsZero() {
		t.Fatalf("attached monitor blacklist = %+v, want active blacklist", snap)
	}

	state.mu.Lock()
	state.blacklisted = false
	state.blacklistedUntil = time.Time{}
	state.mu.Unlock()
	state.attachEntry(handle)
	if snap := handle.Snapshot(); snap.Blacklisted {
		t.Fatalf("attached monitor retained stale blacklist: %+v", snap)
	}
}

func TestDetachedOldActiveCloseCannotDecrementReattachedCandidateEntry(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	oldState := acquireSharedState("node")
	oldState.active.Store(1)
	oldState.attachEntry(handle)

	txn := BeginSharedStateTransaction()
	candidateState := acquireSharedState("node")
	candidateState.attachEntry(handle)
	if got := handle.Snapshot().ActiveConnections; got != 0 {
		t.Fatalf("candidate attach inherited retired active count = %d, want 0", got)
	}

	oldState.decActive()
	if got := handle.Snapshot().ActiveConnections; got != 0 {
		t.Fatalf("late old close changed candidate active count = %d, want 0", got)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
}

func TestSharedStateAttachEntryReconcilesTrafficTotals(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	handle.AddTraffic(100, 200)
	state := acquireSharedState("node")

	state.attachEntry(handle)
	if snap := handle.Snapshot(); snap.TotalUpload != 0 || snap.TotalDownload != 0 {
		t.Fatalf("attached monitor retained stale traffic: upload=%d download=%d, want 0/0", snap.TotalUpload, snap.TotalDownload)
	}
}

func TestSharedStateRollbackReattachRestoresOldTrafficTotals(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	oldState := acquireSharedState("node")
	oldState.totalUpload.Store(11)
	oldState.totalDownload.Store(22)
	oldState.attachEntry(handle)

	txn := BeginSharedStateTransaction()
	candidateState := acquireSharedState("node")
	candidateState.attachEntry(handle)
	if snap := handle.Snapshot(); snap.TotalUpload != 0 || snap.TotalDownload != 0 {
		t.Fatalf("candidate attach retained old traffic: upload=%d download=%d, want 0/0", snap.TotalUpload, snap.TotalDownload)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	oldState.attachEntry(handle)
	if snap := handle.Snapshot(); snap.TotalUpload != 11 || snap.TotalDownload != 22 {
		t.Fatalf("rollback reattach traffic = upload:%d download:%d, want 11/22", snap.TotalUpload, snap.TotalDownload)
	}
}

func TestDetachedOldTrafficCannotMutateReattachedCandidateEntry(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	handle := mgr.Register(monitor.NodeInfo{Tag: "node"})
	oldState := acquireSharedState("node")
	oldState.attachEntry(handle)
	oldState.addTraffic(100, 200)

	txn := BeginSharedStateTransaction()
	candidateState := acquireSharedState("node")
	candidateState.attachEntry(handle)
	candidateState.addTraffic(1, 2)

	oldState.addTraffic(7, 9)
	if snap := handle.Snapshot(); snap.TotalUpload != 1 || snap.TotalDownload != 2 {
		t.Fatalf("detached old traffic mutated candidate entry: upload=%d download=%d, want 1/2", snap.TotalUpload, snap.TotalDownload)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
}
