package dispatch

import (
	"net/http"
	"testing"
	"time"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"
)

func strp(s string) *string { return &s }

func TestParseTokens_Strategy(t *testing.T) {
	o, ok := parseTokens("stable")
	if !ok || o.Strategy == nil || *o.Strategy != pool.StrategyStable {
		t.Fatalf("stable token not parsed: ok=%v o=%+v", ok, o)
	}
	o, ok = parseTokens("session")
	if !ok || o.Strategy == nil || *o.Strategy != pool.StrategySession {
		t.Fatalf("session token not parsed")
	}
}

func TestParseTokens_Combined(t *testing.T) {
	o, ok := parseTokens("session+us+cc=JP+long+pin=node7+sid=abc+nosplit")
	if !ok {
		t.Fatal("combined tokens not parsed")
	}
	if o.Strategy == nil || *o.Strategy != pool.StrategySession {
		t.Errorf("strategy not session")
	}
	if len(o.Regions) != 1 || o.Regions[0] != "us" {
		t.Errorf("region not parsed: %v", o.Regions)
	}
	if len(o.Countries) != 1 || o.Countries[0] != "JP" {
		t.Errorf("country not parsed: %v", o.Countries)
	}
	if o.LongLived == nil || !*o.LongLived {
		t.Errorf("long not parsed")
	}
	if o.PinnedTag == nil || *o.PinnedTag != "node7" {
		t.Errorf("pin not parsed: %v", o.PinnedTag)
	}
	if o.SessionID == nil || *o.SessionID != "abc" {
		t.Errorf("sid not parsed: %v", o.SessionID)
	}
	if o.Split == nil || *o.Split != false {
		t.Errorf("nosplit not parsed: %v", o.Split)
	}
}

func TestParseTokens_NoMatch(t *testing.T) {
	if _, ok := parseTokens("example.com"); ok {
		t.Error("a bare hostname should not parse as a token prefix")
	}
	if _, ok := parseTokens(""); ok {
		t.Error("empty prefix should not match")
	}
}

func TestParseHeaders(t *testing.T) {
	h := http.Header{}
	h.Set(headerStrategy, "stable")
	h.Set(headerCountry, "us, jp")
	h.Set(headerRegion, "HK,tw")
	h.Set(headerLongLived, "true")
	h.Set(headerPin, "node3")
	h.Set(headerSession, "sess-1")
	h.Set(headerSplit, "off")

	o := parseHeaders(h)
	if o.Strategy == nil || *o.Strategy != pool.StrategyStable {
		t.Errorf("strategy header")
	}
	if len(o.Countries) != 2 || o.Countries[0] != "US" || o.Countries[1] != "JP" {
		t.Errorf("country header upper/split: %v", o.Countries)
	}
	if len(o.Regions) != 2 || o.Regions[0] != "hk" || o.Regions[1] != "tw" {
		t.Errorf("region header lower/split: %v", o.Regions)
	}
	if o.LongLived == nil || !*o.LongLived {
		t.Errorf("longlived header")
	}
	if o.Split == nil || *o.Split != false {
		t.Errorf("split=off should disable: %v", o.Split)
	}
}

func TestOverlayMerge_Priority(t *testing.T) {
	lo := directiveOverlay{Strategy: stratp(pool.StrategyAuto), Countries: []string{"US"}}
	hi := directiveOverlay{Strategy: stratp(pool.StrategyStable)}
	out := lo.merge(hi)
	if out.Strategy == nil || *out.Strategy != pool.StrategyStable {
		t.Errorf("higher-priority strategy should win")
	}
	// Countries only set in lo, preserved when hi doesn't set it.
	if len(out.Countries) != 1 || out.Countries[0] != "US" {
		t.Errorf("lower country should survive: %v", out.Countries)
	}
}

func stratp(s pool.Strategy) *pool.Strategy { return &s }

func TestResolve_SessionFallback(t *testing.T) {
	o := directiveOverlay{Strategy: stratp(pool.StrategySession)}
	res := o.resolve(pool.StrategyStable, "203.0.113.9")
	if res.directive.Strategy != pool.StrategySession {
		t.Fatalf("strategy not session")
	}
	if res.directive.SessionKey != "203.0.113.9" {
		t.Errorf("session key should fall back to client IP, got %q", res.directive.SessionKey)
	}
}

func TestResolve_ExplicitSession(t *testing.T) {
	o := directiveOverlay{Strategy: stratp(pool.StrategySession), SessionID: strp("my-sess")}
	res := o.resolve(pool.StrategyStable, "203.0.113.9")
	if res.directive.SessionKey != "my-sess" {
		t.Errorf("explicit session key should win, got %q", res.directive.SessionKey)
	}
}

func TestResolve_DefaultStrategy(t *testing.T) {
	o := directiveOverlay{} // nothing set
	res := o.resolve(pool.StrategyStable, "1.1.1.1")
	if res.directive.Strategy != pool.StrategyStable {
		t.Errorf("default strategy should apply, got %s", res.directive.Strategy)
	}
	if !res.split {
		t.Errorf("split should default to true")
	}
}

func TestOverlayApplyToPreservesProfileStateAndDeepCopiesFilters(t *testing.T) {
	longLived := true
	baseLongLived := false
	base := pool.SelectionDirective{
		ProfileID:       "device:laptop",
		ProfileRevision: 7,
		Strategy:        pool.StrategySession,
		SessionKey:      "",
		SessionTTL:      15 * time.Minute,
		Filter: pool.NodeFilter{
			Countries: []string{"US"},
			Regions:   []string{"americas"},
			LongLived: &baseLongLived,
		},
		LongLived: pool.LongLivedPolicy{MinUptime: 2 * time.Hour, MinSuccessRate: 0.9},
	}
	overlay := directiveOverlay{
		Strategy:  stratp(pool.StrategyStable),
		Countries: []string{"JP"},
		LongLived: &longLived,
		SessionID: strp("crawl-1"),
		PinnedTag: strp("node-7"),
		Split:     boolp(false),
	}
	resolved := overlay.applyTo(base, "192.0.2.10")
	if resolved.directive.ProfileID != base.ProfileID || resolved.directive.ProfileRevision != base.ProfileRevision {
		t.Fatalf("profile identity changed: %#v", resolved.directive)
	}
	if resolved.directive.SessionTTL != base.SessionTTL || resolved.directive.LongLived != base.LongLived {
		t.Fatalf("profile thresholds changed: %#v", resolved.directive)
	}
	if resolved.directive.Strategy != pool.StrategyStable || resolved.directive.SessionKey != "crawl-1" || resolved.directive.PinnedTag != "node-7" {
		t.Fatalf("overlay fields not applied: %#v", resolved.directive)
	}
	if len(resolved.directive.Filter.Countries) != 1 || resolved.directive.Filter.Countries[0] != "JP" || resolved.directive.Filter.LongLived == nil || !*resolved.directive.Filter.LongLived {
		t.Fatalf("overlay filters not applied: %#v", resolved.directive.Filter)
	}
	if resolved.split {
		t.Fatal("split=false overlay was ignored")
	}
	resolved.directive.Filter.Countries[0] = "DE"
	*resolved.directive.Filter.LongLived = false
	if base.Filter.Countries[0] != "US" || *base.Filter.LongLived {
		t.Fatalf("base profile filters were mutated: %#v", base.Filter)
	}
}

func boolp(value bool) *bool { return &value }

func TestPolicyForSplit(t *testing.T) {
	engine := routerule.New([]string{"DOMAIN-SUFFIX,cn,DIRECT", "FINAL,PROXY"}, routerule.PolicyProxy, nil)

	// Split disabled: always PROXY regardless of rules.
	if p := policyForSplit(false, engine, "example.cn"); p != routerule.PolicyProxy {
		t.Errorf("nosplit should force PROXY, got %s", p)
	}
	// Split enabled: rules consulted.
	if p := policyForSplit(true, engine, "example.cn"); p != routerule.PolicyDirect {
		t.Errorf("split should consult rules (DIRECT), got %s", p)
	}
	if p := policyForSplit(true, engine, "example.org"); p != routerule.PolicyProxy {
		t.Errorf("split FINAL PROXY, got %s", p)
	}
	// Nil engine + split: defaults to PROXY.
	if p := policyForSplit(true, nil, "example.cn"); p != routerule.PolicyProxy {
		t.Errorf("nil engine should be PROXY, got %s", p)
	}
}

func TestSplitConnectAuthority(t *testing.T) {
	overlay, authority := splitConnectAuthority("stable+us/example.com:443")
	if authority != "example.com:443" {
		t.Errorf("authority = %q", authority)
	}
	if overlay.Strategy == nil || *overlay.Strategy != pool.StrategyStable {
		t.Errorf("overlay strategy not parsed from CONNECT authority")
	}

	// No prefix: authority unchanged, empty overlay.
	overlay, authority = splitConnectAuthority("example.com:443")
	if authority != "example.com:443" {
		t.Errorf("plain authority changed: %q", authority)
	}
	if overlay.Strategy != nil {
		t.Errorf("plain authority should yield empty overlay")
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in          string
		defaultPort uint16
		host        string
		port        uint16
	}{
		{"example.com:443", 80, "example.com", 443},
		{"example.com", 80, "example.com", 80},
		{"[::1]:8080", 80, "::1", 8080},
		{"[2001:db8::1]", 443, "2001:db8::1", 443},
	}
	for _, c := range cases {
		h, p := splitHostPort(c.in, c.defaultPort)
		if h != c.host || p != c.port {
			t.Errorf("splitHostPort(%q,%d) = (%q,%d), want (%q,%d)", c.in, c.defaultPort, h, p, c.host, c.port)
		}
	}
}

func TestClientIP(t *testing.T) {
	if got := clientIP("203.0.113.5:51234"); got != "203.0.113.5" {
		t.Errorf("clientIP = %q", got)
	}
	if got := clientIP("203.0.113.5"); got != "203.0.113.5" {
		t.Errorf("clientIP no-port = %q", got)
	}
}
