package boxmgr

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
)

type captureAuthLogger struct {
	log.ContextLogger
	message string
}

func (l *captureAuthLogger) ErrorContext(_ context.Context, args ...any) {
	l.message = fmt.Sprint(args...)
}

func TestRegisteredMixedInboundRedactsRealRejectedCredentials(t *testing.T) {
	registry := inbound.NewRegistry()
	registerAuthRedactingInbounds(registry)
	logger := &captureAuthLogger{ContextLogger: log.NewNOPFactory().Logger()}
	instance, err := registry.Create(context.Background(), nil, logger, "test-in", "mixed", &option.HTTPMixedInboundOptions{Users: []auth.User{{Username: "test-user", Password: "correct-password"}}})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		instance.(adapter.TCPInjectableInbound).NewConnectionEx(context.Background(), server, adapter.InboundContext{Source: M.ParseSocksaddr("192.0.2.1:1234")}, nil)
	}()
	credential := base64.StdEncoding.EncodeToString([]byte("test-user:wrong-password"))
	if _, err := fmt.Fprintf(client, "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\nProxy-Authorization: Basic %s\r\n\r\n", credential); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("wrong credentials response = %d", response.StatusCode)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("authentication handler did not finish")
	}
	if strings.Contains(logger.message, "wrong-password") || strings.Contains(logger.message, "test-user") || !strings.Contains(logger.message, "[REDACTED]") {
		t.Fatalf("registered inbound did not redact authentication failure: %q", logger.message)
	}
}

func TestInboundAuthLogRedactsCredentialSuffix(t *testing.T) {
	for _, message := range []string{
		"process connection from 192.0.2.1:1234: http: authentication failed, username=test-user, password=test-password",
		"http: authentication failed, Proxy-Authorization=Basic dGVzdC11c2VyOnRlc3QtcGFzc3dvcmQ=",
		"socks5: authentication failed, username=test-user, password=test-password\nforged-log-secret",
		"socks4: authentication failed, username=test-user",
		"socks5: authentication failed, username=test-user, password=test-password http: authentication failed",
		"socks4: authentication failed, username=test-user http: authentication failed",
	} {
		for _, args := range [][]any{{message}, {errors.New(message)}, {"prefix: ", errors.New(message)}} {
			result := fmt.Sprint(redactInboundAuthError(args)...)
			for _, secret := range []string{"test-user", "test-password", "dGVzdC11c2Vy", "forged-log-secret"} {
				if strings.Contains(result, secret) {
					t.Fatalf("authentication log retained credential material: %q", result)
				}
			}
			if !strings.Contains(result, "authentication failed") || !strings.Contains(result, "[REDACTED]") {
				t.Fatalf("authentication diagnostic category was lost: %q", result)
			}
		}
	}
}

func TestInboundAuthLogPreservesUnrelatedErrors(t *testing.T) {
	args := []any{"process connection: ", errors.New("context deadline exceeded")}
	if got := fmt.Sprint(redactInboundAuthError(args)...); got != fmt.Sprint(args...) {
		t.Fatalf("unrelated log changed: %q", got)
	}
}
