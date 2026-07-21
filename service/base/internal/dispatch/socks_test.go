package dispatch

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeConn is an in-memory net.Conn driven by two buffers, used to script a
// SOCKS5 client/server exchange without real sockets.
type fakeConn struct {
	toServer   *bytes.Buffer // bytes the client "sends" (server reads)
	fromServer *bytes.Buffer // bytes the server writes (client reads)
}

func newFakeConn(clientBytes []byte) *fakeConn {
	return &fakeConn{
		toServer:   bytes.NewBuffer(clientBytes),
		fromServer: &bytes.Buffer{},
	}
}

func (c *fakeConn) Read(b []byte) (int, error) {
	if c.toServer.Len() == 0 {
		return 0, io.EOF
	}
	return c.toServer.Read(b)
}
func (c *fakeConn) Write(b []byte) (int, error)      { return c.fromServer.Write(b) }
func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1080} }
func (c *fakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5555} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

func TestSocksHandshakeNoAuth(t *testing.T) {
	// VER=5, NMETHODS=1, METHOD=0x00 (no-auth)
	client := []byte{socks5Version, 0x01, authNoAuth}
	conn := newFakeConn(client)
	s := &Server{}

	username, ok := s.socksHandshake(conn)
	if !ok {
		t.Fatalf("expected handshake to succeed")
	}
	if username.BaseUsername != "" || username.ExplicitDeviceID != "" || username.Overlay.Strategy != nil ||
		len(username.Overlay.Countries) != 0 || len(username.Overlay.Regions) != 0 {
		t.Fatalf("expected empty parsed username for no-auth, got %#v", username)
	}
	// Server should have replied: VER=5, METHOD=0x00
	reply := conn.fromServer.Bytes()
	if len(reply) != 2 || reply[0] != socks5Version || reply[1] != authNoAuth {
		t.Fatalf("unexpected method-select reply: %v", reply)
	}
}

func TestSocksHandshakeUserPassCarriesToken(t *testing.T) {
	// Method selection offering username/password (0x02), then RFC1929 creds
	// with the username carrying a directive token.
	token := "stable+us"
	var client bytes.Buffer
	client.Write([]byte{socks5Version, 0x01, authUserPass})
	// auth: VER=1, ULEN, UNAME, PLEN, PASSWD
	client.WriteByte(authUserPassVersion)
	client.WriteByte(byte(len(token)))
	client.WriteString(token)
	client.WriteByte(0x00) // empty password

	conn := newFakeConn(client.Bytes())
	s := &Server{} // no configured auth → token accepted as-is

	username, ok := s.socksHandshake(conn)
	if !ok {
		t.Fatalf("expected handshake to succeed")
	}
	if username.BaseUsername != "" {
		t.Fatalf("legacy token-only auth should not retain a base username, got %#v", username)
	}
	if username.Overlay.Strategy == nil || *username.Overlay.Strategy != "stable" {
		t.Fatalf("expected strategy token from %q, got %#v", token, username)
	}
	if username.Overlay.Regions == nil || len(username.Overlay.Regions) != 1 || username.Overlay.Regions[0] != "us" {
		t.Fatalf("expected username token overlay for %q, got %#v", token, username)
	}
}

func TestSocksCredentialsValidation(t *testing.T) {
	s := &Server{cfg: Config{Username: "alice", Password: "secret"}}

	// Correct password, username base matches (with trailing token).
	parsed, err := s.authenticateProxy("alice+stable+us", "secret")
	if err != nil {
		t.Fatalf("expected creds to pass with matching base username and password: %v", err)
	}
	if parsed.BaseUsername != "alice" || parsed.Overlay.Strategy == nil || *parsed.Overlay.Strategy != "stable" {
		t.Fatalf("parsed credentials = %#v", parsed)
	}
	// Wrong password.
	if _, err := s.authenticateProxy("alice", "wrong"); err == nil {
		t.Fatal("expected creds to fail with wrong password")
	}
	// Wrong username base.
	if _, err := s.authenticateProxy("bob+stable", "secret"); err == nil {
		t.Fatal("expected creds to fail with wrong username base")
	}
}

func TestReadSocksRequestDomain(t *testing.T) {
	host := "example.com"
	var req bytes.Buffer
	req.Write([]byte{socks5Version, cmdConnect, 0x00, atypDomain})
	req.WriteByte(byte(len(host)))
	req.WriteString(host)
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, 443)
	req.Write(port)

	conn := newFakeConn(req.Bytes())
	gotHost, gotPort, ok := readSOCKSRequest(conn)
	if !ok {
		t.Fatalf("expected request parse to succeed")
	}
	if gotHost != host || gotPort != 443 {
		t.Fatalf("expected %s:443, got %s:%d", host, gotHost, gotPort)
	}
}

func TestReadSocksRequestIPv4(t *testing.T) {
	var req bytes.Buffer
	req.Write([]byte{socks5Version, cmdConnect, 0x00, atypIPv4})
	req.Write([]byte{1, 2, 3, 4})
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, 8080)
	req.Write(port)

	conn := newFakeConn(req.Bytes())
	gotHost, gotPort, ok := readSOCKSRequest(conn)
	if !ok {
		t.Fatalf("expected request parse to succeed")
	}
	if gotHost != "1.2.3.4" || gotPort != 8080 {
		t.Fatalf("expected 1.2.3.4:8080, got %s:%d", gotHost, gotPort)
	}
}

func TestReadSocksRequestRejectsNonConnect(t *testing.T) {
	// CMD=0x02 (BIND) is unsupported.
	req := []byte{socks5Version, 0x02, 0x00, atypIPv4, 1, 2, 3, 4, 0x1f, 0x90}
	conn := newFakeConn(req)
	if _, _, ok := readSOCKSRequest(conn); ok {
		t.Fatalf("expected non-CONNECT command to be rejected")
	}
	// Server should have written a command-not-supported reply.
	reply := conn.fromServer.Bytes()
	if len(reply) < 2 || reply[1] != repCommandNotSupport {
		t.Fatalf("expected command-not-support reply, got %v", reply)
	}
}
