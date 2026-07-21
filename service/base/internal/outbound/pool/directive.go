package pool

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Strategy selects how the pool picks a member for a given request.
type Strategy string

const (
	// StrategyAuto keeps the pool's configured Mode behaviour (health-based,
	// random, balance, sequential). This is the default when no directive is
	// present, preserving full backward compatibility.
	StrategyAuto Strategy = "auto"
	// StrategyStable pins all traffic in the same filter bucket to a single
	// long-lived member. When that member becomes unavailable the bucket is
	// promoted to the next best healthy candidate. Designed for anti-ban
	// scenarios where a stable egress IP matters.
	StrategyStable Strategy = "stable"
	// StrategySession keeps a session (identified by SessionKey, falling back
	// to the client source IP) pinned to one member for the session TTL. When
	// the pinned member dies the session is forced onto a new candidate.
	// Designed for crawlers that need short-term IP stickiness.
	StrategySession Strategy = "session"
)

// NormalizeStrategy maps an arbitrary string to a known Strategy, defaulting
// to StrategyAuto for empty or unknown values.
func NormalizeStrategy(value string) Strategy {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(StrategyStable):
		return StrategyStable
	case string(StrategySession):
		return StrategySession
	case string(StrategyAuto):
		return StrategyAuto
	default:
		return StrategyAuto
	}
}

// NodeFilter narrows the candidate set before selection. Empty fields impose
// no constraint.
type NodeFilter struct {
	Countries []string // ISO country codes, upper-cased (e.g. "US", "JP")
	Regions   []string // region codes: jp/kr/us/hk/tw/other
	LongLived *bool    // nil = no constraint; true = only long-lived nodes
}

type LongLivedPolicy struct {
	MinUptime      time.Duration
	MinSuccessRate float64
}

// IsZero reports whether the filter imposes no constraint at all.
func (f NodeFilter) IsZero() bool {
	return len(f.Countries) == 0 && len(f.Regions) == 0 && f.LongLived == nil
}

// normalized returns a canonical copy with trimmed, upper-cased, sorted,
// de-duplicated slices so that bucket keys are stable regardless of input
// ordering or casing.
func (f NodeFilter) normalized() NodeFilter {
	out := NodeFilter{LongLived: f.LongLived}
	out.Countries = normalizeTokens(f.Countries, strings.ToUpper)
	out.Regions = normalizeTokens(f.Regions, strings.ToLower)
	return out
}

func normalizeTokens(values []string, transform func(string) string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		t := transform(strings.TrimSpace(v))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// bucketKey produces a stable identifier for the filter, used to group
// stable-strategy stickiness. Equivalent filters always map to the same key.
func (f NodeFilter) bucketKey() string {
	n := f.normalized()
	var b strings.Builder
	b.WriteString("c=")
	b.WriteString(strings.Join(n.Countries, ","))
	b.WriteString("|r=")
	b.WriteString(strings.Join(n.Regions, ","))
	b.WriteString("|l=")
	switch {
	case n.LongLived == nil:
		b.WriteString("any")
	case *n.LongLived:
		b.WriteString("yes")
	default:
		b.WriteString("no")
	}
	sum := sha1.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// SelectionDirective carries per-request / per-session selection intent from
// the dispatcher into the pool outbound through the dial context.
type SelectionDirective struct {
	ProfileID       string
	ProfileRevision int64
	Strategy        Strategy
	SessionKey      string // session stickiness key (StrategySession)
	SessionTTL      time.Duration
	PinnedTag       string // manually requested member tag
	Filter          NodeFilter
	LongLived       LongLivedPolicy
}

func (d SelectionDirective) namespaced(value string) string {
	if d.ProfileID == "" {
		return value
	}
	return fmt.Sprintf("%s@%d\x00%s", d.ProfileID, d.ProfileRevision, value)
}

func (d SelectionDirective) namespacedSessionKey() string {
	return d.namespaced(d.SessionKey)
}

func (d SelectionDirective) stableBucketKey() string {
	return d.namespaced(d.Filter.bucketKey())
}

type directiveCtxKey struct{}

// WithDirective returns a context carrying the selection directive. A nil
// directive leaves the context unchanged.
func WithDirective(ctx context.Context, d *SelectionDirective) context.Context {
	if d == nil {
		return ctx
	}
	return context.WithValue(ctx, directiveCtxKey{}, d)
}

// DirectiveFrom extracts the selection directive from the context, or nil if
// none was set. A nil result means the pool should use its configured Mode
// (backward-compatible behaviour).
func DirectiveFrom(ctx context.Context) *SelectionDirective {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(directiveCtxKey{}).(*SelectionDirective)
	return d
}
