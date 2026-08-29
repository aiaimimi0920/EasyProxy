package builder

import (
	"fmt"

	"easy_proxies/internal/config"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/json/badoption"
)

func buildPoolInbound(cfg *config.Config) (option.Inbound, error) {
	listenAddr, err := parseAddr(cfg.Listener.Address)
	if err != nil {
		return option.Inbound{}, fmt.Errorf("parse listener address: %w", err)
	}
	return buildInboundByProtocol(
		cfg.Listener.Protocol,
		listenAddr,
		cfg.Listener.Port,
		cfg.Listener.Username,
		cfg.Listener.Password,
		"http-in",
	)
}

func buildInboundByProtocol(protocol string, listenAddr *badoption.Addr, port uint16, username, password, tag string) (option.Inbound, error) {
	users := []auth.User(nil)
	if username != "" {
		users = []auth.User{{Username: username, Password: password}}
	}

	switch protocol {
	case config.InboundProtocolHTTP:
		opts := &option.HTTPMixedInboundOptions{
			ListenOptions: option.ListenOptions{
				Listen:     listenAddr,
				ListenPort: port,
			},
		}
		if len(users) > 0 {
			opts.Users = users
		}
		return option.Inbound{Type: C.TypeHTTP, Tag: tag, Options: opts}, nil
	case config.InboundProtocolSOCKS5:
		opts := &option.SocksInboundOptions{
			ListenOptions: option.ListenOptions{
				Listen:     listenAddr,
				ListenPort: port,
			},
		}
		if len(users) > 0 {
			opts.Users = users
		}
		return option.Inbound{Type: C.TypeSOCKS, Tag: tag, Options: opts}, nil
	case config.InboundProtocolMixed:
		opts := &option.HTTPMixedInboundOptions{
			ListenOptions: option.ListenOptions{
				Listen:     listenAddr,
				ListenPort: port,
			},
		}
		if len(users) > 0 {
			opts.Users = users
		}
		return option.Inbound{Type: C.TypeMixed, Tag: tag, Options: opts}, nil
	default:
		return option.Inbound{}, fmt.Errorf("unsupported inbound protocol %q", protocol)
	}
}
