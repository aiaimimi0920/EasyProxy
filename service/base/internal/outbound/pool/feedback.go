package pool

import (
	"fmt"
	"net"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

func (p *poolOutbound) shouldSkipProbeTLSVerify() bool {
	return p != nil && p.monitor != nil && p.monitor.SkipCertVerify()
}

func upstreamProxyError(outbound adapter.Outbound, cause error) error {
	// sing's HTTP client returns this exact error only for a CONNECT 407.
	// Do not classify destination 401/403 responses or arbitrary auth messages.
	if outbound != nil && outbound.Type() == "http" && cause != nil && cause.Error() == "authentication required" {
		return fmt.Errorf("%w: %v", monitor.ErrUpstreamAuthenticationRequired, cause)
	}
	return cause
}

func (p *poolOutbound) recordFailure(member *memberState, network string, cause error, destination string) {
	cause = upstreamProxyError(member.outbound, cause)
	if member.shared == nil {
		p.logger.Warn("proxy ", member.tag, " failure (no shared state): ", cause)
		return
	}
	failures, blacklisted, _ := member.shared.recordTransportFailure(network, cause, p.options.FailureThreshold, p.options.BlacklistDuration, destination)
	if blacklisted {
		p.logger.Warn("proxy ", member.tag, " blacklisted for ", p.options.BlacklistDuration, ": ", cause)
	} else {
		p.logger.Warn("proxy ", member.tag, " failure ", failures, "/", p.options.FailureThreshold, ": ", cause)
	}
}

func (p *poolOutbound) recordSuccess(member *memberState, network, destination string) {
	if member.shared != nil {
		member.shared.recordTransportSuccess(network, destination)
	}
}

func (p *poolOutbound) wrapConn(conn net.Conn, member *memberState, network, destination string) net.Conn {
	return &trackedConn{
		Conn: conn,
		release: func() {
			p.decActive(member)
		},
		onTraffic: func(upload, download int64) {
			if member.shared != nil {
				member.shared.addTraffic(upload, download)
			}
		},
		onConfirmedSuccess: func() {
			p.recordSuccess(member, network, destination)
		},
		onUnconfirmedFailure: func(cause error) {
			p.recordFailure(member, network, cause, destination)
		},
	}
}

func (p *poolOutbound) wrapPacketConn(conn net.PacketConn, member *memberState, destination string) net.PacketConn {
	return &trackedPacketConn{
		PacketConn: conn,
		release: func() {
			p.decActive(member)
		},
		onTraffic: func(upload, download int64) {
			if member.shared != nil {
				member.shared.addTraffic(upload, download)
			}
		},
		onConfirmedSuccess: func() {
			p.recordSuccess(member, N.NetworkUDP, destination)
		},
		onUnconfirmedFailure: func(cause error) {
			p.recordFailure(member, N.NetworkUDP, cause, destination)
		},
	}
}

func (p *poolOutbound) makeReleaseFunc(member *memberState) func() {
	return func() {
		if member.shared != nil {
			member.shared.forceRelease()
		}
	}
}

// httpProbe performs an HTTP probe through the connection and measures unified
// delay using the same warm keep-alive request semantics as Mihomo URLTest.
