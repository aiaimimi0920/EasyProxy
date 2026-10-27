package boxmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/http"
	"github.com/sagernet/sing-box/protocol/mixed"
	"github.com/sagernet/sing-box/protocol/socks"
)

// sing's inbound authentication errors include submitted passwords or raw
// Proxy-Authorization headers. Keep the protocol/source/error category, never
// the credential-bearing suffix (which may also contain attacker newlines).
func redactInboundAuthError(args []any) []any {
	message := fmt.Sprint(args...)
	firstIndex, firstEnd := -1, 0
	for _, marker := range []string{"http: authentication failed", "socks5: authentication failed", "socks4: authentication failed"} {
		if index := strings.Index(message, marker); index >= 0 && (firstIndex < 0 || index < firstIndex) {
			firstIndex, firstEnd = index, index+len(marker)
		}
	}
	if firstIndex >= 0 {
		return []any{message[:firstEnd] + ", credentials=[REDACTED]"}
	}
	return args
}

type authRedactingLogger struct{ log.ContextLogger }

func (l authRedactingLogger) ErrorContext(ctx context.Context, args ...any) {
	l.ContextLogger.ErrorContext(ctx, redactInboundAuthError(args)...)
}

func (l authRedactingLogger) DebugContext(ctx context.Context, args ...any) {
	l.ContextLogger.DebugContext(ctx, redactInboundAuthError(args)...)
}

func withRedactedAuthLog[T any](constructor inbound.ConstructorFunc[T]) inbound.ConstructorFunc[T] {
	return func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options T) (adapter.Inbound, error) {
		return constructor(ctx, router, authRedactingLogger{logger}, tag, options)
	}
}

func registerAuthRedactingInbounds(registry *inbound.Registry) {
	inbound.Register(registry, C.TypeHTTP, withRedactedAuthLog(http.NewInbound))
	inbound.Register(registry, C.TypeMixed, withRedactedAuthLog(mixed.NewInbound))
	inbound.Register(registry, C.TypeSOCKS, withRedactedAuthLog[option.SocksInboundOptions](socks.NewInbound))
}
