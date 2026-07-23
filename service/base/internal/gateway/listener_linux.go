//go:build linux

package gateway

import (
	"context"
	"net"
	"syscall"

	"easy_proxies/internal/config"
	"golang.org/x/sys/unix"
)

type socketOptionSetter func(fd, level, option, value int) error

func configureTransparentSocket(fd int, setOption socketOptionSetter) error {
	for _, option := range [][3]int{
		{unix.SOL_SOCKET, unix.SO_REUSEADDR, 1},
		{unix.IPPROTO_IP, unix.IP_TRANSPARENT, 1},
	} {
		if err := setOption(fd, option[0], option[1], option[2]); err != nil {
			return err
		}
	}
	return nil
}

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
			controlErr = configureTransparentSocket(int(fd), unix.SetsockoptInt)
		}); err != nil {
			return err
		}
		return controlErr
	}
	return lc.Listen(ctx, "tcp", cfg.Listen)
}
