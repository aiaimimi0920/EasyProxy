package pool

import (
	"context"
	"net"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

type retainedContextConn struct {
	net.Conn
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *retainedContextConn) Write(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (c *retainedContextConn) Close() error { c.cancel(); return nil }

type retainedContextPacket struct {
	net.PacketConn
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *retainedContextPacket) WriteTo(b []byte, _ net.Addr) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (c *retainedContextPacket) Close() error { c.cancel(); return nil }

type retainedContextOutbound struct {
	failingOutbound
	retained *context.Context
}

func (o retainedContextOutbound) Network() []string { return []string{"tcp", "udp"} }
func (o retainedContextOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	*o.retained = ctx
	return &retainedContextConn{ctx: ctx, cancel: cancel}, nil
}
func (o retainedContextOutbound) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	*o.retained = ctx
	return &retainedContextPacket{ctx: ctx, cancel: cancel}, nil
}

func TestTierSuccessfulTransportRetainsLiveContext(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			p := tierTestPool(t)
			p.logger = log.NewNOPFactory().Logger()
			var retained context.Context
			for _, m := range p.members {
				m.outbound = retainedContextOutbound{retained: &retained}
			}
			parent, parentCancel := context.WithCancel(context.Background())
			defer parentCancel()
			d := &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "JP"}}}
			ctx := WithDirective(parent, d)
			var closeConn func() error
			var write func() error
			if network == "tcp" {
				c, err := p.DialContext(ctx, network, M.Socksaddr{})
				if err != nil {
					t.Fatal(err)
				}
				closeConn = c.Close
				write = func() error { _, err := c.Write([]byte("alive")); return err }
			} else {
				c, err := p.ListenPacket(ctx, M.Socksaddr{})
				if err != nil {
					t.Fatal(err)
				}
				closeConn = c.Close
				write = func() error { _, err := c.WriteTo([]byte("alive"), &net.UDPAddr{}); return err }
			}
			if err := write(); err != nil {
				t.Fatalf("returned transport cancelled: %v", err)
			}
			if deadline, ok := retained.Deadline(); !ok || time.Until(deadline) < 50*time.Minute {
				t.Fatal("setup deadline retained by successful transport")
			}
			if retained.Err() != nil {
				t.Fatal("context cancelled on return")
			}
			if err := closeConn(); err != nil {
				t.Fatal(err)
			}
			if retained.Err() != context.Canceled {
				t.Fatal("close did not release context ownership")
			}
		})
	}
}

func TestSelectionSetupDisarmAndCancellation(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	c, cancel := newSelectionSetupContext(parent, 10*time.Millisecond)
	defer cancel()
	if err := c.disarm(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if c.Err() != nil {
		t.Fatal("disarmed setup timeout cancelled live relay")
	}
	parentCancel()
	if c.Err() != context.Canceled {
		t.Fatal("parent cancellation not propagated")
	}
	expired, expiredCancel := newSelectionSetupContext(context.Background(), time.Millisecond)
	defer expiredCancel()
	<-expired.Done()
	if expired.Err() != context.DeadlineExceeded || expired.disarm() != context.DeadlineExceeded {
		t.Fatal("expired setup revived")
	}
}

func TestTierAlreadyCanceledReturnsContextError(t *testing.T) {
	p := tierTestPool(t)
	ctx, cancel := context.WithCancel(WithDirective(context.Background(), &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "latency"}}}))
	cancel()
	if _, err := p.DialContext(ctx, "tcp", M.Socksaddr{}); err != context.Canceled {
		t.Fatalf("dial err=%v", err)
	}
	if _, err := p.ListenPacket(ctx, M.Socksaddr{}); err != context.Canceled {
		t.Fatalf("packet err=%v", err)
	}
}

func TestSelectionSetupDoesNotLeakDeadlineToRetainedChild(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Hour)
	defer parentCancel()
	setup, cancel := newSelectionSetupContext(parent, 10*time.Millisecond)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	before, ok := setup.Deadline()
	if !ok || before != parentDeadline {
		t.Fatal("setup-only deadline advertised")
	}
	// Models an outbound constructing a context for its own long-lived relay.
	child, childCancel := context.WithTimeout(setup, time.Minute)
	defer childCancel()
	deadline, _ := child.Deadline()
	if time.Until(deadline) < 50*time.Second {
		t.Fatal("relay captured setup deadline")
	}
	if err := setup.disarm(); err != nil {
		t.Fatal(err)
	}
	after, ok := setup.Deadline()
	if !ok || after != before {
		t.Fatal("context Deadline changed after disarm")
	}
	time.Sleep(30 * time.Millisecond)
	if child.Err() != nil {
		t.Fatalf("retained relay expired: %v", child.Err())
	}
	parentCancel()
	<-child.Done()
	if child.Err() != context.Canceled {
		t.Fatalf("parent cancellation lost: %v", child.Err())
	}
}
