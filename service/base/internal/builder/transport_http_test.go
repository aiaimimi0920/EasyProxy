package builder

import (
	"net/url"
	"testing"
)

func TestHTTPTransportPreservesClashOptions(t *testing.T) {
	transport, err := buildV2RayTransport(url.Values{
		"type": {"http"}, "path": {"/tunnel"}, "method": {"GET"},
		"host": {"one.example", "two.example"}, "httpHeaders": {`{"X-Test":["value"]}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	http := transport.HTTPOptions
	if http.Path != "/tunnel" || http.Method != "GET" || len(http.Host) != 2 || http.Headers["X-Test"][0] != "value" {
		t.Fatalf("HTTP transport options lost: %+v", http)
	}
	if _, err := buildV2RayTransport(url.Values{"type": {"http"}, "httpHeaders": {"invalid"}}); err == nil {
		t.Fatal("malformed HTTP headers must fail closed")
	}
}
