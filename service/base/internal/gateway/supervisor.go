package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"easy_proxies/internal/config"
)

var ErrUnsupported = errors.New("transparent gateway is unsupported on this platform")

// CommandRunner isolates ip/nft execution from tests and from the listener
// lifecycle. Production uses ExecRunner; tests record commands in memory.
type CommandRunner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil && len(output) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return err
}

type gatewayCommand struct {
	name string
	args []string
}

// Supervisor owns every host networking rule installed for one gateway
// generation. It is intentionally independent of Docker or overlay vendors.
type Supervisor struct {
	runner  CommandRunner
	mu      sync.Mutex
	applied bool
	cleanup []gatewayCommand
}

func NewSupervisor(runner CommandRunner) *Supervisor {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Supervisor{runner: runner}
}

func (s *Supervisor) Apply(ctx context.Context, cfg config.GatewayConfig) error {
	if s == nil || !cfg.Enabled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applied {
		return nil
	}
	commands := buildGatewayCommands(cfg)
	ownedCleanup := make([]gatewayCommand, 0, 3)
	for _, command := range commands {
		created, err := s.runIdempotent(ctx, command)
		if err != nil {
			_ = s.stopLocked(ctx, ownedCleanup)
			return fmt.Errorf("apply gateway rule %s %v: %w", command.name, command.args, err)
		}
		if created {
			ownedCleanup = append(ownedCleanup, buildGatewayCleanup([]gatewayCommand{command})...)
		}
	}
	s.cleanup = ownedCleanup
	s.applied = true
	return nil
}

func (s *Supervisor) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked(ctx, s.cleanup)
}

func (s *Supervisor) stopLocked(ctx context.Context, cleanup []gatewayCommand) error {
	if !s.applied && len(cleanup) == 0 {
		return nil
	}
	var firstErr error
	for idx := len(cleanup) - 1; idx >= 0; idx-- {
		if _, err := s.runIdempotent(ctx, cleanup[idx]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.cleanup = nil
	s.applied = false
	return firstErr
}

// runIdempotent reports whether this invocation changed host state. Accepted
// already-present or already-absent state is not owned by this generation.
func (s *Supervisor) runIdempotent(ctx context.Context, command gatewayCommand) (bool, error) {
	err := s.runner.Run(ctx, command.name, command.args...)
	if err == nil {
		return true, nil
	}
	if isIdempotentNetworkError(err) {
		return false, nil
	}
	return false, err
}

func isIdempotentNetworkError(err error) bool {
	if err == nil {
		return true
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(message, "executable file not found") {
		return false
	}
	return strings.Contains(message, "already exists") ||
		strings.Contains(message, "file exists") ||
		strings.Contains(message, "no such file") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "cannot find")
}

func buildGatewayCommands(cfg config.GatewayConfig) []gatewayCommand {
	if strings.EqualFold(strings.TrimSpace(cfg.Mode), "tun") {
		return buildTunGatewayCommands(cfg)
	}
	return buildTransparentGatewayCommands(cfg)
}

func buildTransparentGatewayCommands(cfg config.GatewayConfig) []gatewayCommand {
	port := gatewayPort(cfg.Listen)
	commands := []gatewayCommand{
		{name: "nft", args: []string{"delete", "table", "inet", "easyproxy_gateway"}},
		{name: "ip", args: []string{"rule", "add", "fwmark", "0x1/0x1", "lookup", "100"}},
		{name: "ip", args: []string{"route", "add", "local", "0.0.0.0/0", "dev", "lo", "table", "100"}},
		{name: "nft", args: []string{"add", "table", "inet", "easyproxy_gateway"}},
		{name: "nft", args: []string{"add", "chain", "inet", "easyproxy_gateway", "prerouting", "{", "type", "filter", "hook", "prerouting", "priority", "mangle;", "policy", "accept;", "}"}},
		{name: "nft", args: []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting", "meta", "mark", "0x1", "return"}},
	}
	commands = append(commands, gatewayCommand{
		name: "nft",
		args: []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting", "tcp", "dport", "{", port, ",", "22323", ",", "29888", "}", "return"},
	})
	for _, privateCIDR := range []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"} {
		commands = append(commands, gatewayCommand{
			name: "nft",
			args: []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting", "ip", "daddr", privateCIDR, "return"},
		})
	}
	for _, iface := range cfg.Ingress.Interfaces {
		for _, cidr := range cfg.Ingress.TrustedCIDRs {
			commands = appendCaptureRule(commands, "iifname", iface, cidr, port)
		}
	}
	for _, pattern := range cfg.Ingress.InterfacePatterns {
		for _, cidr := range cfg.Ingress.TrustedCIDRs {
			commands = appendCaptureRule(commands, "iifname", pattern, cidr, port)
		}
	}
	if len(cfg.Ingress.Interfaces) == 0 && len(cfg.Ingress.InterfacePatterns) == 0 {
		for _, cidr := range cfg.Ingress.TrustedCIDRs {
			commands = appendCaptureRule(commands, "", "", cidr, port)
		}
	}
	return commands
}

func appendCaptureRule(commands []gatewayCommand, qualifier, value, cidr, port string) []gatewayCommand {
	args := []string{"add", "rule", "inet", "easyproxy_gateway", "prerouting"}
	if qualifier != "" {
		args = append(args, qualifier, value)
	}
	args = append(args,
		"ip", "saddr", cidr,
		"meta", "l4proto", "tcp",
		"meta", "mark", "set", "0x1",
		"tproxy", "ip", "to", ":"+port,
	)
	return append(commands, gatewayCommand{name: "nft", args: args})
}

func gatewayPort(listen string) string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "15001"
	}
	if value, err := strconv.Atoi(port); err == nil && value > 0 && value <= 65535 {
		return strconv.Itoa(value)
	}
	return "15001"
}

func buildGatewayCleanup(commands []gatewayCommand) []gatewayCommand {
	cleanup := make([]gatewayCommand, 0, len(commands))
	for _, command := range commands {
		switch {
		case command.name == "nft" && len(command.args) >= 3 && command.args[0] == "add" && command.args[1] == "table":
			cleanup = append(cleanup, gatewayCommand{name: "nft", args: []string{"delete", "table", "inet", "easyproxy_gateway"}})
		case command.name == "ip":
			if inverse, ok := tunIPCleanup(command.args); ok {
				cleanup = append(cleanup, gatewayCommand{name: "ip", args: inverse})
			}
		}
	}
	return cleanup
}
