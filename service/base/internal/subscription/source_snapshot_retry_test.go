package subscription

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

type manifestTimeoutError struct{}

func (manifestTimeoutError) Error() string   { return "TLS handshake timeout" }
func (manifestTimeoutError) Timeout() bool   { return true }
func (manifestTimeoutError) Temporary() bool { return true }

func TestManifestTimeoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failures  int
		status    int
		body      string
		budget    time.Duration
		wantCalls int
		wantError bool
	}{
		{"recovers after two timeouts", 2, 200, `{"success":true,"sources":[]}`, 5 * time.Second, 3, false},
		{"bounded attempts", 9, 200, "", 5 * time.Second, 3, true},
		{"authentication is not retried", 0, 401, "", 5 * time.Second, 1, true},
		{"invalid JSON is not retried", 0, 200, "invalid", 5 * time.Second, 1, true},
		{"remote failure is not retried", 0, 200, `{"success":false}`, 5 * time.Second, 1, true},
		{"total budget includes backoff", 9, 200, "", 50 * time.Millisecond, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{}
			cfg.SourceSync.ManifestURL = "https://manifest.invalid/sources"
			cfg.SourceSync.RequestTimeout = tc.budget
			calls := 0
			var requestCtx context.Context
			m := &Manager{ctx: context.Background(), logger: defaultLogger{}, httpClient: &http.Client{
				Transport: connectorRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if requestCtx != nil && requestCtx != req.Context() {
						t.Fatal("retry reset request budget")
					}
					requestCtx = req.Context()
					if calls <= tc.failures {
						return nil, manifestTimeoutError{}
					}
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
				}),
			}}
			_, err := m.fetchManifestSources(cfg)
			if (err != nil) != tc.wantError || calls != tc.wantCalls {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestManifestRetryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &config.Config{}
	cfg.SourceSync.ManifestURL = "https://manifest.invalid/sources"
	m := &Manager{ctx: ctx, logger: defaultLogger{}, httpClient: &http.Client{Transport: connectorRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})}}
	_, err := m.fetchManifestSources(cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
