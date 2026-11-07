package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/log"
)

func recoveryPool(t *testing.T) (*poolOutbound, func(string, bool, bool) *memberState) {
	t.Helper()
	ResetSharedStateStore()
	t.Cleanup(ResetSharedStateStore)
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Stop)
	p := &poolOutbound{logger: log.NewNOPFactory().Logger(), monitor: mgr}
	add := func(tag string, authFailed, blacklisted bool) *memberState {
		entry := mgr.Register(monitor.NodeInfo{Tag: t.Name() + "/" + tag})
		entry.MarkInitialCheckDone(true)
		shared := acquireSharedState(t.Name() + "/" + tag)
		shared.attachEntry(entry)
		if blacklisted {
			shared.recordFailure(context.DeadlineExceeded, 1, 24*time.Hour, "example.com:443")
		}
		if authFailed {
			entry.RecordFailure(monitor.ErrUpstreamAuthenticationRequired, "example.com:443")
		}
		member := &memberState{tag: tag, entry: entry, shared: shared, outbound: authHTTPOutbound{}}
		p.members = append(p.members, member)
		return member
	}
	return p, add
}

func TestAuthQuarantineDoesNotBlockOrdinaryBlacklistRecovery(t *testing.T) {
	for _, authBlacklisted := range []bool{false, true} {
		t.Run(map[bool]string{false: "quarantine-only", true: "quarantine-and-blacklist"}[authBlacklisted], func(t *testing.T) {
			p, add := recoveryPool(t)
			blocked := add("auth", true, authBlacklisted)
			recoverable := add("recoverable", false, true)
			// Match production: a fresh successful probe does not clear a 24h
			// transport blacklist, so the existing all-blacklisted fallback is needed.
			recoverable.entry.SetProbe(func(context.Context) (time.Duration, error) { return time.Millisecond, nil })
			if _, err := p.monitor.Probe(context.Background(), recoverable.entry.Snapshot().Tag); err != nil {
				t.Fatal(err)
			}
			if !recoverable.entry.Snapshot().Available || recoverable.entry.Snapshot().EffectiveAvailable {
				t.Fatal("fixture must retain probe availability behind the transport blacklist")
			}
			selected, err := p.pickMember("tcp", nil, nil)
			if err != nil || selected != recoverable {
				t.Fatalf("quarantined member blocked ordinary recovery: selected=%v err=%v", selected, err)
			}
			if recoverable.shared.isBlacklisted(time.Now()) {
				t.Error("recoverable transport remained blacklisted")
			}
			if !blocked.entry.UpstreamAuthFailed() || blocked.entry.Snapshot().EffectiveAvailable {
				t.Error("ordinary recovery bypassed authentication quarantine")
			}
			if blocked.shared.isBlacklisted(time.Now()) != authBlacklisted {
				t.Error("recovery changed quarantined member's independent blacklist")
			}
		})
	}
}

func TestAllAuthQuarantinedMembersStayUnavailable(t *testing.T) {
	p, add := recoveryPool(t)
	first := add("auth-with-blacklist", true, true)
	add("auth-without-blacklist", true, false)
	if _, err := p.pickMember("tcp", nil, nil); err == nil {
		t.Fatal("all-auth-quarantined pool became selectable")
	}
	if p.releaseIfAllBlacklistedLocked(time.Now(), "tcp") {
		t.Fatal("empty non-quarantined set triggered recovery")
	}
	if !first.shared.isBlacklisted(time.Now()) {
		t.Fatal("all-auth-quarantined pool had its transport blacklist cleared")
	}
}

func TestAuthAwareRecoveryPreservesExistingRecoveryBoundaries(t *testing.T) {
	t.Run("healthy-member", func(t *testing.T) {
		p, add := recoveryPool(t)
		add("auth", true, false)
		blocked := add("blocked", false, true)
		healthy := add("healthy", false, false)
		if selected, err := p.pickMember("tcp", nil, nil); err != nil || selected != healthy {
			t.Fatalf("healthy member not preferred: selected=%v err=%v", selected, err)
		}
		if !blocked.shared.isBlacklisted(time.Now()) {
			t.Fatal("healthy candidate caused unnecessary blacklist release")
		}
	})
	t.Run("request-exclusions", func(t *testing.T) {
		p, add := recoveryPool(t)
		add("auth", true, false)
		blocked := add("blocked", false, true)
		if _, err := p.pickMember("tcp", map[string]struct{}{"blocked": {}}, nil); err == nil {
			t.Fatal("request exclusion was bypassed")
		}
		if !blocked.shared.isBlacklisted(time.Now()) {
			t.Fatal("excluded retry triggered recovery")
		}
	})
	t.Run("udp", func(t *testing.T) {
		p, add := recoveryPool(t)
		add("auth", true, false)
		blocked := add("blocked", false, false)
		blocked.shared.recordTransportFailure("udp", errors.New("UDP unavailable"), 1, time.Hour, "example.com:443")
		if p.releaseIfAllBlacklistedLocked(time.Now(), "udp") {
			t.Fatal("UDP triggered TCP-style fail-open")
		}
		if !blocked.shared.isTransportBlacklisted("udp", time.Now()) {
			t.Fatal("UDP blacklist was cleared")
		}
	})
}
