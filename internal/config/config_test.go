package config

import "testing"

func TestIsProxyURIRecognizesHTTPAndSOCKS5(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want bool
	}{
		{name: "http", uri: "http://alice:secret@example.com:8080", want: true},
		{name: "socks5", uri: "socks5://alice:secret@example.com:1080", want: true},
		{name: "vmess", uri: "vmess://example", want: true},
		{name: "invalid", uri: "ftp://example.com", want: false},
	}

	for _, tt := range tests {
		if got := IsProxyURI(tt.uri); got != tt.want {
			t.Fatalf("%s: IsProxyURI(%q) = %v, want %v", tt.name, tt.uri, got, tt.want)
		}
	}
}

func TestApplyDefaultsSetsCloudflareProbeTarget(t *testing.T) {
	cfg := &Config{}

	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults() error = %v", err)
	}

	if cfg.Management.ProbeTarget != "https://www.google.com/generate_204" {
		t.Fatalf("unexpected default probe target: %q", cfg.Management.ProbeTarget)
	}
	if cfg.Pool.Mode != "auto" {
		t.Fatalf("unexpected default pool mode: %q", cfg.Pool.Mode)
	}
}
