package routerule

import (
	"net/netip"
	"testing"
)

// fakeGeo implements CountryLookup for GEOIP rule tests.
type fakeGeo map[string]string // ip string -> ISO code

func (f fakeGeo) CountryISO(ip netip.Addr) string {
	return f[ip.String()]
}

func TestMatch_DomainSuffix(t *testing.T) {
	e := New([]string{
		"DOMAIN-SUFFIX,google.com,PROXY",
		"DOMAIN-SUFFIX,cn,DIRECT",
		"FINAL,PROXY",
	}, PolicyProxy, nil)

	cases := []struct {
		host string
		want Policy
	}{
		{"www.google.com", PolicyProxy},
		{"google.com", PolicyProxy},
		{"notgoogle.com", PolicyProxy}, // FINAL, suffix must be dot-bounded
		{"example.cn", PolicyDirect},
		{"cn", PolicyDirect},
		{"example.org", PolicyProxy}, // FINAL
	}
	for _, c := range cases {
		if got := e.Match(c.host); got != c.want {
			t.Errorf("Match(%q) = %s, want %s", c.host, got, c.want)
		}
	}
}

func TestMatch_SuffixNotSubstring(t *testing.T) {
	e := New([]string{"DOMAIN-SUFFIX,baidu.com,DIRECT", "FINAL,PROXY"}, PolicyProxy, nil)
	// "xbaidu.com" must NOT match "baidu.com" suffix.
	if got := e.Match("xbaidu.com"); got != PolicyProxy {
		t.Errorf("xbaidu.com leaked to DIRECT: got %s", got)
	}
	if got := e.Match("a.baidu.com"); got != PolicyDirect {
		t.Errorf("a.baidu.com should be DIRECT: got %s", got)
	}
}

func TestMatch_Keyword(t *testing.T) {
	e := New([]string{"DOMAIN-KEYWORD,google,PROXY", "FINAL,DIRECT"}, PolicyDirect, nil)
	if got := e.Match("scholar.google.co.jp"); got != PolicyProxy {
		t.Errorf("keyword match failed: got %s", got)
	}
	if got := e.Match("example.com"); got != PolicyDirect {
		t.Errorf("non-match should be FINAL DIRECT: got %s", got)
	}
}

func TestMatch_ExactDomain(t *testing.T) {
	e := New([]string{"DOMAIN,api.example.com,PROXY", "FINAL,DIRECT"}, PolicyDirect, nil)
	if got := e.Match("api.example.com"); got != PolicyProxy {
		t.Errorf("exact match failed: got %s", got)
	}
	if got := e.Match("x.api.example.com"); got != PolicyDirect {
		t.Errorf("exact rule must not match subdomain: got %s", got)
	}
}

func TestMatch_IPCIDR(t *testing.T) {
	e := New([]string{
		"IP-CIDR,10.0.0.0/8,DIRECT",
		"IP-CIDR,192.168.0.0/16,DIRECT",
		"FINAL,PROXY",
	}, PolicyProxy, nil)
	if got := e.Match("10.1.2.3"); got != PolicyDirect {
		t.Errorf("10.1.2.3 should be DIRECT: got %s", got)
	}
	if got := e.Match("192.168.1.1:443"); got != PolicyDirect {
		t.Errorf("192.168.1.1 (with port) should be DIRECT: got %s", got)
	}
	if got := e.Match("8.8.8.8"); got != PolicyProxy {
		t.Errorf("8.8.8.8 should be PROXY: got %s", got)
	}
}

func TestMatch_GeoIP(t *testing.T) {
	geo := fakeGeo{
		"1.2.3.4":  "CN",
		"8.8.8.8":  "US",
		"9.9.9.9":  "US",
	}
	e := New([]string{
		"GEOIP,CN,DIRECT",
		"FINAL,PROXY",
	}, PolicyProxy, geo)

	if got := e.Match("1.2.3.4"); got != PolicyDirect {
		t.Errorf("CN IP should be DIRECT: got %s", got)
	}
	if got := e.Match("8.8.8.8"); got != PolicyProxy {
		t.Errorf("US IP should be PROXY: got %s", got)
	}
	// GEOIP must not apply to domains (no per-request DNS), falls to FINAL.
	if got := e.Match("example.com"); got != PolicyProxy {
		t.Errorf("domain should fall to FINAL PROXY: got %s", got)
	}
}

func TestMatch_GeoIPNilLookup(t *testing.T) {
	e := New([]string{"GEOIP,CN,DIRECT", "FINAL,PROXY"}, PolicyProxy, nil)
	// No geo lookup: GEOIP never matches, falls through to FINAL.
	if got := e.Match("1.2.3.4"); got != PolicyProxy {
		t.Errorf("with nil geo, should fall to FINAL: got %s", got)
	}
}

func TestMatch_RuleOrderPriority(t *testing.T) {
	// Earlier rules win: a custom PROXY override before the cn DIRECT rule.
	e := New([]string{
		"DOMAIN-SUFFIX,vpn.example.cn,PROXY",
		"DOMAIN-SUFFIX,cn,DIRECT",
		"FINAL,PROXY",
	}, PolicyProxy, nil)
	if got := e.Match("vpn.example.cn"); got != PolicyProxy {
		t.Errorf("earlier rule should win: got %s", got)
	}
	if got := e.Match("other.cn"); got != PolicyDirect {
		t.Errorf("later cn rule applies: got %s", got)
	}
}

func TestMatch_FinalDefault(t *testing.T) {
	// No FINAL rule line: the constructor's final argument applies.
	e := New([]string{"DOMAIN-SUFFIX,cn,DIRECT"}, PolicyDirect, nil)
	if got := e.Match("example.org"); got != PolicyDirect {
		t.Errorf("constructor final should apply: got %s", got)
	}
}

func TestDefaultRules_ChinaDirect(t *testing.T) {
	e := New(DefaultRules(), PolicyProxy, fakeGeo{"114.114.114.114": "CN", "8.8.8.8": "US"})

	directHosts := []string{
		"www.baidu.com",
		"taobao.com",
		"music.163.com",
		"192.168.1.1",
		"127.0.0.1",
		"10.0.0.5",
		"example.cn",
		"114.114.114.114", // GEOIP CN
	}
	for _, h := range directHosts {
		if got := e.Match(h); got != PolicyDirect {
			t.Errorf("default rules: %q should be DIRECT, got %s", h, got)
		}
	}

	proxyHosts := []string{
		"www.google.com",
		"youtube.com",
		"8.8.8.8", // GEOIP US
		"example.org",
	}
	for _, h := range proxyHosts {
		if got := e.Match(h); got != PolicyProxy {
			t.Errorf("default rules: %q should be PROXY, got %s", h, got)
		}
	}
}

func TestParseRule_Malformed(t *testing.T) {
	bad := []string{
		"",
		"# comment",
		"// comment",
		"GARBAGE",
		"DOMAIN-SUFFIX",        // missing value+policy
		"DOMAIN-SUFFIX,cn",     // missing policy
		"IP-CIDR,not-a-cidr,DIRECT",
	}
	for _, line := range bad {
		if _, ok := parseRule(line); ok {
			t.Errorf("parseRule(%q) should fail", line)
		}
	}
	// Malformed lines are skipped by SetRules, leaving only valid ones.
	e := New(append(bad, "DOMAIN-SUFFIX,ok.com,PROXY"), PolicyDirect, nil)
	if e.RuleCount() != 1 {
		t.Errorf("expected 1 valid rule, got %d", e.RuleCount())
	}
}

func TestSetRules_Reload(t *testing.T) {
	e := New([]string{"DOMAIN-SUFFIX,a.com,PROXY", "FINAL,DIRECT"}, PolicyDirect, nil)
	if got := e.Match("a.com"); got != PolicyProxy {
		t.Fatalf("initial: got %s", got)
	}
	e.SetRules([]string{"DOMAIN-SUFFIX,b.com,PROXY", "FINAL,DIRECT"})
	if got := e.Match("a.com"); got != PolicyDirect {
		t.Errorf("after reload a.com should be FINAL DIRECT: got %s", got)
	}
	if got := e.Match("b.com"); got != PolicyProxy {
		t.Errorf("after reload b.com should be PROXY: got %s", got)
	}
}
