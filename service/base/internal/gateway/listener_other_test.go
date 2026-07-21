//go:build !linux

package gateway

import (
	"context"
	"errors"
	"testing"

	"easy_proxies/internal/config"
)

func TestTransparentListenerReportsUnsupportedPlatform(t *testing.T) {
	_, err := ListenTransparent(context.Background(), config.GatewayConfig{Listen: "0.0.0.0:15001"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ListenTransparent() error = %v, want ErrUnsupported", err)
	}
}
