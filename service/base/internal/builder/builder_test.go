package builder

import (
	"encoding/base64"
	"strings"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

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
