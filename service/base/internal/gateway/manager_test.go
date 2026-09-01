package gateway

import (
	"context"
	"errors"
	"net"
	"testing"

	"easy_proxies/internal/config"
)

type recordingRuleSupervisor struct {
	applyCalls int
	stopCalls  int
}

func (s *recordingRuleSupervisor) Apply(context.Context, config.GatewayConfig) error {
	s.applyCalls++
	return nil
}

func (s *recordingRuleSupervisor) Stop(context.Context) error {
	s.stopCalls++
	return nil
}

func TestManagerDisabledIsNoOp(t *testing.T) {
	rules := &recordingRuleSupervisor{}
	manager := NewManager(rules, func(context.Context, config.GatewayConfig) (net.Listener, error) {
		t.Fatal("disabled gateway attempted to listen")
		return nil, nil
	}, nil)
	if err := manager.Start(context.Background(), config.GatewayConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if rules.applyCalls != 0 || rules.stopCalls != 0 {
		t.Fatalf("disabled gateway changed rules: %+v", rules)
	}
}

func TestManagerDoesNotApplyRulesWhenListenerFails(t *testing.T) {
	rules := &recordingRuleSupervisor{}
	manager := NewManager(rules, func(context.Context, config.GatewayConfig) (net.Listener, error) {
		return nil, errors.New("listen denied")
	}, nil)
	err := manager.Start(context.Background(), config.GatewayConfig{Enabled: true, Listen: "0.0.0.0:15001"})
	if err == nil {
		t.Fatal("expected listener error")
	}
	if rules.applyCalls != 0 || rules.stopCalls != 0 {
		t.Fatalf("listener failure changed host rules: %+v", rules)
	}
	if got := manager.Status().Applied; got {
		t.Fatal("manager reports applied after failed startup")
	}
}

func TestManagerTunModeAppliesWithoutTransparentListener(t *testing.T) {
	rules := &recordingRuleSupervisor{}
	manager := NewManager(rules, func(context.Context, config.GatewayConfig) (net.Listener, error) {
		t.Fatal("TUN mode attempted to create a transparent TCP listener")
		return nil, nil
	}, nil)
	cfg := config.GatewayConfig{
		Enabled: true,
		Mode:    "tun",
		Tun: config.GatewayTunConfig{
			InterfaceName: "easyproxy0", Stack: "mixed", MTU: 1500,
			IPv4: true, IPv6: true, UDP: true, DNSHijack: true,
		},
		DNS: config.GatewayDNSConfig{Enabled: true},
	}
	if err := manager.Start(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	status := manager.Status()
	if rules.applyCalls != 1 || !status.Applied || !status.TunReady || !status.IPv6 || !status.UDP || !status.DNS {
		t.Fatalf("unexpected TUN manager state: rules=%+v status=%+v", rules, status)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if rules.stopCalls != 1 {
		t.Fatalf("TUN Stop calls = %d, want 1", rules.stopCalls)
	}
}
