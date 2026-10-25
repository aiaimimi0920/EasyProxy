package config

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testSubscriptionBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"

const negotiatedClash = `proxies:
  - {name: fixture, type: ss, server: 192.0.2.1, port: 443, cipher: aes-128-gcm, password: synthetic}
`

func TestSubscriptionNegotiatesClashAfterBrowserHTML(t *testing.T) {
	var agents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.UserAgent())
		if r.UserAgent() == "clash.meta" {
			fmt.Fprint(w, negotiatedClash)
		} else {
			fmt.Fprint(w, "<!doctype html><html>subscription client required</html>")
		}
	}))
	defer server.Close()
	cache := t.TempDir()
	for i := 0; i < 2; i++ {
		nodes, err := FetchSubscriptionNodes(server.URL, time.Second, cache, time.Hour)
		if err != nil || len(nodes) != 1 || !strings.HasPrefix(nodes[0].URI, "ss://") {
			t.Fatalf("negotiated nodes=%d error=%v", len(nodes), err)
		}
	}
	if !reflect.DeepEqual(agents, []string{testSubscriptionBrowserUserAgent, "clash.meta"}) {
		t.Fatalf("unexpected requests (including cache read): %v", agents)
	}
}

func TestSubscriptionDoesNotRetryUsableOrHTTPErrorResponses(t *testing.T) {
	for _, status := range []int{200, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(status)
				fmt.Fprint(w, negotiatedClash)
			}))
			defer server.Close()
			nodes, err := FetchSubscriptionNodes(server.URL, time.Second, "", 0)
			if requests != 1 || (status == 200 && (err != nil || len(nodes) != 1)) || (status != 200 && err == nil) {
				t.Fatalf("status=%d requests=%d count=%d error=%v", status, requests, len(nodes), err)
			}
		})
	}
}

func TestSubscriptionNegotiationHasOneSharedDeadlineAndBoundedFailures(t *testing.T) {
	var deadlines []time.Time
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("request must have a deadline")
		}
		deadlines = append(deadlines, deadline)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("<html>empty</html>")), Header: make(http.Header)}, nil
	})}
	cache := t.TempDir()
	for i := 0; i < 2; i++ {
		_, err := FetchSubscriptionNodesWithClient("http://example.invalid/sub", time.Second, cache, time.Hour, client)
		if err == nil || !strings.Contains(err.Error(), "no usable nodes") {
			t.Fatalf("expected bounded failure, got %v", err)
		}
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("expected two attempts sharing a deadline, got %v", deadlines)
	}
}

func TestSubscriptionHTTP1FallbackPreservesNegotiatedUserAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "clash.meta" {
			t.Errorf("HTTP/1.1 fallback lost content negotiation: %q", r.UserAgent())
		}
		fmt.Fprint(w, negotiatedClash)
	}))
	defer server.Close()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.UserAgent() == "clash.meta" {
			return nil, fmt.Errorf("http2: timeout awaiting response headers")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("<html>empty</html>")), Header: make(http.Header)}, nil
	})}
	nodes, err := FetchSubscriptionNodesWithClient(server.URL, time.Second, "", 0, client)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("HTTP/1.1 content negotiation failed: count=%d error=%v", len(nodes), err)
	}
}
