package builder

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"easy_proxies/internal/config"
	"easy_proxies/internal/geoip"
	poolout "easy_proxies/internal/outbound/pool"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

var (
	echConfigCache      sync.Map
	resolveECHConfigPEM = resolveECHConfigPEMFromQuery
)

// ErrNoValidNodes indicates that the configured node set cannot produce any
// sing-box outbound. The box manager uses this to keep the dispatcher in an
// idle/direct-capable state when proxy sources are temporarily empty or invalid.
var ErrNoValidNodes = errors.New("no valid nodes available")

// Build converts high level config into sing-box Options tree.
func Build(cfg *config.Config) (option.Options, error) {
	baseOutbounds := make([]option.Outbound, 0, len(cfg.Nodes))
	memberTags := make([]string, 0, len(cfg.Nodes))
	metadata := make(map[string]poolout.MemberMeta)
	nodeEndpointDomains := make([]string, 0, len(cfg.Nodes))
	var failedNodes []string
	usedTags := make(map[string]int) // Track tag usage for uniqueness
	detourSourceRefs := detourSourceRefSet(cfg.Pool.DetourSourceRefs)
	bootstrapMembers := make([]string, 0, len(cfg.Nodes))
	bootstrapMetadata := make(map[string]poolout.MemberMeta)
	detouredNodes := 0
	if len(detourSourceRefs) > 0 {
		usedTags[poolout.BootstrapTag] = 1
	}

	// Initialize GeoIP lookup if enabled
	var geoLookup *geoip.Lookup
	if cfg.GeoIP.Enabled && cfg.GeoIP.DatabasePath != "" {
		// Build only needs a point-in-time lookup to classify this generation.
		// The routing controller owns the long-lived auto-update lifecycle.
		var err error
		geoLookup, err = geoip.New(cfg.GeoIP.DatabasePath)
		if err != nil {
			log.Printf("⚠️  GeoIP database load failed: %v (region routing disabled)", err)
		} else {
			log.Printf("✅ GeoIP database loaded: %s", cfg.GeoIP.DatabasePath)
		}
	}

	// Track nodes by region for GeoIP routing
	regionMembers := make(map[string][]string)
	for _, region := range geoip.AllRegions() {
		regionMembers[region] = []string{}
	}

	for _, node := range cfg.Nodes {
		baseTag := sanitizeTag(node.Name)
		if baseTag == "" {
			baseTag = fmt.Sprintf("node-%d", len(memberTags)+1)
		}

		// Ensure tag uniqueness by appending a counter if needed
		tag := baseTag
		if count, exists := usedTags[baseTag]; exists {
			usedTags[baseTag] = count + 1
			tag = fmt.Sprintf("%s-%d", baseTag, count+1)
		} else {
			usedTags[baseTag] = 1
		}

		outbound, err := buildNodeOutbound(tag, node.URI, cfg.SkipCertVerify)
		if err != nil {
			log.Printf("❌ Failed to build node '%s': %v (skipping)", node.Name, err)
			failedNodes = append(failedNodes, node.Name)
			continue
		}
		detourSource := sourceUsesBootstrapDetour(node.SourceRef, detourSourceRefs)
		if detourSource && outbound.Type != C.TypeHTTP {
			if err := setOutboundDetour(&outbound, poolout.BootstrapTag); err != nil {
				return option.Options{}, fmt.Errorf("configure bootstrap detour for node %q: %w", node.Name, err)
			}
			detouredNodes++
		}
		memberTags = append(memberTags, tag)
		baseOutbounds = append(baseOutbounds, outbound)
		if endpointDomain := nodeEndpointDomain(node.URI); endpointDomain != "" {
			nodeEndpointDomains = append(nodeEndpointDomains, endpointDomain)
		}
		traits := describeNodeRoutingTraits(node.URI)
		meta := poolout.MemberMeta{
			Name:           node.Name,
			URI:            node.URI,
			Mode:           cfg.Mode,
			SourceKind:     node.SourceKind,
			SourceName:     node.SourceName,
			SourceRef:      node.SourceRef,
			ProtocolFamily: traits.ProtocolFamily,
			NodeMode:       traits.NodeMode,
			DomainFamily:   traits.DomainFamily,
		}
		// For multi-port and hybrid modes, use per-node port
		if cfg.Mode == "multi-port" || cfg.Mode == "hybrid" {
			meta.ListenAddress = cfg.MultiPort.Address
			meta.Port = node.Port
		} else {
			meta.ListenAddress = cfg.Listener.Address
			meta.Port = cfg.Listener.Port
		}

		// GeoIP lookup for region classification
		if geoLookup != nil && geoLookup.IsEnabled() {
			regionInfo := geoLookup.LookupURI(node.URI)
			meta.Region = regionInfo.Code
			meta.Country = regionInfo.Country
			meta.CountryISO = regionInfo.ISOCode
			regionMembers[regionInfo.Code] = append(regionMembers[regionInfo.Code], tag)
		} else {
			meta.Region = geoip.RegionOther
			meta.Country = "Unknown"
			regionMembers[geoip.RegionOther] = append(regionMembers[geoip.RegionOther], tag)
		}

		metadata[tag] = meta
		if !detourSource {
			bootstrapMembers = append(bootstrapMembers, tag)
			bootstrapMetadata[tag] = meta
		}
	}

	// Close GeoIP database after lookup
	if geoLookup != nil {
		geoLookup.Close()
	}

	// Check if we have at least one valid node
	if len(baseOutbounds) == 0 {
		return option.Options{}, fmt.Errorf("%w (all %d nodes failed to build)", ErrNoValidNodes, len(cfg.Nodes))
	}

	// Log summary
	if len(failedNodes) > 0 {
		log.Printf("⚠️  %d/%d nodes failed and were skipped: %v", len(failedNodes), len(cfg.Nodes), failedNodes)
	}
	log.Printf("✅ Successfully built %d/%d nodes", len(baseOutbounds), len(cfg.Nodes))

	// Log GeoIP region distribution
	if cfg.GeoIP.Enabled {
		log.Println("🌍 GeoIP Region Distribution:")
		for _, region := range geoip.AllRegions() {
			count := len(regionMembers[region])
			if count > 0 {
				log.Printf("   %s %s: %d nodes", geoip.RegionEmoji(region), geoip.RegionName(region), count)
			}
		}
	}

	// Print proxy links for each node
	printProxyLinks(cfg, metadata)

	var (
		inbounds  []option.Inbound
		outbounds = make([]option.Outbound, len(baseOutbounds))
		route     option.RouteOptions
	)
	copy(outbounds, baseOutbounds)
	bootstrapOutbound, err := buildBootstrapPool(cfg, bootstrapMembers, bootstrapMetadata, detouredNodes)
	if err != nil {
		return option.Options{}, err
	}
	if bootstrapOutbound != nil {
		outbounds = append(outbounds, *bootstrapOutbound)
	}

	// Determine which components to enable based on mode
	enablePoolInbound := cfg.Mode == "pool" || cfg.Mode == "hybrid"
	enableMultiPort := cfg.Mode == "multi-port" || cfg.Mode == "hybrid"

	if !enablePoolInbound && !enableMultiPort {
		return option.Options{}, fmt.Errorf("unsupported mode %s", cfg.Mode)
	}

	// Build pool inbound (single entry point for all nodes).
	//
	// When the smart dispatch entry takes over the same host:port (routing route
	// A), the plain pool inbound must be omitted so the dispatcher can bind that
	// port. The pool *outbound* is still built below — the dispatcher dials it
	// directly with the per-request selection directive — so health checks,
	// blacklisting, and stats are unchanged.
	if enablePoolInbound {
		if !cfg.DispatchOwnsPrimaryInbound() {
			inbound, err := buildPoolInbound(cfg)
			if err != nil {
				return option.Options{}, err
			}
			inbounds = append(inbounds, inbound)
		} else {
			log.Printf("🧭 dispatch entry takes over %s; plain pool inbound omitted", cfg.DispatchListen())
		}
	}

	// Smart routing dials the global pool outbound directly. Pure multi-port
	// mode therefore still needs proxy-pool even though it has no plain pool
	// inbound and keeps its per-node pools below.
	if enablePoolInbound || cfg.DispatchEnabled() {
		poolOptions := poolout.Options{
			Mode:              cfg.Pool.Mode,
			Members:           memberTags,
			FailureThreshold:  cfg.Pool.FailureThreshold,
			BlacklistDuration: cfg.Pool.BlacklistDuration,
			Metadata:          metadata,
			MaxRetries:        cfg.Pool.MaxRetries,
			SessionTTL:        cfg.Routing.Session.TTL,
		}
		outbounds = append(outbounds, option.Outbound{
			Type:    poolout.Type,
			Tag:     poolout.Tag,
			Options: &poolOptions,
		})
	}
	if enablePoolInbound {
		route.Final = poolout.Tag
	}

	// Build extra listeners (same pool members, different selection modes)
	if enablePoolInbound && len(cfg.ExtraListeners) > 0 {
		for _, extra := range cfg.ExtraListeners {
			if extra.Port == 0 {
				continue
			}
			mode := extra.PoolMode
			if mode == "" {
				mode = cfg.Pool.Mode
			}
			extraPoolTag := fmt.Sprintf("%s-%s-%d", poolout.Tag, mode, extra.Port)
			extraPoolOptions := poolout.Options{
				Mode:              mode,
				Members:           memberTags,
				FailureThreshold:  cfg.Pool.FailureThreshold,
				BlacklistDuration: cfg.Pool.BlacklistDuration,
				Metadata:          metadata,
				MaxRetries:        cfg.Pool.MaxRetries,
			}
			outbounds = append(outbounds, option.Outbound{
				Type:    poolout.Type,
				Tag:     extraPoolTag,
				Options: &extraPoolOptions,
			})

			extraAddr, err := parseAddr(extra.Address)
			if err != nil {
				return option.Options{}, fmt.Errorf("parse extra listener address: %w", err)
			}
			protocol := extra.Protocol
			if protocol == "" {
				protocol = cfg.Listener.Protocol
			}
			inboundTag := fmt.Sprintf("extra-in-%d", extra.Port)
			inbound, err := buildInboundByProtocol(
				protocol,
				extraAddr,
				extra.Port,
				extra.Username,
				extra.Password,
				inboundTag,
			)
			if err != nil {
				return option.Options{}, fmt.Errorf("build extra listener on port %d: %w", extra.Port, err)
			}
			inbounds = append(inbounds, inbound)
			route.Rules = append(route.Rules, option.Rule{
				Type: C.RuleTypeDefault,
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: badoption.Listable[string]{inboundTag},
					},
					RuleAction: option.RuleAction{
						Action: C.RuleActionTypeRoute,
						RouteOptions: option.RouteActionOptions{
							Outbound: extraPoolTag,
						},
					},
				},
			})
			log.Printf("   Extra listener: :%d [%s] → pool mode: %s", extra.Port, protocol, mode)
		}
	}

	// Build multi-port inbounds (one port per node)
	if enableMultiPort {
		addr, err := parseAddr(cfg.MultiPort.Address)
		if err != nil {
			return option.Options{}, fmt.Errorf("parse multi-port address: %w", err)
		}
		for _, tag := range memberTags {
			meta := metadata[tag]
			perMeta := map[string]poolout.MemberMeta{tag: meta}
			poolTag := fmt.Sprintf("%s-%s", poolout.Tag, tag)
			perOptions := poolout.Options{
				Mode:              "sequential",
				Members:           []string{tag},
				FailureThreshold:  cfg.Pool.FailureThreshold,
				BlacklistDuration: cfg.Pool.BlacklistDuration,
				Metadata:          perMeta,
			}
			perPool := option.Outbound{
				Type:    poolout.Type,
				Tag:     poolTag,
				Options: &perOptions,
			}
			outbounds = append(outbounds, perPool)
			inboundTag := fmt.Sprintf("in-%s", tag)
			inbound, err := buildInboundByProtocol(
				cfg.MultiPort.Protocol,
				addr,
				meta.Port,
				cfg.MultiPort.Username,
				cfg.MultiPort.Password,
				inboundTag,
			)
			if err != nil {
				return option.Options{}, fmt.Errorf("build multi-port inbound for %s: %w", tag, err)
			}
			inbounds = append(inbounds, inbound)
			route.Rules = append(route.Rules, option.Rule{
				Type: C.RuleTypeDefault,
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: badoption.Listable[string]{inboundTag},
					},
					RuleAction: option.RuleAction{
						Action: C.RuleActionTypeRoute,
						RouteOptions: option.RouteActionOptions{
							Outbound: poolTag,
						},
					},
				},
			})
		}
	}

	// Build GeoIP region-based pool outbounds and routing
	if cfg.GeoIP.Enabled && enablePoolInbound {
		// Create pool outbound for each region that has nodes
		for _, region := range geoip.AllRegions() {
			members := regionMembers[region]
			if len(members) == 0 {
				continue
			}

			// Build metadata for this region's members
			regionMeta := make(map[string]poolout.MemberMeta)
			for _, tag := range members {
				regionMeta[tag] = metadata[tag]
			}

			regionPoolTag := fmt.Sprintf("pool-%s", region)
			regionPoolOptions := poolout.Options{
				Mode:              cfg.Pool.Mode,
				Members:           members,
				FailureThreshold:  cfg.Pool.FailureThreshold,
				BlacklistDuration: cfg.Pool.BlacklistDuration,
				Metadata:          regionMeta,
			}
			outbounds = append(outbounds, option.Outbound{
				Type:    poolout.Type,
				Tag:     regionPoolTag,
				Options: &regionPoolOptions,
			})
		}

		// Log GeoIP routing info
		geoipPort := cfg.GeoIP.Port
		if geoipPort == 0 {
			geoipPort = cfg.Listener.Port
		}
		geoipListen := cfg.GeoIP.Listen
		if geoipListen == "" {
			geoipListen = cfg.Listener.Address
		}
		log.Println("🌐 GeoIP Region Routing Enabled:")
		log.Printf("   Access via: http://%s:%d/{region}", geoipListen, geoipPort)
		log.Println("   Available regions: /jp, /kr, /us, /hk, /tw, /other")
		log.Println("   Default (no path): all nodes pool")
	}

	dnsOptions, err := buildDNSOptions(cfg.DNS, nodeEndpointDomains, len(memberTags) > 0 && (enablePoolInbound || cfg.DispatchEnabled()))
	if err != nil {
		return option.Options{}, fmt.Errorf("build DNS options: %w", err)
	}

	opts := option.Options{
		Log:       &option.LogOptions{Level: strings.ToLower(cfg.LogLevel)},
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Route:     &route,
		DNS:       dnsOptions,
	}
	return opts, nil
}
