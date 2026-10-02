package monitor

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAcceptsNodesGzip(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"", false}, {"identity", false}, {"gzip", true}, {"GZip", true},
		{"deflate, gzip;q=0.5", true}, {"gzip;q=0", false},
		{"gzip;q=0, *;q=1", false}, {"*;q=1", true}, {"*;q=0", false},
		{"gzip;q=invalid", false}, {"gzip;q=1.2", false}, {"gzip;q=NaN", false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			if got := acceptsNodesGzip(tc.header); got != tc.want {
				t.Fatalf("acceptsNodesGzip(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestWriteNodesJSONPreservesFullPayloadWithGzip(t *testing.T) {
	payload := map[string]any{"nodes": []map[string]any{{
		"tag": "node-a", "uri": "test-uri", "effective_available": true,
		"timeline": []map[string]any{{"error": strings.Repeat("diagnostic history ", 1000)}},
	}}}
	baseline := httptest.NewRecorder()
	writeJSON(baseline, payload)
	req := httptest.NewRequest(http.MethodGet, "/api/nodes?only_available=1", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	writeNodesJSON(rec, req, payload)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("unexpected gzip response: status=%d headers=%v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" || rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("missing negotiation or length headers: %v", rec.Header())
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if string(decoded) != baseline.Body.String() {
		t.Fatal("gzip changed the complete legacy JSON payload")
	}
	var result map[string]any
	if err := json.Unmarshal(decoded, &result); err != nil {
		t.Fatal(err)
	}
}

func TestWriteNodesJSONKeepsLegacyIdentityResponse(t *testing.T) {
	payload := map[string]any{"nodes": []string{"node-a"}}
	baseline := httptest.NewRecorder()
	writeJSON(baseline, payload)
	for _, header := range []string{"", "identity", "gzip;q=0, *;q=1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
		req.Header.Set("Accept-Encoding", header)
		rec := httptest.NewRecorder()
		writeNodesJSON(rec, req, payload)
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != baseline.Body.String() {
			t.Fatalf("legacy identity contract changed for %q", header)
		}
	}
}
