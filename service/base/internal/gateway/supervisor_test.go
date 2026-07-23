package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"easy_proxies/internal/config"
)

type recordingRunner struct {
	commands []string
}

func TestMissingNetworkExecutableIsNotIdempotent(t *testing.T) {
	if isIdempotentNetworkError(errors.New(`exec: "nft": executable file not found in $PATH`)) {
		t.Fatal("missing network executable must fail instead of being treated as an existing rule")
	}
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.commands = append(r.commands, strings.Join(append([]string{name}, args...), " "))
	return nil
}

func TestSupervisorApplyAndStopOwnsTransparentRules(t *testing.T) {
	runner := &recordingRunner{}
	supervisor := NewSupervisor(runner)
	cfg := config.GatewayConfig{
		Enabled: true,
		Listen:  "0.0.0.0:16000",
		Ingress: config.GatewayIngressConfig{
			Interfaces:   []string{"eth0"},
			TrustedCIDRs: []string{"192.168.15.0/24"},
		},
	}

	if err := supervisor.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("second Apply should be idempotent: %v", err)
	}
	appliedCount := len(runner.commands)
	joined := strings.Join(runner.commands, "\n")
	for _, expected := range []string{
		"ip rule add fwmark 0x1/0x1 lookup 100",
		"ip route add local 0.0.0.0/0 dev lo table 100",
		"nft add table inet easyproxy_gateway",
		"tcp dport { 16000 , 22323 , 29888 } return",
		"iifname eth0",
		"ip saddr 192.168.15.0/24",
		"meta l4proto tcp",
		"tproxy ip to :16000",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("commands do not contain %q:\n%s", expected, joined)
		}
	}

	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) <= appliedCount {
		t.Fatal("Stop did not remove applied rules")
	}
	cleanup := strings.Join(runner.commands[appliedCount:], "\n")
	for _, expected := range []string{
		"nft delete table inet easyproxy_gateway",
		"ip route del local 0.0.0.0/0 dev lo table 100",
		"ip rule del fwmark 0x1/0x1 lookup 100",
	} {
		if !strings.Contains(cleanup, expected) {
			t.Fatalf("cleanup does not contain %q:\n%s", expected, cleanup)
		}
	}
}
