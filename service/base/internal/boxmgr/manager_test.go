package boxmgr

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
)

type fakeManagedBox struct {
	events   *[]string
	name     string
	startErr error
	closeErr error
	starts   int
	closes   int
	outbound adapter.OutboundManager
}

func (b *fakeManagedBox) Start() error {
	b.starts++
	if b.events != nil {
		*b.events = append(*b.events, b.name+":start")
	}
	return b.startErr
}

func (b *fakeManagedBox) Close() error {
	b.closes++
	if b.events != nil {
		*b.events = append(*b.events, b.name+":close")
	}
	return b.closeErr
}

func (b *fakeManagedBox) Outbound() adapter.OutboundManager {
	return b.outbound
}

type recordingReloadListener struct {
	manager     *Manager
	events      *[]string
	prepareErr  error
	completeErr error
	failedErr   error
	completeBox managedBox
	failed      []reloadFailureRecord
}

type blockingPrepareReloadListener struct {
	entered chan struct{}
	release chan struct{}
}

type blockingCompleteReloadListener struct {
	entered chan struct{}
	release chan struct{}
}

type signalingReloadIntentListener struct {
	started chan struct{}
	release chan struct{}
}

type recordingReloadIntentListener struct {
	mu            sync.Mutex
	active        int
	begins        int
	ends          int
	prepareActive int
}

func (l *recordingReloadIntentListener) PrepareReload(context.Context, ReloadState, ReloadState) error {
	l.mu.Lock()
	if l.active > 0 {
		l.prepareActive++
	}
	l.mu.Unlock()
	return nil
}

func (l *recordingReloadIntentListener) CompleteReload(context.Context, ReloadState, ReloadState) error {
	return nil
}

func (l *recordingReloadIntentListener) FailedReload(context.Context, ReloadState, ReloadState, error, bool) error {
	return nil
}

func (l *recordingReloadIntentListener) BeginReloadIntent(context.Context) error {
	l.mu.Lock()
	l.active++
	l.begins++
	l.mu.Unlock()
	return nil
}

func (l *recordingReloadIntentListener) EndReloadIntent(context.Context) {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.ends++
	l.mu.Unlock()
}

func (l *blockingPrepareReloadListener) PrepareReload(context.Context, ReloadState, ReloadState) error {
	close(l.entered)
	<-l.release
	return nil
}

func (l *blockingPrepareReloadListener) CompleteReload(context.Context, ReloadState, ReloadState) error {
	return nil
}

func (l *blockingPrepareReloadListener) FailedReload(context.Context, ReloadState, ReloadState, error, bool) error {
	return nil
}

func (l *blockingCompleteReloadListener) PrepareReload(context.Context, ReloadState, ReloadState) error {
	return nil
}

func (l *blockingCompleteReloadListener) CompleteReload(context.Context, ReloadState, ReloadState) error {
	close(l.entered)
	<-l.release
	return nil
}

func (l *blockingCompleteReloadListener) FailedReload(context.Context, ReloadState, ReloadState, error, bool) error {
	return nil
}

func (l *signalingReloadIntentListener) BeginReloadIntent(context.Context) error {
	close(l.started)
	<-l.release
	return nil
}

func (l *signalingReloadIntentListener) EndReloadIntent(context.Context) {}

func (l *signalingReloadIntentListener) PrepareReload(context.Context, ReloadState, ReloadState) error {
	return nil
}

func (l *signalingReloadIntentListener) CompleteReload(context.Context, ReloadState, ReloadState) error {
	return nil
}

func (l *signalingReloadIntentListener) FailedReload(context.Context, ReloadState, ReloadState, error, bool) error {
	return nil
}

type reloadFailureRecord struct {
	cause    error
	restored bool
}

func (l *recordingReloadListener) PrepareReload(_ context.Context, from, to ReloadState) error {
	*l.events = append(*l.events, "prepare")
	return l.prepareErr
}

func (l *recordingReloadListener) CompleteReload(_ context.Context, from, to ReloadState) error {
	*l.events = append(*l.events, "complete")
	if l.manager != nil {
		l.manager.mu.RLock()
		l.completeBox = l.manager.currentBox
		l.manager.mu.RUnlock()
	}
	return l.completeErr
}

func (l *recordingReloadListener) FailedReload(_ context.Context, from, to ReloadState, cause error, restored bool) error {
	*l.events = append(*l.events, "failed")
	l.failed = append(l.failed, reloadFailureRecord{cause: cause, restored: restored})
	return l.failedErr
}

func newInitialIdleTestManager(t *testing.T) *Manager {
	t.Helper()
	manager := New(&config.Config{
		Mode:     "pool",
		Listener: config.ListenerConfig{Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed},
		Management: config.ManagementConfig{
			Enabled: boolPtr(false),
		},
		LocalServer: config.LocalServerConfig{
			Enabled:              true,
			Auth:                 config.LocalServerAuthConfig{Username: "easyproxy", Password: "secret"},
			SharedRevision:       1,
			CredentialGeneration: 1,
		},
	}, monitor.Config{Enabled: false})
	manager.boxFactory = func(context.Context, *config.Config) (managedBox, error) {
		return &fakeManagedBox{name: "recovered"}, nil
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func boolPtr(value bool) *bool { return &value }

type blockingStartBox struct {
	startEntered chan struct{}
	releaseStart chan struct{}
	startOnce    sync.Once

	mu     sync.Mutex
	closes int
}

func (b *blockingStartBox) Start() error {
	b.startOnce.Do(func() { close(b.startEntered) })
	<-b.releaseStart
	return nil
}

func (b *blockingStartBox) Close() error {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	return nil
}

func (b *blockingStartBox) Outbound() adapter.OutboundManager { return nil }

func (b *blockingStartBox) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

type recordingConfigListener struct {
	events  *[]string
	configs []*config.Config
	lastRaw *config.Config
}

type configCommitLockProbe struct {
	server  *monitor.Server
	manager *Manager
	result  chan error
}

type ephemeralCommitProbe struct {
	manager *Manager
	seen    chan []config.NodeConfig
}

func (p *ephemeralCommitProbe) OnConfigUpdate(*config.Config) {
	p.manager.mu.RLock()
	ephemeralNodes := cloneNodes(p.manager.ephemeralNodes)
	p.manager.mu.RUnlock()
	p.seen <- ephemeralNodes
}

func (p *configCommitLockProbe) OnConfigUpdate(cfg *config.Config) {
	p.server.SetConfig(cfg)
	// The reload bookkeeping must be visible before listeners are called.
	p.manager.mu.RLock()
	applied := p.manager.lastAppliedCfg
	p.manager.mu.RUnlock()
	if applied == nil || applied.Mode != cfg.Mode {
		p.result <- fmt.Errorf("last-applied config not committed before listener: %#v", applied)
		return
	}
	p.result <- nil
}

func (l *recordingConfigListener) OnConfigUpdate(cfg *config.Config) {
	*l.events = append(*l.events, "notify")
	l.lastRaw = cfg
	l.configs = append(l.configs, snapshotConfig(cfg))
}

func reserveFreePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatalf("release free port: %v", err)
	}
	return port
}

func waitReachableAddress(t *testing.T, address string, want bool) {
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
			t.Fatalf("listen %s reachable=%v, want %v (last error: %v)", address, err == nil, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
