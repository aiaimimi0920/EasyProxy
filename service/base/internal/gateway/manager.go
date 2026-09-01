package gateway

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"easy_proxies/internal/config"
	"easy_proxies/internal/dispatch"
)

type RuleSupervisor interface {
	Apply(context.Context, config.GatewayConfig) error
	Stop(context.Context) error
}

type ListenerFactory func(context.Context, config.GatewayConfig) (net.Listener, error)
type RouterFactory func(config.GatewayConfig) *dispatch.TransparentRouter

type Status struct {
	Enabled           bool   `json:"enabled"`
	Mode              string `json:"mode,omitempty"`
	Listen            string `json:"listen,omitempty"`
	Interface         string `json:"interface,omitempty"`
	Stack             string `json:"stack,omitempty"`
	MTU               uint32 `json:"mtu,omitempty"`
	Applied           bool   `json:"applied"`
	TunReady          bool   `json:"tun_ready"`
	IPv4              bool   `json:"ipv4"`
	IPv6              bool   `json:"ipv6"`
	TCP               bool   `json:"tcp"`
	UDP               bool   `json:"udp"`
	DNS               bool   `json:"dns"`
	ActiveConnections int64  `json:"active_connections"`
	DirectConnections uint64 `json:"direct_connections"`
	ProxyConnections  uint64 `json:"proxy_connections"`
	DirectFallbacks   uint64 `json:"direct_fallbacks"`
	LastError         string `json:"last_error,omitempty"`
}

// Manager owns one transparent gateway generation and makes rule cleanup part
// of listener shutdown, so a dead process cannot leave a blackhole redirect.
type Manager struct {
	supervisor RuleSupervisor
	listenerFn ListenerFactory
	routerFn   RouterFactory

	mu       sync.Mutex
	cancel   context.CancelFunc
	listener net.Listener
	router   *dispatch.TransparentRouter
	wg       sync.WaitGroup
	active   atomic.Int64
	status   Status
}

func NewManager(supervisor RuleSupervisor, listenerFn ListenerFactory, routerFn RouterFactory) *Manager {
	if supervisor == nil {
		supervisor = NewSupervisor(nil)
	}
	if listenerFn == nil {
		listenerFn = ListenTransparent
	}
	return &Manager{supervisor: supervisor, listenerFn: listenerFn, routerFn: routerFn}
}

func (m *Manager) Start(ctx context.Context, cfg config.GatewayConfig) error {
	if m == nil || !cfg.Enabled {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if m.Status().Applied {
		if err := m.Stop(); err != nil {
			m.setError(err)
			return fmt.Errorf("stop previous transparent gateway: %w", err)
		}
	}
	if cfg.Mode == "tun" {
		if err := m.supervisor.Apply(ctx, cfg); err != nil {
			m.setError(err)
			return fmt.Errorf("apply native TUN gateway rules: %w", err)
		}
		m.mu.Lock()
		m.status = statusForConfig(cfg, true)
		m.mu.Unlock()
		return nil
	}
	ln, err := m.listenerFn(ctx, cfg)
	if err != nil {
		m.setError(err)
		return fmt.Errorf("listen transparent gateway: %w", err)
	}
	if err := m.supervisor.Apply(ctx, cfg); err != nil {
		_ = ln.Close()
		m.setError(err)
		return fmt.Errorf("apply transparent gateway rules: %w", err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	var router *dispatch.TransparentRouter
	if m.routerFn != nil {
		router = m.routerFn(cfg)
	}
	m.mu.Lock()
	m.cancel = cancel
	m.listener = ln
	m.router = router
	m.status = statusForConfig(cfg, true)
	m.mu.Unlock()
	m.wg.Add(1)
	go m.acceptLoop(serveCtx, ln, router)
	return nil
}

func (m *Manager) Stop() error {
	if m == nil {
		return nil
	}
	cleanupErr := m.supervisor.Stop(context.Background())
	m.mu.Lock()
	cancel := m.cancel
	ln := m.listener
	m.cancel = nil
	m.listener = nil
	m.status.Applied = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	m.wg.Wait()
	return cleanupErr
}

func statusForConfig(cfg config.GatewayConfig, applied bool) Status {
	status := Status{Enabled: cfg.Enabled, Mode: cfg.Mode, Listen: cfg.Listen, Applied: applied, TCP: true}
	if cfg.Mode == "tun" {
		status.Interface = cfg.Tun.InterfaceName
		status.Stack = cfg.Tun.Stack
		status.MTU = cfg.Tun.MTU
		status.TunReady = applied
		status.IPv4 = cfg.Tun.IPv4
		status.IPv6 = cfg.Tun.IPv6
		status.UDP = cfg.Tun.UDP
		status.DNS = cfg.DNS.Enabled && cfg.Tun.DNSHijack
		return status
	}
	status.IPv4 = true
	return status
}

func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.Lock()
	status := m.status
	m.mu.Unlock()
	status.ActiveConnections = m.active.Load()
	m.mu.Lock()
	router := m.router
	m.mu.Unlock()
	if router != nil {
		stats := router.Stats()
		status.DirectConnections = stats.DirectConnections
		status.ProxyConnections = stats.ProxyConnections
		status.DirectFallbacks = stats.DirectFallbacks
	}
	return status
}

func (m *Manager) GatewayStatus() any { return m.Status() }

func (m *Manager) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Applied = false
	if err == nil {
		m.status.LastError = ""
		return
	}
	m.status.LastError = err.Error()
}

func (m *Manager) acceptLoop(ctx context.Context, ln net.Listener, router *dispatch.TransparentRouter) {
	defer m.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			m.setError(err)
			return
		}
		m.active.Add(1)
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer m.active.Add(-1)
			if router == nil {
				_ = conn.Close()
				return
			}
			router.ServeConn(ctx, conn)
		}()
	}
}
