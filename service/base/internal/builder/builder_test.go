package builder

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"easy_proxies/internal/config"
	poolout "easy_proxies/internal/outbound/pool"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestBuildReportsNoValidNodesWithSentinel(t *testing.T) {
	cfg := &config.Config{
		Mode:  "pool",
		Nodes: []config.NodeConfig{{Name: "broken", URI: "unsupported://node.example"}},
	}

	if HasValidNode(cfg) {
		t.Fatal("HasValidNode() = true for an entirely invalid node set")
	}
	_, err := Build(cfg)
	if !errors.Is(err, ErrNoValidNodes) {
		t.Fatalf("Build error = %v, want ErrNoValidNodes", err)
	}
}

func TestHasValidNodeStopsAtFirstBuildableNode(t *testing.T) {
	cfg := &config.Config{
		Mode: "pool",
		Nodes: []config.NodeConfig{
			{Name: "broken", URI: "unsupported://node.example"},
			{Name: "working", URI: "socks5://127.0.0.1:1080"},
		},
	}
	if !HasValidNode(cfg) {
		t.Fatal("HasValidNode() = false with a buildable node")
	}
}

func TestBuildMultiPortRoutingIncludesGlobalPoolWithoutPlainPoolInbound(t *testing.T) {
	cfg := multiPortBuildConfig(true)

	opts, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	outboundTags := make(map[string]bool, len(opts.Outbounds))
	for _, outbound := range opts.Outbounds {
		outboundTags[outbound.Tag] = true
	}
	if !outboundTags[poolout.Tag] {
		t.Fatalf("outbounds = %#v, want global %q outbound for smart routing", outboundTags, poolout.Tag)
	}
	if !outboundTags[poolout.Tag+"-node-one"] {
		t.Fatalf("outbounds = %#v, want per-node pool outbound", outboundTags)
	}

	inboundTags := make(map[string]bool, len(opts.Inbounds))
	for _, inbound := range opts.Inbounds {
		inboundTags[inbound.Tag] = true
	}
	if inboundTags["http-in"] {
		t.Fatalf("inbounds = %#v, pure multi-port mode must not add the plain pool inbound", inboundTags)
	}
	if !inboundTags["in-node-one"] {
		t.Fatalf("inbounds = %#v, want per-node multi-port inbound", inboundTags)
	}

	if opts.Route == nil {
		t.Fatal("route options are nil")
	}
	if opts.Route.Final != "" {
		t.Fatalf("route final = %q, pure multi-port mode must not set the global sing-box final", opts.Route.Final)
	}
	if len(opts.Route.Rules) != 1 {
		t.Fatalf("route rules = %d, want one per-node inbound route", len(opts.Route.Rules))
	}
	if got := opts.Route.Rules[0].DefaultOptions.RouteOptions.Outbound; got != poolout.Tag+"-node-one" {
		t.Fatalf("per-node route outbound = %q, want %q", got, poolout.Tag+"-node-one")
	}
}

func TestBuildMultiPortWithoutRoutingOmitsGlobalPool(t *testing.T) {
	cfg := multiPortBuildConfig(false)

	opts, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	outboundTags := make(map[string]bool, len(opts.Outbounds))
	for _, outbound := range opts.Outbounds {
		outboundTags[outbound.Tag] = true
	}
	if outboundTags[poolout.Tag] {
		t.Fatalf("outbounds = %#v, routing-disabled multi-port mode must not add global %q", outboundTags, poolout.Tag)
	}
	if !outboundTags[poolout.Tag+"-node-one"] {
		t.Fatalf("outbounds = %#v, want per-node pool outbound", outboundTags)
	}

	inboundTags := make(map[string]bool, len(opts.Inbounds))
	for _, inbound := range opts.Inbounds {
		inboundTags[inbound.Tag] = true
	}
	if !inboundTags["in-node-one"] {
		t.Fatalf("inbounds = %#v, want per-node multi-port inbound", inboundTags)
	}

	if opts.Route == nil {
		t.Fatal("route options are nil")
	}
	if opts.Route.Final != "" {
		t.Fatalf("route final = %q, pure multi-port mode must not set the global sing-box final", opts.Route.Final)
	}
}

func TestBuildLocalServerSuppressesPlainInboundAndKeepsPoolOutbound(t *testing.T) {
	cfg := localServerBuildConfig()

	opts, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	inboundTags := inboundTagSet(opts.Inbounds)
	if inboundTags["http-in"] {
		t.Fatalf("inbounds = %#v, Local Server must suppress the plain pool inbound", inboundTags)
	}

	outboundTags := outboundTagSet(opts.Outbounds)
	if !outboundTags[poolout.Tag] {
		t.Fatalf("outbounds = %#v, want global %q outbound for Local Server dispatcher", outboundTags, poolout.Tag)
	}

	if opts.Route == nil {
		t.Fatal("route options are nil")
	}
	if got, want := opts.Route.Final, poolout.Tag; got != want {
		t.Fatalf("route final = %q, want %q", got, want)
	}
}

func TestBuildLegacyRoutingRouteBKeepsPlainInbound(t *testing.T) {
	cfg := localServerBuildConfig()
	cfg.LocalServer.Enabled = false
	cfg.Routing = config.RoutingConfig{
		Enabled: true,
		Listen:  "127.0.0.1:22324",
	}

	opts, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	inboundTags := inboundTagSet(opts.Inbounds)
	if !inboundTags["http-in"] {
		t.Fatalf("inbounds = %#v, legacy route-B routing must keep the plain pool inbound", inboundTags)
	}

	outboundTags := outboundTagSet(opts.Outbounds)
	if !outboundTags[poolout.Tag] {
		t.Fatalf("outbounds = %#v, want global %q outbound for smart routing", outboundTags, poolout.Tag)
	}
}

func multiPortBuildConfig(routingEnabled bool) *config.Config {
	return &config.Config{
		Mode: "multi-port",
		Listener: config.ListenerConfig{
			Address:  "127.0.0.1",
			Port:     22323,
			Protocol: config.InboundProtocolHTTP,
		},
		MultiPort: config.MultiPortConfig{
			Address:  "127.0.0.1",
			Protocol: config.InboundProtocolHTTP,
		},
		Pool: config.PoolConfig{
			Mode: "auto",
		},
		Routing: config.RoutingConfig{
			Enabled: routingEnabled,
		},
		Nodes: []config.NodeConfig{
			{
				Name: "node-one",
				URI:  "socks5://127.0.0.1:1080",
				Port: 25001,
			},
		},
	}
}

func localServerBuildConfig() *config.Config {
	return &config.Config{
		Mode: "pool",
		Listener: config.ListenerConfig{
			Address:  "127.0.0.1",
			Port:     22323,
			Protocol: config.InboundProtocolMixed,
			Username: "easyproxy",
			Password: "shared-secret",
		},
		Pool: config.PoolConfig{
			Mode: "auto",
		},
		LocalServer: config.LocalServerConfig{
			Enabled: true,
			Listen:  "127.0.0.1:32323",
			Auth: config.LocalServerAuthConfig{
				Username: "easyproxy",
				Password: "shared-secret",
			},
		},
		Nodes: []config.NodeConfig{
			{
				Name: "node-one",
				URI:  "socks5://127.0.0.1:1080",
				Port: 25001,
			},
		},
	}
}

func inboundTagSet(inbounds []option.Inbound) map[string]bool {
	tags := make(map[string]bool, len(inbounds))
	for _, inbound := range inbounds {
		tags[inbound.Tag] = true
	}
	return tags
}

func outboundTagSet(outbounds []option.Outbound) map[string]bool {
	tags := make(map[string]bool, len(outbounds))
	for _, outbound := range outbounds {
		tags[outbound.Tag] = true
	}
	return tags
}

func TestBuildNodeOutboundSupportsSOCKS5(t *testing.T) {
	outbound, err := buildNodeOutbound("socks-node", "socks5://demo:secret@99.144.123.135:30350", false)
	if err != nil {
		t.Fatalf("buildNodeOutbound returned error: %v", err)
	}
	if outbound.Type != C.TypeSOCKS {
		t.Fatalf("outbound type = %q, want %q", outbound.Type, C.TypeSOCKS)
	}

	opts, ok := outbound.Options.(*option.SOCKSOutboundOptions)
	if !ok {
		t.Fatalf("outbound options type = %T, want *option.SOCKSOutboundOptions", outbound.Options)
	}
	if opts.Server != "99.144.123.135" {
		t.Fatalf("server = %q, want %q", opts.Server, "99.144.123.135")
	}
	if opts.ServerPort != 30350 {
		t.Fatalf("server port = %d, want %d", opts.ServerPort, 30350)
	}
	if opts.Username != "demo" {
		t.Fatalf("username = %q, want %q", opts.Username, "demo")
	}
	if opts.Password != "secret" {
		t.Fatalf("password = %q, want %q", opts.Password, "secret")
	}
	if opts.Version != "5" {
		t.Fatalf("version = %q, want %q", opts.Version, "5")
	}
}

func TestBuildNodeOutboundSupportsHTTP(t *testing.T) {
	outbound, err := buildNodeOutbound("http-node", "http://alice:wonderland@example.com:8080/proxy", false)
	if err != nil {
		t.Fatalf("buildNodeOutbound returned error: %v", err)
	}
	if outbound.Type != C.TypeHTTP {
		t.Fatalf("outbound type = %q, want %q", outbound.Type, C.TypeHTTP)
	}

	opts, ok := outbound.Options.(*option.HTTPOutboundOptions)
	if !ok {
		t.Fatalf("outbound options type = %T, want *option.HTTPOutboundOptions", outbound.Options)
	}
	if opts.Server != "example.com" {
		t.Fatalf("server = %q, want %q", opts.Server, "example.com")
	}
	if opts.ServerPort != 8080 {
		t.Fatalf("server port = %d, want %d", opts.ServerPort, 8080)
	}
	if opts.Username != "alice" {
		t.Fatalf("username = %q, want %q", opts.Username, "alice")
	}
	if opts.Password != "wonderland" {
		t.Fatalf("password = %q, want %q", opts.Password, "wonderland")
	}
	if opts.Path != "/proxy" {
		t.Fatalf("path = %q, want %q", opts.Path, "/proxy")
	}
}

func TestBuildNodeOutboundSupportsShadowsocksObfsPlugin(t *testing.T) {
	uri := "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTp0MHNybWR4cm0zeHlqbnZxejlld2x4YjJteXE3cmp1dg==@b497b27.r8.glados-config.net:2377?plugin=obfs-local&plugin-opts=obfs%3Dtls%3Bobfs-host%3Db497b27.default.microsoft.lt%3A100531#Glados-SS"

	outbound, err := buildNodeOutbound("ss-obfs-node", uri, false)
	if err != nil {
		t.Fatalf("buildNodeOutbound returned error: %v", err)
	}
	if outbound.Type != C.TypeShadowsocks {
		t.Fatalf("outbound type = %q, want %q", outbound.Type, C.TypeShadowsocks)
	}

	opts, ok := outbound.Options.(*option.ShadowsocksOutboundOptions)
	if !ok {
		t.Fatalf("outbound options type = %T, want *option.ShadowsocksOutboundOptions", outbound.Options)
	}
	if opts.Plugin != "obfs-local" {
		t.Fatalf("plugin = %q, want %q", opts.Plugin, "obfs-local")
	}
	if opts.PluginOptions != "obfs=tls;obfs-host=b497b27.default.microsoft.lt:100531" {
		t.Fatalf("plugin options = %q", opts.PluginOptions)
	}
}

func TestBuildNodeOutboundRejectsUnsupportedShadowsocksPlugin(t *testing.T) {
	uri := "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpzZWNyZXQ=@example.com:8388?plugin=gost-plugin&plugin-opts=mode%3Dtls#unsupported-plugin"

	_, err := buildNodeOutbound("unsupported-ss-plugin", uri, false)
	if err == nil {
		t.Fatal("expected unsupported shadowsocks plugin to be rejected before sing-box startup")
	}
	if !strings.Contains(err.Error(), "unsupported shadowsocks plugin") {
		t.Fatalf("expected unsupported plugin error, got %v", err)
	}
}

func TestBuildNodeOutboundTreatsRawVMessTransportAsTCP(t *testing.T) {
	vmessJSON := `{"v":"2","ps":"raw-test","add":"example.com","port":"443","id":"11111111-1111-1111-1111-111111111111","aid":"0","net":"raw"}`
	uri := "vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessJSON))

	outbound, err := buildNodeOutbound("vmess-raw-node", uri, false)
	if err != nil {
		t.Fatalf("buildNodeOutbound returned error: %v", err)
	}
	if outbound.Type != C.TypeVMess {
		t.Fatalf("outbound type = %q, want %q", outbound.Type, C.TypeVMess)
	}

	opts, ok := outbound.Options.(*option.VMessOutboundOptions)
	if !ok {
		t.Fatalf("outbound options type = %T, want *option.VMessOutboundOptions", outbound.Options)
	}
	if opts.Transport != nil {
		t.Fatalf("expected raw vmess transport to be normalized to tcp (nil transport), got %+v", opts.Transport)
	}
}

func TestBuildNodeOutboundSupportsVMessH2URLAlias(t *testing.T) {
	uri := "vmess://11111111-1111-1111-1111-111111111111@example.com:443?type=h2&path=%2Fhttp"

	outbound, err := buildNodeOutbound("vmess-h2-node", uri, false)
	if err != nil {
		t.Fatalf("buildNodeOutbound returned error: %v", err)
	}
	if outbound.Type != C.TypeVMess {
		t.Fatalf("outbound type = %q, want %q", outbound.Type, C.TypeVMess)
	}

	opts, ok := outbound.Options.(*option.VMessOutboundOptions)
	if !ok {
		t.Fatalf("outbound options type = %T, want *option.VMessOutboundOptions", outbound.Options)
	}
	if opts.Transport == nil {
		t.Fatal("expected vmess h2 alias to produce an HTTP transport")
	}
	if opts.Transport.Type != C.V2RayTransportTypeHTTP {
		t.Fatalf("transport type = %q, want %q", opts.Transport.Type, C.V2RayTransportTypeHTTP)
	}
	if opts.Transport.HTTPOptions.Path != "/http" {
		t.Fatalf("http path = %q, want %q", opts.Transport.HTTPOptions.Path, "/http")
	}
}

func TestBuildNodeOutboundEnablesStandardECHForVLESS(t *testing.T) {
	originalResolver := resolveECHConfigPEM
	resolveECHConfigPEM = func(value string) (string, error) {
		if value != "cloudflare-ech.com+https://dns.alidns.com/dns-query" {
			t.Fatalf("unexpected ech query value: %s", value)
		}
		return "-----BEGIN ECH CONFIGS-----\nZWNobGlzdA==\n-----END ECH CONFIGS-----\n", nil
	}
	defer func() {
		resolveECHConfigPEM = originalResolver
	}()

	uri := "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&type=ws&host=edge.example.com&sni=edge.example.com&fp=chrome&ech=cloudflare-ech.com%2Bhttps%3A%2F%2Fdns.alidns.com%2Fdns-query&path=%2Fws"

	outbound, err := buildNodeOutbound("vless-ech-node", uri, false)
	if err != nil {
		t.Fatalf("buildNodeOutbound returned error: %v", err)
	}
	if outbound.Type != C.TypeVLESS {
		t.Fatalf("outbound type = %q, want %q", outbound.Type, C.TypeVLESS)
	}

	opts, ok := outbound.Options.(*option.VLESSOutboundOptions)
	if !ok {
		t.Fatalf("outbound options type = %T, want *option.VLESSOutboundOptions", outbound.Options)
	}
	if opts.OutboundTLSOptionsContainer.TLS == nil {
		t.Fatal("expected TLS options to be present")
	}
	if opts.OutboundTLSOptionsContainer.TLS.ECH == nil || !opts.OutboundTLSOptionsContainer.TLS.ECH.Enabled {
		t.Fatalf("expected ECH to be enabled, got %+v", opts.OutboundTLSOptionsContainer.TLS.ECH)
	}
	if len(opts.OutboundTLSOptionsContainer.TLS.ECH.Config) == 0 {
		t.Fatal("expected inline ECH config to be populated")
	}
}

func TestBuildNodeOutboundPreservesWebSocketPathWithEarlyDataQuery(t *testing.T) {
	originalResolver := resolveECHConfigPEM
	resolveECHConfigPEM = func(value string) (string, error) {
		return "-----BEGIN ECH CONFIGS-----\nZWNobGlzdA==\n-----END ECH CONFIGS-----\n", nil
	}
	defer func() {
		resolveECHConfigPEM = originalResolver
	}()

	uri := "vless://11111111-1111-1111-1111-111111111111@27.50.48.8:443?encryption=none&security=tls&type=ws&ech=cloudflare-ech.com%2Bhttps%3A%2F%2Fdns.alidns.com%2Fdns-query&host=snip.zrfme.ccwu.cc&fp=chrome&sni=snip.zrfme.ccwu.cc&path=%2FTelegram%40lsmoo%26%3Fed%3D2560"

	outbound, err := buildNodeOutbound("vless-ech-ws-node", uri, false)
	if err != nil {
		t.Fatalf("buildNodeOutbound returned error: %v", err)
	}
	opts, ok := outbound.Options.(*option.VLESSOutboundOptions)
	if !ok {
		t.Fatalf("outbound options type = %T, want *option.VLESSOutboundOptions", outbound.Options)
	}
	if opts.Transport == nil {
		t.Fatal("expected websocket transport to be configured")
	}
	if opts.Transport.Type != C.V2RayTransportTypeWebsocket {
		t.Fatalf("transport type = %q, want %q", opts.Transport.Type, C.V2RayTransportTypeWebsocket)
	}
	if opts.Transport.WebsocketOptions.Path != "/Telegram@lsmoo&?ed=2560" {
		t.Fatalf("websocket path = %q", opts.Transport.WebsocketOptions.Path)
	}
	if opts.Transport.WebsocketOptions.MaxEarlyData != 2560 {
		t.Fatalf("max early data = %d, want %d", opts.Transport.WebsocketOptions.MaxEarlyData, 2560)
	}
	if opts.Transport.WebsocketOptions.EarlyDataHeaderName != "Sec-WebSocket-Protocol" {
		t.Fatalf("early data header = %q", opts.Transport.WebsocketOptions.EarlyDataHeaderName)
	}
	if got := opts.Transport.WebsocketOptions.Headers["User-Agent"]; len(got) != 1 || !strings.Contains(got[0], "Chrome/135") {
		t.Fatalf("expected browser-like user agent header, got %#v", opts.Transport.WebsocketOptions.Headers)
	}
}

func TestBuildNodeOutboundRejectsInvalidRealityShortID(t *testing.T) {
	uri := "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=reality&pbk=public-key&sid=INVALID"

	_, err := buildNodeOutbound("vless-reality-invalid-sid", uri, false)
	if err == nil {
		t.Fatal("expected invalid reality short_id to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid reality short_id") {
		t.Fatalf("expected invalid short_id error, got %v", err)
	}
}
