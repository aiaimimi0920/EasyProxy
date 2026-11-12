package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"easy_proxies/internal/builder"
	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/outbound/gatewayroute"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/outbound/profileselect"
	"easy_proxies/internal/profile"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
)

func (m *Manager) PrepareMonitor(ctx context.Context) error {
	return m.ensureMonitor(ctx)
}

// createBox builds a sing-box instance from config.
func (m *Manager) createBox(ctx context.Context, cfg *config.Config) (*box.Box, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	if m.monitorMgr == nil {
		return nil, errors.New("monitor manager not initialized")
	}

	opts, err := builder.Build(cfg)
	if err != nil {
		return nil, fmt.Errorf("build sing-box options: %w", err)
	}

	inboundRegistry := include.InboundRegistry()
	registerAuthRedactingInbounds(inboundRegistry)
	outboundRegistry := include.OutboundRegistry()
	pool.Register(outboundRegistry)
	gatewayroute.Register(outboundRegistry)
	endpointRegistry := include.EndpointRegistry()
	dnsRegistry := include.DNSTransportRegistry()
	serviceRegistry := include.ServiceRegistry()

	boxCtx := box.Context(ctx, inboundRegistry, outboundRegistry, endpointRegistry, dnsRegistry, serviceRegistry)
	boxCtx = monitor.ContextWith(boxCtx, m.monitorMgr)
	if m.profiles != nil {
		boxCtx = profileselect.WithResolver(boxCtx, m.profiles)
		gate := profileselect.AdmissionFrom(ctx)
		boxCtx = pool.WithSharedSelection(boxCtx, func(flow context.Context) (*pool.SelectionDirective, error) {
			if err := gate.Check(); err != nil {
				return nil, err
			}
			d := profileselect.Directive(m.profiles.Resolve(profile.RequestIdentity{}), "")
			return &d, nil
		})
	}

	instance, err := box.New(box.Options{Context: boxCtx, Options: opts})
	if err != nil {
		return nil, fmt.Errorf("create sing-box instance: %w", err)
	}
	return instance, nil
}

func (m *Manager) createManagedBox(ctx context.Context, cfg *config.Config) (managedBox, error) {
	var gate *profileselect.Admission
	if m.profiles != nil && cfg != nil && (cfg.DispatchEnabled() || cfg.Mode == "pool" || cfg.Mode == "hybrid" || (cfg.Gateway.Enabled && strings.EqualFold(cfg.Gateway.Mode, "tun"))) {
		gate = new(profileselect.Admission)
		ctx = profileselect.WithAdmission(ctx, gate)
	}
	var instance managedBox
	var err error
	if m.boxFactory != nil {
		instance, err = m.boxFactory(ctx, cfg)
	} else {
		instance, err = m.createBox(ctx, cfg)
	}
	if err != nil || gate == nil {
		return instance, err
	}
	return &profileGatedBox{managedBox: instance, gate: gate}, nil
}

// waitForHealthCheck polls until enough nodes are available or timeout.
func (m *Manager) waitForHealthCheck(timeout time.Duration) error {
	return m.waitForHealthCheckAtLeast(timeout, m.minAvailableNodes)
}

func (m *Manager) waitForHealthCheckAtLeast(timeout time.Duration, minAvailableNodes int) error {
	if m.monitorMgr == nil || minAvailableNodes <= 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = defaultHealthCheckTimeout
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(healthCheckPollInterval)
	defer ticker.Stop()

	for {
		available, total := m.availableNodeCount()
		if available >= minAvailableNodes {
			m.logger.Infof("health check passed: %d/%d nodes available", available, total)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout: %d/%d nodes available (need >= %d)", available, total, minAvailableNodes)
		}
		<-ticker.C
	}
}

// availableNodeCount returns (available, total) node counts.
func (m *Manager) availableNodeCount() (int, int) {
	if m.monitorMgr == nil {
		return 0, 0
	}
	snapshots := m.monitorMgr.Snapshot()
	total := len(snapshots)
	available := 0
	for _, snap := range snapshots {
		if snap.EffectiveAvailable {
			available++
		}
	}
	return available, total
}
