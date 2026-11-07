package pool

import (
	"context"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

func TestPoolStartHydrationKeepsTransportAndControlBoundaries(t *testing.T) {
	p, mgr, shared := persistedStartPool(t)
	restoreStartState(t, mgr, monitor.PersistedState{TotalUpload: 11, TotalDownload: 22})
	shared.recordTransportFailure(N.NetworkUDP, context.DeadlineExceeded, 1, time.Hour, "example.test:53")
	shared.incActive()
	if err := p.Start(adapter.StartStateInitialize); err != nil {
		t.Fatal(err)
	}
	if shared.totalUpload.Load() != 0 {
		t.Fatal("non-start lifecycle stage hydrated monitor state")
	}
	startRestoredPool(t, p)
	if !shared.isTransportBlacklisted(N.NetworkUDP, time.Now()) || shared.activeCount() != 1 {
		t.Fatal("startup hydration changed UDP blacklist or active connections")
	}
	if shared.isTransportBlacklisted(N.NetworkTCP, time.Now()) {
		t.Fatal("UDP failure was imported as a TCP blacklist")
	}
}

func TestPoolStartHydrationRollbackRetainsOldRegistry(t *testing.T) {
	p, _, old := persistedStartPool(t)
	startRestoredPool(t, p)
	old.addTraffic(11, 22)
	_, _, oldUntil := old.recordFailure(context.DeadlineExceeded, 1, time.Hour, "example.test:443")
	handle := old.entryHandle()
	txn := BeginSharedStateTransaction()
	t.Cleanup(func() { _ = txn.Rollback() })
	candidate := acquireSharedState("restored")
	candidate.attachEntry(handle)
	candidate.hydrateFromMonitor()
	candidate.addTraffic(100, 200)
	candidate.recordFailure(context.DeadlineExceeded, 1, 2*time.Hour, "example.test:443")
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
	if acquireSharedState("restored") != old {
		t.Fatal("rollback lost original shared-state identity")
	}
	old.attachEntry(handle)
	old.hydrateFromMonitor()
	if snap := handle.Snapshot(); snap.TotalUpload != 11 || snap.TotalDownload != 22 || !snap.Blacklisted || !snap.BlacklistedUntil.Equal(oldUntil) {
		t.Fatalf("startup hydration mixed candidate state into rollback: %+v", snap)
	}
	if old.totalUpload.Load() != 11 || old.totalDownload.Load() != 22 || !old.isBlacklisted(time.Now()) {
		t.Fatal("rollback shared counters or blacklist changed")
	}
}

func TestPoolStartHydrationSerializesSharedAccounting(t *testing.T) {
	p, mgr, shared := persistedStartPool(t)
	restoreStartState(t, mgr, monitor.PersistedState{TotalUpload: 11, TotalDownload: 22})
	startRestoredPool(t, p)
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				shared.hydrateFromMonitor()
				shared.addTraffic(1, 2)
				shared.recordFailure(context.DeadlineExceeded, 3, time.Hour, "example.test:443")
				shared.forceReleaseTransport(N.NetworkTCP)
			}
		}()
	}
	wg.Wait()
	shared.hydrateFromMonitor()
	if snap := shared.entryHandle().Snapshot(); snap.TotalUpload != 311 || snap.TotalDownload != 622 || snap.Blacklisted {
		t.Fatalf("concurrent hydration lost accounting or release: %+v", snap)
	}
	if shared.totalUpload.Load() != 311 || shared.totalDownload.Load() != 622 || shared.isBlacklisted(time.Now()) {
		t.Fatal("concurrent shared state diverged from monitor")
	}
}

func TestPoolStartHydrationWithoutMonitorIsNoop(t *testing.T) {
	var absent *sharedMemberState
	absent.hydrateFromMonitor()
	shared := &sharedMemberState{blacklisted: true, failures: 2}
	shared.totalUpload.Store(11)
	shared.hydrateFromMonitor()
	if !shared.blacklisted || shared.failures != 2 || shared.totalUpload.Load() != 11 {
		t.Fatal("missing monitor destroyed existing shared state")
	}
}
