package app

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
)

type thresholdUpdate struct {
	uptime time.Duration
	rate   float64
}

type blockingRoutingBoxManager struct {
	calls        chan thresholdUpdate
	releaseFirst chan struct{}

	mu      sync.Mutex
	current thresholdUpdate
}

type recordingAppliedConfigBoxManager struct {
	mu       sync.Mutex
	recorded *config.Config
}

type routingBoxManagerStub struct{}

func (*routingBoxManagerStub) PoolOutbound() (adapter.Outbound, bool) { return nil, false }

func (*routingBoxManagerStub) StickySnapshot() (pool.StickySnapshot, bool) {
	return pool.StickySnapshot{}, false
}

func (*routingBoxManagerStub) SetLongLivedThresholds(time.Duration, float64) {}

func (*routingBoxManagerStub) RecordAppliedConfig(*config.Config) {}

type profileRuntimeStub struct {
	shared *profile.CompiledProfile
}

func (r *profileRuntimeStub) Credentials() profile.CredentialSnapshot {
	return profile.CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1}
}

func (r *profileRuntimeStub) Resolve(profile.RequestIdentity) profile.Resolution {
	return profile.Resolution{
		Source:          profile.IdentitySharedFallback,
		ProfileID:       r.shared.ID(),
		ProfileRevision: r.shared.Revision(),
		Profile:         r.shared,
	}
}

func (*profileRuntimeStub) Observe(profile.Resolution, netip.Addr, time.Time) {}

func (*profileRuntimeStub) PrepareConfig(*config.Config) error { return nil }

func disabledSharedProfile(t *testing.T) *profile.CompiledProfile {
	t.Helper()
	compiled, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       false,
		FinalPolicy:   "DIRECT",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func enabledSharedProfile(t *testing.T) *profile.CompiledProfile {
	t.Helper()
	compiled, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func localServerConfigForTest(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Mode:     "pool",
		Listener: config.ListenerConfig{Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed},
		Routing:  config.RoutingConfig{Enabled: false, FinalPolicy: "PROXY"},
		LocalServer: config.LocalServerConfig{
			Enabled:              true,
			Listen:               freeRoutingListen(t),
			Auth:                 config.LocalServerAuthConfig{Username: "easyproxy", Password: "secret"},
			SharedRevision:       1,
			CredentialGeneration: 1,
		},
	}
}

func (m *recordingAppliedConfigBoxManager) PoolOutbound() (adapter.Outbound, bool) {
	return nil, false
}

func (m *recordingAppliedConfigBoxManager) StickySnapshot() (pool.StickySnapshot, bool) {
	return pool.StickySnapshot{}, false
}

func (m *recordingAppliedConfigBoxManager) SetLongLivedThresholds(time.Duration, float64) {}

func (m *recordingAppliedConfigBoxManager) RecordAppliedConfig(cfg *config.Config) {
	m.mu.Lock()
	m.recorded = cfg
	m.mu.Unlock()
}

func (m *recordingAppliedConfigBoxManager) recordedConfig() *config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recorded
}

func newBlockingRoutingBoxManager() *blockingRoutingBoxManager {
	return &blockingRoutingBoxManager{
		calls:        make(chan thresholdUpdate, 2),
		releaseFirst: make(chan struct{}),
	}
}

func (m *blockingRoutingBoxManager) PoolOutbound() (adapter.Outbound, bool) {
	return nil, false
}

func (m *blockingRoutingBoxManager) StickySnapshot() (pool.StickySnapshot, bool) {
	return pool.StickySnapshot{}, false
}

func (m *blockingRoutingBoxManager) SetLongLivedThresholds(uptime time.Duration, rate float64) {
	update := thresholdUpdate{uptime: uptime, rate: rate}
	m.calls <- update
	if uptime == time.Hour {
		<-m.releaseFirst
	}
	m.mu.Lock()
	m.current = update
	m.mu.Unlock()
}

func (m *blockingRoutingBoxManager) RecordAppliedConfig(*config.Config) {}

func (m *blockingRoutingBoxManager) currentThresholds() thresholdUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

func routingLifecycleConfig(t *testing.T, enabled bool, listen, username, password string) *config.Config {
	t.Helper()
	useDefaults := false
	cfg := &config.Config{}
	cfg.Listener.Username = username
	cfg.Listener.Password = password
	cfg.Routing.Enabled = enabled
	cfg.Routing.Listen = listen
	cfg.Routing.UseDefaultRules = &useDefaults
	cfg.Routing.FinalPolicy = string(routerule.PolicyProxy)
	return cfg
}

func freeRoutingListen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve routing listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release routing listen: %v", err)
	}
	return address
}

func waitRoutingReachable(t *testing.T, address string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			if want {
				return
			}
		} else if !want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("routing listen %s reachable=%v, want %v (last error: %v)", address, err == nil, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func routingProxyStatus(t *testing.T, address, target, username, password string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial routing proxy: %v", err)
	}
	defer conn.Close()
	credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	if _, err := fmt.Fprintf(conn,
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
		target,
		target,
		credentials,
	); err != nil {
		t.Fatalf("write proxy request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}
	defer response.Body.Close()
	return response.StatusCode
}
