package dispatch

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"easy_proxies/internal/routerule"

	"github.com/sagernet/sing-box/adapter"
)

type transparentTestPoolProvider struct{}

func (transparentTestPoolProvider) PoolOutbound() (adapter.Outbound, bool) {
	return nil, false
}

func TestTransparentRouterUsesOriginalDestinationAndDirectFallback(t *testing.T) {
	echo := startTransparentEcho(t)
	provider := transparentTestPoolProvider{}
	engine := routerule.New(nil, routerule.PolicyProxy, nil)
	router := NewTransparentRouter(TransparentRouterConfig{
		DialTimeout:            time.Second,
		NoAvailableProxyPolicy: routerule.PolicyDirect,
	}, provider, engine, nil)

	client, server := net.Pipe()
	target := mustTCPAddr(t, echo)
	wrapped := &transparentAddressedConn{
		Conn:   server,
		local:  target,
		remote: mustTCPAddr(t, "192.168.15.100:40000"),
	}
	done := make(chan struct{})
	go func() {
		router.ServeConn(context.Background(), wrapped)
		close(done)
	}()

	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if got := string(buf); got != "ping" {
		t.Fatalf("echo = %q, want ping", got)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transparent router did not stop")
	}
}

func startTransparentEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func mustTCPAddr(t *testing.T, value string) *net.TCPAddr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", value)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

type transparentAddressedConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (c *transparentAddressedConn) LocalAddr() net.Addr  { return c.local }
func (c *transparentAddressedConn) RemoteAddr() net.Addr { return c.remote }
