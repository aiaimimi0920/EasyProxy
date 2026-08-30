package pool

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const maxProbeFallbackReserve = 5 * time.Second

func httpProbe(conn net.Conn, destination M.Socksaddr, skipCertVerify ...bool) (time.Duration, error) {
	return httpProbeTarget(conn, monitor.ProbeTargetSpec{
		Scheme:  map[bool]string{true: "https", false: "http"}[destination.Port == 443],
		Host:    destination.AddrString(),
		Port:    destination.Port,
		Path:    "/generate_204",
		HostHdr: destination.AddrString(),
		Dst:     destination,
	}, skipCertVerify...)
}

func httpProbeTarget(conn net.Conn, target monitor.ProbeTargetSpec, skipCertVerify ...bool) (time.Duration, error) {
	return httpProbeTargetContext(context.Background(), conn, target, skipCertVerify...)
}

func httpProbeTargetContext(ctx context.Context, conn net.Conn, target monitor.ProbeTargetSpec, skipCertVerify ...bool) (time.Duration, error) {
	stopContextInterrupt := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer stopContextInterrupt()
	_ = conn.SetDeadline(probeOperationDeadline(ctx, 10*time.Second))
	defer conn.SetDeadline(time.Time{})
	probeConn := conn
	host := target.Host
	hostHeader := target.HostHdr
	if hostHeader == "" {
		hostHeader = target.Host
	}
	if target.Scheme == "https" {
		serverName := target.Dst.Fqdn
		if serverName == "" {
			serverName = host
		}
		insecure := len(skipCertVerify) > 0 && skipCertVerify[0]
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: insecure,
		})
		if err := tlsConn.Handshake(); err != nil {
			return 0, fmt.Errorf("tls handshake: %w", err)
		}
		probeConn = tlsConn
	}

	path := target.Path
	if path == "" {
		path = "/"
	}
	req := fmt.Sprintf("HEAD %s HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\nUser-Agent: EasyProxy-URLTest/1.0\r\nAccept: */*\r\n\r\n", path, hostHeader)
	reader := bufio.NewReader(probeConn)
	firstDuration, firstStatus, err := performHTTPProbeRequest(ctx, probeConn, reader, req)
	if err != nil {
		return 0, err
	}
	secondDuration, secondStatus, secondErr := performHTTPProbeRequest(ctx, probeConn, reader, req)
	if secondErr == nil {
		if err := validateProbeStatus(target, secondStatus); err != nil {
			return 0, err
		}
		return secondDuration, nil
	}
	if err := validateProbeStatus(target, firstStatus); err != nil {
		return 0, err
	}
	return firstDuration, nil
}

func performHTTPProbeRequest(ctx context.Context, conn net.Conn, reader *bufio.Reader, request string) (time.Duration, int, error) {
	_ = conn.SetWriteDeadline(probeOperationDeadline(ctx, 5*time.Second))
	start := time.Now()
	if _, err := conn.Write([]byte(request)); err != nil {
		return 0, 0, fmt.Errorf("write request: %w", err)
	}
	_ = conn.SetReadDeadline(probeOperationDeadline(ctx, 10*time.Second))
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, 0, fmt.Errorf("read response: %w", err)
	}
	duration := time.Since(start)
	parts := strings.Fields(strings.TrimSpace(statusLine))
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("invalid status line: %q", strings.TrimSpace(statusLine))
	}
	var status int
	if _, err := fmt.Sscanf(parts[1], "%d", &status); err != nil {
		return 0, 0, fmt.Errorf("parse status line %q: %w", strings.TrimSpace(statusLine), err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, 0, fmt.Errorf("read response headers: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return duration, status, nil
}

func probeOperationDeadline(ctx context.Context, maximum time.Duration) time.Time {
	deadline := time.Now().Add(maximum)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func nextProbeTargetContext(parent context.Context, remainingTargets int) (context.Context, context.CancelFunc) {
	deadline, hasDeadline := parent.Deadline()
	if !hasDeadline || remainingTargets <= 1 {
		return context.WithCancel(parent)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithCancel(parent)
	}
	evenBudget := remaining / time.Duration(remainingTargets)
	fallbackReserve := remaining - evenBudget
	// Long-lived connectors need more than an even slice, while fallbacks still
	// need a bounded chance to run before the node deadline.
	if fallbackReserve > maxProbeFallbackReserve {
		fallbackReserve = maxProbeFallbackReserve
	}
	return context.WithTimeout(parent, remaining-fallbackReserve)
}

func validateProbeStatus(target monitor.ProbeTargetSpec, status int) error {
	if probeTargetRequiresStrictSuccessStatus(target) {
		if status < 200 || status >= 400 {
			return fmt.Errorf("unexpected HTTP status %d from %s", status, target.Original)
		}
	} else if status < 200 || status >= 500 {
		return fmt.Errorf("unexpected HTTP status %d from %s", status, target.Original)
	}
	return nil
}

func probeTargetRequiresStrictSuccessStatus(target monitor.ProbeTargetSpec) bool {
	host := strings.ToLower(strings.TrimSpace(target.Host))
	path := strings.ToLower(strings.TrimSpace(target.Path))
	switch host {
	case "auth.openai.com":
		return path == "" || strings.HasPrefix(path, "/")
	case "platform.openai.com":
		return strings.HasPrefix(path, "/login")
	case "chatgpt.com", "www.chatgpt.com":
		return strings.HasPrefix(path, "/auth/")
	default:
		return false
	}
}

func (p *poolOutbound) makeProbeFunc(member *memberState) func(ctx context.Context) (time.Duration, error) {
	if p.monitor == nil {
		return nil
	}
	return func(ctx context.Context) (time.Duration, error) {
		// 每次执行时动态获取最新的探测目标
		targets, ok := p.monitor.ProbeTargets()
		if !ok {
			return 0, E.New("probe target not configured")
		}

		duration, err := p.runProbeTargetsForMember(ctx, member, targets)
		return duration, err
	}
}

// makeProbeByTagFunc creates a probe function that works before member initialization
func (p *poolOutbound) makeProbeByTagFunc(tag string) func(ctx context.Context) (time.Duration, error) {
	if p.monitor == nil {
		return nil
	}
	return func(ctx context.Context) (time.Duration, error) {
		// 每次执行时动态获取最新的探测目标
		targets, ok := p.monitor.ProbeTargets()
		if !ok {
			return 0, E.New("probe target not configured")
		}

		// Ensure members are initialized
		p.mu.Lock()
		if len(p.members) == 0 {
			if err := p.initializeMembersLocked(); err != nil {
				p.mu.Unlock()
				return 0, err
			}
		}

		// Find the member by tag
		var member *memberState
		for _, m := range p.members {
			if m.tag == tag {
				member = m
				break
			}
		}
		p.mu.Unlock()

		if member == nil {
			return 0, E.New("member not found: ", tag)
		}

		duration, err := p.runProbeTargetsForMember(ctx, member, targets)
		return duration, err
	}
}

func probeTargetLabels(targets []monitor.ProbeTargetSpec) []string {
	labels := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Original != "" {
			labels = append(labels, target.Original)
			continue
		}
		labels = append(labels, target.Dst.String())
	}
	return labels
}

func normalizeLocalProbeHost(host string) string {
	trimmed := strings.TrimSpace(host)
	switch trimmed {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return trimmed
	}
}

func (p *poolOutbound) memberProbeProxyAddress(member *memberState) string {
	if member == nil {
		return ""
	}
	meta, ok := p.options.Metadata[member.tag]
	if !ok || meta.Port == 0 {
		return ""
	}
	// Pool-mode members all advertise the same shared listener. Probing through
	// it selects an arbitrary pool member, so the result cannot describe the
	// member currently being checked. Only dedicated per-node listeners may be
	// used as the preferred probe path; pool members must use their raw outbound.
	if strings.EqualFold(strings.TrimSpace(meta.Mode), "pool") {
		return ""
	}
	host := normalizeLocalProbeHost(meta.ListenAddress)
	return net.JoinHostPort(host, strconv.Itoa(int(meta.Port)))
}

func (p *poolOutbound) runProbeTargetsForMember(ctx context.Context, member *memberState, targets []monitor.ProbeTargetSpec) (time.Duration, error) {
	if member != nil && member.outbound != nil {
		return p.runProbeTargets(ctx, member.outbound, targets)
	}
	if proxyAddress := p.memberProbeProxyAddress(member); proxyAddress != "" {
		return p.runProbeTargetsViaHTTPProxy(ctx, proxyAddress, targets)
	}
	return 0, E.New("member probe failed: missing outbound and local proxy metadata")
}

func dialContextTCP(ctx context.Context, address string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return dialer.DialContext(ctx, "tcp", address)
}

func connectHTTPProxy(ctx context.Context, conn net.Conn, target monitor.ProbeTargetSpec) error {
	stopContextInterrupt := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer stopContextInterrupt()

	host := target.Host
	if host == "" {
		host = target.Dst.AddrString()
	}
	port := target.Port
	if port == 0 {
		port = target.Dst.Port
	}
	authority := net.JoinHostPort(host, strconv.Itoa(int(port)))
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\nUser-Agent: EasyProxy-Probe/1.0\r\n\r\n", authority, authority)

	_ = conn.SetWriteDeadline(probeOperationDeadline(ctx, 5*time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("write CONNECT request: %w", err)
	}

	_ = conn.SetReadDeadline(probeOperationDeadline(ctx, 10*time.Second))
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read CONNECT response: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(statusLine))
	if len(parts) < 2 {
		return fmt.Errorf("invalid CONNECT status line: %q", strings.TrimSpace(statusLine))
	}
	var status int
	if _, err := fmt.Sscanf(parts[1], "%d", &status); err != nil {
		return fmt.Errorf("parse CONNECT status %q: %w", strings.TrimSpace(statusLine), err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("unexpected CONNECT status %d for %s", status, authority)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read CONNECT headers: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	_ = conn.SetDeadline(time.Time{})
	return nil
}

func (p *poolOutbound) runProbeTargetsViaHTTPProxy(ctx context.Context, proxyAddress string, targets []monitor.ProbeTargetSpec) (time.Duration, error) {
	var errs []string
	for index, target := range targets {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err.Error())
			break
		}
		targetCtx, cancel := nextProbeTargetContext(ctx, len(targets)-index)
		start := time.Now()
		conn, err := dialContextTCP(targetCtx, proxyAddress)
		if err != nil {
			cancel()
			errs = append(errs, fmt.Sprintf("%s proxy dial: %v", target.Original, err))
			continue
		}
		err = connectHTTPProxy(targetCtx, conn, target)
		if err != nil {
			conn.Close()
			cancel()
			errs = append(errs, fmt.Sprintf("%s proxy connect: %v", target.Original, err))
			continue
		}
		if target.Scheme == "tcp" {
			conn.Close()
			cancel()
			return time.Since(start), nil
		}
		duration, err := httpProbeTargetContext(targetCtx, conn, target, p.shouldSkipProbeTLSVerify())
		conn.Close()
		cancel()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s proxy probe: %v", target.Original, err))
			continue
		}
		return duration, nil
	}
	return 0, E.New("all proxy probe targets failed: ", strings.Join(errs, " | "))
}

func (p *poolOutbound) runProbeTargets(ctx context.Context, outbound adapter.Outbound, targets []monitor.ProbeTargetSpec) (time.Duration, error) {
	var errs []string
	for index, target := range targets {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err.Error())
			break
		}
		targetCtx, cancel := nextProbeTargetContext(ctx, len(targets)-index)
		start := time.Now()
		conn, err := outbound.DialContext(targetCtx, N.NetworkTCP, target.Dst)
		if err != nil {
			cancel()
			errs = append(errs, fmt.Sprintf("%s dial: %v", target.Original, err))
			continue
		}
		if target.Scheme == "tcp" {
			conn.Close()
			cancel()
			return time.Since(start), nil
		}
		duration, err := httpProbeTargetContext(targetCtx, conn, target, p.shouldSkipProbeTLSVerify())
		conn.Close()
		cancel()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s probe: %v", target.Original, err))
			continue
		}
		return duration, nil
	}
	return 0, E.New("all probe targets failed: ", strings.Join(errs, " | "))
}
