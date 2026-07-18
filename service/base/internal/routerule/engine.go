// Package routerule implements Clash/mihomo-style traffic splitting: it decides
// whether a destination should be reached directly (DIRECT) or through the proxy
// pool (PROXY) based on an ordered rule list.
//
// The engine is intentionally lightweight and allocation-free on the hot path.
// Domain rules are matched via a suffix map / keyword scan, IP rules via netip
// prefix containment, and GEOIP rules via an injected country lookup. Per the
// design, GEOIP rules only apply when the destination is already a literal IP
// (no per-request DNS resolution), so domain destinations fall through to the
// domain rules and the FINAL policy.
package routerule

import (
	"net/netip"
	"strings"
	"sync"
)

// Policy is the routing decision for a destination.
type Policy string

const (
	// PolicyDirect sends traffic straight out via a direct dialer.
	PolicyDirect Policy = "DIRECT"
	// PolicyProxy sends traffic through the proxy pool.
	PolicyProxy Policy = "PROXY"
)

// NormalizePolicy maps a string to a known Policy, defaulting to PolicyProxy.
func NormalizePolicy(value string) Policy {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(PolicyDirect):
		return PolicyDirect
	case string(PolicyProxy):
		return PolicyProxy
	default:
		return PolicyProxy
	}
}

// CountryLookup resolves the ISO country code (upper-case, e.g. "CN") for a
// literal IP address. Implementations should return "" when unknown.
type CountryLookup interface {
	CountryISO(ip netip.Addr) string
}

// ruleKind enumerates the supported rule matchers.
type ruleKind int

const (
	kindDomainSuffix ruleKind = iota
	kindDomainKeyword
	kindDomain
	kindIPCIDR
	kindGeoIP
	kindFinal
)

type rule struct {
	kind    ruleKind
	value   string     // domain / keyword / ISO code (upper for geoip)
	prefix  netip.Prefix // for kindIPCIDR
	policy  Policy
}

// Engine evaluates an ordered rule list against destinations.
type Engine struct {
	mu        sync.RWMutex
	rules     []rule
	final     Policy
	geo       CountryLookup
	geoUsed   bool // whether any GEOIP rule exists (skip lookup work otherwise)
}

// New builds an engine from rule strings plus a fallback FINAL policy. Unknown
// or malformed rule lines are skipped. The geo lookup may be nil; in that case
// GEOIP rules never match and the engine falls through to FINAL.
func New(ruleLines []string, final Policy, geo CountryLookup) *Engine {
	e := &Engine{final: final, geo: geo}
	e.SetRules(ruleLines)
	return e
}

// SetRules replaces the rule list atomically (used for live reloads).
func (e *Engine) SetRules(ruleLines []string) {
	parsed, geoUsed, finalOverride := parseRules(ruleLines)
	e.mu.Lock()
	e.rules = parsed
	e.geoUsed = geoUsed
	if finalOverride != "" {
		e.final = finalOverride
	}
	e.mu.Unlock()
}

// SetRulesAndFinal replaces the rule list and authoritative fallback policy in
// one atomic update. FINAL entries in ruleLines are parsed for compatibility
// but cannot override final.
func (e *Engine) SetRulesAndFinal(ruleLines []string, final Policy) {
	parsed, geoUsed, _ := parseRules(ruleLines)
	e.mu.Lock()
	e.rules = parsed
	e.geoUsed = geoUsed
	e.final = NormalizePolicy(string(final))
	e.mu.Unlock()
}

func parseRules(ruleLines []string) ([]rule, bool, Policy) {
	parsed := make([]rule, 0, len(ruleLines))
	geoUsed := false
	finalOverride := Policy("")
	for _, line := range ruleLines {
		r, ok := parseRule(line)
		if !ok {
			continue
		}
		if r.kind == kindFinal {
			finalOverride = r.policy
			continue
		}
		if r.kind == kindGeoIP {
			geoUsed = true
		}
		parsed = append(parsed, r)
	}
	return parsed, geoUsed, finalOverride
}

// SetGeoLookup swaps the country lookup (e.g. after the GeoIP DB is loaded).
func (e *Engine) SetGeoLookup(geo CountryLookup) {
	e.mu.Lock()
	e.geo = geo
	e.mu.Unlock()
}

// SetFinal swaps the fallback FINAL policy applied when no rule matches.
func (e *Engine) SetFinal(final Policy) {
	e.mu.Lock()
	e.final = final
	e.mu.Unlock()
}

// Match returns the routing policy for a destination host. The host may be a
// domain name or a literal IP (with or without brackets for IPv6). Matching is
// case-insensitive for domains.
func (e *Engine) Match(host string) Policy {
	host = normalizeHost(host)
	if host == "" {
		e.mu.RLock()
		f := e.final
		e.mu.RUnlock()
		return f
	}

	var addr netip.Addr
	isIP := false
	if a, err := netip.ParseAddr(host); err == nil {
		addr = a
		isIP = true
	}
	lowerHost := strings.ToLower(host)

	e.mu.RLock()
	defer e.mu.RUnlock()

	for i := range e.rules {
		r := &e.rules[i]
		switch r.kind {
		case kindDomainSuffix:
			if !isIP && matchDomainSuffix(lowerHost, r.value) {
				return r.policy
			}
		case kindDomainKeyword:
			if !isIP && strings.Contains(lowerHost, r.value) {
				return r.policy
			}
		case kindDomain:
			if !isIP && lowerHost == r.value {
				return r.policy
			}
		case kindIPCIDR:
			if isIP && r.prefix.Contains(addr) {
				return r.policy
			}
		case kindGeoIP:
			if isIP && e.geo != nil {
				if iso := e.geo.CountryISO(addr); iso != "" && strings.EqualFold(iso, r.value) {
					return r.policy
				}
			}
		}
	}
	return e.final
}

// Final returns the configured fallback policy.
func (e *Engine) Final() Policy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.final
}

// RuleCount returns the number of active (non-FINAL) rules, for observability.
func (e *Engine) RuleCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.rules)
}

// matchDomainSuffix reports whether host is equal to suffix or ends with
// ".suffix" so that "google.com" matches "www.google.com" but not
// "notgoogle.com".
func matchDomainSuffix(host, suffix string) bool {
	if host == suffix {
		return true
	}
	if len(host) > len(suffix) {
		return strings.HasSuffix(host, "."+suffix)
	}
	return false
}

// normalizeHost strips a port and IPv6 brackets, returning the bare host.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	// Strip IPv6 brackets: [::1]:443 or [::1]
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end > 0 {
			return host[1:end]
		}
	}
	// Strip :port only when there is a single colon (avoid breaking bare IPv6).
	if strings.Count(host, ":") == 1 {
		if idx := strings.IndexByte(host, ':'); idx > 0 {
			return host[:idx]
		}
	}
	return strings.TrimSuffix(host, ".")
}

// parseRule parses a single "TYPE,value[,POLICY]" line. FINAL accepts
// "FINAL,POLICY". Returns ok=false for blank/comment/unknown lines.
func parseRule(line string) (rule, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return rule{}, false
	}
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	kind := strings.ToUpper(parts[0])

	switch kind {
	case "FINAL":
		if len(parts) < 2 {
			return rule{}, false
		}
		return rule{kind: kindFinal, policy: NormalizePolicy(parts[1])}, true
	case "DOMAIN-SUFFIX":
		if len(parts) < 3 {
			return rule{}, false
		}
		return rule{kind: kindDomainSuffix, value: strings.ToLower(parts[1]), policy: NormalizePolicy(parts[2])}, true
	case "DOMAIN-KEYWORD":
		if len(parts) < 3 {
			return rule{}, false
		}
		return rule{kind: kindDomainKeyword, value: strings.ToLower(parts[1]), policy: NormalizePolicy(parts[2])}, true
	case "DOMAIN":
		if len(parts) < 3 {
			return rule{}, false
		}
		return rule{kind: kindDomain, value: strings.ToLower(parts[1]), policy: NormalizePolicy(parts[2])}, true
	case "IP-CIDR", "IP-CIDR6":
		if len(parts) < 3 {
			return rule{}, false
		}
		prefix, err := netip.ParsePrefix(parts[1])
		if err != nil {
			return rule{}, false
		}
		return rule{kind: kindIPCIDR, prefix: prefix, policy: NormalizePolicy(parts[2])}, true
	case "GEOIP":
		if len(parts) < 3 {
			return rule{}, false
		}
		return rule{kind: kindGeoIP, value: strings.ToUpper(parts[1]), policy: NormalizePolicy(parts[2])}, true
	default:
		return rule{}, false
	}
}
