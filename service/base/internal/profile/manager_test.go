package profile

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"easy_proxies/internal/config"
	"easy_proxies/internal/routerule"
	"easy_proxies/internal/store"
)

func TestNormalizeDeviceID(t *testing.T) {
	got, err := NormalizeDeviceID("Laptop-WORK")
	if err != nil {
		t.Fatalf("NormalizeDeviceID returned error: %v", err)
	}
	if got != "laptop-work" {
		t.Fatalf("NormalizeDeviceID = %q, want %q", got, "laptop-work")
	}

	if _, err := NormalizeDeviceID("bad device"); err == nil {
		t.Fatal("NormalizeDeviceID accepted invalid device id")
	}
}

func TestManagerDeviceProfileCASAndDeleteFallback(t *testing.T) {
	ctx := context.Background()
	st := openProfileTestStore(t)
	mgr := newProfileTestManager(t, ctx, st)

	created, err := mgr.PutDeviceProfile(ctx, "Laptop", Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
	}, 0)
	if err != nil {
		t.Fatalf("PutDeviceProfile create: %v", err)
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Revision)
	}

	var conflict *store.RevisionConflictError
	if _, err := mgr.PutDeviceProfile(ctx, "laptop", created.Profile.Definition(), 0); !errors.As(err, &conflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}

	if _, err := mgr.DeleteDeviceProfile(ctx, "LAPTOP", created.Revision); err != nil {
		t.Fatalf("DeleteDeviceProfile: %v", err)
	}

	resolved := mgr.Resolve(RequestIdentity{ExplicitDeviceID: "laptop"})
	if resolved.Profile == nil || resolved.Profile.Kind() != KindShared {
		t.Fatalf("profile after delete = %#v, want shared fallback", resolved)
	}
}

func TestProviderCallbackCannotMutateRetiredProfile(t *testing.T) {
	runners := newManualProviderFactory()
	mgr := newProfileTestManager(t, context.Background(), openProfileTestStore(t), WithProviderFactory(runners.Factory))

	first := putProfileWithProvider(t, mgr, "laptop", 0, "https://rules.test/one")
	retiredRunner := runners.LastRunner(t)
	second := putProfileWithProvider(t, mgr, "laptop", first.Revision, "https://rules.test/two")

	retiredRunner.Emit([]string{"DOMAIN-SUFFIX,old.example,DIRECT"})

	got := mgr.Resolve(RequestIdentity{ExplicitDeviceID: "laptop"})
	if got.Profile == nil || got.Profile.Revision() != second.Revision {
		t.Fatalf("resolved profile = %#v, want revision %d", got, second.Revision)
	}
	if got.Profile.Match("old.example") == routerule.PolicyDirect {
		t.Fatalf("late callback mutated current profile: %#v", got)
	}
}

func openProfileTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newProfileTestManager(t *testing.T, ctx context.Context, st store.Store, opts ...Option) *Manager {
	t.Helper()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Enabled:         true,
			DefaultStrategy: "stable",
			FinalPolicy:     "PROXY",
		},
		LocalServer: config.LocalServerConfig{
			Enabled:              true,
			SharedRevision:       1,
			CredentialGeneration: 1,
			Auth: config.LocalServerAuthConfig{
				Username: "easyproxy",
				Password: "secret",
			},
		},
	}
	mgr, err := NewManager(ctx, cfg, st, opts...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func putProfileWithProvider(t *testing.T, mgr *Manager, deviceID string, expected int64, providerURL string) MutationResult {
	t.Helper()
	result, err := mgr.PutDeviceProfile(context.Background(), deviceID, Definition{
		SchemaVersion:   1,
		Enabled:         true,
		DefaultStrategy: "stable",
		FinalPolicy:     "PROXY",
		RuleProviders: []RuleProvider{{
			URL:      providerURL,
			Policy:   "DIRECT",
			Behavior: "domain",
			Interval: "1h",
		}},
	}, expected)
	if err != nil {
		t.Fatalf("PutDeviceProfile(%q): %v", deviceID, err)
	}
	return result
}

type manualProviderRunner struct {
	onRules  func([]string)
	onStatus func(ProviderStatus)
}

func (r *manualProviderRunner) Start(context.Context) {}

func (r *manualProviderRunner) Stop() {}

func (r *manualProviderRunner) Emit(rules []string) {
	if r.onRules != nil {
		r.onRules(rules)
	}
}

func (r *manualProviderRunner) Update(status ProviderStatus) {
	if r.onStatus != nil {
		r.onStatus(status)
	}
}

type manualProviderFactory struct {
	mu      sync.Mutex
	runners []*manualProviderRunner
}

func newManualProviderFactory() *manualProviderFactory {
	return &manualProviderFactory{}
}

func (f *manualProviderFactory) Factory(_ []routerule.ProviderSpec, onRules func([]string), onStatus func(ProviderStatus)) ProviderRunner {
	runner := &manualProviderRunner{onRules: onRules, onStatus: onStatus}
	f.mu.Lock()
	f.runners = append(f.runners, runner)
	f.mu.Unlock()
	return runner
}

func (f *manualProviderFactory) LastRunner(t *testing.T) *manualProviderRunner {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runners) == 0 {
		t.Fatal("provider runner was not created")
	}
	return f.runners[len(f.runners)-1]
}
