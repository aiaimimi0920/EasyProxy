package pool

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"easy_proxies/internal/config"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
)

// Models the pinned Hysteria2 contract: empty writes/reads perform framing only.
// Application payload is deliberately unavailable to the pool during setup.
type lazyHandshakeConn struct {
	net.Conn
	closed          chan struct{}
	once            sync.Once
	responseErr     error
	stall           bool
	requestSent     bool
	responseRead    bool
	application     []byte
	banner          []byte
	failApplication bool
}

func newLazyHandshakeConn() *lazyHandshakeConn {
	return &lazyHandshakeConn{closed: make(chan struct{})}
}
func (c *lazyHandshakeConn) NeedHandshake() bool { return !c.requestSent }
func (c *lazyHandshakeConn) Close() error        { c.once.Do(func() { close(c.closed) }); return nil }
func (c *lazyHandshakeConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		c.requestSent = true
		return 0, nil
	}
	if c.failApplication {
		return 0, io.ErrClosedPipe
	}
	c.application = append(c.application, b...)
	return len(b), nil
}
func (c *lazyHandshakeConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		if !c.requestSent {
			return 0, errors.New("request not sent")
		}
		if c.stall {
			<-c.closed
			return 0, net.ErrClosed
		}
		c.responseRead = c.responseErr == nil
		return 0, c.responseErr
	}
	n := copy(b, c.banner)
	c.banner = c.banner[n:]
	return n, nil
}

func TestTieredHysteriaHandshakeDoesNotConsumeApplicationData(t *testing.T) {
	c := newLazyHandshakeConn()
	c.banner = []byte("server-first greeting")
	ctx, cancel := context.WithCancel(context.Background())
	if err := completeTieredTCPSetup(ctx, "tcp", C.TypeHysteria2, c); err != nil {
		t.Fatal(err)
	}
	if !c.requestSent || !c.responseRead || len(c.application) != 0 {
		t.Fatal("handshake not isolated from application")
	}
	if string(c.banner) != "server-first greeting" {
		t.Fatal("consumed server data")
	}
	cancel()
	select {
	case <-c.closed:
		t.Fatal("completed handshake retained cancellation callback")
	default:
	}
}

func TestTieredHysteriaHandshakeIsBoundedBySetupCancellation(t *testing.T) {
	c := newLazyHandshakeConn()
	c.stall = true
	ctx, cancel := newSelectionSetupContext(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := completeTieredTCPSetup(ctx, "tcp", C.TypeHysteria2, c); err != context.DeadlineExceeded {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("lazy handshake escaped setup budget")
	}
	select {
	case <-c.closed:
	default:
		t.Fatal("stalled stream not closed")
	}
}

func TestTieredHandshakeDoesNotGeneralizeZeroLengthIO(t *testing.T) {
	for _, tc := range []struct{ network, protocol string }{{"udp", C.TypeHysteria2}, {"tcp", "tuic"}, {"tcp", "http"}} {
		c := newLazyHandshakeConn()
		if err := completeTieredTCPSetup(context.Background(), tc.network, tc.protocol, c); err != nil {
			t.Fatal(err)
		}
		if c.requestSent || c.responseRead {
			t.Fatal("touched unaudited protocol")
		}
	}
}

type lazyHandshakeOutbound struct {
	failingOutbound
	conn  *lazyHandshakeConn
	calls *int
}

func (o lazyHandshakeOutbound) Type() string { return C.TypeHysteria2 }
func (o lazyHandshakeOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	*o.calls++
	return o.conn, nil
}

func TestTieredHysteriaFirstFlowFallsThroughBeforeApplicationWrite(t *testing.T) {
	p := tierTestPool(t)
	p.logger = log.NewNOPFactory().Logger()
	p.members = []*memberState{p.members[0], p.members[3]}
	failed, ready := newLazyHandshakeConn(), newLazyHandshakeConn()
	failed.responseErr = errors.New("stale QUIC session")
	var calls int
	p.members[0].outbound = lazyHandshakeOutbound{conn: failed, calls: &calls}
	p.members[1].outbound = lazyHandshakeOutbound{conn: ready, calls: &calls}
	d := &SelectionDirective{SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "JP"}, {Kind: "country", Country: "US"}}}
	conn, err := p.DialContext(WithDirective(context.Background(), d), "tcp", M.ParseSocksaddr("example.com:443"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if calls != 2 || !ready.responseRead {
		t.Fatalf("calls=%d ready=%v", calls, ready.responseRead)
	}
	select {
	case <-failed.closed:
	default:
		t.Fatal("failed stream leaked")
	}
	payload := []byte("non-idempotent application payload")
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if len(failed.application) != 0 || string(ready.application) != string(payload) {
		t.Fatal("application replayed or lost")
	}
	ready.failApplication = true
	if _, err = conn.Write([]byte("later failure")); err == nil {
		t.Fatal("expected application failure")
	}
	if calls != 2 || len(failed.application) != 0 {
		t.Fatal("retried after application data")
	}
}

func TestLegacyHysteriaDialRetainsExistingBehavior(t *testing.T) {
	p := tierTestPool(t)
	p.logger = log.NewNOPFactory().Logger()
	p.members = p.members[:1]
	c := newLazyHandshakeConn()
	var calls int
	p.members[0].outbound = lazyHandshakeOutbound{conn: c, calls: &calls}
	conn, err := p.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.com:443"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if c.requestSent || c.responseRead {
		t.Fatal("changed legacy lazy handshake")
	}
}
