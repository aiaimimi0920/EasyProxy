package builder

import (
	"encoding/base64"
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
