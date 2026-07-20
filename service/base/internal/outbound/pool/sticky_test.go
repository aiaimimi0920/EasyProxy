package pool

import (
	"testing"
	"time"
)

func boolp(b bool) *bool { return &b }

func TestNodeFilter_IsZero(t *testing.T) {
	if !(NodeFilter{}).IsZero() {
		t.Error("empty filter should be zero")
	}
	if (NodeFilter{Countries: []string{"US"}}).IsZero() {
		t.Error("country filter is not zero")
	}
	if (NodeFilter{LongLived: boolp(true)}).IsZero() {
		t.Error("longlived filter is not zero")
	}
}

func TestNodeFilter_BucketKeyStable(t *testing.T) {
	// Same logical filter, different input ordering/casing → same bucket key.
	a := NodeFilter{Countries: []string{"us", "JP"}, Regions: []string{"hk"}}
	b := NodeFilter{Countries: []string{"JP", "US"}, Regions: []string{"HK"}}
	if a.bucketKey() != b.bucketKey() {
		t.Errorf("equivalent filters should share bucket key: %s vs %s", a.bucketKey(), b.bucketKey())
	}

	// Different long-lived constraint → different bucket.
	c := NodeFilter{Countries: []string{"US"}, LongLived: boolp(true)}
	d := NodeFilter{Countries: []string{"US"}, LongLived: boolp(false)}
	if c.bucketKey() == d.bucketKey() {
		t.Error("differing long-lived constraint should change bucket key")
	}

	// Empty filter has a stable, non-empty key.
	if (NodeFilter{}).bucketKey() == "" {
		t.Error("zero filter should still produce a key")
	}
}

func TestNormalizeStrategy(t *testing.T) {
	cases := map[string]Strategy{
		"stable":  StrategyStable,
		"STABLE":  StrategyStable,
		"session": StrategySession,
		"auto":    StrategyAuto,
		"":        StrategyAuto,
		"bogus":   StrategyAuto,
	}
	for in, want := range cases {
		if got := NormalizeStrategy(in); got != want {
			t.Errorf("NormalizeStrategy(%q) = %s, want %s", in, got, want)
		}
	}
}

// fakeMember builds a memberState with just a tag for sticky tests.
func fakeMember(tag string) *memberState { return &memberState{tag: tag} }

func TestStickyState_StablePinAndPromote(t *testing.T) {
	s := newStickyState(0)
	bucket := "b1"

	m1, m2 := fakeMember("n1"), fakeMember("n2")
	candidates := []*memberState{m1, m2}

	// First pick promotes the fallback (best) and remembers it.
	got := s.pickStable(bucket, candidates, m1)
	if got.tag != "n1" {
		t.Fatalf("first stable pick = %s, want n1", got.tag)
	}
	// Subsequent pick reuses n1 even if a different fallback is offered.
	got = s.pickStable(bucket, candidates, m2)
	if got.tag != "n1" {
		t.Errorf("stable should reuse pinned n1, got %s", got.tag)
	}

	// n1 dies (drops out of candidates): bucket promotes to next available.
	got = s.pickStable(bucket, []*memberState{m2}, m2)
	if got.tag != "n2" {
		t.Errorf("stable should promote to n2 after n1 gone, got %s", got.tag)
	}
	// And now n2 is the pinned one.
	got = s.pickStable(bucket, []*memberState{m2}, m2)
	if got.tag != "n2" {
		t.Errorf("stable should stay on n2, got %s", got.tag)
	}
}

func TestStickyState_SessionBindingAndRebind(t *testing.T) {
	s := newStickyState(0)
	m1, m2 := fakeMember("n1"), fakeMember("n2")
	all := []*memberState{m1, m2}

	got := s.pickSession("sessA", 0, all, m1)
	if got.tag != "n1" {
		t.Fatalf("first session pick = %s", got.tag)
	}
	// Same session reuses n1 even when fallback differs.
	got = s.pickSession("sessA", 0, all, m2)
	if got.tag != "n1" {
		t.Errorf("session should reuse n1, got %s", got.tag)
	}
	// Different session can bind to a different node.
	got = s.pickSession("sessB", 0, all, m2)
	if got.tag != "n2" {
		t.Errorf("sessB should bind n2, got %s", got.tag)
	}
	// n1 dies for sessA → rebind to fallback.
	got = s.pickSession("sessA", 0, []*memberState{m2}, m2)
	if got.tag != "n2" {
		t.Errorf("sessA should rebind to n2 after n1 gone, got %s", got.tag)
	}
}

func TestStickyState_EmptySessionKeyNoStick(t *testing.T) {
	s := newStickyState(0)
	m1, m2 := fakeMember("n1"), fakeMember("n2")

	// Empty key: always returns fallback, never stored.
	if got := s.pickSession("", 0, []*memberState{m1, m2}, m1); got.tag != "n1" {
		t.Errorf("empty-key session returns fallback n1, got %s", got.tag)
	}
	if got := s.pickSession("", 0, []*memberState{m1, m2}, m2); got.tag != "n2" {
		t.Errorf("empty-key session returns new fallback n2, got %s", got.tag)
	}
	if len(s.sessions) != 0 {
		t.Errorf("empty-key sessions must not be stored, have %d", len(s.sessions))
	}
}

func TestStickyState_PruneTags(t *testing.T) {
	s := newStickyState(0)
	m1, m2 := fakeMember("n1"), fakeMember("n2")
	s.pickStable("b1", []*memberState{m1}, m1)
	s.pickSession("sess", 0, []*memberState{m2}, m2)

	// Reload leaves only n2 alive.
	s.pruneTags(map[string]struct{}{"n2": {}})

	if _, ok := s.buckets["b1"]; ok {
		t.Error("stale stable bucket pinned to n1 should be pruned")
	}
	if _, ok := s.sessions["sess"]; !ok {
		t.Error("session pinned to live n2 should survive prune")
	}
}

func TestCandidateByTag(t *testing.T) {
	m1, m2 := fakeMember("n1"), fakeMember("n2")
	cands := []*memberState{m1, m2}
	if candidateByTag(cands, "n2") != m2 {
		t.Error("should find n2")
	}
	if candidateByTag(cands, "nope") != nil {
		t.Error("missing tag should be nil")
	}
	if candidateByTag(cands, "") != nil {
		t.Error("empty tag should be nil")
	}
}

func TestSessionBindingsAreNamespacedAndUseDirectiveTTL(t *testing.T) {
	state := newStickyState(defaultSessionTTL)
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }

	a := &memberState{tag: "a"}
	b := &memberState{tag: "b"}
	profileA := SelectionDirective{ProfileID: "profile-a", ProfileRevision: 1, SessionKey: "job", SessionTTL: 20 * time.Millisecond}
	profileB := SelectionDirective{ProfileID: "profile-b", ProfileRevision: 1, SessionKey: "job", SessionTTL: time.Hour}

	if got := state.pickSession(profileA.namespacedSessionKey(), profileA.SessionTTL, []*memberState{a}, a); got != a {
		t.Fatal("profile-a did not bind a")
	}
	if got := state.pickSession(profileB.namespacedSessionKey(), profileB.SessionTTL, []*memberState{b}, b); got != b {
		t.Fatal("profile-b did not bind b")
	}

	now = now.Add(30 * time.Millisecond)
	if got := state.pickSession(profileA.namespacedSessionKey(), profileA.SessionTTL, []*memberState{b}, b); got != b {
		t.Fatal("short TTL binding did not expire")
	}
}

func TestAffinityNamespaceIncludesProfileRevision(t *testing.T) {
	first := SelectionDirective{ProfileID: "device:laptop", ProfileRevision: 1, SessionKey: "job"}
	second := SelectionDirective{ProfileID: "device:laptop", ProfileRevision: 2, SessionKey: "job"}
	if first.namespacedSessionKey() == second.namespacedSessionKey() || first.stableBucketKey() == second.stableBucketKey() {
		t.Fatal("profile revision reused obsolete affinity namespace")
	}
}
