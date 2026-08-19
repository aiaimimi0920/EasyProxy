package geoip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestEnsureDatabaseRejectsCorruptExistingMMDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.mmdb")
	data := make([]byte, 2048)
	copy(data[len(data)-len("MaxMind.com"):], "MaxMind.com")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	err := EnsureDatabase(path)
	if err == nil {
		t.Fatal("EnsureDatabase() accepted a corrupt existing MMDB")
	}
	if !strings.Contains(err.Error(), "validate existing geoip database") {
		t.Fatalf("EnsureDatabase() error = %v, want existing database validation error", err)
	}
}
