package pool

import (
	"net"
)

func (p *poolOutbound) shouldSkipProbeTLSVerify() bool {
	return p != nil && p.monitor != nil && p.monitor.SkipCertVerify()
}

func (p *poolOutbound) recordFailure(member *memberState, cause error, destination string) {
	if member.shared == nil {
		p.logger.Warn("proxy ", member.tag, " failure (no shared state): ", cause)
		return
	}
	failures, blacklisted, _ := member.shared.recordFailure(cause, p.options.FailureThreshold, p.options.BlacklistDuration, destination)
	if blacklisted {
		p.logger.Warn("proxy ", member.tag, " blacklisted for ", p.options.BlacklistDuration, ": ", cause)
	} else {
		p.logger.Warn("proxy ", member.tag, " failure ", failures, "/", p.options.FailureThreshold, ": ", cause)
	}
}

func (p *poolOutbound) recordSuccess(member *memberState, destination string) {
	if member.shared != nil {
		member.shared.recordSuccess(destination)
	}
}

func (p *poolOutbound) wrapConn(conn net.Conn, member *memberState, destination string) net.Conn {
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
			p.recordSuccess(member, destination)
		},
		onUnconfirmedFailure: func(cause error) {
			p.recordFailure(member, cause, destination)
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
			p.recordSuccess(member, destination)
		},
		onUnconfirmedFailure: func(cause error) {
			p.recordFailure(member, cause, destination)
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
