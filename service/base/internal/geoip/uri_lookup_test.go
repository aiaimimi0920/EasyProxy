package geoip

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type countingResolver struct {
	mu      sync.Mutex
	calls   map[string]int
	results map[string][]net.IPAddr
	block   bool
}

func (r *countingResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	r.calls[host]++
	r.mu.Unlock()
	if r.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.results[host], nil
}

func (r *countingResolver) callCount(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

func TestResolveURIHostsDeduplicatesDomainsAndPreservesIPLiteral(t *testing.T) {
	resolver := &countingResolver{results: map[string][]net.IPAddr{
		"example.com": {{IP: net.ParseIP("203.0.113.8")}},
	}}
	uris := []string{
		"socks5://example.com:1080",
		"http://EXAMPLE.com:8080",
		"socks5://192.0.2.9:1080",
	}

	hosts := resolveURIHosts(uris, resolver, time.Second, 4)
	want := []string{"203.0.113.8", "203.0.113.8", "192.0.2.9"}
	for index := range want {
		if hosts[index] != want[index] {
			t.Fatalf("hosts[%d] = %q, want %q", index, hosts[index], want[index])
		}
	}
	if calls := resolver.callCount("example.com"); calls != 1 {
		t.Fatalf("example.com lookups = %d, want 1", calls)
	}
}

func TestResolveURIHostsUsesOneSharedDeadline(t *testing.T) {
	resolver := &countingResolver{block: true}
	uris := make([]string, 58)
	for index := range uris {
		prefix := string(rune('a'+index/26)) + string(rune('a'+index%26))
		uris[index] = "socks5://node-" + prefix + ".invalid:1080"
	}

	started := time.Now()
	hosts := resolveURIHosts(uris, resolver, 40*time.Millisecond, 8)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("batch lookup took %v, want one bounded shared deadline", elapsed)
	}
	for index, host := range hosts {
		if host != "" {
			t.Fatalf("hosts[%d] = %q after resolver timeout, want empty", index, host)
		}
	}
}
