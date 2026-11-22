package dispatch

import (
	"bufio"
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/routerule"
	"errors"
	M "github.com/sagernet/sing/common/metadata"
	"net"
	"net/http"
	"testing"
	"time"
)

type claudeTestOutbound struct {
	dispatchTestOutbound
	pin string
}

func (o *claudeTestOutbound) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	if d := pool.DirectiveFrom(ctx); d != nil {
		o.pin = d.RequiredTag
	}
	return nil, errors.New("node failed")
}
func TestClaudeRouteHTTPAndCONNECTFailClosed(t *testing.T) {
	for _, method := range []string{"GET", "CONNECT"} {
		out := &claudeTestOutbound{}
		s := NewServer(Config{ClaudeRoute: config.ClaudeRoute{Enabled: true, NodeTag: "fixed", NodeFingerprint: config.NodeFingerprint("test-uri")}}, &dispatchTestPoolProvider{outbound: out}, routerule.New(nil, routerule.PolicyDirect, nil), nil)
		a, b := net.Pipe()
		a.SetDeadline(time.Now().Add(3 * time.Second))
		done := make(chan struct{})
		go func() { defer close(done); defer b.Close(); s.serveHTTP(b, bufio.NewReader(b)) }()
		request := "GET http://api.anthropic.com/ HTTP/1.1\r\nHost: api.anthropic.com\r\nConnection: close\r\n\r\n"
		if method == "CONNECT" {
			request = "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n"
		}
		a.Write([]byte(request))
		response, e := http.ReadResponse(bufio.NewReader(a), nil)
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != 502 {
			t.Fatalf("status=%d", response.StatusCode)
		}
		a.Close()
		<-done
		if out.pin != "fixed" {
			t.Fatalf("lost pin %q", out.pin)
		}
	}
}
func TestClaudeRouteHTTPMissingBindingFailsClosed(t *testing.T) {
	s := NewServer(Config{ClaudeRoute: config.ClaudeRoute{Enabled: true}}, nil, nil, nil)
	if _, e := s.dial(context.Background(), "tcp", "claude.ai", 443, resolved{}, routerule.PolicyDirect); e == nil {
		t.Fatal("missing pin accepted")
	}
}
