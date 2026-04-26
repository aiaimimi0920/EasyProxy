package geoip

import "testing"

func TestExtractHostFromURISupportsHTTPAndSOCKS5(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{name: "http", uri: "http://alice:secret@example.com:8080", want: "example.com"},
		{name: "socks5", uri: "socks5://alice:secret@99.144.123.135:30350", want: "99.144.123.135"},
	}

	for _, tt := range tests {
		if got := extractHostFromURI(tt.uri); got != tt.want {
			t.Fatalf("%s: extractHostFromURI(%q) = %q, want %q", tt.name, tt.uri, got, tt.want)
		}
	}
}
