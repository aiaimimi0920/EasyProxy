package builder

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/geoip"
	poolout "easy_proxies/internal/outbound/pool"

	mDNS "github.com/miekg/dns"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/json/badoption"
)

var (
	echConfigCache      sync.Map
	resolveECHConfigPEM = resolveECHConfigPEMFromQuery
)

// Build converts high level config into sing-box Options tree.
func Build(cfg *config.Config) (option.Options, error) {
	baseOutbounds := make([]option.Outbound, 0, len(cfg.Nodes))
	memberTags := make([]string, 0, len(cfg.Nodes))
	metadata := make(map[string]poolout.MemberMeta)
	var failedNodes []string
	usedTags := make(map[string]int) // Track tag usage for uniqueness

	// Initialize GeoIP lookup if enabled
	var geoLookup *geoip.Lookup
	if cfg.GeoIP.Enabled && cfg.GeoIP.DatabasePath != "" {
		var err error
		// Use auto-update if enabled
		if cfg.GeoIP.AutoUpdateEnabled {
			interval := cfg.GeoIP.AutoUpdateInterval
			if interval == 0 {
				interval = 24 * time.Hour // Default to 24 hours
			}
			geoLookup, err = geoip.NewWithAutoUpdate(cfg.GeoIP.DatabasePath, interval)
		} else {
			geoLookup, err = geoip.New(cfg.GeoIP.DatabasePath)
		}
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
		memberTags = append(memberTags, tag)
		baseOutbounds = append(baseOutbounds, outbound)
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
			regionMembers[regionInfo.Code] = append(regionMembers[regionInfo.Code], tag)
		} else {
			meta.Region = geoip.RegionOther
			meta.Country = "Unknown"
			regionMembers[geoip.RegionOther] = append(regionMembers[geoip.RegionOther], tag)
		}

		metadata[tag] = meta
	}

	// Close GeoIP database after lookup
	if geoLookup != nil {
		geoLookup.Close()
	}

	// Check if we have at least one valid node
	if len(baseOutbounds) == 0 {
		return option.Options{}, fmt.Errorf("no valid nodes available (all %d nodes failed to build)", len(cfg.Nodes))
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

	// Determine which components to enable based on mode
	enablePoolInbound := cfg.Mode == "pool" || cfg.Mode == "hybrid"
	enableMultiPort := cfg.Mode == "multi-port" || cfg.Mode == "hybrid"

	if !enablePoolInbound && !enableMultiPort {
		return option.Options{}, fmt.Errorf("unsupported mode %s", cfg.Mode)
	}

	// Build pool inbound (single entry point for all nodes)
	if enablePoolInbound {
		inbound, err := buildPoolInbound(cfg)
		if err != nil {
			return option.Options{}, err
		}
		inbounds = append(inbounds, inbound)
		poolOptions := poolout.Options{
			Mode:              cfg.Pool.Mode,
			Members:           memberTags,
			FailureThreshold:  cfg.Pool.FailureThreshold,
			BlacklistDuration: cfg.Pool.BlacklistDuration,
			Metadata:          metadata,
			MaxRetries:        cfg.Pool.MaxRetries,
		}
		outbounds = append(outbounds, option.Outbound{
			Type:    poolout.Type,
			Tag:     poolout.Tag,
			Options: &poolOptions,
		})
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

	opts := option.Options{
		Log:       &option.LogOptions{Level: strings.ToLower(cfg.LogLevel)},
		Inbounds:  inbounds,
		Outbounds: outbounds,
		Route:     &route,
	}
	return opts, nil
}

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

func buildNodeOutbound(tag, rawURI string, skipCertVerify bool) (option.Outbound, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("parse uri: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		opts, err := buildHTTPOptions(parsed, skipCertVerify)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeHTTP, Tag: tag, Options: &opts}, nil
	case "socks5":
		opts, err := buildSOCKSOptions(parsed)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeSOCKS, Tag: tag, Options: &opts}, nil
	case "vless":
		opts, err := buildVLESSOptions(parsed, skipCertVerify)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeVLESS, Tag: tag, Options: &opts}, nil
	case "hysteria2", "hy2":
		opts, err := buildHysteria2Options(parsed, skipCertVerify)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeHysteria2, Tag: tag, Options: &opts}, nil
	case "ss", "shadowsocks":
		opts, err := buildShadowsocksOptions(parsed)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeShadowsocks, Tag: tag, Options: &opts}, nil
	case "trojan":
		opts, err := buildTrojanOptions(parsed, skipCertVerify)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeTrojan, Tag: tag, Options: &opts}, nil
	case "vmess":
		opts, err := buildVMessOptions(rawURI, skipCertVerify)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeVMess, Tag: tag, Options: &opts}, nil
	case "anytls":
		opts, err := buildAnyTLSOptions(parsed, skipCertVerify)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{Type: C.TypeAnyTLS, Tag: tag, Options: &opts}, nil
	default:
		return option.Outbound{}, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
}

func buildHTTPOptions(u *url.URL, skipCertVerify bool) (option.HTTPOutboundOptions, error) {
	query := u.Query()
	defaultPort := 80
	if strings.EqualFold(query.Get("security"), "tls") {
		defaultPort = 443
	}
	server, port, err := hostPort(u, defaultPort)
	if err != nil {
		return option.HTTPOutboundOptions{}, err
	}

	opts := option.HTTPOutboundOptions{
		ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
	}
	if u.User != nil {
		opts.Username = u.User.Username()
		if password, ok := u.User.Password(); ok {
			opts.Password = password
		}
	}
	if path := u.EscapedPath(); path != "" {
		opts.Path = path
	}
	if host := strings.TrimSpace(query.Get("host")); host != "" {
		opts.Headers = badoption.HTTPHeader{"Host": {host}}
	}
	if tlsOptions, err := buildTLSOptions(query, skipCertVerify); err != nil {
		return option.HTTPOutboundOptions{}, err
	} else if tlsOptions != nil {
		opts.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{TLS: tlsOptions}
	}

	return opts, nil
}

func buildSOCKSOptions(u *url.URL) (option.SOCKSOutboundOptions, error) {
	server, port, err := hostPort(u, 1080)
	if err != nil {
		return option.SOCKSOutboundOptions{}, err
	}

	opts := option.SOCKSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
		Version:       "5",
	}
	if u.User != nil {
		opts.Username = u.User.Username()
		if password, ok := u.User.Password(); ok {
			opts.Password = password
		}
	}
	if network := strings.ToLower(strings.TrimSpace(u.Query().Get("network"))); network != "" {
		opts.Network = option.NetworkList(network)
	}

	return opts, nil
}

func buildAnyTLSOptions(u *url.URL, skipCertVerify bool) (option.AnyTLSOutboundOptions, error) {
	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	if password == "" {
		return option.AnyTLSOutboundOptions{}, errors.New("anytls uri missing password in userinfo")
	}
	server, port, err := hostPort(u, 443)
	if err != nil {
		return option.AnyTLSOutboundOptions{}, err
	}
	query := u.Query()
	opts := option.AnyTLSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
		Password:      password,
	}

	// AnyTLS requires TLS
	tlsOptions := &option.OutboundTLSOptions{Enabled: true, Insecure: skipCertVerify}
	tlsOptions.ServerName = server
	if sni := query.Get("sni"); sni != "" {
		tlsOptions.ServerName = sni
	}
	insecure := query.Get("insecure")
	if insecure == "" {
		insecure = query.Get("allowInsecure")
	}
	if insecure != "" {
		tlsOptions.Insecure = insecure == "1" || strings.EqualFold(insecure, "true")
	}
	if alpn := query.Get("alpn"); alpn != "" {
		tlsOptions.ALPN = badoption.Listable[string](strings.Split(alpn, ","))
	}
	if fp := query.Get("fp"); fp != "" {
		tlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fp}
	}
	opts.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{TLS: tlsOptions}

	return opts, nil
}

func buildVLESSOptions(u *url.URL, skipCertVerify bool) (option.VLESSOutboundOptions, error) {
	uuid := u.User.Username()
	if uuid == "" {
		return option.VLESSOutboundOptions{}, errors.New("vless uri missing uuid in userinfo")
	}
	server, port, err := hostPort(u, 443)
	if err != nil {
		return option.VLESSOutboundOptions{}, err
	}
	query := u.Query()
	opts := option.VLESSOutboundOptions{
		UUID:          uuid,
		ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
		Network:       option.NetworkList(""),
	}
	if flow := query.Get("flow"); flow != "" {
		opts.Flow = config.NormalizeVLESSFlow(flow)
	}
	if packetEncoding := query.Get("packetEncoding"); packetEncoding != "" {
		opts.PacketEncoding = &packetEncoding
	}
	if transport, err := buildV2RayTransport(query); err != nil {
		return option.VLESSOutboundOptions{}, err
	} else if transport != nil {
		opts.Transport = transport
	}
	if tlsOptions, err := buildTLSOptions(query, skipCertVerify); err != nil {
		return option.VLESSOutboundOptions{}, err
	} else if tlsOptions != nil {
		opts.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{TLS: tlsOptions}
	}
	return opts, nil
}

func buildHysteria2Options(u *url.URL, skipCertVerify bool) (option.Hysteria2OutboundOptions, error) {
	password := u.User.String()
	server, port, err := hostPort(u, 443)
	if err != nil {
		return option.Hysteria2OutboundOptions{}, err
	}
	query := u.Query()
	opts := option.Hysteria2OutboundOptions{
		ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
		Password:      password,
	}
	if up := query.Get("upMbps"); up != "" {
		opts.UpMbps = atoiDefault(up)
	}
	if down := query.Get("downMbps"); down != "" {
		opts.DownMbps = atoiDefault(down)
	}
	if obfs := query.Get("obfs"); obfs != "" {
		opts.Obfs = &option.Hysteria2Obfs{Type: obfs, Password: query.Get("obfs-password")}
	}
	opts.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{TLS: hysteriaTLSOptions(server, query, skipCertVerify)}
	return opts, nil
}

func hysteriaTLSOptions(host string, query url.Values, skipCertVerify bool) *option.OutboundTLSOptions {
	tlsOptions := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: host,
		Insecure:   skipCertVerify,
	}
	if sni := query.Get("sni"); sni != "" {
		tlsOptions.ServerName = sni
	}
	insecure := query.Get("insecure")
	if insecure == "" {
		insecure = query.Get("allowInsecure")
	}
	if insecure != "" {
		tlsOptions.Insecure = insecure == "1" || strings.EqualFold(insecure, "true")
	}
	if alpn := query.Get("alpn"); alpn != "" {
		tlsOptions.ALPN = badoption.Listable[string](strings.Split(alpn, ","))
	}
	return tlsOptions
}

func buildTLSOptions(query url.Values, skipCertVerify bool) (*option.OutboundTLSOptions, error) {
	security := strings.ToLower(query.Get("security"))
	if security == "" || security == "none" {
		return nil, nil
	}
	tlsOptions := &option.OutboundTLSOptions{Enabled: true, Insecure: skipCertVerify}
	if sni := query.Get("sni"); sni != "" {
		tlsOptions.ServerName = sni
	}
	insecure := query.Get("allowInsecure")
	if insecure == "" {
		insecure = query.Get("insecure")
	}
	if insecure != "" {
		tlsOptions.Insecure = insecure == "1" || strings.EqualFold(insecure, "true")
	}
	if alpn := query.Get("alpn"); alpn != "" {
		tlsOptions.ALPN = badoption.Listable[string](strings.Split(alpn, ","))
	}
	fp := query.Get("fp")
	if fp != "" {
		tlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fp}
	}
	if echValue := query.Get("ech"); echValue != "" {
		echOptions := &option.OutboundECHOptions{Enabled: true}
		configPEM, err := resolveECHConfigPEM(echValue)
		if err != nil {
			log.Printf("⚠️  Failed to prefetch ECH config for %q: %v (falling back to runtime DNS)", echValue, err)
		} else if strings.TrimSpace(configPEM) != "" {
			echOptions.Config = badoption.Listable[string](splitNonEmptyLines(configPEM))
		}
		tlsOptions.ECH = echOptions
	}
	if security == "reality" {
		shortID, err := normalizeRealityShortID(query.Get("sid"))
		if err != nil {
			return nil, err
		}
		tlsOptions.Reality = &option.OutboundRealityOptions{Enabled: true, PublicKey: query.Get("pbk"), ShortID: shortID}
		// Reality requires uTLS; use default fingerprint if not specified
		if tlsOptions.UTLS == nil {
			if fp == "" {
				fp = "chrome"
			}
			tlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fp}
		}
	}
	return tlsOptions, nil
}

func normalizeRealityShortID(value string) (string, error) {
	shortID := strings.TrimSpace(value)
	if shortID == "" {
		return "", nil
	}
	if _, err := hex.DecodeString(shortID); err != nil {
		return "", fmt.Errorf("invalid reality short_id %q: %w", shortID, err)
	}
	return strings.ToLower(shortID), nil
}

func splitNonEmptyLines(content string) []string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func parseECHQueryValue(value string) (string, string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "+", 2)
	queryServerName := strings.TrimSpace(parts[0])
	if queryServerName == "" {
		return "", "", false
	}
	dohURL := ""
	if len(parts) == 2 {
		dohURL = strings.TrimSpace(parts[1])
	}
	return queryServerName, dohURL, true
}

func resolveECHConfigPEMFromQuery(value string) (string, error) {
	queryServerName, dohURL, ok := parseECHQueryValue(value)
	if !ok {
		return "", fmt.Errorf("invalid ech query value %q", value)
	}
	if dohURL == "" {
		return "", nil
	}
	cacheKey := queryServerName + "|" + dohURL
	if cached, ok := echConfigCache.Load(cacheKey); ok {
		return cached.(string), nil
	}
	configList, err := fetchECHConfigList(queryServerName, dohURL, 15*time.Second)
	if err != nil {
		return "", err
	}
	configPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "ECH CONFIGS",
		Bytes: configList,
	}))
	echConfigCache.Store(cacheKey, configPEM)
	return configPEM, nil
}

func fetchECHConfigList(queryServerName string, dohURL string, timeout time.Duration) ([]byte, error) {
	message := new(mDNS.Msg)
	message.SetQuestion(mDNS.Fqdn(queryServerName), mDNS.TypeHTTPS)
	message.RecursionDesired = true
	wire, err := message.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack HTTPS RR query: %w", err)
	}
	request, err := http.NewRequest(http.MethodPost, dohURL, bytes.NewReader(wire))
	if err != nil {
		return nil, fmt.Errorf("create doh request: %w", err)
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute doh request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("doh returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read doh response: %w", err)
	}
	var dnsResponse mDNS.Msg
	if err := dnsResponse.Unpack(body); err != nil {
		return nil, fmt.Errorf("unpack doh response: %w", err)
	}
	if dnsResponse.Rcode != mDNS.RcodeSuccess {
		return nil, fmt.Errorf("doh rcode %s", mDNS.RcodeToString[dnsResponse.Rcode])
	}
	for _, rr := range dnsResponse.Answer {
		httpsRR, ok := rr.(*mDNS.HTTPS)
		if !ok {
			continue
		}
		for _, value := range httpsRR.Value {
			if value.Key().String() != "ech" {
				continue
			}
			if echValue, ok := value.(*mDNS.SVCBECHConfig); ok && len(echValue.ECH) > 0 {
				return echValue.ECH, nil
			}
			decoded, err := base64.StdEncoding.DecodeString(value.String())
			if err == nil && len(decoded) > 0 {
				return decoded, nil
			}
		}
	}
	return nil, fmt.Errorf("no ECH config found for %s via %s", queryServerName, dohURL)
}

func buildV2RayTransport(query url.Values) (*option.V2RayTransportOptions, error) {
	transportType, ok := config.NormalizeV2RayTransport(query.Get("type"))
	if !ok {
		return nil, fmt.Errorf("unsupported transport type %q", strings.TrimSpace(query.Get("type")))
	}
	if transportType == "" {
		return nil, nil
	}
	options := &option.V2RayTransportOptions{Type: transportType}
	switch transportType {
	case C.V2RayTransportTypeWebsocket:
		wsPath, earlyDataSize, earlyDataHeader := parseWebSocketPathAndEarlyData(query)
		options.WebsocketOptions.Path = wsPath
		if earlyDataSize > 0 {
			options.WebsocketOptions.MaxEarlyData = earlyDataSize
			options.WebsocketOptions.EarlyDataHeaderName = earlyDataHeader
		}
		headers := badoption.HTTPHeader{}
		if host := query.Get("host"); host != "" {
			headers["Host"] = []string{host}
		}
		if userAgent := websocketDefaultUserAgent(query.Get("fp")); userAgent != "" {
			headers["User-Agent"] = []string{userAgent}
		}
		if len(headers) > 0 {
			options.WebsocketOptions.Headers = headers
		}
	case C.V2RayTransportTypeHTTP:
		options.HTTPOptions.Path = query.Get("path")
		if host := query.Get("host"); host != "" {
			options.HTTPOptions.Host = badoption.Listable[string]{host}
		}
	case C.V2RayTransportTypeGRPC:
		options.GRPCOptions.ServiceName = query.Get("serviceName")
	case C.V2RayTransportTypeHTTPUpgrade:
		options.HTTPUpgradeOptions.Path = query.Get("path")
	default:
		return nil, fmt.Errorf("unsupported transport type %q", transportType)
	}
	return options, nil
}

func parseWebSocketPathAndEarlyData(query url.Values) (string, uint32, string) {
	rawPath := strings.TrimSpace(query.Get("path"))
	if rawPath == "" {
		rawPath = "/"
	} else if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}

	earlyDataHeader := strings.TrimSpace(query.Get("eh"))
	var embeddedQuery url.Values
	if idx := strings.Index(rawPath, "?"); idx != -1 && idx+1 < len(rawPath) {
		parsedQuery, err := url.ParseQuery(rawPath[idx+1:])
		if err == nil {
			embeddedQuery = parsedQuery
			if earlyDataHeader == "" {
				earlyDataHeader = strings.TrimSpace(parsedQuery.Get("eh"))
			}
		}
	}
	if earlyDataHeader == "" {
		earlyDataHeader = "Sec-WebSocket-Protocol"
	}

	earlyDataValue := strings.TrimSpace(query.Get("ed"))
	if earlyDataValue == "" && embeddedQuery != nil {
		earlyDataValue = strings.TrimSpace(embeddedQuery.Get("ed"))
	}
	if earlyDataValue == "" {
		return rawPath, 0, earlyDataHeader
	}

	earlyDataSize, err := strconv.Atoi(earlyDataValue)
	if err != nil || earlyDataSize <= 0 {
		return rawPath, 0, earlyDataHeader
	}

	return rawPath, uint32(earlyDataSize), earlyDataHeader
}

func websocketDefaultUserAgent(fingerprint string) string {
	switch strings.ToLower(strings.TrimSpace(fingerprint)) {
	case "firefox":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:137.0) Gecko/20100101 Firefox/137.0"
	case "safari":
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Safari/605.1.15"
	case "edge":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36 Edg/135.0.0.0"
	case "ios":
		return "Mozilla/5.0 (iPhone; CPU iPhone OS 18_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Mobile/15E148 Safari/604.1"
	case "android":
		return "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Mobile Safari/537.36"
	case "chrome", "random", "randomized", "":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
	default:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
	}
}

func buildShadowsocksOptions(u *url.URL) (option.ShadowsocksOutboundOptions, error) {
	server, port, err := hostPort(u, 8388)
	if err != nil {
		return option.ShadowsocksOutboundOptions{}, err
	}

	// Decode userinfo (base64 encoded method:password)
	userInfo := u.User.String()
	decoded, err := base64.RawURLEncoding.DecodeString(userInfo)
	if err != nil {
		// Try standard base64
		decoded, err = base64.StdEncoding.DecodeString(userInfo)
		if err != nil {
			return option.ShadowsocksOutboundOptions{}, fmt.Errorf("decode shadowsocks userinfo: %w", err)
		}
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return option.ShadowsocksOutboundOptions{}, errors.New("shadowsocks userinfo format must be method:password")
	}

	method := parts[0]
	password := parts[1]

	opts := option.ShadowsocksOutboundOptions{
		ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
		Method:        method,
		Password:      password,
	}

	query := u.Query()
	if plugin := query.Get("plugin"); plugin != "" {
		opts.Plugin = plugin
		opts.PluginOptions = query.Get("plugin-opts")
	}

	return opts, nil
}

func buildTrojanOptions(u *url.URL, skipCertVerify bool) (option.TrojanOutboundOptions, error) {
	password := u.User.Username()
	if password == "" {
		return option.TrojanOutboundOptions{}, errors.New("trojan uri missing password in userinfo")
	}

	server, port, err := hostPort(u, 443)
	if err != nil {
		return option.TrojanOutboundOptions{}, err
	}

	query := u.Query()
	opts := option.TrojanOutboundOptions{
		ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
		Password:      password,
		Network:       option.NetworkList(""),
	}

	// Parse TLS options
	if tlsOptions, err := buildTrojanTLSOptions(query, skipCertVerify); err != nil {
		return option.TrojanOutboundOptions{}, err
	} else if tlsOptions != nil {
		opts.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{TLS: tlsOptions}
	}

	// Parse transport options
	if transport, err := buildV2RayTransport(query); err != nil {
		return option.TrojanOutboundOptions{}, err
	} else if transport != nil {
		opts.Transport = transport
	}

	return opts, nil
}

// vmessJSON represents the JSON structure of a VMess URI
type vmessJSON struct {
	V    interface{} `json:"v"`    // Version, can be string or int
	PS   string      `json:"ps"`   // Remarks/name
	Add  string      `json:"add"`  // Server address
	Port interface{} `json:"port"` // Server port, can be string or int
	ID   string      `json:"id"`   // UUID
	Aid  interface{} `json:"aid"`  // Alter ID, can be string or int
	Scy  string      `json:"scy"`  // Security/cipher
	Net  string      `json:"net"`  // Network type (tcp, ws, etc.)
	Type string      `json:"type"` // Header type
	Host string      `json:"host"` // Host header
	Path string      `json:"path"` // Path
	TLS  string      `json:"tls"`  // TLS (tls or empty)
	SNI  string      `json:"sni"`  // SNI
	ALPN string      `json:"alpn"` // ALPN
	FP   string      `json:"fp"`   // Fingerprint
}

func (v *vmessJSON) GetPort() int {
	switch p := v.Port.(type) {
	case float64:
		return int(p)
	case int:
		return p
	case string:
		port, _ := strconv.Atoi(p)
		return port
	}
	return 443
}

func (v *vmessJSON) GetAlterId() int {
	switch a := v.Aid.(type) {
	case float64:
		return int(a)
	case int:
		return a
	case string:
		aid, _ := strconv.Atoi(a)
		return aid
	}
	return 0
}

func buildVMessOptions(rawURI string, skipCertVerify bool) (option.VMessOutboundOptions, error) {
	// Remove vmess:// prefix
	encoded := strings.TrimPrefix(rawURI, "vmess://")

	// Try to decode as base64 JSON (standard format)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Try URL-safe base64
		decoded, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			// Try as URL format: vmess://uuid@server:port?...
			return buildVMessOptionsFromURL(rawURI, skipCertVerify)
		}
	}

	var vmess vmessJSON
	if err := json.Unmarshal(decoded, &vmess); err != nil {
		return option.VMessOutboundOptions{}, fmt.Errorf("parse vmess json: %w", err)
	}

	if vmess.Add == "" {
		return option.VMessOutboundOptions{}, errors.New("vmess missing server address")
	}
	if vmess.ID == "" {
		return option.VMessOutboundOptions{}, errors.New("vmess missing uuid")
	}

	port := vmess.GetPort()
	if port == 0 {
		port = 443
	}

	opts := option.VMessOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     vmess.Add,
			ServerPort: uint16(port),
		},
		UUID:     vmess.ID,
		AlterId:  vmess.GetAlterId(),
		Security: vmess.Scy,
	}

	// Default security
	if opts.Security == "" {
		opts.Security = "auto"
	}

	// Build transport options
	if transportType, ok := config.NormalizeV2RayTransport(vmess.Net); ok {
		if transportType != "" {
			transport := &option.V2RayTransportOptions{}
			switch transportType {
			case C.V2RayTransportTypeWebsocket:
				transport.Type = C.V2RayTransportTypeWebsocket
				wsPath := vmess.Path
				// Handle early data in path
				if idx := strings.Index(wsPath, "?ed="); idx != -1 {
					edPart := wsPath[idx+4:]
					wsPath = wsPath[:idx]
					edValue := edPart
					if ampIdx := strings.Index(edPart, "&"); ampIdx != -1 {
						edValue = edPart[:ampIdx]
					}
					if ed, err := strconv.Atoi(edValue); err == nil && ed > 0 {
						transport.WebsocketOptions.MaxEarlyData = uint32(ed)
						transport.WebsocketOptions.EarlyDataHeaderName = "Sec-WebSocket-Protocol"
					}
				}
				transport.WebsocketOptions.Path = wsPath
				if vmess.Host != "" {
					transport.WebsocketOptions.Headers = badoption.HTTPHeader{"Host": {vmess.Host}}
				}
			case C.V2RayTransportTypeHTTP:
				transport.Type = C.V2RayTransportTypeHTTP
				transport.HTTPOptions.Path = vmess.Path
				if vmess.Host != "" {
					transport.HTTPOptions.Host = badoption.Listable[string]{vmess.Host}
				}
			case C.V2RayTransportTypeGRPC:
				transport.Type = C.V2RayTransportTypeGRPC
				transport.GRPCOptions.ServiceName = vmess.Path
			case C.V2RayTransportTypeHTTPUpgrade:
				transport.Type = C.V2RayTransportTypeHTTPUpgrade
				transport.HTTPUpgradeOptions.Path = vmess.Path
				if vmess.Host != "" {
					transport.HTTPUpgradeOptions.Headers = badoption.HTTPHeader{"Host": {vmess.Host}}
				}
			default:
				return option.VMessOutboundOptions{}, fmt.Errorf("unsupported vmess transport type %q", vmess.Net)
			}
			opts.Transport = transport
		}
	} else {
		return option.VMessOutboundOptions{}, fmt.Errorf("unsupported vmess transport type %q", vmess.Net)
	}

	// Build TLS options
	if vmess.TLS == "tls" {
		tlsOptions := &option.OutboundTLSOptions{Enabled: true, Insecure: skipCertVerify}
		if vmess.SNI != "" {
			tlsOptions.ServerName = vmess.SNI
		} else if vmess.Host != "" {
			tlsOptions.ServerName = vmess.Host
		}
		if vmess.ALPN != "" {
			tlsOptions.ALPN = badoption.Listable[string](strings.Split(vmess.ALPN, ","))
		}
		if vmess.FP != "" {
			tlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: vmess.FP}
		}
		opts.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{TLS: tlsOptions}
	}

	return opts, nil
}

func buildVMessOptionsFromURL(rawURI string, skipCertVerify bool) (option.VMessOutboundOptions, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return option.VMessOutboundOptions{}, fmt.Errorf("parse vmess url: %w", err)
	}

	uuid := parsed.User.Username()
	if uuid == "" {
		return option.VMessOutboundOptions{}, errors.New("vmess uri missing uuid")
	}

	server, port, err := hostPort(parsed, 443)
	if err != nil {
		return option.VMessOutboundOptions{}, err
	}

	query := parsed.Query()
	opts := option.VMessOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     server,
			ServerPort: uint16(port),
		},
		UUID:     uuid,
		Security: query.Get("encryption"),
	}

	if opts.Security == "" {
		opts.Security = "auto"
	}

	if aid := query.Get("alterId"); aid != "" {
		opts.AlterId, _ = strconv.Atoi(aid)
	}

	// Build transport
	if transport, err := buildV2RayTransport(query); err != nil {
		return option.VMessOutboundOptions{}, err
	} else if transport != nil {
		opts.Transport = transport
	}

	// Build TLS
	if tlsOptions, err := buildTLSOptions(query, skipCertVerify); err != nil {
		return option.VMessOutboundOptions{}, err
	} else if tlsOptions != nil {
		opts.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{TLS: tlsOptions}
	}

	return opts, nil
}

func buildTrojanTLSOptions(query url.Values, skipCertVerify bool) (*option.OutboundTLSOptions, error) {
	// Trojan always uses TLS by default
	tlsOptions := &option.OutboundTLSOptions{Enabled: true, Insecure: skipCertVerify}

	if sni := query.Get("sni"); sni != "" {
		tlsOptions.ServerName = sni
	}
	if peer := query.Get("peer"); peer != "" && tlsOptions.ServerName == "" {
		tlsOptions.ServerName = peer
	}

	insecure := query.Get("allowInsecure")
	if insecure == "" {
		insecure = query.Get("insecure")
	}
	if insecure != "" {
		tlsOptions.Insecure = insecure == "1" || strings.EqualFold(insecure, "true")
	}

	if alpn := query.Get("alpn"); alpn != "" {
		tlsOptions.ALPN = badoption.Listable[string](strings.Split(alpn, ","))
	}

	if fp := query.Get("fp"); fp != "" {
		tlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fp}
	}

	return tlsOptions, nil
}

func hostPort(u *url.URL, defaultPort int) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, errors.New("missing host")
	}
	portStr := u.Port()
	if portStr == "" {
		portStr = strconv.Itoa(defaultPort)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q", portStr)
	}
	return host, port, nil
}

func parseAddr(value string) (*badoption.Addr, error) {
	addr := strings.TrimSpace(value)
	if addr == "" {
		return nil, nil
	}
	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		return nil, err
	}
	bad := badoption.Addr(parsed)
	return &bad, nil
}

func sanitizeTag(name string) string {
	lower := strings.ToLower(name)
	lower = strings.TrimSpace(lower)
	if lower == "" {
		return ""
	}
	segments := strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	result := strings.Join(segments, "-")
	result = strings.Trim(result, "-")
	return result
}

func atoiDefault(value string) int {
	if strings.HasSuffix(value, "mbps") {
		value = strings.TrimSuffix(value, "mbps")
	}
	if strings.HasSuffix(value, "Mbps") {
		value = strings.TrimSuffix(value, "Mbps")
	}
	v, _ := strconv.Atoi(value)
	return v
}

type nodeRoutingTraits struct {
	ProtocolFamily string
	NodeMode       string
	DomainFamily   string
}

func describeNodeRoutingTraits(rawURI string) nodeRoutingTraits {
	trimmed := strings.TrimSpace(rawURI)
	if trimmed == "" {
		return nodeRoutingTraits{}
	}

	protocol := normalizeProtocolFamily(uriScheme(trimmed))
	traits := nodeRoutingTraits{ProtocolFamily: protocol}
	if protocol == "vmess" {
		if parsed, ok := parseVMessTraits(trimmed); ok {
			return parsed
		}
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		traits.NodeMode = defaultNodeMode(protocol)
		return traits
	}

	query := parsed.Query()
	server := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	security := normalizeSecurity(protocol, strings.TrimSpace(query.Get("security")))
	transport := normalizeTransport(protocol, strings.TrimSpace(query.Get("type")), strings.TrimSpace(query.Get("network")))
	hostHint := firstNonEmpty(query.Get("sni"), query.Get("peer"), query.Get("host"), server)

	traits.NodeMode = buildNodeMode(security, transport)
	traits.DomainFamily = domainFamily(hostHint)
	return traits
}

func parseVMessTraits(rawURI string) (nodeRoutingTraits, bool) {
	traits := nodeRoutingTraits{ProtocolFamily: "vmess"}
	encoded := strings.TrimPrefix(strings.TrimSpace(rawURI), "vmess://")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return nodeRoutingTraits{}, false
		}
	}

	var vmess vmessJSON
	if err := json.Unmarshal(decoded, &vmess); err != nil {
		return nodeRoutingTraits{}, false
	}

	security := "plain"
	if strings.EqualFold(strings.TrimSpace(vmess.TLS), "tls") {
		security = "tls"
	}
	transport := normalizeTransport("vmess", strings.TrimSpace(vmess.Net), "")
	hostHint := firstNonEmpty(vmess.SNI, vmess.Host, vmess.Add)

	traits.NodeMode = buildNodeMode(security, transport)
	traits.DomainFamily = domainFamily(hostHint)
	return traits, true
}

func uriScheme(rawURI string) string {
	if idx := strings.Index(rawURI, "://"); idx > 0 {
		return strings.ToLower(strings.TrimSpace(rawURI[:idx]))
	}
	return ""
}

func normalizeProtocolFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hy2":
		return "hysteria2"
	case "shadowsocks":
		return "ss"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeSecurity(protocol string, value string) string {
	security := strings.ToLower(strings.TrimSpace(value))
	if security != "" && security != "none" {
		return security
	}
	switch protocol {
	case "trojan", "anytls", "hysteria2":
		return "tls"
	default:
		return "plain"
	}
}

func normalizeTransport(protocol string, primary string, fallback string) string {
	if transport, ok := config.NormalizeV2RayTransport(primary); ok && transport != "" {
		return transport
	}
	if transport, ok := config.NormalizeV2RayTransport(fallback); ok && transport != "" {
		return transport
	}
	switch protocol {
	case "hysteria2":
		return "udp"
	default:
		return "tcp"
	}
}

func buildNodeMode(security string, transport string) string {
	left := strings.TrimSpace(strings.ToLower(security))
	if left == "" {
		left = "plain"
	}
	right := strings.TrimSpace(strings.ToLower(transport))
	if right == "" {
		right = "tcp"
	}
	return left + "/" + right
}

func defaultNodeMode(protocol string) string {
	return buildNodeMode(normalizeSecurity(protocol, ""), normalizeTransport(protocol, "", ""))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func domainFamily(host string) string {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	if trimmed == "" {
		return ""
	}
	if parsed := net.ParseIP(trimmed); parsed != nil {
		if ipv4 := parsed.To4(); ipv4 != nil {
			return fmt.Sprintf("%d.%d.%d.0/24", ipv4[0], ipv4[1], ipv4[2])
		}
		return parsed.String()
	}
	labels := strings.Split(trimmed, ".")
	if len(labels) <= 2 {
		return trimmed
	}
	suffix := labels[len(labels)-2] + "." + labels[len(labels)-1]
	if len(labels) >= 3 && usesThreeLabelSuffix(suffix) {
		return labels[len(labels)-3] + "." + suffix
	}
	return suffix
}

func usesThreeLabelSuffix(value string) bool {
	switch value {
	case "co.uk", "org.uk", "ac.uk", "gov.uk", "com.cn", "net.cn", "org.cn", "com.hk", "com.tw", "co.jp":
		return true
	default:
		return false
	}
}

// printProxyLinks prints all proxy connection information at startup
func printProxyLinks(cfg *config.Config, metadata map[string]poolout.MemberMeta) {
	log.Println("")
	log.Println("📡 Proxy Links:")
	log.Println("═══════════════════════════════════════════════════════════════")

	showPoolEntry := cfg.Mode == "pool" || cfg.Mode == "hybrid"
	showMultiPort := cfg.Mode == "multi-port" || cfg.Mode == "hybrid"

	if showPoolEntry {
		// Pool mode: single entry point for all nodes
		var auth string
		if cfg.Listener.Username != "" {
			auth = fmt.Sprintf("%s:%s@", cfg.Listener.Username, cfg.Listener.Password)
		}
		proxyURL := fmt.Sprintf("http://%s%s:%d", auth, cfg.Listener.Address, cfg.Listener.Port)
		log.Printf("🌐 Pool Entry Point:")
		log.Printf("   %s [%s]", proxyURL, cfg.Pool.Mode)

		// Print extra listeners
		for _, extra := range cfg.ExtraListeners {
			if extra.Port == 0 {
				continue
			}
			var extraAuth string
			if extra.Username != "" {
				extraAuth = fmt.Sprintf("%s:%s@", extra.Username, extra.Password)
			}
			addr := extra.Address
			if addr == "" {
				addr = cfg.Listener.Address
			}
			mode := extra.PoolMode
			if mode == "" {
				mode = cfg.Pool.Mode
			}
			extraURL := fmt.Sprintf("http://%s%s:%d", extraAuth, addr, extra.Port)
			log.Printf("   %s [%s]", extraURL, mode)
		}

		log.Println("")
		log.Printf("   Nodes in pool (%d):", len(metadata))
		for _, meta := range metadata {
			log.Printf("   • %s", meta.Name)
		}
		if showMultiPort {
			log.Println("")
		}
	}

	if showMultiPort {
		// Multi-port mode: each node has its own port
		log.Printf("🔌 Multi-Port Entry Points (%d nodes):", len(metadata))
		log.Println("")
		entries := make([]poolout.MemberMeta, 0, len(metadata))
		for _, meta := range metadata {
			entries = append(entries, meta)
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].Port != entries[j].Port {
				return entries[i].Port < entries[j].Port
			}
			return entries[i].Name < entries[j].Name
		})
		for _, meta := range entries {
			var auth string
			username := cfg.MultiPort.Username
			password := cfg.MultiPort.Password
			if username != "" {
				auth = fmt.Sprintf("%s:%s@", username, password)
			}
			proxyURL := fmt.Sprintf("http://%s%s:%d", auth, cfg.MultiPort.Address, meta.Port)
			log.Printf("   [%d] %s", meta.Port, meta.Name)
			log.Printf("       %s", proxyURL)
		}
	}

	log.Println("═══════════════════════════════════════════════════════════════")
	log.Println("")
}
