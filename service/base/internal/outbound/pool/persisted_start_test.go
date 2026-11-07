package pool

import (
	"context"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

type persistedOutboundManager struct{ adapter.OutboundManager }

func (persistedOutboundManager) Outbound(string) (adapter.Outbound, bool) {
	return directProbeOutbound{}, true
}

func persistedStartPool(t *testing.T) (*poolOutbound, *monitor.Manager, *sharedMemberState) {
	t.Helper()
	ResetSharedStateStore()
	t.Cleanup(ResetSharedStateStore)
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Stop)
	ctx := service.ContextWith[adapter.OutboundManager](context.Background(), persistedOutboundManager{})
	ctx = monitor.ContextWith(ctx, mgr)
	raw, err := newPool(ctx, nil, log.NewNOPFactory().Logger(), "restore-pool", Options{
		Members: []string{"restored", "healthy"},
		Metadata: map[string]MemberMeta{
			"restored": {URI: "test://restored"},
			"healthy":  {URI: "test://healthy"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	acquireSharedState("healthy").entryHandle().MarkInitialCheckDone(true)
	return raw.(*poolOutbound), mgr, acquireSharedState("restored")
}

func restoreStartState(t *testing.T, mgr *monitor.Manager, state monitor.PersistedState) {
	t.Helper()
	if !mgr.RestorePersistedState("test://restored", "", state) {
		t.Fatal("registered entry was not restored")
	}
}

func startRestoredPool(t *testing.T, p *poolOutbound) {
	t.Helper()
	if err := p.Start(adapter.StartStateStart); err != nil {
		t.Fatal(err)
	}
}

func TestPoolStartHydratesPersistedBlacklist(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		t.Run(map[bool]string{false: "constructor", true: "already-lazy-initialized"}[lazy], func(t *testing.T) {
			p, mgr, shared := persistedStartPool(t)
			if lazy {
				if err := p.initializeMembersLocked(); err != nil {
					t.Fatal(err)
				}
			}
			until := time.Now().Add(time.Hour)
			restoreStartState(t, mgr, monitor.PersistedState{
				Blacklisted: true, BlacklistedUntil: until, Available: true, InitialCheckDone: true,
			})
			startRestoredPool(t, p)
			if !shared.isTransportBlacklisted(N.NetworkTCP, time.Now()) {
				t.Error("persisted monitor blacklist did not reach runtime selection")
			}
			if shared.isTransportBlacklisted(N.NetworkUDP, time.Now()) {
				t.Error("TCP persisted blacklist leaked into UDP state")
			}
			_, err := p.pickMember(N.NetworkTCP, nil, &SelectionDirective{RequiredTag: "restored"})
			if err == nil {
				t.Error("runtime selected persisted-blacklisted node while another healthy node exists")
			}
			if snap := shared.entryHandle().Snapshot(); !snap.Blacklisted || !snap.BlacklistedUntil.Equal(until) {
				t.Errorf("startup lost persisted monitor blacklist: %+v", snap)
			}
		})
	}
}

func TestPoolStartHydratesPersistedTrafficBeforeFurtherAccounting(t *testing.T) {
	p, mgr, shared := persistedStartPool(t)
	restoreStartState(t, mgr, monitor.PersistedState{
		TotalUpload: 100, TotalDownload: 200, FailureCount: 100,
	})
	shared.incActive()
	startRestoredPool(t, p)
	shared.addTraffic(3, 7)
	if got := shared.entryHandle().Snapshot(); got.TotalUpload != 103 || got.TotalDownload != 207 || got.ActiveConnections != 1 {
		t.Fatalf("restored counters overwritten by next traffic event: %+v", got)
	}
	if got := shared.totalUpload.Load(); got != 103 {
		t.Errorf("shared upload=%d, want 103", got)
	}
	if got := shared.totalDownload.Load(); got != 207 {
		t.Errorf("shared download=%d, want 207", got)
	}
	if _, blacklisted, _ := shared.recordFailure(context.DeadlineExceeded, 3, time.Hour, "example.test:443"); blacklisted {
		t.Error("cumulative persisted failures were imported as consecutive route failures")
	}
}

func TestPoolStartPersistedBlacklistDeadlineSemantics(t *testing.T) {
	for _, tc := range []struct {
		name  string
		until time.Time
		want  bool
	}{
		{"expired", time.Now().Add(-time.Hour), false},
		{"no-deadline", time.Time{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, mgr, shared := persistedStartPool(t)
			restoreStartState(t, mgr, monitor.PersistedState{Blacklisted: true, BlacklistedUntil: tc.until})
			startRestoredPool(t, p)
			if got := shared.isBlacklisted(time.Now()); got != tc.want {
				t.Errorf("runtime blacklist=%v, want %v", got, tc.want)
			}
			if got := shared.entryHandle().Snapshot().Blacklisted; got != tc.want {
				t.Errorf("monitor blacklist=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestPoolStartHydrationPreservesAuthenticationQuarantine(t *testing.T) {
	p, mgr, shared := persistedStartPool(t)
	restoreStartState(t, mgr, monitor.PersistedState{Available: true, InitialCheckDone: true})
	shared.entryHandle().RecordFailure(monitor.ErrUpstreamAuthenticationRequired, "example.test:443")
	startRestoredPool(t, p)
	shared.forceReleaseTransport(N.NetworkTCP)
	if !shared.entryHandle().UpstreamAuthFailed() {
		t.Fatal("hydration cleared authentication quarantine")
	}
	if _, err := p.pickMember(N.NetworkTCP, nil, &SelectionDirective{RequiredTag: "restored"}); err == nil {
		t.Fatal("hydrated authentication-quarantined node was selected")
	}
}
