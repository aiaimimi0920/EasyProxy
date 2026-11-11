package pool

import (
	"context"
	"net"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func (p *poolOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ctx, directive, maxRetries, cancel := p.selectionDialBudget(ctx)
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	var lastErr error
	excluded := make(map[string]struct{})
	dst := destination.String()
	outboundCtx := memberDialContext(ctx)

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
		attemptCtx, attemptCancel := selectionAttemptContext(outboundCtx, directive)
		conn, err := member.outbound.DialContext(attemptCtx, network, destination)
		if err == nil && hasSelectionTiers(directive) {
			err = completeTieredTCPSetup(attemptCtx, network, member.outbound.Type(), conn)
			if err == nil {
				err = disarmSelectionSetup(attemptCtx)
			}
			if err == nil {
				err = disarmSelectionSetup(ctx)
			}
			if err != nil {
				conn.Close()
			}
		}
		if err != nil {
			attemptCancel()
			p.decActive(member)
			if hasSelectionTiers(directive) && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			p.recordFailure(member, network, err, dst)
			lastErr = err
			continue
		}
		if hasSelectionTiers(directive) {
			ownerCancel := cancel
			cancel = nil
			conn = &selectionOwnedConn{Conn: conn, cancel: func() { attemptCancel(); ownerCancel() }}
		} else {
			attemptCancel()
		}
		return p.wrapConn(conn, member, network, dst), nil
	}

	if hasSelectionTiers(directive) && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, E.New("no healthy proxy available")
}

func (p *poolOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ctx, directive, maxRetries, cancel := p.selectionDialBudget(ctx)
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	var lastErr error
	excluded := make(map[string]struct{})
	dst := destination.String()
	outboundCtx := memberDialContext(ctx)

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
		attemptCtx, attemptCancel := selectionAttemptContext(outboundCtx, directive)
		conn, err := member.outbound.ListenPacket(attemptCtx, destination)
		if err == nil && hasSelectionTiers(directive) {
			err = disarmSelectionSetup(attemptCtx)
			if err == nil {
				err = disarmSelectionSetup(ctx)
			}
			if err != nil {
				conn.Close()
			}
		}
		if err != nil {
			attemptCancel()
			p.decActive(member)
			if hasSelectionTiers(directive) && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			p.recordFailure(member, N.NetworkUDP, err, dst)
			lastErr = err
			continue
		}
		if hasSelectionTiers(directive) {
			ownerCancel := cancel
			cancel = nil
			conn = &selectionOwnedPacketConn{PacketConn: conn, cancel: func() { attemptCancel(); ownerCancel() }}
		} else {
			attemptCancel()
		}
		return p.wrapPacketConn(conn, member, dst), nil
	}

	if hasSelectionTiers(directive) && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, E.New("no healthy proxy available")
}

// Each configured tier gets at most two unique candidates. This reserves retry
// capacity for lower tiers even when the preferred country contains many nodes.
const (
	selectionTierAttempts   = 2
	selectionDialTimeout    = 30 * time.Second
	selectionAttemptTimeout = 5 * time.Second
)

func (p *poolOutbound) selectionDialBudget(ctx context.Context) (context.Context, *SelectionDirective, int, context.CancelFunc) {
	d := DirectiveFrom(ctx)
	if !hasSelectionTiers(d) {
		return ctx, d, p.options.MaxRetries, func() {}
	}
	next := *d
	next.tierAttempts = new([3]int)
	ctx, cancel := newSelectionSetupContext(ctx, selectionDialTimeout)
	return ctx, &next, (min(len(d.SelectionTiers), 2)+1)*selectionTierAttempts - 1, cancel
}

func selectionAttemptContext(ctx context.Context, d *SelectionDirective) (context.Context, context.CancelFunc) {
	if !hasSelectionTiers(d) {
		return ctx, func() {}
	}
	return newSelectionSetupContext(ctx, selectionAttemptTimeout)
}
