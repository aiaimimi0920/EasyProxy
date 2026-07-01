// Rule providers fetch remote rule lists (Clash/mihomo-style payload or plain
// one-rule-per-line text) and merge them into an engine on an interval. This
// lets operators subscribe to community rulesets (e.g. China-direct domain
// lists) without hard-coding them.
package routerule

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultProviderInterval = 24 * time.Hour
	maxProviderBodyBytes    = 8 * 1024 * 1024
)

// ProviderSpec describes a remote ruleset to fetch and the policy to apply to
// every entry it contains.
type ProviderSpec struct {
	URL      string        // http(s) URL of the ruleset
	Policy   Policy        // policy applied to all entries (DIRECT/PROXY)
	Interval time.Duration // refresh interval; <=0 uses default (24h)
	// Behavior controls how raw payload lines are interpreted:
	//   - "" / "domain": treat each line as a DOMAIN-SUFFIX entry
	//   - "classical":   treat each line as a full "TYPE,value" rule
	Behavior string
}

// ProviderManager fetches one or more providers and feeds their materialized
// rule lines into a sink (typically Engine.SetRules with the static rules
// prepended). It is safe for concurrent use.
type ProviderManager struct {
	mu        sync.Mutex
	specs     []ProviderSpec
	cached    map[string][]string // url -> materialized rule lines
	client    *http.Client
	onUpdate  func(allProviderRules []string)
	cancel    context.CancelFunc
}

// NewProviderManager creates a manager. onUpdate is invoked (off the caller's
// goroutine) whenever any provider's content changes, with the concatenated
// rule lines from all providers in spec order.
func NewProviderManager(specs []ProviderSpec, onUpdate func(allProviderRules []string)) *ProviderManager {
	return &ProviderManager{
		specs:    specs,
		cached:   make(map[string][]string),
		client:   &http.Client{Timeout: 30 * time.Second},
		onUpdate: onUpdate,
	}
}

// Start performs an initial fetch and then refreshes each provider on its
// interval until ctx is cancelled or Stop is called.
func (m *ProviderManager) Start(ctx context.Context) {
	if len(m.specs) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	// Initial synchronous-ish fetch so rules are present soon after startup.
	m.refreshAll(ctx)

	for i := range m.specs {
		spec := m.specs[i]
		interval := spec.Interval
		if interval <= 0 {
			interval = defaultProviderInterval
		}
		go func(spec ProviderSpec, interval time.Duration) {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if m.refreshOne(ctx, spec) {
						m.emit()
					}
				}
			}
		}(spec, interval)
	}
}

// Stop cancels background refreshing.
func (m *ProviderManager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *ProviderManager) refreshAll(ctx context.Context) {
	changed := false
	for _, spec := range m.specs {
		if m.refreshOne(ctx, spec) {
			changed = true
		}
	}
	if changed {
		m.emit()
	}
}

// refreshOne fetches a single provider and updates the cache. Returns true when
// the materialized lines changed.
func (m *ProviderManager) refreshOne(ctx context.Context, spec ProviderSpec) bool {
	lines, err := m.fetch(ctx, spec)
	if err != nil {
		// Keep previously cached content on failure (fail-soft).
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.cached[spec.URL]
	if equalLines(prev, lines) {
		return false
	}
	m.cached[spec.URL] = lines
	return true
}

func (m *ProviderManager) emit() {
	if m.onUpdate == nil {
		return
	}
	m.mu.Lock()
	all := make([]string, 0, 256)
	for _, spec := range m.specs {
		all = append(all, m.cached[spec.URL]...)
	}
	m.mu.Unlock()
	m.onUpdate(all)
}

// fetch downloads and materializes a provider's payload into rule lines.
func (m *ProviderManager) fetch(ctx context.Context, spec ProviderSpec) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rule provider %s: status %d", spec.URL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBodyBytes))
	if err != nil {
		return nil, err
	}
	return materialize(string(body), spec.Policy, spec.Behavior), nil
}

// materialize converts a raw payload into normalized rule lines. It supports
// both a "classical" form (each line is "TYPE,value") and a "domain" form
// (each line is a bare domain → DOMAIN-SUFFIX). YAML "payload:" wrappers and
// leading list markers ("- ") are tolerated.
func materialize(body string, policy Policy, behavior string) []string {
	classical := strings.EqualFold(strings.TrimSpace(behavior), "classical")
	out := make([]string, 0, 128)
	scanner := bufio.NewScanner(body2reader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// Tolerate YAML list / payload wrappers.
		if line == "payload:" || strings.HasPrefix(line, "payload:") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		line = strings.Trim(line, "'\"")
		if line == "" {
			continue
		}
		if classical {
			// Already a TYPE,value rule; append the policy.
			out = append(out, line+","+string(policy))
			continue
		}
		// Domain behavior: treat as a domain suffix entry.
		domain := strings.TrimPrefix(line, "+.")
		domain = strings.TrimPrefix(domain, ".")
		out = append(out, "DOMAIN-SUFFIX,"+domain+","+string(policy))
	}
	return out
}

func body2reader(s string) *strings.Reader { return strings.NewReader(s) }

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
