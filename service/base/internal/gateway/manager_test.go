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

func TestManagerStopsRulesWhenListenerFails(t *testing.T) {
	rules := &recordingRuleSupervisor{}
	manager := NewManager(rules, func(context.Context, config.GatewayConfig) (net.Listener, error) {
		return nil, errors.New("listen denied")
	}, nil)
	err := manager.Start(context.Background(), config.GatewayConfig{Enabled: true, Listen: "0.0.0.0:15001"})
	if err == nil {
		t.Fatal("expected listener error")
	}
	if rules.applyCalls != 1 || rules.stopCalls != 1 {
		t.Fatalf("listener failure leaked rules: %+v", rules)
	}
	if got := manager.Status().Applied; got {
		t.Fatal("manager reports applied after failed startup")
	}
}
