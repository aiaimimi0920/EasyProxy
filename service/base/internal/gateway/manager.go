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
	Enabled   bool
	Listen    string
	Applied   bool
	Active    int64
	LastError string
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
	if err := m.supervisor.Apply(ctx, cfg); err != nil {
		m.setError(err)
		return fmt.Errorf("apply transparent gateway rules: %w", err)
	}
	ln, err := m.listenerFn(ctx, cfg)
	if err != nil {
		_ = m.supervisor.Stop(context.Background())
		m.setError(err)
		return fmt.Errorf("listen transparent gateway: %w", err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.listener = ln
	m.status = Status{Enabled: true, Listen: cfg.Listen, Applied: true}
	m.mu.Unlock()

	var router *dispatch.TransparentRouter
	if m.routerFn != nil {
		router = m.routerFn(cfg)
	}
	m.wg.Add(1)
	go m.acceptLoop(serveCtx, ln, router)
	return nil
}

func (m *Manager) Stop() error {
	if m == nil {
		return nil
	}
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
	return m.supervisor.Stop(context.Background())
}

func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.Lock()
	status := m.status
	m.mu.Unlock()
	status.Active = m.active.Load()
	return status
}

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
