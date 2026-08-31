package builder

import (
	"fmt"
	"strings"

	"easy_proxies/internal/config"
	poolout "easy_proxies/internal/outbound/pool"

	"github.com/sagernet/sing-box/option"
)

func detourSourceRefSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	refs := make(map[string]struct{}, len(values))
	for _, value := range values {
		if ref := strings.TrimSpace(value); ref != "" {
			refs[ref] = struct{}{}
		}
	}
	return refs
}

func sourceUsesBootstrapDetour(sourceRef string, refs map[string]struct{}) bool {
	_, matched := refs[strings.TrimSpace(sourceRef)]
	return matched
}

func setOutboundDetour(outbound *option.Outbound, detour string) error {
	switch options := outbound.Options.(type) {
	case *option.HTTPOutboundOptions:
		options.Detour = detour
	case *option.SOCKSOutboundOptions:
		options.Detour = detour
	case *option.VLESSOutboundOptions:
		options.Detour = detour
	case *option.Hysteria2OutboundOptions:
		options.Detour = detour
	case *option.ShadowsocksOutboundOptions:
		options.Detour = detour
	case *option.TrojanOutboundOptions:
		options.Detour = detour
	case *option.VMessOutboundOptions:
		options.Detour = detour
	case *option.AnyTLSOutboundOptions:
		options.Detour = detour
	default:
		return fmt.Errorf("outbound %q does not support detour options (%T)", outbound.Tag, outbound.Options)
	}
	return nil
}

func buildBootstrapPool(
	cfg *config.Config,
	members []string,
	metadata map[string]poolout.MemberMeta,
	detouredNodes int,
) (*option.Outbound, error) {
	if detouredNodes == 0 {
		return nil, nil
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("bootstrap pool requires at least one node outside detour_source_refs")
	}
	poolOptions := poolout.Options{
		Mode:              cfg.Pool.Mode,
		Members:           members,
		FailureThreshold:  cfg.Pool.FailureThreshold,
		BlacklistDuration: cfg.Pool.BlacklistDuration,
		Metadata:          metadata,
		MaxRetries:        cfg.Pool.MaxRetries,
		SessionTTL:        cfg.Routing.Session.TTL,
	}
	return &option.Outbound{
		Type:    poolout.Type,
		Tag:     poolout.BootstrapTag,
		Options: &poolOptions,
	}, nil
}
