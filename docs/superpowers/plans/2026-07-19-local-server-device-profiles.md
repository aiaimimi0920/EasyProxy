# Local Server Device Profiles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn EasyProxy into a LAN Local Server with one embedded Web Console, one shared credential, a shared forwarding Profile, and fully independent per-device forwarding Profiles selected by explicit `device_id` with best-effort IP/CIDR fallback.

**Architecture:** Keep MiSub, subscriptions, connectors, node health, and the proxy pool global. Add an immutable Profile Registry backed by SQLite for device resources, Profile documents, and IP mappings; extend the existing dispatcher to resolve a Profile per request and keep it running even when the selected Profile is disabled or the pool is idle. Reuse `monitor.Server` for all management APIs and the embedded React UI.

**Tech Stack:** Go 1.24+, modernc SQLite, sing-box, React 19, TypeScript 5.9, Vite 7, Vitest, React Testing Library, PowerShell, Python unittest, Docker/Compose.

---

## Execution guardrails

- Read `docs/superpowers/specs/2026-07-19-local-server-device-profiles-design.md` before each task.
- Run from `C:\Users\Public\nas_home\AI\GameEditor\EasyProxy` unless a step changes directory.
- Do not start, stop, rename, attach, or replace the legacy `easy-proxy` container.
- Do not modify, delete, stage, or commit:
  - `tmp-pc2-probe-workerlimit-20260707-005.tar`
  - `tmp-pc2-probe-workerlimit-20260707-006.tar`
- Do not edit ignored operator files such as root `config.yaml` or generated `deploy/service/base/config.yaml`.
- Preserve legacy behavior whenever `local_server.enabled` is absent or false.
- Use TDD for every behavior change: write the focused failing test, prove the expected failure, add the smallest implementation, prove the focused test, then run the containing package.
- Before every commit run `git diff --check` and stage only the files named by that task.

## Locked file structure

### New Go package

```text
service/base/internal/profile/
  types.go          Profile document, duration parsing, selection settings
  compile.go        strict validation and immutable compiled Profile
  registry.go       atomic Registry snapshots and identity resolution
  activity.go       process-local device last-seen tracking
  manager.go        store-backed CAS mutations and atomic publication
  provider.go       per-Profile provider generation and degraded status
  *_test.go         focused unit/integration tests
```

The dependency direction must remain:

```text
config <- profile -> store
           |
           +-> routerule

dispatch -> profile + pool + routerule
pool -> monitor
monitor -> profile + config + store
app -> profile + dispatch + monitor + boxmgr
```

`profile` defines its own node-filter and selection types. It must not import `pool` or `monitor`, which would create an import cycle.

### New frontend files

```text
service/base/frontend/vitest.config.ts
service/base/frontend/src/test/setup.ts
service/base/frontend/src/test/http.ts
service/base/frontend/src/types/localServer.ts
service/base/frontend/src/api/localServer.ts
service/base/frontend/src/components/profiles/ProfileForm.tsx
service/base/frontend/src/components/profiles/ProfileEditor.tsx
service/base/frontend/src/components/profiles/profileAdapters.ts
service/base/frontend/src/components/devices/SharedProfileCard.tsx
service/base/frontend/src/components/devices/DeviceTable.tsx
service/base/frontend/src/components/devices/IPMappingsPanel.tsx
service/base/frontend/src/components/local-server/LocalServerSettingsCard.tsx
service/base/frontend/src/components/DevicesPanel.tsx
service/base/frontend/src/hooks/useUnsavedChangesGuard.ts
```

Tests are colocated as `*.test.ts` / `*.test.tsx`.

## Milestones and dependencies

1. **Configuration and persistence foundation:** Tasks 1-2.
2. **Profile and data-plane core:** Tasks 3-7.
3. **Management control plane:** Tasks 8-10.
4. **Web Console:** Tasks 11-14.
5. **Operator path and end-to-end proof:** Tasks 15-16.

Task 11 can run in parallel with Tasks 2-7 because it only establishes frontend test tooling. Tasks 12-14 wait until Task 9 freezes the API JSON schema. Task 15 waits until Task 1 freezes YAML names. Task 16 waits for all previous tasks.

---

### Task 1: Add the Local Server configuration contract and prevent legacy-entry bypass

**Files:**
- Modify: `service/base/internal/config/config.go`
- Modify: `service/base/internal/config/config_test.go`
- Modify: `service/base/internal/config/routing_validation.go`
- Modify: `service/base/internal/builder/builder.go`
- Modify: `service/base/internal/builder/builder_test.go`

- [ ] **Step 1: Write failing configuration tests**

Add focused tests that prove canonical migration, first-enable session invalidation, strict topology validation, deep cloning, SaveSettings persistence, Local Server listen precedence, unconditional primary-inbound suppression, and disabled-mode compatibility. Use these exact test names so the focused command covers every contract:

```text
TestNormalizeLocalServerMigratesCanonicalCredential
TestNormalizeLocalServerCredentialMigrationIsIdempotent
TestNormalizeLocalServerRejectsBypassTopology
TestNormalizeLocalServerRejectsConflictingLegacyPasswords
TestNormalizeLocalServerUsesDefaultUsernameWhenLegacyUsernameIsInvalid
TestConfigCloneDeepCopiesLocalServerAndRoutingNodeFilter
TestSaveSettingsPersistsLocalServerAndDerivedCredentials
TestDispatchOwnsPrimaryInboundInLocalServerMode
TestDisabledLocalServerPreservesLegacyDispatchTopology
```

```go
func TestNormalizeLocalServerMigratesCanonicalCredential(t *testing.T) {
	cfg := &Config{
		Mode: "pool",
		Listener: ListenerConfig{Protocol: InboundProtocolMixed, Username: "legacy", Password: "secret"},
		Management: ManagementConfig{Password: "secret"},
		LocalServer: LocalServerConfig{Enabled: true},
	}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.LocalServer.Auth.Username != "legacy" || cfg.LocalServer.Auth.Password != "secret" {
		t.Fatalf("canonical auth = %#v", cfg.LocalServer.Auth)
	}
	if cfg.LocalServer.SharedRevision != 1 || cfg.LocalServer.CredentialGeneration != 2 {
		t.Fatalf("revisions = shared:%d credential:%d", cfg.LocalServer.SharedRevision, cfg.LocalServer.CredentialGeneration)
	}
}

func TestNormalizeLocalServerRejectsBypassTopology(t *testing.T) {
	tests := []Config{
		{Mode: "hybrid", LocalServer: LocalServerConfig{Enabled: true, Auth: LocalServerAuthConfig{Username: "easyproxy", Password: "secret"}}},
		{Mode: "pool", Listener: ListenerConfig{Protocol: InboundProtocolHTTP}, LocalServer: LocalServerConfig{Enabled: true, Auth: LocalServerAuthConfig{Username: "easyproxy", Password: "secret"}}},
		{Mode: "pool", Listener: ListenerConfig{Protocol: InboundProtocolMixed}, ExtraListeners: []ExtraListenerConfig{{Port: 23000}}, LocalServer: LocalServerConfig{Enabled: true, Auth: LocalServerAuthConfig{Username: "easyproxy", Password: "secret"}}},
	}
	for i := range tests {
		if err := tests[i].normalize(); err == nil {
			t.Fatalf("case %d unexpectedly accepted", i)
		}
	}
}

func TestDispatchOwnsPrimaryInboundInLocalServerMode(t *testing.T) {
	cfg := &Config{
		Mode: "pool",
		Listener: ListenerConfig{Address: "0.0.0.0", Port: 22323, Protocol: InboundProtocolMixed},
		LocalServer: LocalServerConfig{Enabled: true, Listen: "0.0.0.0:32323", Auth: LocalServerAuthConfig{Username: "easyproxy", Password: "secret"}},
	}
	if !cfg.DispatchOwnsPrimaryInbound() {
		t.Fatal("Local Server must suppress the plain pool inbound even on a different dispatch address")
	}
	if got := cfg.DispatchListen(); got != "0.0.0.0:32323" {
		t.Fatalf("DispatchListen() = %q", got)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/config ./internal/builder -run 'TestNormalizeLocalServer|TestConfigCloneDeepCopiesLocalServer|TestSaveSettingsPersistsLocalServer|TestDispatchOwnsPrimaryInbound|TestDisabledLocalServer|TestBuildLocalServer'
```

Expected: compile failures for missing `LocalServerConfig`, `Config.LocalServer`, and `DispatchOwnsPrimaryInbound`.

- [ ] **Step 3: Add the configuration types and normalization**

Add the following declarations next to `RoutingConfig`:

```go
type LocalServerConfig struct {
	Enabled              bool                  `yaml:"enabled"`
	Listen               string                `yaml:"listen"`
	Auth                 LocalServerAuthConfig `yaml:"auth"`
	SharedRevision       int64                 `yaml:"shared_revision"`
	CredentialGeneration uint64                `yaml:"credential_generation"`
}

type LocalServerAuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type RoutingNodeFilterConfig struct {
	Countries []string `yaml:"countries"`
	Regions   []string `yaml:"regions"`
	LongLived *bool    `yaml:"long_lived"`
}
```

Add `LocalServer LocalServerConfig` to `Config`, add `NodeFilter RoutingNodeFilterConfig` to `RoutingConfig`, deep-copy their reference fields in `Clone`, and persist `saveCfg.LocalServer = c.LocalServer` in `SaveSettings`.

Implement the Local Server contract with these methods:

```go
func (c *Config) DispatchListen() string {
	if c != nil && c.LocalServer.Enabled {
		if listen := strings.TrimSpace(c.LocalServer.Listen); listen != "" {
			return listen
		}
	}
	if c != nil {
		if listen := strings.TrimSpace(c.Routing.Listen); listen != "" {
			return listen
		}
	}
	host := "0.0.0.0"
	port := uint16(22323)
	if c != nil {
		if strings.TrimSpace(c.Listener.Address) != "" {
			host = c.Listener.Address
		}
		if c.Listener.Port != 0 {
			port = c.Listener.Port
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

func (c *Config) DispatchOwnsPrimaryInbound() bool {
	if c == nil {
		return false
	}
	if c.LocalServer.Enabled {
		return true
	}
	return c.RoutingTakesOverPoolInbound()
}

func (c *Config) DispatchEnabled() bool {
	return c != nil && (c.LocalServer.Enabled || c.Routing.Enabled)
}
```

`normalizeLocalServer` must enforce these exact rules:

```go
func (c *Config) normalizeLocalServer() error {
	if !c.LocalServer.Enabled {
		return nil
	}
	if c.Mode != "pool" {
		return fmt.Errorf("local_server requires mode pool, got %q", c.Mode)
	}
	if len(c.ExtraListeners) != 0 {
		return errors.New("local_server does not allow extra_listeners")
	}
	if c.Listener.Protocol != InboundProtocolMixed {
		return fmt.Errorf("local_server requires listener.protocol mixed, got %q", c.Listener.Protocol)
	}
	if a, b := strings.TrimSpace(c.LocalServer.Listen), strings.TrimSpace(c.Routing.Listen); a != "" && b != "" && a != b {
		return errors.New("local_server.listen conflicts with routing.listen")
	}
	firstEnable := c.LocalServer.CredentialGeneration == 0
	migratedCredential := false
	if c.LocalServer.Auth.Password == "" {
		switch {
		case c.Management.Password != "" && c.Listener.Password != "" && c.Management.Password != c.Listener.Password:
			return errors.New("management and listener passwords conflict")
		case c.Listener.Password != "":
			c.LocalServer.Auth.Password = c.Listener.Password
		case c.Management.Password != "":
			c.LocalServer.Auth.Password = c.Management.Password
		default:
			return errors.New("local_server requires a non-empty shared password")
		}
		migratedCredential = true
	}
	if c.LocalServer.Auth.Username == "" {
		legacyUsername := strings.TrimSpace(c.Listener.Username)
		if validIdentityToken(legacyUsername) {
			c.LocalServer.Auth.Username = legacyUsername
		} else {
			c.LocalServer.Auth.Username = "easyproxy"
		}
	}
	if !validIdentityToken(c.LocalServer.Auth.Username) {
		return errors.New("local_server auth username is invalid")
	}
	if strings.IndexByte(c.LocalServer.Auth.Password, 0) >= 0 || len(c.LocalServer.Auth.Password) > 256 {
		return errors.New("local_server auth password is invalid")
	}
	if c.LocalServer.SharedRevision == 0 {
		c.LocalServer.SharedRevision = 1
	}
	if c.LocalServer.CredentialGeneration == 0 {
		c.LocalServer.CredentialGeneration = 1
	}
	if firstEnable || migratedCredential {
		c.LocalServer.CredentialGeneration++
	}
	c.Listener.Username = c.LocalServer.Auth.Username
	c.Listener.Password = c.LocalServer.Auth.Password
	c.Management.Password = c.LocalServer.Auth.Password
	return nil
}
```

Add the username validator in `routing_validation.go`:

```go
func validIdentityToken(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
```

Call `normalizeLocalServer` from both `normalizeInternal` and `NormalizeWithPortMap` immediately after `applyDefaults`, before nodes or listeners are published. This keeps initial load, reload, and port-map reload validation identical.

- [ ] **Step 4: Make the builder Local Server-aware**

Change the pool inbound gate and pool outbound gate to:

```go
if enablePoolInbound {
	if !cfg.DispatchOwnsPrimaryInbound() {
		inbound, err := buildPoolInbound(cfg)
		if err != nil {
			return option.Options{}, err
		}
		inbounds = append(inbounds, inbound)
	}
}

if enablePoolInbound || cfg.DispatchEnabled() {
	// existing pool outbound construction
}
```

Add `TestBuildLocalServerSuppressesPlainInboundAndKeepsPoolOutbound` and keep the existing legacy route-A/route-B tests unchanged.

- [ ] **Step 5: Run package tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/config/config.go internal/config/config_test.go internal/config/routing_validation.go internal/builder/builder.go internal/builder/builder_test.go
go test -count=1 ./internal/config ./internal/builder
git diff --check
```

Expected: both packages pass.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/config service/base/internal/builder
git commit -m "feat(service/base): add local server config contract"
```

---

### Task 2: Add SQLite device, Profile, mapping, and session-generation persistence

**Files:**
- Modify: `service/base/internal/store/migrations.go`
- Modify: `service/base/internal/store/store.go`
- Modify: `service/base/internal/store/sqlite.go`
- Create: `service/base/internal/store/migrations_test.go`
- Create: `service/base/internal/store/local_server_test.go`

- [ ] **Step 1: Write the migration and CAS tests**

Create temporary SQLite stores and use these exact tests: `TestMigration4PreservesExistingRows`, `TestLocalServerMigrationAndProfileCAS`, `TestPutDeviceProfileCreatesDeviceAtomically`, `TestDeviceAndMappingCAS`, `TestMappingRequiresExistingDevice`, `TestLocalServerMigrationRollsBackOnFailure`, and `TestSessionCredentialGenerationRoundTrip`. They must assert schema version 4, preservation of an existing node/session token, Profile create/update conflicts, idempotent delete, mapping CRUD, target-device constraints, transaction rollback, and session generation:

```go
func TestLocalServerMigrationAndProfileCAS(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "data.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	device, err := st.PutDevice(ctx, Device{DeviceID: "laptop", DisplayName: "Laptop"}, 0)
	if err != nil || device.Revision != 1 {
		t.Fatalf("device=%#v err=%v", device, err)
	}
	created, err := st.PutDeviceProfile(ctx, DeviceProfile{
		DeviceID: "laptop", ProfileJSON: []byte(`{"schema_version":1,"enabled":true}`), SchemaVersion: 1,
	}, 0)
	if err != nil || created.Revision != 1 {
		t.Fatalf("profile=%#v err=%v", created, err)
	}
	var conflict *RevisionConflictError
	if _, err := st.PutDeviceProfile(ctx, created, 0); !errors.As(err, &conflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	deleted, err := st.DeleteDeviceProfile(ctx, "laptop", created.Revision)
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	deleted, err = st.DeleteDeviceProfile(ctx, "laptop", created.Revision)
	if err != nil || deleted {
		t.Fatalf("idempotent delete=%v err=%v", deleted, err)
	}
}

func TestSessionCredentialGenerationRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := &Session{Token: "token", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), CredentialGeneration: 7}
	if err := st.CreateSession(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(ctx, "token")
	if err != nil || got.CredentialGeneration != 7 {
		t.Fatalf("session=%#v err=%v", got, err)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/store -run 'TestMigration4|TestLocalServerMigration|TestPutDeviceProfile|TestDeviceAndMapping|TestMappingRequires|TestSessionCredentialGeneration'
```

Expected: compile failures for the new models and Store methods.

- [ ] **Step 3: Add migration 4 and Store models**

Append migration version 4 with this schema:

```sql
CREATE TABLE devices (
    device_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    revision INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE device_profiles (
    device_id TEXT PRIMARY KEY REFERENCES devices(device_id) ON DELETE CASCADE,
    profile_json TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE device_ip_mappings (
    mapping_id TEXT PRIMARY KEY,
    cidr TEXT NOT NULL UNIQUE,
    device_id TEXT NOT NULL REFERENCES devices(device_id) ON DELETE RESTRICT,
    priority INTEGER NOT NULL,
    enabled INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
ALTER TABLE sessions ADD COLUMN credential_generation INTEGER NOT NULL DEFAULT 1;
```

Add these models and the typed conflict:

```go
type RevisionConflictError struct {
	CurrentRevision int64
}

var ErrDeviceNotFound = errors.New("device not found")

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("revision conflict: current revision is %d", e.CurrentRevision)
}

type Device struct {
	DeviceID string
	DisplayName string
	Revision int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type DeviceProfile struct {
	DeviceID string
	ProfileJSON []byte
	SchemaVersion int
	Revision int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type DeviceIPMapping struct {
	MappingID string
	CIDR string
	DeviceID string
	Priority int
	Enabled bool
	Revision int64
	CreatedAt time.Time
	UpdatedAt time.Time
}
```

Add `CredentialGeneration uint64` to `store.Session` and the corresponding monitor session model in Task 8.

- [ ] **Step 4: Add explicit Store methods and CAS SQL**

Extend `Store` with:

```go
ListDevices(context.Context) ([]Device, error)
GetDevice(context.Context, string) (*Device, error)
PutDevice(context.Context, Device, int64) (Device, error)
ListDeviceProfiles(context.Context) ([]DeviceProfile, error)
GetDeviceProfile(context.Context, string) (*DeviceProfile, error)
PutDeviceProfile(context.Context, DeviceProfile, int64) (DeviceProfile, error)
DeleteDeviceProfile(context.Context, string, int64) (bool, error)
ListDeviceIPMappings(context.Context) ([]DeviceIPMapping, error)
GetDeviceIPMapping(context.Context, string) (*DeviceIPMapping, error)
PutDeviceIPMapping(context.Context, DeviceIPMapping, int64) (DeviceIPMapping, error)
DeleteDeviceIPMapping(context.Context, string, int64) (bool, error)
DeleteSessionsBeforeGeneration(context.Context, uint64) error
```

Use `INSERT` for expected revision 0 and an atomic update for existing rows:

```go
result, err := s.conn().ExecContext(ctx, `
    UPDATE device_profiles
       SET profile_json = ?, schema_version = ?, revision = revision + 1, updated_at = ?
     WHERE device_id = ? AND revision = ?`,
	profile.ProfileJSON, profile.SchemaVersion, now, profile.DeviceID, expectedRevision,
)
if err != nil {
	return DeviceProfile{}, err
}
affected, err := result.RowsAffected()
if err != nil {
	return DeviceProfile{}, err
}
if affected == 0 {
	current, lookupErr := s.GetDeviceProfile(ctx, profile.DeviceID)
	if lookupErr != nil {
		return DeviceProfile{}, lookupErr
	}
	currentRevision := int64(0)
	if current != nil {
		currentRevision = current.Revision
	}
	return DeviceProfile{}, &RevisionConflictError{CurrentRevision: currentRevision}
}
saved, err := s.GetDeviceProfile(ctx, profile.DeviceID)
if err != nil {
	return DeviceProfile{}, err
}
if saved == nil {
	return DeviceProfile{}, errors.New("device profile disappeared after CAS update")
}
return *saved, nil
```

Implement equivalent CAS behavior for devices and mappings. `PutDeviceProfile` runs in `WithTx`, creates a missing `devices` row with `display_name=device_id` in the same transaction, and then performs the Profile CAS. `PutDeviceIPMapping` must return `ErrDeviceNotFound` if its normalized target device does not exist. `DeleteDeviceProfile` and `DeleteDeviceIPMapping` return `(false, nil)` when the row is already absent. Do not create a default Profile or automatic IP mapping during migration.

- [ ] **Step 5: Persist and query session generation**

Change session SQL to include `credential_generation`:

```go
`INSERT INTO sessions (token, created_at, expires_at, credential_generation) VALUES (?, ?, ?, ?)`
```

and:

```go
`SELECT token, created_at, expires_at, credential_generation FROM sessions WHERE token = ?`
```

Implement cleanup:

```go
func (s *sqliteStore) DeleteSessionsBeforeGeneration(ctx context.Context, generation uint64) error {
	_, err := s.conn().ExecContext(ctx, "DELETE FROM sessions WHERE credential_generation < ?", generation)
	return err
}
```

- [ ] **Step 6: Run store tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/store/store.go internal/store/migrations.go internal/store/sqlite.go internal/store/migrations_test.go internal/store/local_server_test.go
go test -count=1 ./internal/store
git diff --check
```

Expected: store tests pass and migration version is 4.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/store
git commit -m "feat(service/base): persist device profiles and session generations"
```

---

### Task 3: Build the immutable Profile document, compiler, Registry, and resolver

**Files:**
- Create: `service/base/internal/profile/types.go`
- Create: `service/base/internal/profile/compile.go`
- Create: `service/base/internal/profile/registry.go`
- Create: `service/base/internal/profile/activity.go`
- Create: `service/base/internal/profile/types_test.go`
- Create: `service/base/internal/profile/registry_test.go`
- Modify: `service/base/internal/routerule/engine.go`
- Modify: `service/base/internal/routerule/engine_test.go`

- [ ] **Step 1: Write strict validation and resolver tests**

```go
func TestCompileRejectsInvalidRuleInsteadOfSilentlyDroppingIt(t *testing.T) {
	_, err := Compile("shared", KindShared, 1, Definition{
		SchemaVersion: 1,
		Enabled: true,
		FinalPolicy: "PROXY",
		Rules: []string{"NOT-A-RULE"},
	}, nil)
	if err == nil {
		t.Fatal("invalid rule was accepted")
	}
}

func TestRegistryResolvePrecedenceAndIsolation(t *testing.T) {
	shared := compileProfileForTest(t, "shared", KindShared, 4, Definition{SchemaVersion: 1, Enabled: false, FinalPolicy: "DIRECT"})
	laptop := compileProfileForTest(t, "device:laptop", KindDevice, 2, Definition{SchemaVersion: 1, Enabled: true, FinalPolicy: "PROXY"})
	registry := NewRegistry(shared, map[string]*CompiledProfile{"laptop": laptop}, []IPMapping{
		{MappingID: "map-1", Prefix: netip.MustParsePrefix("192.168.1.0/24"), DeviceID: "mapped"},
	}, CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1}, 1)

	explicit := registry.Resolve(RequestIdentity{ExplicitDeviceID: "laptop", PeerIP: netip.MustParseAddr("192.168.1.10")})
	if explicit.Profile.ID() != "device:laptop" || explicit.Source != IdentityExplicit {
		t.Fatalf("explicit resolution = %#v", explicit)
	}
	unknown := registry.Resolve(RequestIdentity{ExplicitDeviceID: "unknown", PeerIP: netip.MustParseAddr("192.168.1.10")})
	if unknown.Profile.ID() != "shared" || unknown.Source != IdentityExplicit {
		t.Fatalf("unknown explicit ID must use shared without IP fallback: %#v", unknown)
	}
	mapped := registry.Resolve(RequestIdentity{PeerIP: netip.MustParseAddr("192.168.1.10")})
	if mapped.DeviceID != "mapped" || mapped.Source != IdentityIPMapping || mapped.Profile.ID() != "shared" {
		t.Fatalf("mapped resolution = %#v", mapped)
	}
}

func compileProfileForTest(t *testing.T, id string, kind Kind, revision int64, definition Definition) *CompiledProfile {
	t.Helper()
	compiled, err := Compile(id, kind, revision, definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/profile ./internal/routerule
```

Expected: the profile package does not exist and `routerule.ValidateRules` is missing.

- [ ] **Step 3: Define the Profile document without importing pool or monitor**

Use these domain types:

```go
type Kind string

const (
	KindShared Kind = "shared"
	KindDevice Kind = "device"
)

type RuleProvider struct {
	URL string `json:"url"`
	Policy string `json:"policy"`
	Behavior string `json:"behavior"`
	Interval string `json:"interval"`
}

type NodeFilter struct {
	Countries []string `json:"countries"`
	Regions []string `json:"regions"`
	LongLived *bool `json:"long_lived"`
}

type LongLivedPolicy struct {
	MinUptime string `json:"min_uptime"`
	MinSuccessRate float64 `json:"min_success_rate"`
}

type SessionPolicy struct {
	TTL string `json:"ttl"`
}

type Definition struct {
	SchemaVersion int `json:"schema_version"`
	Enabled bool `json:"enabled"`
	DefaultStrategy string `json:"default_strategy"`
	UseDefaultRules bool `json:"use_default_rules"`
	FinalPolicy string `json:"final_policy"`
	Rules []string `json:"rules"`
	RuleProviders []RuleProvider `json:"rule_providers"`
	NodeFilter NodeFilter `json:"node_filter"`
	LongLived LongLivedPolicy `json:"long_lived"`
	Session SessionPolicy `json:"session"`
}

type SelectionSettings struct {
	DefaultStrategy string
	Filter NodeFilter
	LongLivedMinUptime time.Duration
	LongLivedMinSuccessRate float64
	SessionTTL time.Duration
}
```

Lock these signatures before implementing later tasks:

```go
func Compile(id string, kind Kind, revision int64, definition Definition, lookup routerule.CountryLookup) (*CompiledProfile, error)
func NewRegistry(shared *CompiledProfile, devices map[string]*CompiledProfile, mappings []IPMapping, credentials CredentialSnapshot, revision uint64) *Registry
func DefinitionFromRouting(config.RoutingConfig) Definition
func ApplyDefinitionToRouting(Definition, *config.RoutingConfig) error
func cloneDefinition(Definition) Definition
func cloneSelection(SelectionSettings) SelectionSettings
```

`ApplyDefinitionToRouting` updates only Profile-owned fields (`enabled`, strategy, default-rules flag, final policy, rules, providers, node filter, long-lived thresholds, and session TTL). It must preserve `routing.listen` and any future topology-only fields. This keeps the shared YAML and device JSON on one logical schema without accidentally erasing the legacy route-B listen address.

- [ ] **Step 4: Add strict rule validation and immutable compilation**

Add `routerule.ValidateRules` without changing the legacy lenient `routerule.New` behavior:

```go
func ValidateRules(lines []string) error {
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if _, ok := parseRule(trimmed); !ok {
			return fmt.Errorf("invalid routing rule at index %d: %q", index, line)
		}
	}
	return nil
}
```

`Compile` validates schema version, strategy, policy, duration strings, rate bounds, providers, and rules, then stores private immutable fields:

```go
type CompiledProfile struct {
	id string
	kind Kind
	revision int64
	definition Definition
	selection SelectionSettings
	engine *routerule.Engine
}

func (p *CompiledProfile) ID() string { return p.id }
func (p *CompiledProfile) Kind() Kind { return p.kind }
func (p *CompiledProfile) Revision() int64 { return p.revision }
func (p *CompiledProfile) Enabled() bool { return p.definition.Enabled }
func (p *CompiledProfile) Match(host string) routerule.Policy { return p.engine.Match(host) }
func (p *CompiledProfile) Selection() SelectionSettings { return cloneSelection(p.selection) }
func (p *CompiledProfile) Definition() Definition { return cloneDefinition(p.definition) }
func (p *CompiledProfile) RuleCount() int { return p.engine.RuleCount() }
func (p *CompiledProfile) FinalPolicy() routerule.Policy { return p.engine.Final() }
func (p *CompiledProfile) WithRevision(revision int64) *CompiledProfile
```

- [ ] **Step 5: Implement immutable Registry resolution**

```go
type CredentialSnapshot struct {
	Username string
	Password string
	Generation uint64
}

type RequestIdentity struct {
	ExplicitDeviceID string
	PeerIP netip.Addr
}

type IdentitySource string

const (
	IdentityExplicit IdentitySource = "explicit"
	IdentityIPMapping IdentitySource = "ip_mapping"
	IdentitySharedFallback IdentitySource = "shared_fallback"
)

type IPMapping struct {
	MappingID string
	Prefix netip.Prefix
	DeviceID string
	Priority int
}

type Resolution struct {
	DeviceID string
	Source IdentitySource
	ProfileID string
	ProfileRevision int64
	Profile *CompiledProfile
}

type Registry struct {
	shared *CompiledProfile
	devices map[string]*CompiledProfile
	mappings []IPMapping
	credentials CredentialSnapshot
	revision uint64
}

func (r *Registry) SharedProfile() *CompiledProfile
func (r *Registry) DeviceProfile(deviceID string) *CompiledProfile
func (r *Registry) Credentials() CredentialSnapshot
func (r *Registry) Revision() uint64
func (r *Registry) ProfileCount() int
func (r *Registry) MappingCount() int
func (r *Registry) CloneReplacingDevice(deviceID string, profile *CompiledProfile) *Registry
func (r *Registry) CloneReplacingMappings(mappings []IPMapping) *Registry
func (r *Registry) CloneReplacingShared(shared *CompiledProfile) *Registry
func (r *Registry) CloneReplacingCredentials(credentials CredentialSnapshot) *Registry
```

`NewRegistry` must deep-copy its map/slices, keep all fields private, and never return mutable internal maps or slices. Every `CloneReplacing*` method creates a fresh Registry, increments the registry revision exactly once, and shares only immutable `CompiledProfile` pointers. `Resolve` must stop after an explicit identity even if that device has no independent Profile. IP mappings use normalized prefixes sorted by prefix length descending and priority descending.

Add `DeviceActivityTracker` using a mutex-protected map; it records last-seen data but never affects `Resolve`:

```go
type DeviceActivity struct {
	DeviceID string
	Source IdentitySource
	LastSeenIP netip.Addr
	LastSeenAt time.Time
}

type DeviceActivityTracker struct {
	mu sync.RWMutex
	byDevice map[string]DeviceActivity
}

func NewDeviceActivityTracker() *DeviceActivityTracker
func (t *DeviceActivityTracker) Observe(resolution Resolution, peer netip.Addr, at time.Time)
func (t *DeviceActivityTracker) Snapshot() map[string]DeviceActivity
```

`Snapshot` returns a new map, and `Observe` ignores empty device IDs so anonymous shared fallback does not create a fake device row.

- [ ] **Step 6: Run tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/profile internal/routerule/engine.go internal/routerule/engine_test.go
go test -count=1 ./internal/profile ./internal/routerule
git diff --check
```

Expected: all profile and routerule tests pass.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/profile service/base/internal/routerule
git commit -m "feat(service/base): add device profile registry"
```

---

### Task 4: Isolate pool selection state and thresholds by Profile

**Files:**
- Modify: `service/base/internal/outbound/pool/directive.go`
- Modify: `service/base/internal/outbound/pool/sticky.go`
- Modify: `service/base/internal/outbound/pool/sticky_test.go`
- Modify: `service/base/internal/outbound/pool/pool.go`
- Modify: `service/base/internal/outbound/pool/pool_test.go`
- Modify: `service/base/internal/monitor/manager.go`
- Modify: `service/base/internal/monitor/manager_test.go`

- [ ] **Step 1: Write failing isolation tests**

Add tests proving that identical session and stable keys do not cross Profiles, different Profile TTLs expire independently, and raw monitor data can meet one Profile threshold but not another:

```go
func TestSessionBindingsAreNamespacedAndUseDirectiveTTL(t *testing.T) {
	state := newStickyState(defaultSessionTTL)
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return now }
	a := &memberState{tag: "a"}
	b := &memberState{tag: "b"}
	profileA := SelectionDirective{ProfileID: "profile-a", ProfileRevision: 1, SessionKey: "job", SessionTTL: 20 * time.Millisecond}
	profileB := SelectionDirective{ProfileID: "profile-b", ProfileRevision: 1, SessionKey: "job", SessionTTL: time.Hour}
	if got := state.pickSession(profileA.namespacedSessionKey(), profileA.SessionTTL, []*memberState{a}, a); got != a {
		t.Fatal("profile-a did not bind a")
	}
	if got := state.pickSession(profileB.namespacedSessionKey(), profileB.SessionTTL, []*memberState{b}, b); got != b {
		t.Fatal("profile-b did not bind b")
	}
	now = now.Add(30 * time.Millisecond)
	if got := state.pickSession(profileA.namespacedSessionKey(), profileA.SessionTTL, []*memberState{b}, b); got != b {
		t.Fatal("short TTL binding did not expire")
	}
}

func TestAffinityNamespaceIncludesProfileRevision(t *testing.T) {
	first := SelectionDirective{ProfileID: "device:laptop", ProfileRevision: 1, SessionKey: "job"}
	second := SelectionDirective{ProfileID: "device:laptop", ProfileRevision: 2, SessionKey: "job"}
	if first.namespacedSessionKey() == second.namespacedSessionKey() || first.stableBucketKey() == second.stableBucketKey() {
		t.Fatal("profile revision reused obsolete affinity namespace")
	}
}

func TestLongLivedPolicyUsesRawSnapshot(t *testing.T) {
	snapshot := monitor.Snapshot{EffectiveAvailable: true, UptimeSeconds: 7200, ReportedSuccessCount: 9, ReportedFailureCount: 1}
	if !monitor.MeetsLongLivedPolicy(snapshot, time.Hour, 0.8) {
		t.Fatal("snapshot should meet relaxed policy")
	}
	if monitor.MeetsLongLivedPolicy(snapshot, 3*time.Hour, 0.8) {
		t.Fatal("snapshot should not meet strict uptime policy")
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/outbound/pool ./internal/monitor -run 'TestSessionBindingsAreNamespaced|TestLongLivedPolicy|TestProfile'
```

Expected: compile failures for the new directive fields and helper.

- [ ] **Step 3: Extend the directive and make namespacing internal**

```go
type LongLivedPolicy struct {
	MinUptime time.Duration
	MinSuccessRate float64
}

type SelectionDirective struct {
	ProfileID string
	ProfileRevision int64
	Strategy Strategy
	SessionKey string
	SessionTTL time.Duration
	PinnedTag string
	Filter NodeFilter
	LongLived LongLivedPolicy
}

func (d SelectionDirective) namespaced(value string) string {
	if d.ProfileID == "" {
		return value
	}
	return fmt.Sprintf("%s@%d\x00%s", d.ProfileID, d.ProfileRevision, value)
}

func (d SelectionDirective) namespacedSessionKey() string { return d.namespaced(d.SessionKey) }
func (d SelectionDirective) stableBucketKey() string { return d.namespaced(d.Filter.bucketKey()) }
```

Use `directive.stableBucketKey()` for stable bindings and `directive.namespacedSessionKey()` for session bindings. Legacy directives with empty ProfileID keep their existing keys. A Profile revision change intentionally starts a fresh affinity namespace so a recreated or materially changed Profile cannot reuse obsolete sticky/session state.

- [ ] **Step 4: Store per-binding session TTL and evaluate raw long-lived data**

Change `sessionBinding` to carry expiry:

```go
type sessionBinding struct {
	tag string
	lastSeen time.Time
	expiresAt time.Time
}
```

Add `now func() time.Time` to `stickyState`, default it to `time.Now`, and let tests replace it with a deterministic clock. Change `pickSession` to accept TTL, normalize non-positive values to `defaultSessionTTL`, update `lastSeen/expiresAt` on reuse, and expire each binding against its own `expiresAt`. Sweep expired entries on each session access; do not gate cleanup on one pool-wide TTL.

Export this pure monitor helper:

```go
func MeetsLongLivedPolicy(snapshot Snapshot, minUptime time.Duration, minRate float64) bool {
	if !snapshot.EffectiveAvailable {
		return false
	}
	minUptime, minRate = normalizeLongLivedThresholds(minUptime, minRate)
	rate := reportedSuccessRate(snapshot.ReportedSuccessCount, snapshot.ReportedFailureCount)
	return time.Duration(snapshot.UptimeSeconds)*time.Second >= minUptime && rate >= minRate
}
```

When directive thresholds are non-zero, both filter matching and stable preference call this helper; otherwise preserve `Snapshot.LongLived` legacy behavior.

- [ ] **Step 5: Run tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/outbound/pool internal/monitor/manager.go internal/monitor/manager_test.go
go test -count=1 ./internal/outbound/pool ./internal/monitor
git diff --check
```

Expected: pool and monitor tests pass, including all legacy sticky tests.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/outbound/pool service/base/internal/monitor/manager.go service/base/internal/monitor/manager_test.go
git commit -m "feat(service/base): isolate pool selection by profile"
```

---

### Task 5: Add the store-backed Profile Manager and provider generation fencing

**Files:**
- Create: `service/base/internal/profile/manager.go`
- Create: `service/base/internal/profile/provider.go`
- Create: `service/base/internal/profile/manager_test.go`
- Create: `service/base/internal/profile/provider_test.go`
- Modify: `service/base/internal/profile/types.go`
- Modify: `service/base/internal/profile/compile.go`
- Modify: `service/base/internal/profile/registry.go`
- Modify: `service/base/internal/routerule/provider.go`
- Create: `service/base/internal/routerule/provider_status_test.go`

- [ ] **Step 1: Write failing CAS, normalization, and immutable-provider tests**

Canonical device IDs are lower-case. Add tests that prove `Laptop` and `laptop` resolve to one resource, stale revisions fail, copy-shared is one-time, delete returns to shared, and a late provider callback cannot mutate an already-published compiled Profile:

```go
func TestNormalizeDeviceID(t *testing.T) {
	got, err := NormalizeDeviceID("Laptop-WORK")
	if err != nil || got != "laptop-work" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := NormalizeDeviceID("bad device"); err == nil {
		t.Fatal("invalid device ID accepted")
	}
}

func TestManagerDeviceProfileCASAndDeleteFallback(t *testing.T) {
	ctx := context.Background()
	st := openProfileTestStore(t)
	mgr := newProfileTestManager(t, ctx, st)
	created, err := mgr.PutDeviceProfile(ctx, "Laptop", Definition{SchemaVersion: 1, Enabled: true, FinalPolicy: "PROXY"}, 0)
	if err != nil || created.Revision != 1 {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	var conflict *store.RevisionConflictError
	if _, err := mgr.PutDeviceProfile(ctx, "laptop", created.Profile.Definition(), 0); !errors.As(err, &conflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if _, err := mgr.DeleteDeviceProfile(ctx, "LAPTOP", created.Revision); err != nil {
		t.Fatal(err)
	}
	resolved := mgr.Resolve(RequestIdentity{ExplicitDeviceID: "laptop"})
	if resolved.Profile.Kind() != KindShared {
		t.Fatalf("profile after delete = %s", resolved.Profile.Kind())
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
	if got.Profile.Revision() != second.Revision || got.Profile.Match("old.example") == routerule.PolicyDirect {
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
		Routing: config.RoutingConfig{Enabled: true, DefaultStrategy: "stable", FinalPolicy: "PROXY"},
		LocalServer: config.LocalServerConfig{
			Enabled: true, SharedRevision: 1, CredentialGeneration: 1,
			Auth: config.LocalServerAuthConfig{Username: "easyproxy", Password: "secret"},
		},
	}
	mgr, err := NewManager(ctx, cfg, st, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

func putProfileWithProvider(t *testing.T, mgr *Manager, deviceID string, expected int64, providerURL string) MutationResult {
	t.Helper()
	result, err := mgr.PutDeviceProfile(context.Background(), deviceID, Definition{
		SchemaVersion: 1, Enabled: true, DefaultStrategy: "stable", FinalPolicy: "PROXY",
		RuleProviders: []RuleProvider{{URL: providerURL, Policy: "DIRECT", Behavior: "domain", Interval: "1h"}},
	}, expected)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/profile -run 'TestNormalizeDeviceID|TestManager|TestProviderCallback'
```

Expected: missing Manager/provider symbols.

- [ ] **Step 3: Implement canonical identity and Manager construction**

```go
func NormalizeDeviceID(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 0 || len(value) > 64 {
		return "", errors.New("device_id length must be 1-64")
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return "", errors.New("device_id contains invalid characters")
		}
	}
	return value, nil
}
```

Manager owns one atomic Registry and one mutation lock:

```go
type Manager struct {
	ctx context.Context
	cancel context.CancelFunc
	store store.Store
	lookup routerule.CountryLookup
	mutationMu sync.Mutex
	registry atomic.Pointer[Registry]
	activity *DeviceActivityTracker
	providerFactory ProviderFactory
	providers map[string]providerRuntime
}

type Option func(*Manager)

func WithProviderFactory(factory ProviderFactory) Option {
	return func(manager *Manager) { manager.providerFactory = factory }
}

func NewManager(ctx context.Context, cfg *config.Config, st store.Store, opts ...Option) (*Manager, error)
func (m *Manager) Close()
func (m *Manager) snapshot() *Registry
func (m *Manager) LocalServerEnabled() bool
func (m *Manager) Credentials() CredentialSnapshot
func (m *Manager) Resolve(RequestIdentity) Resolution
func (m *Manager) Observe(Resolution, netip.Addr, time.Time)
func (m *Manager) PublishConfigSnapshot(*config.Config) error
func (m *Manager) PrepareConfig(*config.Config) error
func (m *Manager) OnConfigUpdate(*config.Config)
func (m *Manager) SharedProfile() *CompiledProfile
func (m *Manager) DeviceProfile(deviceID string) *CompiledProfile
func (m *Manager) ProviderStatus(profileID string) ProviderStatus
func (m *Manager) ListDevices(context.Context) ([]store.Device, error)
func (m *Manager) ListIPMappings(context.Context) ([]store.DeviceIPMapping, error)
func (m *Manager) ActivitySnapshot() map[string]DeviceActivity
```

`NewManager` loads devices, Profile JSON, and mappings, compiles every persisted Profile strictly, and returns an error naming the offending device if persisted JSON is corrupt. `PrepareConfig` validates/compiles the candidate shared Profile and canonical credential without publication. After reload commit, `OnConfigUpdate` performs the infallible `PublishConfigSnapshot` swap from that prepared result. Tests must prove every Local Server config notification is preceded by `PrepareConfig`; initial construction already publishes the initial snapshot, and shared/credential hot updates use their dedicated publish methods rather than this reload callback.

- [ ] **Step 4: Implement CAS mutations without publishing partial state**

Expose these methods:

```go
type MutationResult struct {
	Revision int64
	RegistryRevision uint64
	Profile *CompiledProfile
}

type PreparedShared struct {
	ExpectedRevision int64
	Profile *CompiledProfile
	Definition Definition
}

func (m *Manager) PutDevice(ctx context.Context, deviceID, displayName string, expected int64) (store.Device, error)
func (m *Manager) PutDeviceProfile(ctx context.Context, deviceID string, definition Definition, expected int64) (MutationResult, error)
func (m *Manager) CopySharedProfile(ctx context.Context, deviceID string) (MutationResult, error)
func (m *Manager) SetDeviceProfileEnabled(ctx context.Context, deviceID string, enabled bool, expected int64) (MutationResult, error)
func (m *Manager) DeleteDeviceProfile(ctx context.Context, deviceID string, expected int64) (MutationResult, error)
func (m *Manager) PutIPMapping(ctx context.Context, mapping store.DeviceIPMapping, expected int64) (store.DeviceIPMapping, uint64, error)
func (m *Manager) DeleteIPMapping(ctx context.Context, mappingID string, expected int64) (uint64, error)
func (m *Manager) PrepareShared(definition Definition, expected int64) (*PreparedShared, error)
func (m *Manager) PublishShared(prepared *PreparedShared) MutationResult
func (m *Manager) PublishCredentials(snapshot CredentialSnapshot) uint64
```

The mutation algorithm is always:

```go
m.mutationMu.Lock()
defer m.mutationMu.Unlock()

compiled, encoded, err := m.prepareDefinition(profileID, kind, nextRevision, definition)
if err != nil {
	return MutationResult{}, err
}
persisted, err := m.persistDeviceProfileCAS(ctx, deviceID, encoded, expected)
if err != nil {
	return MutationResult{}, err
}
current := m.snapshot()
published := compiled.WithRevision(persisted.Revision)
next := current.CloneReplacingDevice(deviceID, published)
m.registry.Store(next)
m.restartProviderLocked(next.DeviceProfile(deviceID))
return mutationResult(next, persisted.Revision, published), nil
```

Define the private helpers with these signatures so the algorithm above has no hidden placeholder APIs:

```go
func (m *Manager) prepareDefinition(profileID string, kind Kind, revision int64, definition Definition) (*CompiledProfile, []byte, error)
func (m *Manager) persistDeviceProfileCAS(ctx context.Context, deviceID string, encoded []byte, expected int64) (store.DeviceProfile, error)
func (m *Manager) restartProviderLocked(profile *CompiledProfile)
func mutationResult(registry *Registry, revision int64, profile *CompiledProfile) MutationResult
```

`persistDeviceProfileCAS` uses `Store.WithTx`: normalize the device ID, create the device row with `display_name=device_id` when absent, then write the Profile CAS in the same transaction. Do not cache a pre-write Registry candidate across the store transaction; always clone the current Registry after the successful CAS so a concurrent provider publication is not overwritten. `PrepareShared` only validates/compiles; `PublishShared` rechecks the expected revision under `mutationMu`, clones the latest Registry after YAML persistence succeeds, and performs the infallible pointer swap.

- [ ] **Step 5: Add asynchronous provider runners with generation checks**

```go
type ProviderRunner interface {
	Start(context.Context)
	Stop()
}

type ProviderStatus struct {
	Degraded bool `json:"degraded"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type ProviderFactory func([]routerule.ProviderSpec, func([]string), func(ProviderStatus)) ProviderRunner

type providerRuntime struct {
	revision int64
	generation uint64
	runner ProviderRunner
}
```

For tests, implement the referenced manual runner in `provider_test.go` rather than adding test-only methods to production types:

```go
type manualProviderRunner struct {
	onRules func([]string)
	onStatus func(ProviderStatus)
}

func (r *manualProviderRunner) Start(context.Context) {}
func (r *manualProviderRunner) Stop() {}
func (r *manualProviderRunner) Emit(rules []string) { r.onRules(rules) }

type manualProviderFactory struct {
	mu sync.Mutex
	runners []*manualProviderRunner
}

func newManualProviderFactory() *manualProviderFactory { return &manualProviderFactory{} }
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
```

Extend `routerule.ProviderManager` with an optional status callback while keeping `NewProviderManager` backward-compatible. Each failed fetch publishes degraded/error status but retains cached last-success rules; a later successful fetch clears degraded status. Start each Profile runner in a goroutine so an initial network fetch cannot block the API. Every callback checks Profile ID, Profile revision, and provider generation under `mutationMu`, creates a new `routerule.Engine`, clones the compiled Profile and current Registry, and atomically publishes the new snapshot. Never call `SetRulesAndFinal` on an Engine already reachable from a published Registry.

- [ ] **Step 6: Run tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/profile
gofmt -w internal/routerule/provider.go internal/routerule/provider_status_test.go
go test -count=1 ./internal/profile ./internal/store ./internal/routerule
git diff --check
```

Expected: profile manager, provider fencing, store, and routerule tests pass.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/profile service/base/internal/routerule/provider.go service/base/internal/routerule/provider_status_test.go
git commit -m "feat(service/base): manage persistent device profiles"
```

---

### Task 6: Route HTTP, CONNECT, and SOCKS5 through the selected Profile

**Files:**
- Create: `service/base/internal/dispatch/auth.go`
- Create: `service/base/internal/dispatch/auth_test.go`
- Create: `service/base/internal/dispatch/profile_protocol_test.go`
- Modify: `service/base/internal/dispatch/server.go`
- Modify: `service/base/internal/dispatch/server_test.go`
- Modify: `service/base/internal/dispatch/socks.go`
- Modify: `service/base/internal/dispatch/socks_test.go`
- Modify: `service/base/internal/dispatch/params.go`
- Modify: `service/base/internal/dispatch/params_test.go`

- [ ] **Step 1: Write failing shared username-grammar tests**

```go
func TestSplitProxyUsername(t *testing.T) {
	tests := []struct {
		raw string
		base string
		device string
		strategy pool.Strategy
		wantErr bool
	}{
		{raw: "easyproxy", base: "easyproxy"},
		{raw: "easyproxy+dev=Laptop", base: "easyproxy", device: "laptop"},
		{raw: "easyproxy+dev=laptop+stable+us", base: "easyproxy", device: "laptop", strategy: pool.StrategyStable},
		{raw: "easyproxy+dev=a+dev=b", wantErr: true},
		{raw: "easyproxy+dev=bad device", wantErr: true},
	}
	for _, tt := range tests {
		got, err := splitProxyUsername(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Fatalf("%q err=%v", tt.raw, err)
		}
		if err == nil && (got.BaseUsername != tt.base || got.ExplicitDeviceID != tt.device) {
			t.Fatalf("%q parsed as %#v", tt.raw, got)
		}
	}
}
```

Add cross-protocol tests using one fake resolver:

```go
func TestProfileSelectionMatchesAcrossHTTPConnectAndSOCKS5(t *testing.T) {
	resolver := newFakeProfileResolver(t, "laptop")
	harness := newProfileTestServer(t, resolver)
	assertHTTPUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
	assertCONNECTUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
	assertSOCKSUsesProfile(t, harness, "easyproxy+dev=laptop", "secret", "device:laptop")
}
```

Implement the test harness in `profile_protocol_test.go` using the existing `dispatchTestOutbound`, `dispatchTestPoolProvider`, `newDispatchTestOrigin`, and `dialDispatchTestTCP` fixtures from `server_test.go`:

```go
type fakeProfileResolver struct {
	shared *profile.CompiledProfile
	deviceID string
	device *profile.CompiledProfile
}

func newFakeProfileResolver(t *testing.T, deviceID string) *fakeProfileResolver {
	t.Helper()
	shared, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{SchemaVersion: 1, Enabled: true, FinalPolicy: "PROXY"}, nil)
	if err != nil { t.Fatal(err) }
	device, err := profile.Compile("device:"+deviceID, profile.KindDevice, 1, profile.Definition{SchemaVersion: 1, Enabled: true, FinalPolicy: "PROXY"}, nil)
	if err != nil { t.Fatal(err) }
	return &fakeProfileResolver{shared: shared, deviceID: deviceID, device: device}
}

func (r *fakeProfileResolver) Credentials() profile.CredentialSnapshot {
	return profile.CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1}
}
func (r *fakeProfileResolver) Resolve(identity profile.RequestIdentity) profile.Resolution {
	if identity.ExplicitDeviceID == r.deviceID {
		return profile.Resolution{DeviceID: r.deviceID, Source: profile.IdentityExplicit, ProfileID: r.device.ID(), ProfileRevision: r.device.Revision(), Profile: r.device}
	}
	return profile.Resolution{Source: profile.IdentitySharedFallback, ProfileID: r.shared.ID(), ProfileRevision: r.shared.Revision(), Profile: r.shared}
}
func (r *fakeProfileResolver) Observe(profile.Resolution, netip.Addr, time.Time) {}

type profileProtocolHarness struct {
	address string
	target string
	outbound *dispatchTestOutbound
}

func newProfileTestServer(t *testing.T, resolver ProfileResolver) profileProtocolHarness {
	t.Helper()
	origin := newDispatchTestOrigin(t)
	outbound := &dispatchTestOutbound{tag: "profile-test"}
	provider := &dispatchTestPoolProvider{outbound: outbound}
	server := NewServer(Config{Listen: "127.0.0.1:0", LocalServer: true, Profiles: resolver}, provider, nil, nil)
	if err := server.Start(context.Background()); err != nil { t.Fatal(err) }
	t.Cleanup(server.Stop)
	server.mu.RLock()
	address := server.ln.Addr().String()
	server.mu.RUnlock()
	return profileProtocolHarness{address: address, target: origin.Listener.Addr().String(), outbound: outbound}
}

func assertLastProfileDial(t *testing.T, harness profileProtocolHarness, profileID string) {
	t.Helper()
	dials := harness.outbound.snapshotDials()
	if len(dials) == 0 || dials[len(dials)-1].directive.ProfileID != profileID {
		t.Fatalf("dials = %#v, want profile %q", dials, profileID)
	}
}

func proxyAuthorization(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func assertHTTPUsesProfile(t *testing.T, harness profileProtocolHarness, username, password, profileID string) {
	t.Helper()
	conn := dialDispatchTestTCP(t, harness.address)
	defer conn.Close()
	headers := make(http.Header)
	headers.Set("Proxy-Authorization", proxyAuthorization(username, password))
	body := dispatchTestProxyGET(t, conn, bufio.NewReader(conn), "http://"+harness.target+"/", headers)
	if body == "" { t.Fatal("empty HTTP proxy response") }
	assertLastProfileDial(t, harness, profileID)
}

func assertCONNECTUsesProfile(t *testing.T, harness profileProtocolHarness, username, password, profileID string) {
	t.Helper()
	conn := dialDispatchTestTCP(t, harness.address)
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", harness.target, harness.target, proxyAuthorization(username, password))
	request := &http.Request{Method: http.MethodConnect}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil { t.Fatal(err) }
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK { t.Fatalf("CONNECT status = %d", response.StatusCode) }
	assertLastProfileDial(t, harness, profileID)
}

func assertSOCKSUsesProfile(t *testing.T, harness profileProtocolHarness, username, password, profileID string) {
	t.Helper()
	conn := dialDispatchTestTCP(t, harness.address)
	defer conn.Close()
	_, _ = conn.Write([]byte{0x05, 0x01, 0x02})
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil || !bytes.Equal(method, []byte{0x05, 0x02}) { t.Fatalf("method=%v err=%v", method, err) }
	auth := append([]byte{0x01, byte(len(username))}, []byte(username)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	_, _ = conn.Write(auth)
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil || authReply[1] != 0x00 { t.Fatalf("auth=%v err=%v", authReply, err) }
	host, portText, err := net.SplitHostPort(harness.target)
	if err != nil { t.Fatal(err) }
	port, err := strconv.Atoi(portText)
	if err != nil { t.Fatal(err) }
	ip := net.ParseIP(host).To4()
	request := append([]byte{0x05, 0x01, 0x00, 0x01}, ip...)
	request = append(request, byte(port>>8), byte(port))
	_, _ = conn.Write(request)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x00 { t.Fatalf("reply=%v err=%v", reply, err) }
	assertLastProfileDial(t, harness, profileID)
}
```

Keep these byte-level helpers in the test file so all three paths exercise the real listener and parser rather than calling private resolver helpers directly.

Add explicit tests that disabled Profiles stay DIRECT even with `nosplit`, `X-Proxy-Split: off`, path tokens, and SOCKS username tokens.

Also add `TestHTTPKeepAliveReadsProfileRegistryPerRequest`, `TestDirectProfileDoesNotReadPoolProvider`, `TestProxyProfileWithoutPoolReturns502`, `TestUnknownExplicitDeviceUsesSharedWithoutIPFallback`, and the equivalent SOCKS general-failure assertion. These close the per-request publication, idle-pool, and explicit-identity rules that protocol parity alone does not prove.

- [ ] **Step 2: Run dispatch tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/dispatch -run 'TestSplitProxyUsername|TestProfileSelection|TestDisabledProfile'
```

Expected: missing profile resolver/authentication symbols.

- [ ] **Step 3: Add Local Server resolver and credential interfaces**

```go
type ProfileResolver interface {
	Credentials() profile.CredentialSnapshot
	Resolve(profile.RequestIdentity) profile.Resolution
	Observe(profile.Resolution, netip.Addr, time.Time)
}

type Config struct {
	Listen string
	Username string
	Password string
	DefaultStrategy pool.Strategy
	DialTimeout time.Duration
	BoundTokens string
	LocalServer bool
	Profiles ProfileResolver
}
```

`Profiles == nil` preserves the complete legacy path.

Add the shared parser:

```go
type parsedProxyUsername struct {
	BaseUsername string
	ExplicitDeviceID string
	Overlay directiveOverlay
}

func splitBaseUsername(raw string) (base string, rawTokens []string)
func splitProxyUsername(raw string) (parsedProxyUsername, error)
func (s *Server) authenticateProxy(username, password string) (parsedProxyUsername, error)
func (s *Server) authenticateHTTPRequest(req *http.Request) (parsedProxyUsername, error)
```

Authentication order is fixed: `splitBaseUsername` separates only the first segment without interpreting `dev=`; hash the supplied/canonical base username and password with SHA-256 and compare both digests using `subtle.ConstantTimeCompare`; only after that succeeds does `splitProxyUsername` validate/extract exactly one `dev=` and pass remaining tokens to `parseTokens`. This prevents invalid `dev=` syntax from becoming a credential oracle. HTTP maps bad base/password to 407 and authenticated malformed `dev=` to 400; SOCKS maps both to RFC 1929 auth failure.

- [ ] **Step 4: Change HTTP and SOCKS handlers to retain authenticated identity**

Use these signatures:

```go
func (s *Server) authenticateHTTPConn(conn net.Conn, req *http.Request) (parsedProxyUsername, bool)
func (s *Server) handleConnectConn(conn net.Conn, req *http.Request, auth parsedProxyUsername)
func (s *Server) handleHTTPConn(conn net.Conn, br *bufio.Reader, req *http.Request, auth parsedProxyUsername) bool
func (s *Server) socksHandshake(conn net.Conn) (parsedProxyUsername, bool)
func (s *Server) socksUserPassAuth(conn net.Conn) (parsedProxyUsername, bool)
```

Local Server requires RFC 1929 username/password for SOCKS5. Legacy no-auth behavior remains unchanged.

- [ ] **Step 5: Resolve the Profile before interpreting split overrides**

Implement one shared request resolver:

```go
func (s *Server) resolveProfileRequest(
	auth parsedProxyUsername,
	requestOverlay directiveOverlay,
	host string,
	remoteAddr net.Addr,
) (resolved, routerule.Policy, profile.Resolution, error) {
	peer := peerAddr(remoteAddr)
	resolution := s.cfg.Profiles.Resolve(profile.RequestIdentity{
		ExplicitDeviceID: auth.ExplicitDeviceID,
		PeerIP: peer,
	})
	s.cfg.Profiles.Observe(resolution, peer, time.Now())
	base := directiveFromProfile(resolution)
	if !resolution.Profile.Enabled() {
		return resolved{directive: base, split: true}, routerule.PolicyDirect, resolution, nil
	}
	overlay := s.bound.merge(auth.Overlay).merge(requestOverlay)
	final := overlay.applyTo(base, peer.String())
	return final, policyForProfile(final.split, resolution.Profile, host), resolution, nil
}
```

The exact precedence is:

```text
Profile defaults < BoundTokens < username tokens < HTTP path tokens < X-Proxy-* headers
```

`applyTo` may change strategy/filter/pin/session/split but must not change ProfileID, ProfileRevision, SessionTTL, or long-lived thresholds.

Define every helper used by the resolver in `params.go`/`server.go`:

```go
func peerAddr(addr net.Addr) netip.Addr
func cloneBool(value *bool) *bool
func (o directiveOverlay) applyTo(base pool.SelectionDirective, sessionFallback string) resolved

func policyForProfile(split bool, compiled *profile.CompiledProfile, host string) routerule.Policy {
	if !split {
		return routerule.PolicyProxy
	}
	return compiled.Match(host)
}
```

`peerAddr` accepts `*net.TCPAddr` first, then parses `host:port`, and returns an invalid `netip.Addr` rather than trusting headers. `applyTo` starts from a deep copy of the Profile directive, applies only explicitly present overlay fields, and fills an empty session key from the peer IP fallback.

- [ ] **Step 6: Map the selected Profile into the pool directive**

`directiveFromProfile` converts `profile.SelectionSettings` to the pool package at the dispatch boundary:

```go
func directiveFromProfile(r profile.Resolution) pool.SelectionDirective {
	selection := r.Profile.Selection()
	return pool.SelectionDirective{
		ProfileID: r.ProfileID,
		ProfileRevision: r.ProfileRevision,
		Strategy: pool.NormalizeStrategy(selection.DefaultStrategy),
		SessionTTL: selection.SessionTTL,
		Filter: pool.NodeFilter{
			Countries: append([]string(nil), selection.Filter.Countries...),
			Regions: append([]string(nil), selection.Filter.Regions...),
			LongLived: cloneBool(selection.Filter.LongLived),
		},
		LongLived: pool.LongLivedPolicy{
			MinUptime: selection.LongLivedMinUptime,
			MinSuccessRate: selection.LongLivedMinSuccessRate,
		},
	}
}
```

- [ ] **Step 7: Run dispatch tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/dispatch
go test -count=1 ./internal/dispatch ./internal/profile ./internal/outbound/pool
git diff --check
```

Expected: HTTP, CONNECT, SOCKS5, legacy dispatch, and Profile tests pass.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/dispatch
git commit -m "feat(service/base): route local clients by device profile"
```

---

### Task 7: Keep the Local Server dispatcher alive across initial idle and reload transitions

**Files:**
- Modify: `service/base/internal/boxmgr/manager.go`
- Modify: `service/base/internal/boxmgr/manager_test.go`
- Modify: `service/base/internal/app/routing_controller.go`
- Modify: `service/base/internal/app/routing_controller_test.go`
- Modify: `service/base/internal/app/app.go`
- Modify: `service/base/internal/app/app_test.go`
- Modify: `service/base/internal/monitor/server.go`

- [ ] **Step 1: Write failing initial-idle BoxManager tests**

```go
func TestStartWithoutNodesEntersInitialIdle(t *testing.T) {
	manager := newInitialIdleTestManager(t)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := manager.CurrentReloadState()
	if !state.Idle || state.Config == nil || manager.currentBox != nil {
		t.Fatalf("state=%#v box=%v", state, manager.currentBox)
	}
}

func TestInitialIdleCanReloadWhenNodesAppear(t *testing.T) {
	manager := newInitialIdleTestManager(t)
	if err := manager.Start(context.Background()); err != nil { t.Fatal(err) }
	withNode := manager.CurrentReloadState().Config.Clone()
	withNode.Nodes = []config.NodeConfig{{Name: "node", URI: "http://127.0.0.1:18080"}}
	if err := manager.Reload(withNode); err != nil {
		t.Fatal(err)
	}
	if manager.CurrentReloadState().Idle {
		t.Fatal("manager remained idle after nodes appeared")
	}
}

func newInitialIdleTestManager(t *testing.T) *Manager {
	t.Helper()
	cfg := &config.Config{
		Mode: "pool",
		Listener: config.ListenerConfig{Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed},
		Management: config.ManagementConfig{Enabled: boolPointer(false)},
		LocalServer: config.LocalServerConfig{
			Enabled: true,
			Auth: config.LocalServerAuthConfig{Username: "easyproxy", Password: "secret"},
			SharedRevision: 1,
			CredentialGeneration: 1,
		},
	}
	manager := New(cfg, monitor.Config{Enabled: false})
	manager.boxFactory = func(context.Context, *config.Config) (managedBox, error) {
		return &fakeManagedBox{name: "recovered"}, nil
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func boolPointer(value bool) *bool { return &value }
```

- [ ] **Step 2: Write failing RoutingController Local Server lifecycle tests**

Add tests for shared disabled startup, `Idle=true`, running-to-idle, idle-to-running, source-only reload, credential hot swap without listener restart, listen change with transactional restart, and failed pool rollback with the DIRECT dispatcher restored.

Use the explicit assertion:

```go
func TestLocalServerStartsWhileSharedDisabledAndBoxIdle(t *testing.T) {
	cfg := localServerConfigForTest()
	cfg.Routing.Enabled = false
	box := &routingBoxManagerStub{}
	profiles := newProfileRuntimeStub(disabledSharedProfile())
	controller := NewRoutingController(context.Background(), box, WithProfileRuntime(profiles))
	if err := controller.StartState(boxmgr.ReloadState{Config: cfg, Idle: true}); err != nil {
		t.Fatal(err)
	}
	if !controller.Running() {
		t.Fatal("Local Server dispatcher did not start")
	}
}

type routingBoxManagerStub struct{}
func (*routingBoxManagerStub) PoolOutbound() (adapter.Outbound, bool) { return nil, false }
func (*routingBoxManagerStub) StickySnapshot() (pool.StickySnapshot, bool) { return pool.StickySnapshot{}, false }
func (*routingBoxManagerStub) SetLongLivedThresholds(time.Duration, float64) {}
func (*routingBoxManagerStub) RecordAppliedConfig(*config.Config) {}

type profileRuntimeStub struct { shared *profile.CompiledProfile }
func newProfileRuntimeStub(shared *profile.CompiledProfile) *profileRuntimeStub { return &profileRuntimeStub{shared: shared} }
func (r *profileRuntimeStub) Credentials() profile.CredentialSnapshot { return profile.CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1} }
func (r *profileRuntimeStub) Resolve(profile.RequestIdentity) profile.Resolution {
	return profile.Resolution{Source: profile.IdentitySharedFallback, ProfileID: r.shared.ID(), ProfileRevision: r.shared.Revision(), Profile: r.shared}
}
func (*profileRuntimeStub) Observe(profile.Resolution, netip.Addr, time.Time) {}
func (*profileRuntimeStub) PrepareConfig(*config.Config) error { return nil }

func disabledSharedProfile() *profile.CompiledProfile {
	compiled, err := profile.Compile("shared", profile.KindShared, 1, profile.Definition{SchemaVersion: 1, Enabled: false, FinalPolicy: "DIRECT"}, nil)
	if err != nil { panic(err) }
	return compiled
}

func localServerConfigForTest() *config.Config {
	return &config.Config{
		Mode: "pool",
		Listener: config.ListenerConfig{Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed},
		Routing: config.RoutingConfig{Enabled: true, FinalPolicy: "PROXY"},
		LocalServer: config.LocalServerConfig{Enabled: true, Auth: config.LocalServerAuthConfig{Username: "easyproxy", Password: "secret"}, SharedRevision: 1, CredentialGeneration: 1},
	}
}
```

- [ ] **Step 3: Run lifecycle tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/boxmgr ./internal/app -run 'TestStartWithoutNodes|TestInitialIdle|TestLocalServer'
```

Expected: current BoxManager tries to build a zero-member box and current routing controller suppresses the dispatcher while idle.

- [ ] **Step 4: Implement initial idle and an immutable state accessor**

Add:

```go
func (m *Manager) CurrentReloadState() ReloadState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return ReloadState{Config: snapshotConfig(m.lastAppliedCfg), Idle: m.lastAppliedIdle}
}
```

At the start of `Manager.Start`, after monitor preparation and before `createManagedBox`, branch when `cfg.LocalServer.Enabled && len(cfg.Nodes) == 0`. The initial-idle path must set `cfg`, `lastAppliedCfg`, `idle`, `lastAppliedIdle`, `currentBox=nil`, call `monitorServer.SetConfig`, and start the periodic health lifecycle exactly once. Legacy mode keeps its existing zero-node startup behavior.

- [ ] **Step 5: Make RoutingController mode-aware without duplicating it**

Use an option so legacy call sites remain simple:

```go
type RoutingControllerOption func(*RoutingController)

type ProfileRuntime interface {
	dispatch.ProfileResolver
	PrepareConfig(*config.Config) error
}

func WithProfileRuntime(runtime ProfileRuntime) RoutingControllerOption {
	return func(rc *RoutingController) { rc.profiles = runtime }
}

func NewRoutingController(ctx context.Context, boxMgr routingBoxManager, opts ...RoutingControllerOption) *RoutingController
func (rc *RoutingController) StartState(state boxmgr.ReloadState) error
func (rc *RoutingController) Running() bool
```

`NewRoutingController` sets `managerRef` only when `boxMgr` is a concrete `*boxmgr.Manager`; tests may pass the interface stub above. `Start(cfg)` remains as the legacy convenience wrapper and calls `StartState(boxmgr.ReloadState{Config: cloneConfigSnapshot(cfg)})`. App startup calls `StartState(boxMgr.CurrentReloadState())` so an initial idle state is not lost. `Running` reads the existing `running` field under the controller mutex and is used for status/health assertions, not as a test-only backdoor.

Replace `routingEnabled` with:

```go
func dispatchEnabled(state boxmgr.ReloadState) bool {
	if state.Config == nil {
		return false
	}
	if state.Config.LocalServer.Enabled {
		return true
	}
	return state.Config.Routing.Enabled && !state.Idle
}
```

Split `startLocked` into legacy and Local Server branches. The Local Server branch injects the Profile resolver/credential source and does not create the legacy single engine or ProviderManager. Its topology comparison includes only mode and listen; credentials are dynamic and do not restart the socket.

`PrepareReload` calls `profiles.PrepareConfig(to.Config)` before any listener transition. Do not publish the prepared Registry from `CompleteReload`, because BoxManager can still fail its shared-state commit afterward; publication happens through Profile Manager's ordered `OnConfigUpdate` callback only after BoxManager commit. During running-to-idle and idle-to-running reloads, keep the Local Server dispatcher alive. If box rollback fails, restore the previous Local Server listener so DIRECT remains available while PROXY returns `pool not available`.

Make `ApplyHot` mode-aware: Local Server shared session TTL/filter/rules/providers are already compiled and atomically published by Profile Manager, so the controller must not create a second Engine/ProviderManager and must treat shared session TTL as hot-applicable; legacy mode keeps the existing single-engine/provider path and still requires reload for its pool-wide session TTL. `RoutingStatus` must expose dispatcher readiness separately from shared enabled/revision/rule count.

- [ ] **Step 6: Wire Profile Manager in app startup**

After opening Store, create Profile Manager before `boxMgr.PrepareMonitor`/`boxMgr.Start` so the reusable monitor runtime receives canonical auth before its listener becomes public:

```go
profileMgr, err := profile.NewManager(ctx, cfg, dataStore)
if err != nil {
	return fmt.Errorf("start profile manager: %w", err)
}
defer profileMgr.Close()

boxMgr := boxmgr.New(cfg, monitorCfg, boxmgr.WithStore(dataStore))
if err := boxMgr.PrepareMonitor(ctx); err != nil {
	return fmt.Errorf("prepare monitor server: %w", err)
}
if server := boxMgr.MonitorServer(); server != nil {
	server.SetProfileManager(profileMgr)
}
boxMgr.AddConfigListener(profileMgr)
if err := boxMgr.Start(ctx); err != nil {
	return fmt.Errorf("start box manager: %w", err)
}

routingCtl := NewRoutingController(ctx, boxMgr, WithProfileRuntime(profileMgr))
if err := routingCtl.StartState(boxMgr.CurrentReloadState()); err != nil {
	return fmt.Errorf("start routing controller: %w", err)
}
```

Add `profiles *profile.Manager` under `monitor.Server.depsMu` plus the dependency-only accessors in this task; Task 8 uses them for auth/session behavior:

```go
func (s *Server) SetProfileManager(manager *profile.Manager) {
	s.depsMu.Lock()
	s.profiles = manager
	s.depsMu.Unlock()
}

func (s *Server) profileManagerSnapshot() *profile.Manager {
	s.depsMu.RLock()
	defer s.depsMu.RUnlock()
	return s.profiles
}
```

Register Profile Manager/config/reload listeners before background source refresh starts, and order config notifications so a successfully committed Config is published to Profile Manager before management APIs can mutate the new snapshot.

- [ ] **Step 7: Run lifecycle packages and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/boxmgr/manager.go internal/boxmgr/manager_test.go internal/app
gofmt -w internal/monitor/server.go
go test -count=1 ./internal/boxmgr ./internal/app ./internal/builder ./internal/monitor
git diff --check
```

Expected: initial idle, Local Server lifecycle, rollback, and all legacy routing tests pass.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/boxmgr service/base/internal/app service/base/internal/monitor/server.go
git commit -m "feat(service/base): run local server dispatch lifecycle"
```

---

### Task 8: Unify Web and proxy authentication with session generations

**Files:**
- Modify: `service/base/internal/monitor/server.go`
- Modify: `service/base/internal/monitor/server_test.go`
- Modify: `service/base/internal/monitor/server_lifecycle_test.go`
- Modify: `service/base/internal/app/app.go`
- Modify: `service/base/internal/profile/manager.go`

- [ ] **Step 1: Write failing authentication and rotation tests**

Add tests for unauthenticated auth-mode discovery, canonical JSON login, Basic management auth, legacy password login, ignored `Proxy-Authorization`, password redaction, generation invalidation, and no listener restart on Local Server rotation:

```go
func TestLocalServerAuthRequiresCanonicalPair(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 4)
	status := performJSONRequest(t, harness.server, http.MethodGet, "/api/auth", nil, nil)
	if status.Body["auth_mode"] != "canonical_pair" {
		t.Fatalf("auth status = %#v", status.Body)
	}
	login := performJSONRequest(t, harness.server, http.MethodPost, "/api/auth", map[string]any{
		"username": "easyproxy", "password": "secret",
	}, nil)
	if login.Code != http.StatusOK || login.Body["token"] == "" {
		t.Fatalf("login = %#v", login)
	}
}

func TestManagementDoesNotAcceptProxyAuthorization(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	rr := httptest.NewRecorder()
	harness.server.handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestSessionGenerationInvalidatesOldSession(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "old", 2)
	session, err := harness.server.createSession()
	if err != nil { t.Fatal(err) }
	harness.profiles.PublishCredentials(profile.CredentialSnapshot{Username: "easyproxy", Password: "new", Generation: 3})
	if harness.server.validateSession(session.Token) {
		t.Fatal("old-generation session remained valid")
	}
}

type localServerMonitorHarness struct {
	server *Server
	profiles *profile.Manager
}

func newLocalServerMonitor(t *testing.T, username, password string, generation uint64) localServerMonitorHarness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		Routing: config.RoutingConfig{Enabled: true, FinalPolicy: "PROXY"},
		LocalServer: config.LocalServerConfig{Enabled: true, SharedRevision: 1, CredentialGeneration: generation, Auth: config.LocalServerAuthConfig{Username: username, Password: password}},
	}
	profiles, err := profile.NewManager(ctx, cfg, st)
	if err != nil { t.Fatal(err) }
	t.Cleanup(profiles.Close)
	monitorManager, err := NewManager(Config{Password: password})
	if err != nil { t.Fatal(err) }
	server := NewServer(Config{Password: password}, monitorManager, nil)
	server.SetConfig(cfg)
	server.SetStore(st)
	server.SetProfileManager(profiles)
	return localServerMonitorHarness{server: server, profiles: profiles}
}

type jsonTestResponse struct {
	Code int
	Body map[string]any
}

func performJSONRequest(t *testing.T, server *Server, method, path string, body any, headers http.Header) jsonTestResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil { t.Fatal(err) }
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	for key, values := range headers { for _, value := range values { req.Header.Add(key, value) } }
	if body != nil { req.Header.Set("Content-Type", "application/json") }
	rr := httptest.NewRecorder()
	server.handler.ServeHTTP(rr, req)
	result := jsonTestResponse{Code: rr.Code, Body: map[string]any{}}
	if rr.Body.Len() != 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &result.Body); err != nil { t.Fatal(err) }
	}
	return result
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/monitor -run 'TestLocalServerAuth|TestManagementDoesNotAccept|TestSessionGeneration'
```

Expected: current auth is password-only and sessions have no generation.

- [ ] **Step 3: Use the Task 7 Profile Manager dependency as the dynamic credential snapshot**

```go
func (s *Server) credentialSnapshot() profile.CredentialSnapshot {
	if manager := s.profileManagerSnapshot(); manager != nil && manager.LocalServerEnabled() {
		return manager.Credentials()
	}
	return profile.CredentialSnapshot{Password: s.managementPassword(), Generation: 1}
}
```

App wiring must call `server.SetProfileManager(profileMgr)` before public management startup.

- [ ] **Step 4: Implement auth-mode discovery and canonical login**

`GET /api/auth` returns:

```json
{"auth_mode":"canonical_pair","username_required":true,"no_password":false}
```

in Local Server mode and keeps the existing legacy response semantics otherwise.

`POST /api/auth` decodes:

```go
var req struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
```

Local Server hashes both supplied/canonical fields with SHA-256 and compares both fixed-size digests with `subtle.ConstantTimeCompare`. `withAuth` accepts Cookie/Bearer session, Basic canonical credentials, and existing raw/Bearer canonical password compatibility, but never reads `Proxy-Authorization`.

- [ ] **Step 5: Carry credential generation through memory and SQLite sessions**

Add `CredentialGeneration uint64` to monitor `Session`. `createSession` snapshots the current generation into both memory and `store.Session`. `validateSession` rejects memory or store sessions whose generation differs from the current credential snapshot.

`profile.Manager.PublishCredentials` owns cleanup so API updates and successful external reloads share one path. After its atomic Registry swap, schedule:

```go
go func(storeRef store.Store, generation uint64) {
	if storeRef != nil {
		_ = storeRef.DeleteSessionsBeforeGeneration(context.Background(), generation)
	}
}(m.store, snapshot.Generation)
```

Correctness comes from generation comparison, not cleanup completion.

- [ ] **Step 6: Implement write-only password rotation on `/api/local-server/config` foundation**

The update DTO uses a pointer:

```go
type localServerConfigUpdate struct {
	Enabled *bool `json:"enabled,omitempty"`
	Listen *string `json:"listen,omitempty"`
	AuthUsername *string `json:"auth_username,omitempty"`
	AuthPassword *string `json:"auth_password,omitempty"`
}
```

Missing `auth_password` preserves the current secret; an empty supplied value returns 422. Treat username change, non-empty password change, and `enabled: false -> true` as credential-generation changes. The exact update matrix is:

```text
active Local Server + credential-only change
  -> validate cloned candidate
  -> SaveSettings atomically
  -> ProfileManager.PublishCredentials(new snapshot)
  -> RecordAppliedConfig(candidate)
  -> need_reload=false; do not rebind either listener

enabled or listen structural change
  -> validate and SaveSettings target candidate
  -> increment generation on false -> true
  -> need_reload=true
  -> do not publish active-mode/listen/credentials before reload commits

successful external/API reload
  -> ProfileManager.PublishConfigSnapshot(committed config)
  -> management and dispatcher begin reading the same snapshot

save failure, reload-window conflict, or failed reload
  -> retain old Profile Registry, credential snapshot, sessions, and listener
```

The ordered Profile Manager config-listener path calls `PublishConfigSnapshot` only after the committed Config becomes active; `RoutingController.CompleteReload` must not publish it early. This makes file-based credential changes follow the same atomic publication rule as API changes.

- [ ] **Step 7: Run monitor tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/monitor/server.go internal/monitor/server_test.go internal/monitor/server_lifecycle_test.go internal/app/app.go internal/profile/manager.go
go test -count=1 ./internal/monitor ./internal/app ./internal/profile ./internal/store
git diff --check
```

Expected: canonical and legacy auth tests pass, old sessions fail after generation changes, and the same credential snapshot protects monitor and dispatcher.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/monitor service/base/internal/app/app.go service/base/internal/profile/manager.go
git commit -m "feat(service/base): unify local server credentials"
```

---

### Task 9: Expose Local Server, device Profile, and IP mapping management APIs

**Files:**
- Create: `service/base/internal/monitor/local_server.go`
- Create: `service/base/internal/monitor/local_server_test.go`
- Modify: `service/base/internal/monitor/server.go`
- Modify: `service/base/internal/monitor/server_lifecycle_test.go`
- Modify: `service/base/internal/profile/manager.go`
- Modify: `service/base/internal/profile/types.go`

- [ ] **Step 1: Write failing handler tests for the complete resource contract**

Test status/config redaction, shared revision, device resource upsert, Profile CAS upsert, one-time copy-shared, enabled patch, idempotent delete, mapping CRUD by mapping ID, reload-window conflict, and persistence rollback:

```go
func TestDeviceProfileAPIUsesCASAndReturnsMutationEnvelope(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	create := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile": validProfilePayload(true),
	})
	if create.Code != http.StatusOK || create.Body["revision"] != float64(1) || create.Body["profile_scope"] != "device" {
		t.Fatalf("create = %#v", create)
	}
	conflict := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile": validProfilePayload(false),
	})
	if conflict.Code != http.StatusConflict || conflict.Body["current_revision"] != float64(1) {
		t.Fatalf("conflict = %#v", conflict)
	}
}

func TestLocalServerConfigNeverReturnsPassword(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	res := performAuthedJSON(t, server, http.MethodGet, "/api/local-server/config", nil)
	encoded, _ := json.Marshal(res.Body)
	if bytes.Contains(encoded, []byte("secret")) || res.Body["password_set"] != true {
		t.Fatalf("config response = %s", encoded)
	}
}

func TestProfileMutationDuringReloadReturns409(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	server.BeginReloadWindow()
	defer server.EndReloadWindow()
	res := performAuthedJSON(t, server, http.MethodPatch, "/api/local-server/devices/laptop/profile/enabled", map[string]any{
		"expected_revision": 1, "enabled": false,
	})
	if res.Code != http.StatusConflict || res.Body["error"] != "reload_in_progress" {
		t.Fatalf("response = %#v", res)
	}
}

func newLocalServerAPITestServer(t *testing.T) *Server {
	t.Helper()
	return newLocalServerMonitor(t, "easyproxy", "secret", 1).server
}

func performAuthedJSON(t *testing.T, server *Server, method, path string, body any) jsonTestResponse {
	t.Helper()
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	return performJSONRequest(t, server, method, path, body, headers)
}

func validProfilePayload(enabled bool) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"enabled": enabled,
		"default_strategy": "stable",
		"use_default_rules": true,
		"final_policy": "PROXY",
		"rules": []string{},
		"rule_providers": []any{},
		"node_filter": map[string]any{"countries": []string{}, "regions": []string{}, "long_lived": nil},
		"long_lived": map[string]any{"min_uptime": "2h", "min_success_rate": 0.9},
		"session": map[string]any{"ttl": "10m"},
	}
}
```

- [ ] **Step 2: Run the handler tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/monitor -run 'TestDeviceProfileAPI|TestLocalServerConfig|TestProfileMutationDuringReload'
```

Expected: all new routes return 404.

- [ ] **Step 3: Register the Local Server route family in one place**

In `NewServer`, add:

```go
s.registerLocalServerRoutes(mux)
```

and in `local_server.go`:

```go
func (s *Server) registerLocalServerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/local-server/status", s.withAuth(s.handleLocalServerStatus))
	mux.HandleFunc("/api/local-server/config", s.withAuth(s.handleLocalServerConfig))
	mux.HandleFunc("/api/local-server/profiles/shared", s.withAuth(s.handleSharedProfile))
	mux.HandleFunc("/api/local-server/devices", s.withAuth(s.handleDevices))
	mux.HandleFunc("/api/local-server/devices/", s.withAuth(s.handleDeviceResource))
	mux.HandleFunc("/api/local-server/ip-mappings", s.withAuth(s.handleIPMappings))
	mux.HandleFunc("/api/local-server/ip-mappings/", s.withAuth(s.handleIPMappingResource))
}
```

Parse escaped path segments with `url.PathUnescape`, then pass every device ID through `profile.NormalizeDeviceID`.

- [ ] **Step 4: Use one mutation envelope and structured errors**

```go
type mutationEnvelope struct {
	Revision int64 `json:"revision"`
	RegistryRevision uint64 `json:"registry_revision"`
	NeedReload bool `json:"need_reload"`
	ProfileScope string `json:"profile_scope"`
	Resource any `json:"resource,omitempty"`
	Message string `json:"message,omitempty"`
}

type apiError struct {
	Error string `json:"error"`
	CurrentRevision int64 `json:"current_revision,omitempty"`
	NeedReload bool `json:"need_reload,omitempty"`
}

type localServerStatusResponse struct {
	Enabled bool `json:"enabled"`
	Listen string `json:"listen"`
	DispatcherReady bool `json:"dispatcher_ready"`
	RegistryRevision uint64 `json:"registry_revision"`
	CredentialGeneration uint64 `json:"credential_generation"`
	ProfileCount int `json:"profile_count"`
	MappingCount int `json:"mapping_count"`
	ProviderDegradedCount int `json:"provider_degraded_count"`
	PeerAddressMode string `json:"peer_address_mode"`
	SourceIPWarning string `json:"source_ip_warning"`
}

type localServerConfigResponse struct {
	Enabled bool `json:"enabled"`
	Listen string `json:"listen"`
	AuthUsername string `json:"auth_username"`
	PasswordSet bool `json:"password_set"`
	SharedRevision int64 `json:"shared_revision"`
	CredentialGeneration uint64 `json:"credential_generation"`
}

type profileResourceResponse struct {
	ProfileScope string `json:"profile_scope"`
	DeviceID string `json:"device_id,omitempty"`
	Revision int64 `json:"revision"`
	RegistryRevision uint64 `json:"registry_revision"`
	NeedReload bool `json:"need_reload"`
	Profile profile.Definition `json:"profile"`
	ProviderStatus profile.ProviderStatus `json:"provider_status"`
}

type deviceSummaryResponse struct {
	DeviceID string `json:"device_id"`
	DisplayName string `json:"display_name"`
	Revision int64 `json:"revision"`
	ProfileMode string `json:"profile_mode"`
	ProfileRevision int64 `json:"profile_revision,omitempty"`
	EffectiveEnabled bool `json:"effective_enabled"`
	EffectiveState string `json:"effective_state"`
	IdentitySource string `json:"identity_source,omitempty"`
	LastSeenIP string `json:"last_seen_ip,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	MappingCount int `json:"mapping_count"`
}

type ipMappingResponse struct {
	MappingID string `json:"mapping_id"`
	CIDR string `json:"cidr"`
	DeviceID string `json:"device_id"`
	Priority int `json:"priority"`
	Enabled bool `json:"enabled"`
	Revision int64 `json:"revision"`
}
```

Map `store.RevisionConflictError` to 409, validation errors to 422, `errReloadInProgress` to 409, absent resources to 404, and unexpected persistence failures to 500 while preserving the old Registry.

Parse optimistic revisions in one helper. JSON `expected_revision` wins only when no header is present; `If-None-Match: *` means revision 0; `If-Match: "4"` or `If-Match: 4` means revision 4; contradictory body/header values return 422:

```go
func expectedRevision(r *http.Request, bodyRevision *int64) (int64, error)
```

Before any YAML/SQLite mutation, acquire the existing BoxManager barrier through a narrow interface and release it after persistence plus Registry publication:

```go
type configMutationGuard interface {
	BeginConfigMutation(context.Context) (func(), error)
}

func (s *Server) beginConfigMutation(ctx context.Context) (func(), error) {
	manager := s.nodeManagerSnapshot()
	guard, ok := manager.(configMutationGuard)
	if !ok {
		return func() {}, nil
	}
	return guard.BeginConfigMutation(ctx)
}
```

Keep `configUpdateMu` and `reloadWindowCount` checks inside that barrier so reload target capture cannot race a Profile/config write.

- [ ] **Step 5: Implement shared Profile candidate-save and one compatibility transaction**

For shared PUT:

```text
decode complete Definition + expected revision
-> ProfileManager.PrepareShared
-> lock configUpdateMu and reject reload window
-> re-read current cfgSrc/revision
-> clone config, replace Routing, increment LocalServer.SharedRevision
-> candidate.SaveSettings
-> publish candidate into cfgSrc
-> ProfileManager.PublishShared
-> return mutationEnvelope
```

Expose `GET/PUT /api/routing/config` through the same shared helpers in Local Server mode. Convert the legacy flattened request into a complete `profile.Definition`, preserve `routing.listen`, apply the expected current shared revision under `configUpdateMu`, and return `profile_scope: "shared"`. Do not keep the old `updateRoutingConfig` transaction as a second implementation.

Also make `GET /api/routing/status` a true shared alias in Local Server mode: `enabled` is the shared Profile switch, `dispatcher_ready` reports the always-on listener separately, and rule count/final policy/default strategy/revision come from Profile Manager's immutable shared snapshot rather than `dispatch.Server`'s legacy single engine.

- [ ] **Step 6: Implement device and mapping handlers**

Implement the complete resource family, not only Profile writes:

```text
GET /devices                  -> persisted devices merged with Profile mode, mappings, and activity
GET /devices/{id}             -> one merged device resource or 404
PUT /devices/{id}             -> CAS create/update display_name
PUT /devices/{id}/profile     -> complete Profile CAS upsert
PATCH /devices/{id}/profile/enabled
POST /devices/{id}/profile/copy-shared
DELETE /devices/{id}/profile  -> idempotent; device and mappings remain
```

Device Profile full PUT calls `PutDeviceProfile`; `PATCH .../enabled` loads the complete existing Definition and only changes enabled; `copy-shared` returns 409 when a Profile already exists; delete returns the selected shared summary in `mutationEnvelope.Resource`. The list response must merge process-local `DeviceActivityTracker` observations without making activity part of configuration correctness.

Mapping POST creates a server-generated mapping ID with `crypto/rand`; mapping PUT/DELETE address the ID, never the CIDR path. Normalize bare IP input to `/32` or `/128` before persistence.

`GET /api/local-server/status` must include dispatcher readiness, provider degraded count/summary, `peer_address_mode: "tcp_peer"`, Profile/mapping counts, and the Docker/NAT source-IP warning. Add resolver tests for exact IP over CIDR, longest prefix over shorter prefix, higher priority within equal prefix length, disabled mapping exclusion, and explicit `dev=` overriding all mappings.

- [ ] **Step 7: Run monitor/profile tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/monitor/local_server.go internal/monitor/local_server_test.go internal/monitor/server.go internal/monitor/server_lifecycle_test.go internal/profile
go test -count=1 ./internal/monitor ./internal/profile ./internal/store
git diff --check
```

Expected: every Local Server route, revision conflict, rollback, and legacy shared alias test passes.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/monitor service/base/internal/profile
git commit -m "feat(service/base): expose device profile management api"
```

---

### Task 10: Close compatibility, settings, lease, and rollback-snapshot gaps

**Files:**
- Modify: `service/base/internal/monitor/proxy_service_compat.go`
- Modify: `service/base/internal/monitor/proxy_service_compat_logic_test.go`
- Modify: `service/base/internal/monitor/server.go`
- Modify: `service/base/internal/monitor/server_test.go`
- Modify: `service/base/internal/boxmgr/manager.go`
- Modify: `service/base/internal/boxmgr/manager_test.go`

- [ ] **Step 1: Write failing compatibility tests**

```go
func TestProxyUsernameForHostEncodesCanonicalDeviceID(t *testing.T) {
	if got := proxyUsernameForHost("easyproxy", "Laptop-Work"); got != "easyproxy+dev=laptop-work" {
		t.Fatalf("username = %q", got)
	}
}

func TestLocalServerSettingsDoNotExposeOrAcceptLegacyPasswords(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	settings := performAuthedJSON(t, server, http.MethodGet, "/api/settings", nil)
	encoded, _ := json.Marshal(settings.Body)
	if bytes.Contains(encoded, []byte("listener_password")) || bytes.Contains(encoded, []byte("management_password")) {
		t.Fatalf("legacy password leaked: %s", encoded)
	}
}

func TestRecordAppliedConfigKeepsHotLocalServerFieldsForRollback(t *testing.T) {
	base := &config.Config{
		Mode: "pool",
		Listener: config.ListenerConfig{Address: "127.0.0.1", Port: 22323, Protocol: config.InboundProtocolMixed},
		Routing: config.RoutingConfig{Enabled: true, Session: config.SessionConfig{TTL: 10 * time.Minute}},
		LocalServer: config.LocalServerConfig{Enabled: true, SharedRevision: 1, CredentialGeneration: 1, Auth: config.LocalServerAuthConfig{Username: "easyproxy", Password: "secret"}},
	}
	manager := New(base, monitor.Config{Enabled: false})
	manager.lastAppliedCfg = base.Clone()
	manager.lastAppliedIdle = false
	updated := base.Clone()
	updated.LocalServer.SharedRevision = 9
	updated.LocalServer.CredentialGeneration = 4
	updated.Routing.Session.TTL = 45 * time.Minute
	manager.RecordAppliedConfig(updated)
	got := manager.CurrentReloadState().Config
	if got.LocalServer.SharedRevision != 9 || got.LocalServer.CredentialGeneration != 4 || got.Routing.Session.TTL != 45*time.Minute {
		t.Fatalf("applied config = %#v", got)
	}
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/monitor ./internal/boxmgr -run 'TestProxyUsernameForHost|TestProxyCompatCheckoutLifecycle|TestLocalServerSettings|TestRecordAppliedConfigKeeps'
```

Expected: checkout returns the old static username, settings expose legacy secrets, and rollback snapshots omit Local Server hot fields.

- [ ] **Step 3: Update compatibility checkout and settings behavior**

In Local Server mode, checkout obtains credentials from Profile Manager and formats:

```go
func proxyUsernameForHost(base, hostID string) string {
	normalized, err := profile.NormalizeDeviceID(hostID)
	if err != nil || normalized == "" {
		return base
	}
	return base + "+dev=" + normalized
}
```

Keep the response shape unchanged. Invalid or empty HostID uses the canonical base username and therefore shared/IP fallback.

Extend the existing `TestProxyCompatCheckoutLifecycle` fixture (which already has selectable nodes) with `HostID: "Laptop-Work"` and assert the returned lease credentials contain `easyproxy+dev=laptop-work` plus the canonical password. This provides the endpoint-level proof in addition to the pure helper test above.

In `/api/settings`, Local Server GET uses a dedicated sanitized response DTO/map that omits `listener_password` and `management_password` keys entirely, returns `local_server_enabled`, `local_server_auth_username`, and `local_server_password_set`, and never marshals any password. Do not rely on empty strings in the existing static DTO. Local Server PUT rejects supplied conflicting legacy credential fields with `409 credential_source_conflict`; missing old fields do not clear canonical auth.

- [ ] **Step 4: Preserve hot Local Server state in BoxManager rollback snapshots**

Make `mergeHotAppliedConfig` mode-aware. In Local Server mode merge:

- shared Routing fields, including session TTL and node filter;
- `LocalServer.Auth`;
- `LocalServer.SharedRevision`;
- `LocalServer.CredentialGeneration`.

Do not merge structural `LocalServer.Enabled`, `LocalServer.Listen`, mode, listener address/port/protocol, nodes, or source topology before a successful reload.

After every successful shared Profile hot publication or active Local Server credential publication, call `boxMgr.RecordAppliedConfig(candidate)` through the existing routing/node-manager dependency. Structural Local Server changes call it only after the reload commits. Add a rollback test that performs the real handler update followed by a failed source reload and proves the last successful hot fields are retained.

- [ ] **Step 5: Run tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
gofmt -w internal/monitor/proxy_service_compat.go internal/monitor/proxy_service_compat_logic_test.go internal/monitor/server.go internal/monitor/server_test.go internal/boxmgr/manager.go internal/boxmgr/manager_test.go
go test -count=1 ./internal/monitor ./internal/boxmgr ./internal/app
git diff --check
```

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/internal/monitor service/base/internal/boxmgr
git commit -m "fix(service/base): close local server compatibility gaps"
```

---

### Task 11: Establish the frontend Vitest and React Testing Library baseline

**Files:**
- Modify: `service/base/frontend/package.json`
- Modify: `service/base/frontend/package-lock.json`
- Create: `service/base/frontend/vitest.config.ts`
- Modify: `service/base/frontend/tsconfig.node.json`
- Create: `service/base/frontend/src/test/setup.ts`
- Create: `service/base/frontend/src/test/http.ts`
- Create: `service/base/frontend/src/test/test-environment.test.tsx`
- Modify: `.github/workflows/validate.yml`

- [ ] **Step 1: Add a RED harness test before adding the test script**

```tsx
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it } from 'vitest'

function Probe() {
  return <button onClick={() => localStorage.setItem('clicked', 'yes')}>ready</button>
}

it('provides jsdom, jest-dom, and isolated localStorage', async () => {
	const user = userEvent.setup()
	render(<Probe />)
	await user.click(screen.getByRole('button', { name: 'ready' }))
	expect(localStorage.getItem('clicked')).toBe('yes')
})

it('clears localStorage between tests', () => {
	expect(localStorage.getItem('clicked')).toBeNull()
})
```

- [ ] **Step 2: Run the missing command and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test
```

Expected: npm reports that the `test` script does not exist.

- [ ] **Step 3: Install and configure the official frontend test stack**

Run:

```powershell
npm install --save-dev vitest jsdom @testing-library/react @testing-library/jest-dom @testing-library/user-event
```

Add scripts:

```json
{
  "test": "vitest run",
  "test:watch": "vitest"
}
```

Create `vitest.config.ts`:

```ts
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    restoreMocks: true,
  },
})
```

Keep Vitest globals disabled and import `describe/expect/it/vi` explicitly in every test. Create `setup.ts` with the exact cleanup boundary:

```ts
import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  window.location.hash = ''
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})
```

`http.ts` exports the exact helpers used by later tasks:

```ts
import { vi, type Mock } from 'vitest'

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

export function mockFetch(...responses: Response[]): Mock<typeof fetch> {
  const spy = vi.fn<typeof fetch>()
  for (const response of responses) spy.mockResolvedValueOnce(response)
  if (responses.length > 0) spy.mockResolvedValue(responses[responses.length - 1])
  vi.stubGlobal('fetch', spy)
  return spy
}
```

Do not add MSW.

- [ ] **Step 4: Add the test command to CI**

In `.github/workflows/validate.yml`, run `npm ci`, `npm run test`, `npm run lint`, then `npm run build` for `service/base/frontend`.

- [ ] **Step 5: Run frontend checks and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test
npm run lint
npm run build
git diff --check
```

Expected: the harness test passes and the existing frontend still builds.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/frontend/package.json service/base/frontend/package-lock.json service/base/frontend/vitest.config.ts service/base/frontend/tsconfig.node.json service/base/frontend/src/test .github/workflows/validate.yml
git commit -m "test(frontend): add Vitest and RTL harness"
```

---

### Task 12: Add typed Local Server API clients and canonical login/settings UI

**Files:**
- Create: `service/base/frontend/src/types/localServer.ts`
- Modify: `service/base/frontend/src/types/index.ts`
- Modify: `service/base/frontend/src/api/client.ts`
- Create: `service/base/frontend/src/api/localServer.ts`
- Create: `service/base/frontend/src/api/client.test.ts`
- Create: `service/base/frontend/src/api/localServer.test.ts`
- Create: `service/base/frontend/src/test/localServerFixtures.ts`
- Modify: `service/base/frontend/src/components/LoginPage.tsx`
- Create: `service/base/frontend/src/components/LoginPage.test.tsx`
- Create: `service/base/frontend/src/components/local-server/LocalServerSettingsCard.tsx`
- Create: `service/base/frontend/src/components/local-server/LocalServerSettingsCard.test.tsx`
- Modify: `service/base/frontend/src/components/SettingsPanel.tsx`
- Modify: `service/base/frontend/src/App.tsx`
- Create: `service/base/frontend/src/App.test.tsx`

- [ ] **Step 1: Write RED API transport tests**

```ts
it('preserves structured 409 payloads', async () => {
  mockFetch(jsonResponse({
    error: 'profile_revision_conflict',
    current_revision: 4,
    need_reload: false,
  }, 409))
  await expect(apiRequest('/api/local-server/profiles/shared')).rejects.toMatchObject({
    status: 409,
    payload: { current_revision: 4, need_reload: false },
  })
})

it('creates a device profile with If-None-Match', async () => {
  const fetchSpy = mockFetch(jsonResponse(mutationFixture()))
  await putDeviceProfile('Laptop', profileFixture(), 0)
  expect(fetchSpy).toHaveBeenCalledWith(
    '/api/local-server/devices/laptop/profile',
    expect.objectContaining({ method: 'PUT', headers: expect.objectContaining({ 'If-None-Match': '*' }) }),
  )
})
```

Create `src/test/localServerFixtures.ts` so Tasks 12-14 share one schema-accurate fixture source:

```ts
import type { DeviceSummary, ForwardingProfile, IPMapping, MutationResponse, ProfileResource } from '../types/localServer'

export function profileFixture(overrides: Partial<ForwardingProfile> = {}): ForwardingProfile {
  return {
    schema_version: 1,
    enabled: true,
    default_strategy: 'stable',
    use_default_rules: true,
    final_policy: 'PROXY',
    rules: [],
    rule_providers: [],
    node_filter: { countries: [], regions: [], long_lived: null },
    long_lived: { min_uptime: '2h', min_success_rate: 0.9 },
    session: { ttl: '10m' },
    ...overrides,
  }
}

export function profileResourceFixture(overrides: Partial<ProfileResource> = {}): ProfileResource {
  return { profile_scope: 'device', device_id: 'laptop', revision: 1, registry_revision: 2, need_reload: false, profile: profileFixture(), provider_status: { degraded: false }, ...overrides }
}

export function mutationFixture<T>(resource?: T, overrides: Partial<MutationResponse<T>> = {}): MutationResponse<T> {
  return { revision: 1, registry_revision: 2, need_reload: false, resource, ...overrides }
}

export function deviceFixtures(): DeviceSummary[] {
  return [{ device_id: 'laptop', display_name: 'Laptop', revision: 1, profile_mode: 'shared', effective_enabled: true, effective_state: 'PROFILE', mapping_count: 0 }]
}

export function mappingFixture(overrides: Partial<IPMapping> = {}): IPMapping {
  return { mapping_id: 'map-1', cidr: '192.168.1.10/32', device_id: 'laptop', priority: 100, enabled: true, revision: 1, ...overrides }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test -- src/api/client.test.ts src/api/localServer.test.ts
```

Expected: missing typed transport and domain API.

- [ ] **Step 3: Define DTOs and one exported transport**

`localServer.ts` locks the JSON contract with these fields:

```ts
export interface RuleProvider {
  url: string
  policy: 'DIRECT' | 'PROXY'
  behavior: 'domain' | 'ipcidr' | 'classical'
  interval: string
}

export interface ForwardingProfile {
  schema_version: 1
  enabled: boolean
  default_strategy: 'auto' | 'stable' | 'session'
  use_default_rules: boolean
  final_policy: 'DIRECT' | 'PROXY'
  rules: string[]
  rule_providers: RuleProvider[]
  node_filter: { countries: string[]; regions: string[]; long_lived: boolean | null }
  long_lived: { min_uptime: string; min_success_rate: number }
  session: { ttl: string }
}

export interface ProviderStatus {
  degraded: boolean
  last_error?: string
  updated_at?: string
}

export interface ProfileResource {
  profile_scope: 'shared' | 'device'
  device_id?: string
  revision: number
  registry_revision: number
  need_reload: boolean
  profile: ForwardingProfile
  provider_status: ProviderStatus
}

export interface LocalServerStatus {
  enabled: boolean
  listen: string
  dispatcher_ready: boolean
  registry_revision: number
  credential_generation: number
  profile_count: number
  mapping_count: number
  provider_degraded_count: number
  peer_address_mode: 'tcp_peer'
  source_ip_warning: string
}

export interface LocalServerConfig {
  enabled: boolean
  listen: string
  auth_username: string
  password_set: boolean
  shared_revision: number
  credential_generation: number
}

export interface DeviceSummary {
  device_id: string
  display_name: string
  revision: number
  profile_mode: 'shared' | 'independent'
  profile_revision?: number
  effective_enabled: boolean
  effective_state: 'DIRECT' | 'PROFILE'
  identity_source?: 'explicit' | 'ip_mapping' | 'shared_fallback'
  last_seen_ip?: string
  last_seen_at?: string
  mapping_count: number
}

export interface DeviceResource extends DeviceSummary {
  profile?: ProfileResource
  mappings: IPMapping[]
}

export interface IPMapping {
  mapping_id: string
  cidr: string
  device_id: string
  priority: number
  enabled: boolean
  revision: number
}

export interface MutationResponse<T> {
  revision: number
  registry_revision: number
  need_reload: boolean
  profile_scope?: 'shared' | 'device'
  resource?: T
  message?: string
}
```

Refactor `request` to exported `apiRequest<T>` and preserve structured error bodies:

```ts
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly payload: Record<string, unknown> = {},
  ) {
    super(message)
    this.name = 'ApiError'
  }
}
```

`src/api/localServer.ts` imports `apiRequest`; it does not duplicate transport logic. Encode normalized device IDs with `encodeURIComponent` and use mapping IDs in paths.

Export this complete API surface so later components do not invent ad-hoc fetches:

```ts
fetchLocalServerStatus(): Promise<LocalServerStatus>
fetchLocalServerConfig(): Promise<LocalServerConfig>
updateLocalServerConfig(update: { enabled?: boolean; listen?: string; auth_username?: string; auth_password?: string }): Promise<MutationResponse<LocalServerConfig>>
fetchSharedProfile(): Promise<ProfileResource>
putSharedProfile(profile: ForwardingProfile, expectedRevision: number): Promise<MutationResponse<ProfileResource>>
fetchDevices(): Promise<{ devices: DeviceSummary[] }>
fetchDevice(deviceId: string): Promise<DeviceResource>
putDevice(deviceId: string, displayName: string, expectedRevision: number): Promise<MutationResponse<DeviceResource>>
putDeviceProfile(deviceId: string, profile: ForwardingProfile, expectedRevision: number): Promise<MutationResponse<ProfileResource>>
setDeviceProfileEnabled(deviceId: string, enabled: boolean, expectedRevision: number): Promise<MutationResponse<ProfileResource>>
copySharedProfile(deviceId: string): Promise<MutationResponse<ProfileResource>>
deleteDeviceProfile(deviceId: string, expectedRevision: number): Promise<MutationResponse<ProfileResource>>
fetchIPMappings(): Promise<{ mappings: IPMapping[] }>
createIPMapping(input: Omit<IPMapping, 'mapping_id' | 'revision'>): Promise<MutationResponse<IPMapping>>
updateIPMapping(mappingId: string, input: Omit<IPMapping, 'mapping_id' | 'revision'>, expectedRevision: number): Promise<MutationResponse<IPMapping>>
deleteIPMapping(mappingId: string, expectedRevision: number): Promise<MutationResponse<IPMapping>>
```

Creation sends `If-None-Match: *`; updates/deletes send `If-Match: "<revision>"`. Request bodies may also include the same expected revision, but the client must not send contradictory values.

- [ ] **Step 4: Update login from auth-mode discovery**

Extend `AuthResponse`:

```ts
export interface AuthResponse {
  message?: string
  token?: string
  no_password?: boolean
  auth_mode?: 'legacy_password' | 'canonical_pair'
  username_required?: boolean
}
```

Change login to:

```ts
export async function login(username: string, password: string): Promise<AuthResponse> {
  const response = await fetch('/api/auth', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
    credentials: 'include',
  })
  const payload = await response.json() as AuthResponse & { error?: string }
  if (!response.ok) {
    throw new ApiError(payload.error ?? '登录失败', response.status, payload as unknown as Record<string, unknown>)
  }
  if (payload.token) setToken(payload.token)
  return payload
}
```

`LoginPage` shows username only when `auth_mode === 'canonical_pair'`. Tests verify canonical pair success, legacy password-only rendering, and 401 error display.

`App` must retain the `checkAuth()` response instead of discarding it, pass `authMode={authInfo.auth_mode ?? 'legacy_password'}` to `LoginPage`, and clear that state on unauthorized/logout. `LoginPage` must not make a second discovery request. Add an App test proving canonical discovery renders the username field and legacy discovery does not.

- [ ] **Step 5: Add write-only Local Server settings**

`LocalServerSettingsCard` GETs `/api/local-server/config`, always initializes the password field to `''`, omits `auth_password` when the field remains blank, and sends a non-empty password only when the operator explicitly enters one. Tests verify no password refill, blank save preserves the secret, non-empty save rotates it, and Local Server mode explains that legacy credential fields are derived/unavailable.

Make `SettingsData.listener_password` and `SettingsData.management_password` optional and hide/disable their legacy controls when `local_server_enabled` is true, because the sanitized Local Server `/api/settings` response omits those keys entirely.

- [ ] **Step 6: Run checks and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test -- src/api src/components/LoginPage.test.tsx src/components/local-server
npm run lint
npm run build
git diff --check
```

Commit source files only; generated embedded assets are committed in Task 14.

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/frontend/src/types service/base/frontend/src/api service/base/frontend/src/test/localServerFixtures.ts service/base/frontend/src/components/LoginPage.tsx service/base/frontend/src/components/LoginPage.test.tsx service/base/frontend/src/components/local-server service/base/frontend/src/components/SettingsPanel.tsx service/base/frontend/src/App.tsx service/base/frontend/src/App.test.tsx
git commit -m "feat(frontend): add local server api and canonical login"
```

---

### Task 13: Extract a reusable Profile form and revision-aware editor

**Files:**
- Create: `service/base/frontend/src/components/profiles/ProfileForm.tsx`
- Create: `service/base/frontend/src/components/profiles/ProfileForm.test.tsx`
- Create: `service/base/frontend/src/components/profiles/profileAdapters.ts`
- Create: `service/base/frontend/src/components/profiles/profileAdapters.test.ts`
- Create: `service/base/frontend/src/components/profiles/ProfileEditor.tsx`
- Create: `service/base/frontend/src/components/profiles/ProfileEditor.test.tsx`
- Create: `service/base/frontend/src/hooks/useUnsavedChangesGuard.ts`
- Create: `service/base/frontend/src/hooks/useUnsavedChangesGuard.test.tsx`
- Modify: `service/base/frontend/src/components/RoutingPanel.tsx`

- [ ] **Step 1: Write RED adapter and form tests**

```ts
it('round-trips the legacy flattened routing payload', () => {
  const legacy = legacyRoutingFixture()
  const nested = routingConfigToProfile(legacy)
  expect(profileToRoutingConfig(nested, legacy)).toEqual(legacy)
})

it('edits all Profile-owned fields without server settings', async () => {
  const onChange = vi.fn()
  render(<ProfileForm value={profileFixture()} onChange={onChange} />)
  await userEvent.click(screen.getByRole('checkbox', { name: '启用此配置' }))
  expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ enabled: false }))
  expect(screen.queryByText('管理端口')).not.toBeInTheDocument()
})

function legacyRoutingFixture(): RoutingConfig {
  return {
    enabled: true,
    listen: '127.0.0.1:22324',
    default_strategy: 'stable',
    use_default_rules: true,
    final_policy: 'PROXY',
    rules: [],
    rule_providers: [],
    node_filter: { countries: [], regions: [], long_lived: null },
    long_lived_min_uptime: '2h',
    long_lived_min_success_rate: 0.9,
    session_ttl: '10m',
  }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test -- src/components/profiles
```

Expected: ProfileForm, adapters, and editor are missing.

- [ ] **Step 3: Extract ProfileForm without data fetching**

`ProfileForm` is a controlled component:

```ts
interface ProfileFormProps {
  value: ForwardingProfile
  onChange(value: ForwardingProfile): void
  disabled?: boolean
}
```

Move enabled, strategy, final policy, default rules, rules text, providers, node filter, long-lived thresholds, and session TTL controls from `RoutingPanel`. It must not import API functions.

`RoutingPanel` becomes a shared/legacy adapter around `ProfileForm`; legacy mode continues to use `/api/routing/config` and the flattened DTO. `profileToRoutingConfig(profile, previous)` replaces only Profile-owned fields and preserves `previous.listen` and future topology-only keys.

- [ ] **Step 4: Add revision-aware ProfileEditor and dirty guard**

```ts
interface ProfileEditorProps {
  scope: { kind: 'shared' } | { kind: 'device'; deviceId: string }
  onClose(): void
}
```

The editor loads the explicit scope URL, keeps the server revision, sends the matching revision on save, and displays a 409 conflict without overwriting local input. `useUnsavedChangesGuard` covers `beforeunload` and cross-component navigation with one cancelable event:

```ts
export const BEFORE_NAVIGATION_EVENT = 'easyproxy:before-navigation'

export function requestAppNavigation(): boolean {
  return window.dispatchEvent(new Event(BEFORE_NAVIGATION_EVENT, { cancelable: true }))
}
```

When dirty, the hook listens for that event, calls the injected/default `window.confirm`, and invokes `event.preventDefault()` when the operator rejects navigation. It also returns `confirmNavigation()` for device/editor-local actions. Task 14 makes App tab/hash changes call `requestAppNavigation()` before changing state. Tests prove a rejected confirmation leaves both hash and selected device unchanged.

- [ ] **Step 5: Run checks and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test -- src/components/profiles src/hooks
npm run lint
npm run build
git diff --check
```

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add service/base/frontend/src/components/profiles service/base/frontend/src/hooks service/base/frontend/src/components/RoutingPanel.tsx
git commit -m "refactor(frontend): extract forwarding profile editor"
```

---

### Task 14: Add the Devices console and rebuild verified embedded assets

**Files:**
- Create: `service/base/frontend/src/components/DevicesPanel.tsx`
- Create: `service/base/frontend/src/components/DevicesPanel.test.tsx`
- Create: `service/base/frontend/src/components/devices/SharedProfileCard.tsx`
- Create: `service/base/frontend/src/components/devices/SharedProfileCard.test.tsx`
- Create: `service/base/frontend/src/components/devices/DeviceTable.tsx`
- Create: `service/base/frontend/src/components/devices/IPMappingsPanel.tsx`
- Create: `service/base/frontend/src/components/devices/IPMappingsPanel.test.tsx`
- Modify: `service/base/frontend/src/App.tsx`
- Modify: `service/base/internal/monitor/assets/index.html`
- Replace generated hashes under: `service/base/internal/monitor/assets/assets/`
- Create: `service/base/internal/monitor/server_assets_test.go`
- Modify: `service/base/Dockerfile`
- Modify: `deploy/service/base/Dockerfile`
- Modify: `.github/workflows/publish-ghcr-images.yml`

- [ ] **Step 1: Write RED UI tests for the effective-state matrix and CRUD**

```tsx
it.each([
  ['shared', false, false, 'DIRECT'],
  ['shared', true, true, '共享配置'],
  ['independent', true, false, '独立配置'],
  ['independent', false, true, 'DIRECT'],
] as const)('renders %s mode with effective state', async (mode, independentEnabled, sharedEnabled, label) => {
  mockDeviceAPIs({ mode, independentEnabled, sharedEnabled })
  render(<DevicesPanel />)
  expect(await screen.findByText(label)).toBeInTheDocument()
})

it('creates blank Profile with revision zero and explains copy-shared is one-time', async () => {
  const calls = mockDeviceAPIs({ devices: deviceFixtures() })
  render(<DevicesPanel />)
  await userEvent.click(await screen.findByRole('button', { name: '创建独立配置' }))
  await userEvent.click(screen.getByRole('button', { name: '使用默认配置' }))
  expect(calls.putDeviceProfile).toHaveBeenCalledWith('laptop', expect.anything(), 0)
  expect(screen.getByText(/复制当前值，后续不联动/)).toBeInTheDocument()
})

it('warns that IP mapping is best-effort behind Docker or NAT', async () => {
  mockDeviceAPIs({ devices: deviceFixtures() })
  render(<DevicesPanel />)
  expect(await screen.findByText(/IP 映射仅作为回退/)).toBeInTheDocument()
})

const apiMocks = vi.hoisted(() => ({
  fetchSharedProfile: vi.fn(),
  fetchDevices: vi.fn(),
  fetchIPMappings: vi.fn(),
  putDeviceProfile: vi.fn(),
  copySharedProfile: vi.fn(),
  deleteDeviceProfile: vi.fn(),
  setDeviceProfileEnabled: vi.fn(),
  createIPMapping: vi.fn(),
  updateIPMapping: vi.fn(),
  deleteIPMapping: vi.fn(),
}))

vi.mock('../api/localServer', () => apiMocks)

function mockDeviceAPIs(state: {
  mode?: 'shared' | 'independent'
  independentEnabled?: boolean
  sharedEnabled?: boolean
  devices?: DeviceSummary[]
} = {}) {
  const sharedEnabled = state.sharedEnabled ?? true
  const mode = state.mode ?? 'shared'
  const independentEnabled = state.independentEnabled ?? true
  const devices = state.devices ?? [{
    ...deviceFixtures()[0],
    profile_mode: mode,
    profile_revision: mode === 'independent' ? 1 : undefined,
    effective_enabled: mode === 'independent' ? independentEnabled : sharedEnabled,
    effective_state: (mode === 'independent' ? independentEnabled : sharedEnabled) ? 'PROFILE' : 'DIRECT',
  }]
  apiMocks.fetchSharedProfile.mockResolvedValue(profileResourceFixture({ profile_scope: 'shared', device_id: undefined, profile: profileFixture({ enabled: sharedEnabled }) }))
  apiMocks.fetchDevices.mockResolvedValue({ devices })
  apiMocks.fetchIPMappings.mockResolvedValue({ mappings: [] })
  apiMocks.putDeviceProfile.mockResolvedValue(mutationFixture(profileResourceFixture()))
  return apiMocks
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test -- src/components/DevicesPanel.test.tsx src/components/devices
```

Expected: device components are missing.

- [ ] **Step 3: Implement the device page as explicit resources**

`SharedProfileCard` shows shared enabled, strategy, final policy, rule count, and revision. `DeviceTable` shows display name, canonical device ID, identity source, last seen, Profile mode, effective state, revision, and actions. `IPMappingsPanel` uses mapping IDs and shows the source-IP reliability warning.

`DevicesPanel` owns list refresh and modal/editor selection but delegates Profile editing to `ProfileEditor`. Deleting an independent Profile updates the row to shared mode without deleting the device or mappings.

- [ ] **Step 4: Register the `devices` tab and protect dirty navigation**

Update:

```ts
type TabId = 'monitor' | 'manage' | 'routing' | 'devices' | 'debug' | 'settings'
```

Add the sidebar item “设备策略”, hash `#devices`, and `renderContent` branch. `handleTabClick`, browser hash changes, editor close, and device-row switches call `requestAppNavigation()` before mutating state/hash; a canceled event restores the previous hash. Mobile rows use stacked summaries rather than horizontal overflow.

- [ ] **Step 5: Add an embedded asset regression test before rebuilding**

`server_assets_test.go` must GET `/`, parse every `/assets/...` reference from embedded index, verify each returns 200, verify SPA fallback returns index, and verify unknown `/api/not-real` stays 404.

Run it before rebuilding:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/monitor -run TestEmbeddedFrontendAssets
```

Expected: this is a characterization/regression test and may already pass against the existing bundle. It is not the TDD RED for the Devices UI; source-level component tests above provide that RED. Bundle/source synchronization is enforced after rebuild with a clean-diff check.

- [ ] **Step 6: Build and commit the final UI bundle**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test
npm run lint
npm run build
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
go test -count=1 ./internal/monitor -run TestEmbeddedFrontendAssets
```

Change both Dockerfiles to preserve their explicit output directories while adding TypeScript compilation:

```dockerfile
# service/base/Dockerfile
RUN npm run build -- --outDir /frontend-dist

# deploy/service/base/Dockerfile
RUN npm run build -- --outDir /tmp/frontend-dist
```

Add frontend `npm ci/test/lint/build` and, after the standard tracked-asset build, `git diff --exit-code -- service/base/internal/monitor/assets` to the GHCR workflow preflight.

- [ ] **Step 7: Commit UI and generated assets together**

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git diff --check
git add service/base/frontend/src/components/DevicesPanel.tsx service/base/frontend/src/components/DevicesPanel.test.tsx service/base/frontend/src/components/devices service/base/frontend/src/App.tsx service/base/internal/monitor/assets service/base/internal/monitor/server_assets_test.go service/base/Dockerfile deploy/service/base/Dockerfile .github/workflows/publish-ghcr-images.yml
git commit -m "feat(frontend): add device profile console"
```

---

### Task 15: Render and distribute canonical Local Server configuration

**Files:**
- Modify: `config.example.yaml`
- Modify: `deploy/service/base/config.template.yaml`
- Modify: `service/base/config.example.yaml`
- Modify: `scripts/render-derived-configs.py`
- Modify: `scripts/sync-github-deployment-settings.py`
- Modify: `scripts/deploy-subproject.ps1`
- Modify: `tests/test_script_smoke.py`
- Modify comments only: `deploy/service/base/docker-compose.yaml`
- Modify comments only: `service/base/docker-compose.yml`

- [ ] **Step 1: Write RED renderer tests**

Add tests with temporary root configs rather than rendering an unresolved placeholder directly:

```python
def root_config_fixture(self):
    return yaml.safe_load((REPO_ROOT / "config.example.yaml").read_text(encoding="utf-8")) or {}

def run_renderer(self, root):
    with tempfile.TemporaryDirectory() as temp_dir:
        root_path = Path(temp_dir) / "config.yaml"
        output_path = Path(temp_dir) / "service-config.yaml"
        root_path.write_text(yaml.safe_dump(root, sort_keys=False, allow_unicode=True), encoding="utf-8")
        result = subprocess.run(
            [sys.executable, str(REPO_ROOT / "scripts" / "render-derived-configs.py"), "--root-config", str(root_path), "--service-output", str(output_path)],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=120,
        )
        rendered = yaml.safe_load(output_path.read_text(encoding="utf-8")) if output_path.exists() else None
        return result, rendered

def render_service_runtime(self, root):
    result, rendered = self.run_renderer(root)
    self.assertEqual(result.returncode, 0, msg=result.stderr or result.stdout)
    return rendered or {}

def test_root_local_server_render_derives_canonical_credentials(self):
    root = yaml.safe_load((REPO_ROOT / "config.example.yaml").read_text(encoding="utf-8"))
    runtime = root["serviceBase"]["runtime"]
    runtime["mode"] = "pool"
    runtime["listener"]["protocol"] = "mixed"
    runtime["local_server"] = {
        "enabled": True,
        "listen": "",
        "auth": {"username": "easyproxy", "password": "test-secret"},
    }
    rendered = self.render_service_runtime(root)
    self.assertTrue(rendered["local_server"]["enabled"])
    self.assertEqual(rendered["listener"]["username"], "easyproxy")
    self.assertEqual(rendered["listener"]["password"], "test-secret")
    self.assertEqual(rendered["management"]["password"], "test-secret")

def test_root_local_server_render_rejects_bypass_topology(self):
    root = self.root_config_fixture()
    root["serviceBase"]["runtime"]["mode"] = "hybrid"
    result, _ = self.run_renderer(root)
    self.assertNotEqual(result.returncode, 0)
    self.assertIn("mode: pool", result.stderr)
```

Also test empty/placeholder canonical password, conflicting listens, non-mixed listener, and disabled Local Server preserving legacy credentials.

- [ ] **Step 2: Run script tests and verify RED**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
python -m unittest tests.test_script_smoke.ScriptSmokeTests.test_root_local_server_render_derives_canonical_credentials tests.test_script_smoke.ScriptSmokeTests.test_root_local_server_render_rejects_bypass_topology
```

Expected: Local Server fields are not rendered or validated.

- [ ] **Step 3: Add operator-facing Local Server examples**

Keep tracked `config.example.yaml` and `deploy/service/base/config.template.yaml` renderable and legacy-compatible by default. Add the complete canonical block but leave it disabled until the operator replaces the placeholder:

```yaml
local_server:
  enabled: false
  listen: ""
  auth:
    username: easyproxy
    password: change_me_to_a_strong_shared_password
```

Keep shared `routing.enabled: false` in tracked examples. `docs/local-server.md` and quickstart provide the explicit opt-in snippet (`mode: pool`, `listener.protocol: mixed`, `local_server.enabled: true`, `routing.enabled: true`, real shared secret). GitHub-generated deployment settings may enable Local Server because that script creates a real random secret. The Go default remains legacy-compatible when `local_server` is absent or disabled.

Keep `deploy/service/base/config.template.yaml` and the direct service example backward-compatible with `local_server.enabled: false`, but document the canonical block and Local Server constraints beside it.

- [ ] **Step 4: Normalize the rendered runtime and GitHub-generated settings**

Import `re`, then after `deep_merge`, call:

```python
def normalize_local_server_runtime(config: dict[str, Any]) -> None:
    local = config.get("local_server") or {}
    if not local.get("enabled"):
        return
    if config.get("mode") != "pool":
        raise ValueError("local_server requires mode: pool")
    if config.get("extra_listeners"):
        raise ValueError("local_server does not allow extra_listeners")
    listener = config.setdefault("listener", {})
    if listener.get("protocol") != "mixed":
        raise ValueError("local_server requires listener.protocol: mixed")
    auth = local.get("auth") or {}
    username = str(auth.get("username") or "").strip()
    password = str(auth.get("password") or "")
    if not re.fullmatch(r"[A-Za-z0-9._-]{1,64}", username):
        raise ValueError("local_server canonical username is invalid")
    if not password or "change_me" in password or "\x00" in password or len(password.encode("utf-8")) > 256:
        raise ValueError("local_server canonical credential is unresolved")
    routing_listen = str((config.get("routing") or {}).get("listen") or "").strip()
    local_listen = str(local.get("listen") or "").strip()
    if local_listen and routing_listen and local_listen != routing_listen:
        raise ValueError("local_server.listen conflicts with routing.listen")
    listener["username"] = username
    listener["password"] = password
    config.setdefault("management", {})["password"] = password
    local.setdefault("shared_revision", 1)
    local.setdefault("credential_generation", 2)
```

Update `sync-github-deployment-settings.py` to generate one shared password, set Local Server enabled, mode pool, mixed listener, and canonical auth. Keep the old management environment value as a compatibility alias sourced from the same secret.

- [ ] **Step 5: Update deploy placeholder checks and compose comments**

`deploy-subproject.ps1` rejects an enabled Local Server with empty/example canonical secret before Docker is invoked. Compose comments label `22323` as the Local Server mixed entry, `29888` as the shared Web Console, and the multi-port range as legacy-only.

- [ ] **Step 6: Run renderer/smoke tests and commit**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
python -m unittest tests.test_script_smoke
python scripts/validate-release-contract.py
git diff --check
```

Expected: root rendering, deployment dispatch, and release-contract tests pass.

Commit:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add config.example.yaml deploy/service/base/config.template.yaml service/base/config.example.yaml scripts/render-derived-configs.py scripts/sync-github-deployment-settings.py scripts/deploy-subproject.ps1 tests/test_script_smoke.py deploy/service/base/docker-compose.yaml service/base/docker-compose.yml
git commit -m "feat(config): render canonical local server settings"
```

---

### Task 16: Add a durable isolated Docker E2E for device Profiles

**Files:**
- Create: `deploy/service/base/scripts/local-server-e2e-fixture.py`
- Create: `deploy/service/base/scripts/validate-local-server-device-profiles.ps1`
- Modify: `.github/workflows/publish-ghcr-images.yml`

- [ ] **Step 1: Create a deterministic counted upstream fixture**

The Python helper must use only the standard library and expose this stable CLI:

```text
local-server-e2e-fixture.py origin --listen 0.0.0.0:8080 --name direct
local-server-e2e-fixture.py proxy --listen 0.0.0.0:3128 --counter-listen 0.0.0.0:8081
local-server-e2e-fixture.py --self-test
```

It must provide:

```text
GET  /counter                 -> JSON target hit counts
POST /counter/reset           -> clear counts
HTTP CONNECT target           -> increment target and tunnel
HTTP absolute-form target     -> increment target and forward
```

It must bind inside the disposable Docker network, log structured JSON lines, and exit cleanly on SIGTERM. Add a `--self-test` mode that starts loopback origin/counter instances, sends one CONNECT and one absolute-form request, asserts both counters, then exits 0.

- [ ] **Step 2: Run fixture self-test**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
python deploy/service/base/scripts/local-server-e2e-fixture.py --self-test
```

Expected: exit 0 with a JSON summary showing one CONNECT and one HTTP hit.

- [ ] **Step 3: Write the isolated PowerShell validation runner**

The runner accepts `-Image`, `-ValidationId`, `-KeepArtifacts`, `-KeepRuntime`, and `-CleanupOnly`. It creates unique names and ports, records the legacy container before state (including an explicit `{exists:false}` record), writes a fresh Local Server config, launches counted proxy/origins/EasyProxy/client containers, and cleans them in `finally` unless `-KeepRuntime` was requested. `-CleanupOnly -ValidationId <id>` removes a previously retained topology using its evidence metadata.

Keep the runner self-contained; do not import private functions from the older smoke script. Define `Get-FreeTcpPort`, `Invoke-JsonApi`, `Invoke-ClientContainerCurl`, `Get-LegacyContainerInvariant`, `Assert-LegacyContainerInvariant`, and `Remove-DisposableTopology` locally. Their concrete behavior is fixed: allocate ports with a temporary loopback listener; use `Invoke-RestMethod` for JSON; run curl in the disposable client container for HTTP/CONNECT/SOCKS5; compare only container ID/image/state/exit-code/FinishedAt; and remove only containers/networks/volumes carrying the matching validation-ID label.

The assertions are fixed:

```text
1. startup /api/local-server/status is ready and password is redacted
2. canonical Web login succeeds; wrong username/password fail
3. zero-node initial startup keeps dispatcher ready; DIRECT origin counter increments and PROXY returns 502
4. shared off + no independent Profile -> DIRECT origin +1 and upstream proxy counter 0
5. shared off + device B independent on -> upstream proxy counter +1
6. shared on + device A independent off -> DIRECT origin +1 and proxy counter unchanged
7. changing shared does not change device B revision/content/behavior
8. deleting B Profile returns B to shared
9. explicit dev= identity overrides a conflicting IP mapping
10. HTTP absolute-form, CONNECT, and SOCKS5 use the same device Profile
11. stale revision returns 409 and current_revision
12. rotate canonical credential: old Web session fails, new Web/proxy credential succeeds, dispatcher socket identity/listen remains unchanged
13. embedded `/` and every referenced asset return 200
14. legacy container identity/state fields are unchanged after cleanup
```

Use an upstream counter as the routing source of truth; do not infer route choice only from EasyProxy logs.

Start the isolated EasyProxy instance with zero nodes for assertion 3. Then create the counted upstream proxy node through the existing config-node API and trigger the normal reload before assertions 4-14; do not weaken the zero-node test by preloading a dormant node.

- [ ] **Step 4: Run the new validation locally**

Build a unique image and run on unused ports:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
$tag = "easyproxy/easy-proxy-monorepo-service:local-server-$(Get-Date -Format yyyyMMddHHmmss)"
docker build -f deploy/service/base/Dockerfile -t $tag .
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/service/base/scripts/validate-local-server-device-profiles.ps1 -Image $tag
```

Expected: every named assertion passes; cleanup leaves no test containers/network/ports; legacy container before/after JSON matches.

- [ ] **Step 5: Add the validation to GHCR runtime E2E**

Run the new script after the existing source/runtime validation, upload its JSON evidence directory, and keep the old MiSub/source validation unchanged. The Ubuntu workflow step must use `shell: pwsh` and invoke `./deploy/service/base/scripts/validate-local-server-device-profiles.ps1` directly; do not call Windows-only `powershell.exe` in hosted CI.

- [ ] **Step 6: Commit the durable E2E**

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git diff --check
git add deploy/service/base/scripts/local-server-e2e-fixture.py deploy/service/base/scripts/validate-local-server-device-profiles.ps1 .github/workflows/publish-ghcr-images.yml
git commit -m "test(deploy): validate local server device profiles"
```

---

### Task 17: Document, review, and verify the complete feature

**Files:**
- Create: `docs/local-server.md`
- Modify: `README.md`
- Modify: `docs/quickstart.md`
- Modify: `deploy/service/base/README.md`
- Modify: `service/base/README.md`
- Modify: `docs/smart-routing.md`
- Modify: `docs/service-base-config-distribution.md`
- Modify: `docs/release-checklist.md`
- Modify: `docs/smart-routing-status.md` only to add a pointer to the new Local Server document; do not rewrite historical evidence.

- [ ] **Step 1: Write the operator documentation from the implemented behavior**

`docs/local-server.md` must include:

- topology and no-standalone-client statement;
- shared/independent switch matrix;
- canonical credential and username grammar;
- explicit ID > IP mapping > shared resolution;
- Docker/NAT source-IP caveat;
- API paths and revision examples;
- root config and direct service YAML;
- HTTP, CONNECT, and SOCKS client examples;
- legacy mode and multi-port incompatibility;
- credential rotation/session behavior;
- trusted-LAN firewall rules blocking WAN/guest VLAN access to 22323/29888 and VPN/TLS guidance for untrusted networks;
- troubleshooting for 401, 407, 409, 422, 502, idle pool, and source-IP collapse.

- [ ] **Step 2: Run the full backend, frontend, and script matrix**

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base
Get-ChildItem internal -Recurse -Filter *.go | ForEach-Object { gofmt -w $_.FullName }
$unformatted = Get-ChildItem internal -Recurse -Filter *.go | ForEach-Object { gofmt -l $_.FullName }
if ($unformatted) { throw "unformatted Go files: $($unformatted -join ', ')" }
go test -count=1 -timeout=300s ./...
go vet ./...

Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy\service\base\frontend
npm run test
npm run lint
npm run build

Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
python -m unittest discover -s tests -p "test_*.py"
python scripts/validate-release-contract.py
git diff --check
```

Expected: all commands exit 0. If `npm run build` changes embedded assets, stage those changes with the UI source that produced them rather than leaving a dirty final tree.

- [ ] **Step 3: Run the isolated Docker E2E again from the final source tree**

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
$tag = "easyproxy/easy-proxy-monorepo-service:local-server-final-$(Get-Date -Format yyyyMMddHHmmss)"
$validationId = "local-server-final-$(Get-Date -Format yyyyMMddHHmmss)"
docker build -f deploy/service/base/Dockerfile -t $tag .
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/service/base/scripts/validate-local-server-device-profiles.ps1 -Image $tag -ValidationId $validationId -KeepArtifacts -KeepRuntime
```

Record the immutable image ID and evidence directory in the final report. Read the retained management URL and topology metadata from the evidence JSON for Step 4.

- [ ] **Step 4: Verify the real Web Console in a browser**

Against the isolated container, verify desktop `1440x900` and mobile `390x844`:

- canonical login;
- devices tab and shared card;
- device create/copy/edit/disable/delete;
- IP mapping warning and CRUD;
- ProfileEditor dirty-navigation warning;
- 409 conflict display without overwrite;
- password field never refilled;
- no clipped, overlapping, or horizontally unusable controls;
- `/` and hashed assets remain available after refresh/hash navigation.

Use the `browser-use`/in-app browser skill against the retained isolated management URL. Capture screenshots into the isolated evidence directory, not the tracked source tree. After browser verification, run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/service/base/scripts/validate-local-server-device-profiles.ps1 -ValidationId $validationId -CleanupOnly
```

Verify the retained containers, network, volumes, and host ports are gone before continuing.

- [ ] **Step 5: Request two-stage code review and close findings**

Run one review focused on spec compliance and one focused on regressions/concurrency/security. Fix only validated findings, rerun the smallest affected tests, then rerun the full matrix if any production code changed.

- [ ] **Step 6: Commit documentation and final verified fixes**

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git add docs/local-server.md README.md docs/quickstart.md deploy/service/base/README.md service/base/README.md docs/smart-routing.md docs/service-base-config-distribution.md docs/release-checklist.md docs/smart-routing-status.md
git commit -m "docs: document local server device profiles"
```

If code-review fixes exist, commit them separately before the documentation commit with a scope-specific message.

- [ ] **Step 7: Confirm final repository invariants**

Run:

```powershell
Set-Location C:\Users\Public\nas_home\AI\GameEditor\EasyProxy
git status --short --branch
git log --oneline -20
New-Item -ItemType Directory -Force .tmp | Out-Null
$legacyId = docker ps -a --filter 'name=^/easy-proxy$' --format '{{.ID}}'
if ($legacyId) {
  docker inspect easy-proxy --format '{{json .}}' | Out-File -Encoding utf8 .tmp\legacy-final-inspect.json
} else {
  '{"exists":false}' | Out-File -Encoding utf8 .tmp\legacy-final-inspect.json
}
# Compare with the E2E before-state JSON, then remove this local inspection artifact.
Remove-Item -LiteralPath .tmp\legacy-final-inspect.json -Force -ErrorAction SilentlyContinue
```

Expected working tree output contains only the two pre-existing untracked tar files. Do not stage `.tmp/legacy-final-inspect.json`; remove this newly created inspection artifact after comparing it with E2E evidence. The legacy container must have the same ID, image, state, exit code, and `FinishedAt` captured before testing.

---

## Final acceptance checklist

- [ ] shared off + no independent Profile is DIRECT.
- [ ] shared off + independent on still applies independent policy.
- [ ] shared on + independent off is DIRECT.
- [ ] shared edits never mutate independent Profile JSON or revision.
- [ ] deleting independent Profile returns the device to shared.
- [ ] explicit lower-cased `device_id` beats IP/CIDR mapping.
- [ ] IP mapping is clearly marked best-effort under Docker/NAT.
- [ ] one canonical credential protects Web, management API, HTTP proxy, CONNECT, and SOCKS5.
- [ ] credential rotation invalidates old sessions without rebinding the Local Server socket.
- [ ] device Profile updates use CAS revisions and return 409 on stale writes.
- [ ] Profile provider callbacks cannot mutate retired Registry snapshots.
- [ ] sticky/session/long-lived behavior is namespaced by Profile ID and revision.
- [ ] zero-node initial startup keeps DIRECT available and PROXY returns a clear error.
- [ ] legacy behavior is unchanged when Local Server is disabled.
- [ ] MiSub, subscriptions, connectors, node sync, and health remain global and unchanged.
- [ ] Go, frontend, scripts, CI preflight, embedded assets, isolated Docker E2E, and browser checks pass.
- [ ] legacy container and the two untracked tar files remain unchanged.
