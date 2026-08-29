package profile

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/routerule"
	"easy_proxies/internal/store"
)

const (
	sharedProfileID   = "shared"
	deviceProfilePref = "device:"
)

type Option func(*Manager)

func WithProviderFactory(factory ProviderFactory) Option {
	return func(manager *Manager) {
		manager.providerFactory = factory
	}
}

type preparedConfigSnapshot struct {
	cfg                *config.Config
	shared             *CompiledProfile
	credentials        CredentialSnapshot
	localServerEnabled bool
}

type Manager struct {
	ctx      context.Context
	cancel   context.CancelFunc
	store    store.Store
	lookup   routerule.CountryLookup
	activity *DeviceActivityTracker

	mutationMu             sync.Mutex
	registry               atomic.Pointer[Registry]
	localServerEnabled     atomic.Bool
	providerFactory        ProviderFactory
	providers              map[string]providerRuntime
	nextProviderGeneration uint64
	prepared               *preparedConfigSnapshot
}

type MutationResult struct {
	Revision         int64
	RegistryRevision uint64
	Profile          *CompiledProfile
}

type PreparedShared struct {
	ExpectedRevision int64
	Profile          *CompiledProfile
	Definition       Definition

	manager *Manager
	release sync.Once
}

func NewManager(ctx context.Context, cfg *config.Config, st store.Store, opts ...Option) (*Manager, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if st == nil {
		return nil, errors.New("store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	childCtx, cancel := context.WithCancel(ctx)
	manager := &Manager{
		ctx:             childCtx,
		cancel:          cancel,
		store:           st,
		activity:        NewDeviceActivityTracker(),
		providerFactory: defaultProviderFactory,
		providers:       make(map[string]providerRuntime),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(manager)
		}
	}
	if manager.providerFactory == nil {
		manager.providerFactory = defaultProviderFactory
	}

	prepared, err := manager.prepareConfigSnapshot(cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	devices, err := manager.loadDeviceProfiles(childCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	mappings, err := manager.loadMappings(childCtx)
	if err != nil {
		cancel()
		return nil, err
	}

	registry := NewRegistry(prepared.shared, devices, mappings, prepared.credentials, 1)
	manager.registry.Store(registry)
	manager.localServerEnabled.Store(prepared.localServerEnabled)

	manager.mutationMu.Lock()
	manager.restartAllProvidersLocked(registry)
	manager.mutationMu.Unlock()

	return manager, nil
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.mutationMu.Lock()
	for profileID, runtime := range m.providers {
		if runtime.runner != nil {
			runtime.runner.Stop()
		}
		delete(m.providers, profileID)
	}
	m.prepared = nil
	m.mutationMu.Unlock()
}

func (m *Manager) snapshot() *Registry {
	if m == nil {
		return nil
	}
	return m.registry.Load()
}

func (m *Manager) LocalServerEnabled() bool {
	if m == nil {
		return false
	}
	return m.localServerEnabled.Load()
}

func (m *Manager) Credentials() CredentialSnapshot {
	if m == nil {
		return CredentialSnapshot{}
	}
	current := m.snapshot()
	if current == nil {
		return CredentialSnapshot{}
	}
	return current.Credentials()
}

func (m *Manager) Resolve(identity RequestIdentity) Resolution {
	if m == nil {
		return Resolution{}
	}
	current := m.snapshot()
	if current == nil {
		return Resolution{}
	}
	return current.Resolve(identity)
}

func (m *Manager) Observe(resolution Resolution, peer netip.Addr, at time.Time) {
	if m == nil {
		return
	}
	m.activity.Observe(resolution, peer, at)
}

func (m *Manager) PrepareConfig(cfg *config.Config) error {
	if m == nil {
		return errors.New("manager is nil")
	}
	prepared, err := m.prepareConfigSnapshot(cfg)
	if err != nil {
		return err
	}
	m.mutationMu.Lock()
	m.prepared = prepared
	m.mutationMu.Unlock()
	return nil
}

func (m *Manager) PublishConfigSnapshot(cfg *config.Config) error {
	if m == nil {
		return errors.New("manager is nil")
	}
	prepared, err := m.prepareConfigSnapshot(cfg)
	if err != nil {
		return err
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.publishPreparedConfigLocked(prepared)
}

func (m *Manager) OnConfigUpdate(cfg *config.Config) {
	if m == nil {
		return
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	prepared := m.prepared
	if prepared != nil && prepared.cfg == cfg {
		_ = m.publishPreparedConfigLocked(prepared)
		return
	}
	fallback, err := m.prepareConfigSnapshot(cfg)
	if err != nil {
		return
	}
	_ = m.publishPreparedConfigLocked(fallback)
}

func (m *Manager) SharedProfile() *CompiledProfile {
	current := m.snapshot()
	if current == nil {
		return nil
	}
	return current.SharedProfile()
}

func (m *Manager) DeviceProfile(deviceID string) *CompiledProfile {
	current := m.snapshot()
	if current == nil {
		return nil
	}
	return current.DeviceProfile(deviceID)
}

func (m *Manager) ProviderStatus(profileID string) ProviderStatus {
	if m == nil {
		return ProviderStatus{}
	}
	normalized := normalizeProfileID(profileID)
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	runtime, ok := m.providers[normalized]
	if !ok {
		return ProviderStatus{}
	}
	return runtime.status
}

func (m *Manager) RuntimeStatus() RuntimeStatus {
	if m == nil {
		return RuntimeStatus{}
	}
	registry := m.snapshot()
	status := RuntimeStatus{}
	if registry != nil {
		status.RegistryRevision = registry.Revision()
		status.ProfileCount = registry.ProfileCount()
		status.MappingCount = registry.MappingCount()
	}
	m.mutationMu.Lock()
	for _, runtime := range m.providers {
		if runtime.status.Degraded {
			status.ProviderDegradedCount++
		}
	}
	m.mutationMu.Unlock()
	return status
}

func (m *Manager) ListDevices(ctx context.Context) ([]store.Device, error) {
	if m == nil {
		return nil, errors.New("manager is nil")
	}
	return m.store.ListDevices(ctx)
}

func (m *Manager) GetDevice(ctx context.Context, deviceID string) (*store.Device, error) {
	if m == nil {
		return nil, errors.New("manager is nil")
	}
	normalized, err := NormalizeDeviceID(deviceID)
	if err != nil {
		return nil, err
	}
	return m.store.GetDevice(ctx, normalized)
}

func (m *Manager) ListIPMappings(ctx context.Context) ([]store.DeviceIPMapping, error) {
	if m == nil {
		return nil, errors.New("manager is nil")
	}
	return m.store.ListDeviceIPMappings(ctx)
}

func (m *Manager) GetIPMapping(ctx context.Context, mappingID string) (*store.DeviceIPMapping, error) {
	if m == nil {
		return nil, errors.New("manager is nil")
	}
	return m.store.GetDeviceIPMapping(ctx, strings.TrimSpace(mappingID))
}

func (m *Manager) ActivitySnapshot() map[string]DeviceActivity {
	if m == nil {
		return map[string]DeviceActivity{}
	}
	return m.activity.Snapshot()
}
