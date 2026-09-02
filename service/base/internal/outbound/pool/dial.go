package pool

import (
	"context"
	"net"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func (p *poolOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	maxRetries := p.options.MaxRetries
	var lastErr error
	excluded := make(map[string]struct{})
	dst := destination.String()
	directive := DirectiveFrom(ctx)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			break
		}
		member, err := p.pickMember(network, excluded, directive)
		if err != nil {
			break
		}
		excluded[member.tag] = struct{}{}

		if attempt > 0 {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [", network, "] (retry ", attempt, "/", maxRetries, ")")
		} else {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [", network, "]")
		}

		p.incActive(member)
		conn, err := member.outbound.DialContext(ctx, network, destination)
		if err != nil {
			p.decActive(member)
			p.recordFailure(member, network, err, dst)
			lastErr = err
			continue
		}
		return p.wrapConn(conn, member, network, dst), nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, E.New("no healthy proxy available")
}

func (p *poolOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	maxRetries := p.options.MaxRetries
	var lastErr error
	excluded := make(map[string]struct{})
	dst := destination.String()
	directive := DirectiveFrom(ctx)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			break
		}
		member, err := p.pickMember(N.NetworkUDP, excluded, directive)
		if err != nil {
			break
		}
		excluded[member.tag] = struct{}{}

		if attempt > 0 {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [udp] (retry ", attempt, "/", maxRetries, ")")
		} else {
			p.logger.Info("→ ", dst, " ⇒ ", p.memberName(member), " [udp]")
		}

		p.incActive(member)
		conn, err := member.outbound.ListenPacket(ctx, destination)
		if err != nil {
			p.decActive(member)
			p.recordFailure(member, N.NetworkUDP, err, dst)
			lastErr = err
			continue
		}
		return p.wrapPacketConn(conn, member, dst), nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, E.New("no healthy proxy available")
}
