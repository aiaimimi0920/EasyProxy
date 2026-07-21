package profile

import (
	"net/netip"
	"testing"
	"time"
)

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

func TestRegistryCloneReplacingBumpsRevisionAndCopies(t *testing.T) {
	shared := compileProfileForTest(t, "shared", KindShared, 1, Definition{SchemaVersion: 1, Enabled: true, FinalPolicy: "PROXY"})
	registry := NewRegistry(shared, nil, nil, CredentialSnapshot{Username: "easyproxy", Password: "secret", Generation: 1}, 7)

	mappings := []IPMapping{{MappingID: "map-1", Prefix: netip.MustParsePrefix("10.0.0.0/24"), DeviceID: "desktop", Priority: 5}}
	next := registry.CloneReplacingMappings(mappings)

	if got, want := next.Revision(), uint64(8); got != want {
		t.Fatalf("clone revision = %d, want %d", got, want)
	}
	if registry.MappingCount() != 0 {
		t.Fatalf("original registry mapping count = %d, want 0", registry.MappingCount())
	}

	mappings[0].DeviceID = "changed"
	resolution := next.Resolve(RequestIdentity{PeerIP: netip.MustParseAddr("10.0.0.8")})
	if resolution.DeviceID != "desktop" {
		t.Fatalf("clone retained caller-owned slice mutation: %#v", resolution)
	}
}

func TestRegistryMappingPrecedenceUsesLongestPrefixThenPriority(t *testing.T) {
	shared := compileProfileForTest(t, "shared", KindShared, 1, Definition{SchemaVersion: 1, Enabled: true, FinalPolicy: "PROXY"})
	registry := NewRegistry(shared, nil, []IPMapping{
		{MappingID: "broad", Prefix: netip.MustParsePrefix("192.0.2.0/24"), DeviceID: "broad", Priority: 100},
		{MappingID: "specific-low", Prefix: netip.MustParsePrefix("192.0.2.10/32"), DeviceID: "specific-low", Priority: 1},
		{MappingID: "specific-high", Prefix: netip.MustParsePrefix("192.0.2.10/32"), DeviceID: "specific-high", Priority: 9},
	}, CredentialSnapshot{}, 1)

	peer := netip.MustParseAddr("192.0.2.10")
	mapped := registry.Resolve(RequestIdentity{PeerIP: peer})
	if mapped.DeviceID != "specific-high" || mapped.Source != IdentityIPMapping {
		t.Fatalf("mapping resolution = %#v", mapped)
	}
	explicit := registry.Resolve(RequestIdentity{ExplicitDeviceID: "explicit", PeerIP: peer})
	if explicit.DeviceID != "explicit" || explicit.Source != IdentityExplicit {
		t.Fatalf("explicit resolution did not override mapping: %#v", explicit)
	}
}

func TestDeviceActivityTrackerSnapshotIsolation(t *testing.T) {
	tracker := NewDeviceActivityTracker()
	peer := netip.MustParseAddr("192.168.1.10")
	at := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)

	tracker.Observe(Resolution{
		DeviceID:        "laptop",
		Source:          IdentityExplicit,
		ProfileID:       "device:laptop",
		ProfileRevision: 2,
	}, peer, at)
	tracker.Observe(Resolution{Source: IdentitySharedFallback}, peer, at)

	snapshot := tracker.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot size = %d, want 1", len(snapshot))
	}
	if got := snapshot["laptop"].LastSeenIP; got != peer {
		t.Fatalf("snapshot last seen ip = %v, want %v", got, peer)
	}

	snapshot["laptop"] = DeviceActivity{DeviceID: "mutated"}
	again := tracker.Snapshot()
	if got := again["laptop"].DeviceID; got != "laptop" {
		t.Fatalf("tracker snapshot leaked caller mutation: %q", got)
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
