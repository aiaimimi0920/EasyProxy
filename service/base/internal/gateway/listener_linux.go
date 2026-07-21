//go:build linux

package gateway

import (
	"context"
	"net"
	"syscall"

	"easy_proxies/internal/config"
	"golang.org/x/sys/unix"
)

const transparentMark = 0x1

// ListenTransparent binds a Linux transparent TCP socket. Policy routing and
// nftables are installed separately by Supervisor; this function only owns
// socket capabilities and accept semantics.
func ListenTransparent(ctx context.Context, cfg config.GatewayConfig) (net.Listener, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lc net.ListenConfig
	lc.Control = func(_ string, _ string, raw syscall.RawConn) error {
		var controlErr error
		if err := raw.Control(func(fd uintptr) {
			fileDescriptor := int(fd)
			if err := unix.SetsockoptInt(fileDescriptor, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
				controlErr = err
				return
			}
			if err := unix.SetsockoptInt(fileDescriptor, unix.IPPROTO_IP, unix.IP_TRANSPARENT, 1); err != nil {
				controlErr = err
				return
			}
			if err := unix.SetsockoptInt(fileDescriptor, unix.SOL_SOCKET, unix.SO_MARK, transparentMark); err != nil {
				controlErr = err
			}
		}); err != nil {
			return err
		}
		return controlErr
	}
	return lc.Listen(ctx, "tcp", cfg.Listen)
}
