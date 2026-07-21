//go:build !linux

package gateway

import (
	"context"
	"net"

	"easy_proxies/internal/config"
)

func ListenTransparent(context.Context, config.GatewayConfig) (net.Listener, error) {
	return nil, ErrUnsupported
}
