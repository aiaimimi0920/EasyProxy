package pool

import (
	"context"
	"fmt"
	"net"

	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"
)

// Hysteria2 DialConn returns a lazy stream, including when an existing QUIC
// session has silently died. Complete only its protocol handshake before
// declaring this tier ready. Never retry application writes or reads.
//
// Audited against pinned sing-quic v0.5.3: clientConn.Write(nil) sends only the
// destination header; Read(nil) validates its response then calls QUIC Read(nil),
// which neither consumes nor waits for application data. Do not generalize this
// zero-length I/O contract to other protocols without a protocol-specific audit.
func completeTieredTCPSetup(ctx context.Context, network, protocol string, conn net.Conn) error {
	if N.NetworkName(network) != N.NetworkTCP || protocol != C.TypeHysteria2 {
		return nil
	}
	if _, ok := conn.(N.EarlyConn); !ok {
		return fmt.Errorf("hysteria2 TCP connection lacks the audited handshake contract")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// QUIC stream I/O does not inherit the dial context. Cancellation closes only
	// this stream, not the shared QUIC session or another client's connections.
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		close(done)
	})
	_, err := conn.Write(nil)
	if err == nil {
		_, err = conn.Read(nil)
	}
	if !stop() {
		<-done
	}
	if cause := ctx.Err(); cause != nil {
		return cause
	}
	if err != nil {
		return fmt.Errorf("hysteria2 TCP handshake: %w", err)
	}
	return nil
}
