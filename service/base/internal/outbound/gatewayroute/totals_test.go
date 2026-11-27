package gatewayroute

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type directLoopback struct {
	recordingOutbound
	addr *net.TCPAddr
}

func (d *directLoopback) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return net.DialTCP("tcp4", nil, d.addr)
}

func TestDirectHalfCloseAndReloadLifetime(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	direct := &directLoopback{addr: listener.Addr().(*net.TCPAddr)}
	old := newTestRoute(nil, "DIRECT", "DIRECT", direct, nil)
	next := newTestRoute(nil, "DIRECT", "DIRECT", direct, nil)
	ctx := WithTotals(context.Background(), new(Totals))
	old.Totals, next.Totals = totalsFrom(ctx), totalsFrom(ctx)
	dst := M.ParseSocksaddr(listener.Addr().String())
	c, err := old.DialContext(ctx, N.NetworkTCP, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	deadline := time.Now().Add(4 * time.Second)
	_ = c.SetDeadline(deadline)
	_ = p.SetDeadline(deadline)
	if _, err = c.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	half, ok := c.(N.WriteCloser)
	if !ok {
		t.Fatalf("%T lost CloseWrite", c)
	}
	if err = half.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	req, err := io.ReadAll(p)
	if err != nil || string(req) != "request" {
		t.Fatalf("request=%q err=%v", req, err)
	}
	if _, err = p.Write([]byte("reply")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 5)
	if _, err = io.ReadFull(c, reply); err != nil || string(reply) != "reply" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if old.Snapshot().Active != 1 || next.Snapshot().Active != 0 || next.Snapshot().Direct != 1 {
		t.Fatal("mixed generation counters")
	}
	_ = c.Close()
	_ = c.Close()
	next.direct = &recordingOutbound{dialErr: errors.New("offline")}
	if _, err = next.DialContext(ctx, N.NetworkTCP, dst); err == nil {
		t.Fatal("expected dial failure")
	}
	if old.Snapshot().Active != 0 || next.Snapshot().Direct != 1 {
		t.Fatal("failed dial or repeated close corrupted counters")
	}
}
