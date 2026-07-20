// Package dispatch implements the smart proxy entry that sits in front of the
// proxy pool. It parses per-request / per-session selection parameters (via
// path prefix, HTTP headers, or a port-bound fixed directive), applies the
// traffic-splitting rule engine to decide DIRECT vs PROXY, and then either
// dials the destination directly or hands it to the pool outbound with the
// selection directive injected into the dial context.
package dispatch

import (
	"strings"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"
)

// directiveOverlay is a partial, optional view of selection parameters parsed
// from one source (path prefix / headers / bound port). Nil-able fields use
// pointers so an unset field does not clobber a value supplied by a
// higher-priority source during merge.
type directiveOverlay struct {
	Strategy  *pool.Strategy
	Countries []string
	Regions   []string
	LongLived *bool
	PinnedTag *string
	SessionID *string
	// Split controls whether the routing rule engine is consulted. When false,
	// every destination is forced to PROXY (the "no split" / all-through-proxy
	// mode used by crawlers). nil means "unset" so merging is well-defined.
	Split *bool
}

// merge applies o2 on top of o1, with o2 taking precedence for any field it
// sets. Slice fields are replaced wholesale (not concatenated) so a
// higher-priority source can fully override a lower one.
func (o1 directiveOverlay) merge(o2 directiveOverlay) directiveOverlay {
	out := o1
	if o2.Strategy != nil {
		out.Strategy = o2.Strategy
	}
	if len(o2.Countries) > 0 {
		out.Countries = o2.Countries
	}
	if len(o2.Regions) > 0 {
		out.Regions = o2.Regions
	}
	if o2.LongLived != nil {
		out.LongLived = o2.LongLived
	}
	if o2.PinnedTag != nil {
		out.PinnedTag = o2.PinnedTag
	}
	if o2.SessionID != nil {
		out.SessionID = o2.SessionID
	}
	if o2.Split != nil {
		out.Split = o2.Split
	}
	return out
}

// resolved is the final, merged selection intent for a request.
type resolved struct {
	directive pool.SelectionDirective
	split     bool // whether to consult the routing rules (false = force proxy)
}

// resolve collapses the overlay into a concrete directive, filling defaults
// for any field still unset. defaultStrategy applies when no source specified
// a strategy; sessionFallback supplies the session key when the strategy is
// session but no explicit key was given (typically the client source IP).
func (o directiveOverlay) resolve(defaultStrategy pool.Strategy, sessionFallback string) resolved {
	strat := defaultStrategy
	if o.Strategy != nil {
		strat = *o.Strategy
	}
	dir := pool.SelectionDirective{
		Strategy: strat,
		Filter: pool.NodeFilter{
			Countries: o.Countries,
			Regions:   o.Regions,
			LongLived: o.LongLived,
		},
	}
	if o.PinnedTag != nil {
		dir.PinnedTag = strings.TrimSpace(*o.PinnedTag)
	}
	if strat == pool.StrategySession {
		if o.SessionID != nil && strings.TrimSpace(*o.SessionID) != "" {
			dir.SessionKey = strings.TrimSpace(*o.SessionID)
		} else {
			dir.SessionKey = sessionFallback
		}
	}
	split := true
	if o.Split != nil {
		split = *o.Split
	}
	return resolved{directive: dir, split: split}
}

func (o directiveOverlay) applyTo(base pool.SelectionDirective, sessionFallback string) resolved {
	out := base
	if o.Strategy != nil {
		out.Strategy = *o.Strategy
	}
	if len(o.Countries) > 0 {
		out.Filter.Countries = append([]string(nil), o.Countries...)
	}
	if len(o.Regions) > 0 {
		out.Filter.Regions = append([]string(nil), o.Regions...)
	}
	if o.LongLived != nil {
		out.Filter.LongLived = cloneBool(o.LongLived)
	}
	if o.PinnedTag != nil {
		out.PinnedTag = strings.TrimSpace(*o.PinnedTag)
	}
	if o.SessionID != nil && strings.TrimSpace(*o.SessionID) != "" {
		out.SessionKey = strings.TrimSpace(*o.SessionID)
	} else if out.Strategy == pool.StrategySession && strings.TrimSpace(out.SessionKey) == "" {
		out.SessionKey = sessionFallback
	}
	split := true
	if o.Split != nil {
		split = *o.Split
	}
	return resolved{directive: out, split: split}
}

// parseTokens parses a compact, "+"-separated token prefix used by the
// path-prefix entry style, e.g. "stable+us+long+nosplit" or
// "session+sid=abc123+cc=US". Recognized tokens:
//
//	auto | stable | session          → strategy
//	jp|kr|us|hk|tw|other             → region filter (repeatable)
//	cc=<ISO>                          → country filter (repeatable, e.g. cc=US)
//	long | nolong                     → long-lived filter on/off
//	pin=<tag>                         → manual node pin
//	sid=<key>                         → session key
//	split | nosplit                   → enable/disable routing rules
//
// Unknown tokens are ignored. Token matching is case-insensitive.
func parseTokens(prefix string) (directiveOverlay, bool) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return directiveOverlay{}, false
	}
	var o directiveOverlay
	matchedAny := false
	for _, raw := range strings.Split(prefix, "+") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}
		lower := strings.ToLower(tok)
		switch {
		case lower == "auto" || lower == "stable" || lower == "session":
			s := pool.NormalizeStrategy(lower)
			o.Strategy = &s
			matchedAny = true
		case lower == "long":
			v := true
			o.LongLived = &v
			matchedAny = true
		case lower == "nolong":
			v := false
			o.LongLived = &v
			matchedAny = true
		case lower == "split":
			v := true
			o.Split = &v
			matchedAny = true
		case lower == "nosplit":
			v := false
			o.Split = &v
			matchedAny = true
		case isRegionToken(lower):
			o.Regions = append(o.Regions, lower)
			matchedAny = true
		case strings.HasPrefix(lower, "cc="):
			cc := strings.ToUpper(strings.TrimSpace(tok[3:]))
			if cc != "" {
				o.Countries = append(o.Countries, cc)
				matchedAny = true
			}
		case strings.HasPrefix(lower, "pin="):
			tag := strings.TrimSpace(tok[4:])
			if tag != "" {
				o.PinnedTag = &tag
				matchedAny = true
			}
		case strings.HasPrefix(lower, "sid="):
			sid := strings.TrimSpace(tok[4:])
			if sid != "" {
				o.SessionID = &sid
				matchedAny = true
			}
		}
	}
	return o, matchedAny
}

func isRegionToken(s string) bool {
	switch s {
	case "jp", "kr", "us", "hk", "tw", "other":
		return true
	default:
		return false
	}
}

// HTTP header names recognized by the dispatcher (case-insensitive per the
// net/http canonicalization).
const (
	headerCountry   = "X-Proxy-Country"    // comma-separated ISO codes
	headerRegion    = "X-Proxy-Region"     // comma-separated region codes
	headerStrategy  = "X-Proxy-Strategy"   // auto | stable | session
	headerLongLived = "X-Proxy-Long-Lived" // true | false
	headerPin       = "X-Proxy-Pin"        // node tag
	headerSession   = "X-Proxy-Session"    // session key
	headerSplit     = "X-Proxy-Split"      // on/off | true/false (off = all proxy)
)

// headerGetter abstracts http.Header.Get so the parser is testable without a
// full request.
type headerGetter interface {
	Get(key string) string
}

// parseHeaders builds an overlay from recognized X-Proxy-* headers.
func parseHeaders(h headerGetter) directiveOverlay {
	var o directiveOverlay
	if v := strings.TrimSpace(h.Get(headerStrategy)); v != "" {
		s := pool.NormalizeStrategy(v)
		o.Strategy = &s
	}
	if v := h.Get(headerCountry); strings.TrimSpace(v) != "" {
		o.Countries = splitCSVUpper(v)
	}
	if v := h.Get(headerRegion); strings.TrimSpace(v) != "" {
		o.Regions = splitCSVLower(v)
	}
	if v := strings.TrimSpace(h.Get(headerLongLived)); v != "" {
		if b, ok := parseBool(v); ok {
			o.LongLived = &b
		}
	}
	if v := strings.TrimSpace(h.Get(headerPin)); v != "" {
		o.PinnedTag = &v
	}
	if v := strings.TrimSpace(h.Get(headerSession)); v != "" {
		o.SessionID = &v
	}
	if v := strings.TrimSpace(h.Get(headerSplit)); v != "" {
		// X-Proxy-Split: off / false / 0 → disable splitting (force proxy).
		if b, ok := parseOnOff(v); ok {
			o.Split = &b
		}
	}
	return o
}

func splitCSVUpper(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.ToUpper(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitCSVLower(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.ToLower(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "y":
		return true, true
	case "false", "0", "no", "n":
		return false, true
	default:
		return false, false
	}
}

// parseOnOff parses split-style values where on/true/1 enables splitting.
func parseOnOff(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "1", "yes", "y", "enable", "enabled":
		return true, true
	case "off", "false", "0", "no", "n", "disable", "disabled":
		return false, true
	default:
		return false, false
	}
}

// policyForSplit maps the split flag + a rule engine decision into the final
// policy. When split is disabled, everything is forced to PROXY.
func policyForSplit(split bool, engine *routerule.Engine, host string) routerule.Policy {
	if !split {
		return routerule.PolicyProxy
	}
	if engine == nil {
		return routerule.PolicyProxy
	}
	return engine.Match(host)
}
