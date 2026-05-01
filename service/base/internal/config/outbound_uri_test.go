package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildURIFromSingboxOutboundVMess(t *testing.T) {
	outbound := map[string]any{
		"type":        "vmess",
		"server":      "vmess.example.com",
		"server_port": 443,
		"uuid":        "11111111-1111-1111-1111-111111111111",
		"alter_id":    0,
		"security":    "auto",
		"transport": map[string]any{
			"type": "ws",
			"path": "/ws",
			"headers": map[string]any{
				"Host": "edge.example.com",
			},
		},
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "edge.example.com",
			"insecure":    true,
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": "chrome",
			},
		},
	}

	uri, err := BuildURIFromSingboxOutbound("ZenProxy VMess", outbound)
	if err != nil {
		t.Fatalf("BuildURIFromSingboxOutbound() error = %v", err)
	}

	if !strings.HasPrefix(uri, "vmess://11111111-1111-1111-1111-111111111111@vmess.example.com:443?") {
		t.Fatalf("unexpected vmess uri prefix: %q", uri)
	}
	for _, want := range []string{
		"type=ws",
		"path=%2Fws",
		"host=edge.example.com",
		"security=tls",
		"sni=edge.example.com",
		"insecure=1",
		"fp=chrome",
	} {
		if !strings.Contains(uri, want) {
			t.Fatalf("expected vmess uri to contain %q, got %q", want, uri)
		}
	}
}

func TestBuildURIFromSingboxOutboundVLESSReality(t *testing.T) {
	outbound := map[string]any{
		"type":        "vless",
		"server":      "vless.example.com",
		"server_port": 443,
		"uuid":        "22222222-2222-2222-2222-222222222222",
		"flow":        "xtls-rprx-vision",
		"transport": map[string]any{
			"type":         "grpc",
			"service_name": "zen",
		},
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "reality.example.com",
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": "chrome",
			},
			"reality": map[string]any{
				"enabled":    true,
				"public_key": "public-key",
				"short_id":   "0011223344556677",
			},
		},
	}

	uri, err := BuildURIFromSingboxOutbound("ZenProxy VLESS Reality", outbound)
	if err != nil {
		t.Fatalf("BuildURIFromSingboxOutbound() error = %v", err)
	}

	if !strings.HasPrefix(uri, "vless://22222222-2222-2222-2222-222222222222@vless.example.com:443?") {
		t.Fatalf("unexpected vless uri prefix: %q", uri)
	}
	for _, want := range []string{
		"security=reality",
		"pbk=public-key",
		"sid=0011223344556677",
		"sni=reality.example.com",
		"type=grpc",
		"serviceName=zen",
		"flow=xtls-rprx-vision",
		"fp=chrome",
	} {
		if !strings.Contains(uri, want) {
			t.Fatalf("expected vless uri to contain %q, got %q", want, uri)
		}
	}
}

func TestBuildURIFromSingboxOutboundShadowsocks(t *testing.T) {
	outbound := map[string]any{
		"type":        "shadowsocks",
		"server":      "ss.example.com",
		"server_port": 8388,
		"method":      "aes-256-gcm",
		"password":    "secret-pass",
	}

	uri, err := BuildURIFromSingboxOutbound("ZenProxy SS", outbound)
	if err != nil {
		t.Fatalf("BuildURIFromSingboxOutbound() error = %v", err)
	}

	if !strings.HasPrefix(uri, "ss://") {
		t.Fatalf("expected ss uri, got %q", uri)
	}
	if !strings.Contains(uri, "@ss.example.com:8388") {
		t.Fatalf("expected server/port in ss uri, got %q", uri)
	}
}

func TestBuildURIFromSingboxOutboundHTTPWithTLS(t *testing.T) {
	outbound := map[string]any{
		"type":        "http",
		"server":      "http.example.com",
		"server_port": 443,
		"username":    "alice",
		"password":    "secret",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "origin.example.com",
			"insecure":    true,
		},
	}

	uri, err := BuildURIFromSingboxOutbound("ZenProxy HTTP", outbound)
	if err != nil {
		t.Fatalf("BuildURIFromSingboxOutbound() error = %v", err)
	}

	if !strings.HasPrefix(uri, "http://alice:secret@http.example.com:443?") {
		t.Fatalf("unexpected http uri prefix: %q", uri)
	}
	for _, want := range []string{
		"security=tls",
		"sni=origin.example.com",
		"insecure=1",
	} {
		if !strings.Contains(uri, want) {
			t.Fatalf("expected http uri to contain %q, got %q", want, uri)
		}
	}
}

func TestBuildURIFromSingboxOutboundRejectsUnsupportedSocksVersion(t *testing.T) {
	outbound := map[string]any{
		"type":        "socks",
		"server":      "socks.example.com",
		"server_port": 1080,
		"version":     "4a",
	}

	_, err := BuildURIFromSingboxOutbound("ZenProxy SOCKS4", outbound)
	if err == nil {
		t.Fatal("expected socks4 outbound to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported socks version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildURIFromSingboxOutboundTrojanEscapesPassword(t *testing.T) {
	outbound := map[string]any{
		"type":        "trojan",
		"server":      "trojan.example.com",
		"server_port": 443,
		"password":    "8r<[9'l6hAO",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "trojan.example.com",
		},
	}

	uri, err := BuildURIFromSingboxOutbound("ZenProxy Trojan", outbound)
	if err != nil {
		t.Fatalf("BuildURIFromSingboxOutbound() error = %v", err)
	}
	if !strings.HasPrefix(uri, "trojan://8r%3C%5B9%27l6hAO@trojan.example.com:443?") {
		t.Fatalf("unexpected trojan uri prefix: %q", uri)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.User == nil {
		t.Fatal("expected trojan uri to include user info")
	}
	if got := parsed.User.Username(); got != "8r<[9'l6hAO" {
		t.Fatalf("unexpected trojan password round-trip: %q", got)
	}
}
