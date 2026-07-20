package dispatch

import (
	"bytes"
	"testing"

	"easy_proxies/internal/outbound/pool"
)

func TestSplitProxyUsername(t *testing.T) {
	tests := []struct {
		raw      string
		base     string
		device   string
		strategy pool.Strategy
		wantErr  bool
	}{
		{raw: "easyproxy", base: "easyproxy"},
		{raw: "easyproxy+dev=Laptop", base: "easyproxy", device: "laptop"},
		{raw: "easyproxy+dev=laptop+stable+us", base: "easyproxy", device: "laptop", strategy: pool.StrategyStable},
		{raw: "easyproxy+dev=a+dev=b", wantErr: true},
		{raw: "easyproxy+dev=bad device", wantErr: true},
	}
	for _, tt := range tests {
		got, err := splitProxyUsername(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Fatalf("%q err=%v wantErr=%v", tt.raw, err, tt.wantErr)
		}
		if err != nil {
			continue
		}
		if got.BaseUsername != tt.base || got.ExplicitDeviceID != tt.device {
			t.Fatalf("%q parsed as %#v", tt.raw, got)
		}
		if tt.strategy != "" {
			if got.Overlay.Strategy == nil || *got.Overlay.Strategy != tt.strategy {
				t.Fatalf("%q strategy = %v, want %s", tt.raw, got.Overlay.Strategy, tt.strategy)
			}
		}
	}
}

func TestAuthenticateProxyChecksCredentialsBeforeDeviceSyntax(t *testing.T) {
	s := &Server{cfg: Config{Username: "easyproxy", Password: "secret"}}

	if _, err := s.authenticateProxy("wrong+dev=bad device", "secret"); err != errProxyAuthInvalid {
		t.Fatalf("wrong credentials error = %v, want %v", err, errProxyAuthInvalid)
	}
	if _, err := s.authenticateProxy("easyproxy+dev=bad device", "secret"); err != errProxyUsernameInvalid {
		t.Fatalf("malformed authenticated device error = %v, want %v", err, errProxyUsernameInvalid)
	}
}

func TestSocksAuthenticationMapsCredentialAndDeviceFailuresToRFC1929(t *testing.T) {
	s := &Server{cfg: Config{Username: "easyproxy", Password: "secret"}}
	for _, tt := range []struct {
		name     string
		username string
		password string
	}{
		{name: "bad credentials", username: "wrong+dev=bad device", password: "secret"},
		{name: "malformed device", username: "easyproxy+dev=bad device", password: "secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var client bytes.Buffer
			client.Write([]byte{socks5Version, 0x01, authUserPass})
			client.WriteByte(authUserPassVersion)
			client.WriteByte(byte(len(tt.username)))
			client.WriteString(tt.username)
			client.WriteByte(byte(len(tt.password)))
			client.WriteString(tt.password)

			conn := newFakeConn(client.Bytes())
			if _, ok := s.socksHandshake(conn); ok {
				t.Fatal("malformed or invalid credentials unexpectedly authenticated")
			}
			if got := conn.fromServer.Bytes(); !bytes.Equal(got, []byte{socks5Version, authUserPass, authUserPassVersion, authStatusFailure}) {
				t.Fatalf("RFC1929 failure response = %v", got)
			}
		})
	}
}
