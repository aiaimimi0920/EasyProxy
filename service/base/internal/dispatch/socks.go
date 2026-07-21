package dispatch

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"time"

	N "github.com/sagernet/sing/common/network"
)

// SOCKS5 protocol constants (RFC 1928 / 1929).
const (
	socks5Version = 0x05

	authNoAuth   = 0x00
	authUserPass = 0x02
	authNone     = 0xFF

	authUserPassVersion = 0x01
	authStatusSuccess   = 0x00
	authStatusFailure   = 0x01

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSuccess           = 0x00
	repGeneralFailure    = 0x01
	repCommandNotSupport = 0x07
	repAddrNotSupport    = 0x08
)

// handleSOCKS5 performs the SOCKS5 handshake then services a CONNECT request.
// Selection parameters are carried in the username field of username/password
// auth (a common proxy-pool convention): the username is parsed as a token
// string identical to the path-prefix form (e.g. "stable+us+sid=abc"). When
// proxy auth credentials are configured, the password must match; the username
// still doubles as the directive carrier.
func (s *Server) handleSOCKS5(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	auth, ok := s.socksHandshake(conn)
	if !ok {
		return
	}

	host, port, ok := readSOCKSRequest(conn)
	if !ok {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	// The username field carries the directive token (if any); proxy auth is
	// validated separately during the handshake.
	res, policy, _, resolveErr := s.resolveProfileRequest(auth, directiveOverlay{}, host, conn.RemoteAddr())
	if resolveErr != nil {
		_ = writeSOCKSReply(conn, repGeneralFailure)
		return
	}

	target, err := s.dial(s.baseContext(), N.NetworkTCP, host, port, res, policy)
	if err != nil {
		_ = writeSOCKSReply(conn, repGeneralFailure)
		return
	}
	defer target.Close()

	if err := writeSOCKSReply(conn, repSuccess); err != nil {
		return
	}
	relay(conn, target)
}

// socksHandshake negotiates the auth method and retains the authenticated
// client identity and directive overlay for request routing.
func (s *Server) socksHandshake(conn net.Conn) (parsedProxyUsername, bool) {
	// Method selection: VER, NMETHODS, METHODS...
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return parsedProxyUsername{}, false
	}
	if header[0] != socks5Version {
		return parsedProxyUsername{}, false
	}
	n := int(header[1])
	if n == 0 {
		return parsedProxyUsername{}, false
	}
	methods := make([]byte, n)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return parsedProxyUsername{}, false
	}

	_, _, authRequired := s.authCredentials()
	offersUserPass := containsByte(methods, authUserPass)
	offersNoAuth := containsByte(methods, authNoAuth)

	switch {
	case offersUserPass:
		// Always prefer username/password when offered so the directive token in
		// the username field is delivered, even without configured auth.
		if _, err := conn.Write([]byte{socks5Version, authUserPass}); err != nil {
			return parsedProxyUsername{}, false
		}
		return s.socksUserPassAuth(conn)
	case !authRequired && offersNoAuth:
		if _, err := conn.Write([]byte{socks5Version, authNoAuth}); err != nil {
			return parsedProxyUsername{}, false
		}
		return parsedProxyUsername{}, true
	default:
		_, _ = conn.Write([]byte{socks5Version, authNone})
		return parsedProxyUsername{}, false
	}
}

// socksUserPassAuth reads RFC 1929 credentials and validates them through the
// shared proxy-authentication path used by HTTP and CONNECT.
func (s *Server) socksUserPassAuth(conn net.Conn) (parsedProxyUsername, bool) {
	// VER, ULEN, UNAME, PLEN, PASSWD
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return parsedProxyUsername{}, false
	}
	if head[0] != authUserPassVersion {
		return parsedProxyUsername{}, false
	}
	ulen := int(head[1])
	uname := make([]byte, ulen)
	if _, err := io.ReadFull(conn, uname); err != nil {
		return parsedProxyUsername{}, false
	}
	plenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, plenBuf); err != nil {
		return parsedProxyUsername{}, false
	}
	passwd := make([]byte, int(plenBuf[0]))
	if _, err := io.ReadFull(conn, passwd); err != nil {
		return parsedProxyUsername{}, false
	}

	username := string(uname)
	password := string(passwd)
	parsed, err := s.authenticateProxy(username, password)
	if err != nil {
		_, _ = conn.Write([]byte{authUserPassVersion, authStatusFailure})
		return parsedProxyUsername{}, false
	}

	if _, err := conn.Write([]byte{authUserPassVersion, authStatusSuccess}); err != nil {
		return parsedProxyUsername{}, false
	}
	return parsed, true
}

// readSOCKSRequest reads the CONNECT request and returns the destination host
// and port. Only CONNECT over TCP is supported.
func readSOCKSRequest(conn net.Conn) (string, uint16, bool) {
	// VER, CMD, RSV, ATYP
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", 0, false
	}
	if head[0] != socks5Version {
		return "", 0, false
	}
	if head[1] != cmdConnect {
		_ = writeSOCKSReply(conn, repCommandNotSupport)
		return "", 0, false
	}

	var host string
	switch head[3] {
	case atypIPv4:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", 0, false
		}
		host = net.IP(buf).String()
	case atypIPv6:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", 0, false
		}
		host = net.IP(buf).String()
	case atypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", 0, false
		}
		dom := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(conn, dom); err != nil {
			return "", 0, false
		}
		host = string(dom)
	default:
		_ = writeSOCKSReply(conn, repAddrNotSupport)
		return "", 0, false
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return "", 0, false
	}
	port := binary.BigEndian.Uint16(portBuf)
	if host == "" {
		return "", 0, false
	}
	return host, port, true
}

// writeSOCKSReply writes a SOCKS5 reply with the given status code and a
// zeroed bind address (clients ignore the bind addr for CONNECT replies in
// practice).
func writeSOCKSReply(conn net.Conn, rep byte) error {
	// VER, REP, RSV, ATYP=IPv4, BND.ADDR(4)=0, BND.PORT(2)=0
	_, err := conn.Write([]byte{socks5Version, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

func containsByte(b []byte, target byte) bool {
	for _, v := range b {
		if v == target {
			return true
		}
	}
	return false
}

// baseContext returns the server's lifecycle context for dialing, falling back
// to a background context when unset.
func (s *Server) baseContext() context.Context {
	s.mu.RLock()
	ctx := s.baseCtx
	s.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
