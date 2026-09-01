package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"easy_proxies/internal/config"
)

type recordingRunner struct {
	commands []string
	failOn   string
	failErr  error
	failures map[string]error
}

func TestMissingNetworkExecutableIsNotIdempotent(t *testing.T) {
	if isIdempotentNetworkError(errors.New(`exec: "nft": executable file not found in $PATH`)) {
		t.Fatal("missing network executable must fail instead of being treated as an existing rule")
	}
}

func TestExecRunnerPreservesCommandStderr(t *testing.T) {
	t.Setenv("EASYPROXY_GATEWAY_EXEC_HELPER", "1")
	err := (ExecRunner{}).Run(context.Background(), os.Args[0], "-test.run=TestGatewayExecHelperProcess")
	if err == nil || !strings.Contains(err.Error(), "RTNETLINK answers: File exists") {
		t.Fatalf("ExecRunner error = %v, want command stderr", err)
	}
	if !isIdempotentNetworkError(err) {
		t.Fatalf("network duplicate with captured stderr must be idempotent: %v", err)
	}
}

func TestGatewayExecHelperProcess(t *testing.T) {
	if os.Getenv("EASYPROXY_GATEWAY_EXEC_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "RTNETLINK answers: File exists")
	os.Exit(2)
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	command := strings.Join(append([]string{name}, args...), " ")
	r.commands = append(r.commands, command)
	for fragment, err := range r.failures {
		if strings.Contains(command, fragment) {
			return err
		}
	}
	if r.failOn != "" && strings.Contains(command, r.failOn) {
		if r.failErr != nil {
			return r.failErr
		}
		return errors.New("injected command failure")
	}
	return nil
}

func TestSupervisorApplyAdoptsExistingPolicyRule(t *testing.T) {
	runner := &recordingRunner{
		failOn:  "ip rule add",
		failErr: errors.New("exit status 2: RTNETLINK answers: File exists"),
	}
	supervisor := NewSupervisor(runner)
	if err := supervisor.Apply(context.Background(), config.GatewayConfig{Enabled: true}); err != nil {
		t.Fatalf("Apply rejected an existing gateway rule: %v", err)
	}
	if len(runner.commands) < 2 || runner.commands[0] != "nft delete table inet easyproxy_gateway" {
		t.Fatalf("Apply did not rebuild the dedicated nft table first: %v", runner.commands)
	}
	appliedCount := len(runner.commands)
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanup := strings.Join(runner.commands[appliedCount:], "\n")
	if strings.Contains(cleanup, "ip rule del") {
		t.Fatalf("Stop cleaned an adopted host rule:\n%s", cleanup)
	}
}

func TestSupervisorApplyFailureDoesNotCleanUnappliedRules(t *testing.T) {
	runner := &recordingRunner{failOn: "ip rule add"}
	supervisor := NewSupervisor(runner)
	err := supervisor.Apply(context.Background(), config.GatewayConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected gateway rule failure")
	}
	joined := strings.Join(runner.commands, "\n")
	if !strings.Contains(joined, "nft delete table inet easyproxy_gateway") {
		t.Fatalf("Apply did not clear the dedicated stale table:\n%s", joined)
	}
	if strings.Contains(joined, "ip rule del") || strings.Contains(joined, "ip route del") {
		t.Fatalf("Apply cleaned a rule that it did not install:\n%s", joined)
	}
}

func TestSupervisorApplyFailureDoesNotCleanAdoptedRules(t *testing.T) {
	runner := &recordingRunner{failures: map[string]error{
		"ip rule add":  errors.New("exit status 2: RTNETLINK answers: File exists"),
		"ip route add": errors.New("permission denied"),
	}}
	supervisor := NewSupervisor(runner)
	if err := supervisor.Apply(context.Background(), config.GatewayConfig{Enabled: true}); err == nil {
		t.Fatal("expected route failure")
	}
	joined := strings.Join(runner.commands, "\n")
	if strings.Contains(joined, "ip rule del") {
		t.Fatalf("Apply cleaned an adopted host rule:\n%s", joined)
	}
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

func TestTunSupervisorBuildsDualStackUDPAndDNSRules(t *testing.T) {
	runner := &recordingRunner{}
	supervisor := NewSupervisor(runner)
	cfg := config.GatewayConfig{
		Enabled: true,
		Mode:    "tun",
		Ingress: config.GatewayIngressConfig{
			Interfaces:   []string{"eth0"},
			TrustedCIDRs: []string{"192.168.15.0/24", "fd00:15::/64"},
		},
		DNS: config.GatewayDNSConfig{Enabled: true},
		Tun: config.GatewayTunConfig{
			InterfaceName: "easyproxy0", IPv4: true, IPv6: true, UDP: true, DNSHijack: true,
			FakeIP: true, FakeIPv4Range: "198.18.0.0/16", FakeIPv6Range: "fc00::/18",
		},
	}
	if err := supervisor.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	appliedCount := len(runner.commands)
	joined := strings.Join(runner.commands, "\n")
	for _, expected := range []string{
		"ip link show dev easyproxy0",
		"ip route add default dev easyproxy0 table 100",
		"ip -6 route add default dev easyproxy0 table 101",
		"ip saddr 192.168.15.0/24 meta l4proto { tcp , udp }",
		"ip6 saddr fd00:15::/64 meta l4proto { tcp , udp }",
		"ip6 saddr fd00:15::/64 ip6 daddr fc00::/18 meta l4proto { tcp , udp }",
		"th dport 53 meta mark set 0x1",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("TUN commands do not contain %q:\n%s", expected, joined)
		}
	}
	if strings.Contains(joined, "tproxy") {
		t.Fatalf("TUN mode emitted TPROXY rules:\n%s", joined)
	}
	if strings.Index(joined, "ip6 daddr fc00::/18") > strings.Index(joined, "ip6 daddr fc00::/7 return") {
		t.Fatalf("IPv6 Fake-IP capture must precede the ULA bypass:\n%s", joined)
	}
	if err := supervisor.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanup := strings.Join(runner.commands[appliedCount:], "\n")
	for _, expected := range []string{
		"ip route del default dev easyproxy0 table 100",
		"ip -6 route del default dev easyproxy0 table 101",
		"ip rule del priority 110 fwmark 0x1/0x1 lookup 100",
		"ip -6 rule del priority 110 fwmark 0x1/0x1 lookup 101",
		"nft delete table inet easyproxy_gateway",
	} {
		if !strings.Contains(cleanup, expected) {
			t.Fatalf("TUN cleanup does not contain %q:\n%s", expected, cleanup)
		}
	}
}

func TestTunSupervisorCanDisableGeneralUDPWithoutDisablingDNSHijack(t *testing.T) {
	commands := buildTunGatewayCommands(config.GatewayConfig{
		Mode:    "tun",
		Ingress: config.GatewayIngressConfig{TrustedCIDRs: []string{"192.0.2.0/24"}},
		DNS:     config.GatewayDNSConfig{Enabled: true},
		Tun: config.GatewayTunConfig{
			InterfaceName: "easyproxy0",
			IPv4:          true,
			DNSHijack:     true,
		},
	})
	joined := strings.Join(formatCommands(commands), "\n")
	if !strings.Contains(joined, "meta l4proto { tcp , udp } th dport 53") {
		t.Fatalf("DNS UDP capture is absent:\n%s", joined)
	}
	if !strings.Contains(joined, "ip saddr 192.0.2.0/24 meta l4proto tcp meta mark set 0x1") {
		t.Fatalf("TCP-only general capture is absent:\n%s", joined)
	}
}

func formatCommands(commands []gatewayCommand) []string {
	formatted := make([]string, 0, len(commands))
	for _, command := range commands {
		formatted = append(formatted, command.name+" "+strings.Join(command.args, " "))
	}
	return formatted
}
