package routerule

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProviderManagerStatusTracksFailureAndRecovery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "upstream failed", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("example.com\n"))
	}))
	defer server.Close()

	var mu sync.Mutex
	var statuses []ProviderStatus
	manager := NewProviderManagerWithStatus([]ProviderSpec{{
		URL:      server.URL,
		Policy:   PolicyDirect,
		Behavior: "domain",
	}}, nil, func(status ProviderStatus) {
		mu.Lock()
		statuses = append(statuses, status)
		mu.Unlock()
	})

	if changed := manager.refreshOne(context.Background(), manager.specs[0]); changed {
		t.Fatal("failing refresh should not publish new rule lines")
	}
	first := lastProviderStatus(t, &mu, statuses)
	if !first.Degraded || first.LastError == "" {
		t.Fatalf("first status = %#v, want degraded error", first)
	}

	if changed := manager.refreshOne(context.Background(), manager.specs[0]); !changed {
		t.Fatal("successful refresh should publish new rule lines")
	}
	second := lastProviderStatus(t, &mu, statuses)
	if second.Degraded || second.LastError != "" {
		t.Fatalf("second status = %#v, want cleared degradation", second)
	}
}

func lastProviderStatus(t *testing.T, mu *sync.Mutex, statuses []ProviderStatus) ProviderStatus {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) == 0 {
		t.Fatal("provider status callback was not invoked")
	}
	return statuses[len(statuses)-1]
}
