package monitor

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
)

type staticRoutingController struct {
	status RoutingStatus
}

func (s *staticRoutingController) RoutingStatus() RoutingStatus { return s.status }
func (*staticRoutingController) ApplyHot(*config.Config) bool   { return true }

type mutationGuardNodeManager struct {
	swappingNodeManager
	beginCalls   atomic.Int32
	releaseCalls atomic.Int32
}

type deviceProfileLookupErrorStore struct {
	store.Store
}

func (s *deviceProfileLookupErrorStore) GetDeviceProfile(context.Context, string) (*store.DeviceProfile, error) {
	return nil, errors.New("backend not found while unavailable")
}

func (m *mutationGuardNodeManager) BeginConfigMutation(context.Context) (func(), error) {
	m.beginCalls.Add(1)
	return func() { m.releaseCalls.Add(1) }, nil
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
		"schema_version":    1,
		"enabled":           enabled,
		"default_strategy":  "stable",
		"use_default_rules": true,
		"final_policy":      "PROXY",
		"rules":             []string{},
		"rule_providers":    []any{},
		"node_filter": map[string]any{
			"countries":  []string{},
			"regions":    []string{},
			"long_lived": nil,
		},
		"long_lived": map[string]any{
			"min_uptime":       "2h",
			"min_success_rate": 0.9,
		},
		"session": map[string]any{"ttl": "10m"},
	}
}

func TestSharedProfileAPIUsesCASAndPersistsCandidate(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	read := performAuthedJSON(t, harness.server, http.MethodGet, "/api/local-server/profiles/shared", nil)
	if read.Code != http.StatusOK || read.Body["revision"] != float64(1) || read.Body["profile_scope"] != "shared" {
		t.Fatalf("read = %#v", read)
	}

	updatedProfile := validProfilePayload(false)
	updatedProfile["final_policy"] = "DIRECT"
	update := performAuthedJSON(t, harness.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{
		"expected_revision": 1,
		"profile":           updatedProfile,
	})
	if update.Code != http.StatusOK || update.Body["revision"] != float64(2) || update.Body["need_reload"] != false {
		t.Fatalf("update = %#v", update)
	}
	if shared := harness.profiles.SharedProfile(); shared == nil || shared.Revision() != 2 || shared.Enabled() || shared.Definition().FinalPolicy != "DIRECT" {
		t.Fatalf("published shared profile = %#v", shared)
	}
	harness.config.RLock()
	if harness.config.LocalServer.SharedRevision != 2 || harness.config.Routing.Enabled || harness.config.Routing.FinalPolicy != "DIRECT" {
		t.Fatalf("persisted shared config = local=%#v routing=%#v", harness.config.LocalServer, harness.config.Routing)
	}
	harness.config.RUnlock()

	conflict := performAuthedJSON(t, harness.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{
		"expected_revision": 1,
		"profile":           validProfilePayload(true),
	})
	if conflict.Code != http.StatusConflict || conflict.Body["current_revision"] != float64(2) {
		t.Fatalf("conflict = %#v", conflict)
	}
}

func TestSharedProfileSaveFailurePreservesRegistry(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	harness.config.SetFilePath(filepath.Join(t.TempDir(), "missing", "config.yaml"))
	response := performAuthedJSON(t, harness.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{
		"expected_revision": 1,
		"profile":           validProfilePayload(false),
	})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%#v", response.Code, response.Body)
	}
	if shared := harness.profiles.SharedProfile(); shared == nil || shared.Revision() != 1 || !shared.Enabled() {
		t.Fatalf("failed save mutated shared registry: %#v", shared)
	}
}

func TestDeviceProfileAPIUsesCASAndReturnsMutationEnvelope(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	create := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(true),
	})
	if create.Code != http.StatusOK || create.Body["revision"] != float64(1) || create.Body["profile_scope"] != "device" || create.Body["need_reload"] != false {
		t.Fatalf("create = %#v", create)
	}
	resource, ok := create.Body["resource"].(map[string]any)
	if !ok || resource["profile_scope"] != "device" || resource["revision"] != float64(1) {
		t.Fatalf("create resource = %#v", create.Body["resource"])
	}
	conflict := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(false),
	})
	if conflict.Code != http.StatusConflict || conflict.Body["current_revision"] != float64(1) {
		t.Fatalf("conflict = %#v", conflict)
	}
}

func TestDeviceProfilePersistenceFailureReturns500AndPreservesRegistry(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	if err := harness.store.Close(); err != nil {
		t.Fatal(err)
	}
	response := performAuthedJSON(t, harness.server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(true),
	})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response = %#v", response)
	}
	if profile := harness.profiles.DeviceProfile("laptop"); profile != nil {
		t.Fatalf("failed persistence published device profile: %#v", profile)
	}
}

func TestDeviceProfilePutRequiresCompleteProfile(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	response := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %#v", response)
	}
}

func TestDeviceProfileEnabledUsesTypedNotFoundErrors(t *testing.T) {
	missing := newLocalServerAPITestServer(t)
	response := performAuthedJSON(t, missing, http.MethodPatch, "/api/local-server/devices/laptop/profile/enabled", map[string]any{
		"expected_revision": 1,
		"enabled":           false,
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing profile response = %#v", response)
	}

	harness := newLocalServerMonitorWithStoreDecorator(t, "easyproxy", "secret", 1, true, func(st store.Store) store.Store {
		return &deviceProfileLookupErrorStore{Store: st}
	})
	response = performAuthedJSON(t, harness.server, http.MethodPatch, "/api/local-server/devices/laptop/profile/enabled", map[string]any{
		"expected_revision": 1,
		"enabled":           false,
	})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("store failure response = %#v", response)
	}
}

func TestDeviceProfileDeleteAcceptsBodyRevision(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	created := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(true),
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create = %#v", created)
	}
	deleted := performAuthedJSON(t, server, http.MethodDelete, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 1,
	})
	if deleted.Code != http.StatusOK || deleted.Body["profile_scope"] != "shared" {
		t.Fatalf("delete = %#v", deleted)
	}
}

func TestDeviceResourceAndProfileLifecycle(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	device := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/Laptop", map[string]any{
		"expected_revision": 0,
		"display_name":      "Work Laptop",
	})
	deviceResource, ok := device.Body["resource"].(map[string]any)
	if device.Code != http.StatusOK || !ok || deviceResource["device_id"] != "laptop" || device.Body["revision"] != float64(1) {
		t.Fatalf("device = %#v", device)
	}
	copyShared := performAuthedJSON(t, server, http.MethodPost, "/api/local-server/devices/laptop/profile/copy-shared", nil)
	if copyShared.Code != http.StatusOK || copyShared.Body["revision"] != float64(1) {
		t.Fatalf("copy shared = %#v", copyShared)
	}
	copyConflict := performAuthedJSON(t, server, http.MethodPost, "/api/local-server/devices/laptop/profile/copy-shared", nil)
	if copyConflict.Code != http.StatusConflict {
		t.Fatalf("copy conflict = %#v", copyConflict)
	}
	disable := performAuthedJSON(t, server, http.MethodPatch, "/api/local-server/devices/laptop/profile/enabled", map[string]any{
		"expected_revision": 1,
		"enabled":           false,
	})
	if disable.Code != http.StatusOK || disable.Body["revision"] != float64(2) {
		t.Fatalf("disable = %#v", disable)
	}

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	headers.Set("If-Match", `"2"`)
	deleted := performJSONRequest(t, server, http.MethodDelete, "/api/local-server/devices/laptop/profile", nil, headers)
	if deleted.Code != http.StatusOK || deleted.Body["profile_scope"] != "shared" {
		t.Fatalf("delete = %#v", deleted)
	}
	deletedAgain := performJSONRequest(t, server, http.MethodDelete, "/api/local-server/devices/laptop/profile", nil, headers)
	if deletedAgain.Code != http.StatusOK || deletedAgain.Body["profile_scope"] != "shared" {
		t.Fatalf("idempotent delete = %#v", deletedAgain)
	}

	listed := performAuthedJSON(t, server, http.MethodGet, "/api/local-server/devices", nil)
	devices, ok := listed.Body["devices"].([]any)
	if listed.Code != http.StatusOK || !ok || len(devices) != 1 {
		t.Fatalf("devices = %#v", listed)
	}
	summary, ok := devices[0].(map[string]any)
	if !ok || summary["profile_mode"] != "shared" || summary["effective_enabled"] != true {
		t.Fatalf("device summary = %#v", devices[0])
	}
}

func TestDeviceSummaryUsesIndependentModeAndEffectivePolicyState(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	created := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(true),
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create = %#v", created)
	}

	listed := performAuthedJSON(t, server, http.MethodGet, "/api/local-server/devices", nil)
	devices, ok := listed.Body["devices"].([]any)
	if listed.Code != http.StatusOK || !ok || len(devices) != 1 {
		t.Fatalf("devices = %#v", listed)
	}
	summary, ok := devices[0].(map[string]any)
	if !ok || summary["profile_mode"] != "independent" || summary["effective_state"] != "PROFILE" {
		t.Fatalf("enabled independent summary = %#v", devices[0])
	}

	disabled := performAuthedJSON(t, server, http.MethodPatch, "/api/local-server/devices/laptop/profile/enabled", map[string]any{
		"expected_revision": 1,
		"enabled":           false,
	})
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable = %#v", disabled)
	}
	listed = performAuthedJSON(t, server, http.MethodGet, "/api/local-server/devices", nil)
	devices, ok = listed.Body["devices"].([]any)
	if listed.Code != http.StatusOK || !ok || len(devices) != 1 {
		t.Fatalf("disabled devices = %#v", listed)
	}
	summary, ok = devices[0].(map[string]any)
	if !ok || summary["profile_mode"] != "independent" || summary["effective_state"] != "DIRECT" {
		t.Fatalf("disabled independent summary = %#v", devices[0])
	}
}

func TestDeviceWriteAndReadUseMergedResourceContract(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	created := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop", map[string]any{
		"expected_revision": 0,
		"display_name":      "Work Laptop",
	})
	resource, ok := created.Body["resource"].(map[string]any)
	if created.Code != http.StatusOK || created.Body["revision"] != float64(1) || created.Body["need_reload"] != false || !ok {
		t.Fatalf("created = %#v", created)
	}
	if resource["device_id"] != "laptop" || resource["profile_mode"] != "shared" || resource["effective_state"] != "PROFILE" {
		t.Fatalf("created resource = %#v", resource)
	}

	read := performAuthedJSON(t, server, http.MethodGet, "/api/local-server/devices/laptop", nil)
	profileResource, profileOK := read.Body["profile"].(map[string]any)
	mappings, mappingsOK := read.Body["mappings"].([]any)
	if read.Code != http.StatusOK || !profileOK || profileResource["profile_scope"] != "shared" || !mappingsOK || len(mappings) != 0 {
		t.Fatalf("read = %#v", read)
	}
}

func TestDeviceResourcesIncludeActivityOnlyExplicitDevices(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	seenAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	harness.profiles.Observe(profile.Resolution{
		DeviceID: "guest-laptop",
		Source:   profile.IdentityExplicit,
		Profile:  harness.profiles.SharedProfile(),
	}, netip.MustParseAddr("192.0.2.55"), seenAt)

	listed := performAuthedJSON(t, harness.server, http.MethodGet, "/api/local-server/devices", nil)
	devices, ok := listed.Body["devices"].([]any)
	if listed.Code != http.StatusOK || !ok || len(devices) != 1 {
		t.Fatalf("devices = %#v", listed)
	}
	summary, ok := devices[0].(map[string]any)
	if !ok || summary["device_id"] != "guest-laptop" || summary["revision"] != float64(0) || summary["profile_mode"] != "shared" || summary["identity_source"] != "explicit" || summary["last_seen_ip"] != "192.0.2.55" {
		t.Fatalf("activity-only summary = %#v", devices[0])
	}

	read := performAuthedJSON(t, harness.server, http.MethodGet, "/api/local-server/devices/guest-laptop", nil)
	profileResource, profileOK := read.Body["profile"].(map[string]any)
	if read.Code != http.StatusOK || read.Body["revision"] != float64(0) || !profileOK || profileResource["profile_scope"] != "shared" {
		t.Fatalf("activity-only resource = %#v", read)
	}
}

func TestIPMappingMutationsReturnResourceEnvelope(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	device := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop", map[string]any{
		"expected_revision": 0,
		"display_name":      "Laptop",
	})
	if device.Code != http.StatusOK {
		t.Fatalf("device = %#v", device)
	}
	created := performAuthedJSON(t, server, http.MethodPost, "/api/local-server/ip-mappings", map[string]any{
		"cidr":      "192.0.2.10",
		"device_id": "laptop",
		"priority":  5,
		"enabled":   true,
	})
	resource, ok := created.Body["resource"].(map[string]any)
	if created.Code != http.StatusOK || created.Body["revision"] != float64(1) || created.Body["registry_revision"] == nil || created.Body["need_reload"] != false || !ok {
		t.Fatalf("created = %#v", created)
	}
	if resource["mapping_id"] == "" || resource["cidr"] != "192.0.2.10/32" || resource["revision"] != float64(1) {
		t.Fatalf("created resource = %#v", resource)
	}
}

func TestIPMappingAPIUsesGeneratedIDNormalizedCIDRAndCAS(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	device := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop", map[string]any{
		"expected_revision": 0,
		"display_name":      "Laptop",
	})
	if device.Code != http.StatusOK {
		t.Fatalf("device = %#v", device)
	}
	created := performAuthedJSON(t, server, http.MethodPost, "/api/local-server/ip-mappings", map[string]any{
		"cidr":      "192.0.2.10",
		"device_id": "laptop",
		"priority":  5,
		"enabled":   true,
	})
	createdResource, _ := created.Body["resource"].(map[string]any)
	mappingID, _ := createdResource["mapping_id"].(string)
	if created.Code != http.StatusOK || mappingID == "" || createdResource["cidr"] != "192.0.2.10/32" || created.Body["revision"] != float64(1) {
		t.Fatalf("created = %#v", created)
	}
	updated := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/ip-mappings/"+mappingID, map[string]any{
		"expected_revision": 1,
		"cidr":              "2001:db8::1",
		"device_id":         "laptop",
		"priority":          9,
		"enabled":           false,
	})
	updatedResource, _ := updated.Body["resource"].(map[string]any)
	if updated.Code != http.StatusOK || updatedResource["cidr"] != "2001:db8::1/128" || updated.Body["revision"] != float64(2) {
		t.Fatalf("updated = %#v", updated)
	}
	resolution := server.profileManagerSnapshot().Resolve(profile.RequestIdentity{PeerIP: netip.MustParseAddr("2001:db8::1")})
	if resolution.Source != profile.IdentitySharedFallback {
		t.Fatalf("disabled mapping remained active: %#v", resolution)
	}
	stale := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/ip-mappings/"+mappingID, map[string]any{
		"expected_revision": 1,
		"cidr":              "192.0.2.0/24",
		"device_id":         "laptop",
		"priority":          1,
		"enabled":           true,
	})
	if stale.Code != http.StatusConflict || stale.Body["current_revision"] != float64(2) {
		t.Fatalf("stale = %#v", stale)
	}

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	headers.Set("If-Match", "2")
	deleted := performJSONRequest(t, server, http.MethodDelete, "/api/local-server/ip-mappings/"+mappingID, nil, headers)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete = %#v", deleted)
	}
	deletedAgain := performJSONRequest(t, server, http.MethodDelete, "/api/local-server/ip-mappings/"+mappingID, nil, headers)
	if deletedAgain.Code != http.StatusOK {
		t.Fatalf("idempotent delete = %#v", deletedAgain)
	}
}

func TestProfileMutationDuringReloadReturns409(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	server.BeginReloadWindow()
	defer server.EndReloadWindow()
	res := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(true),
	})
	if res.Code != http.StatusConflict || res.Body["error"] != "reload_in_progress" {
		t.Fatalf("response = %#v", res)
	}
}

func TestLocalServerStatusSummarizesRegistry(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	performAuthedJSON(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(true),
	})
	status := performAuthedJSON(t, server, http.MethodGet, "/api/local-server/status", nil)
	if status.Code != http.StatusOK || status.Body["enabled"] != true || status.Body["profile_count"] != float64(2) || status.Body["mapping_count"] != float64(0) {
		t.Fatalf("status = %#v", status)
	}
	if status.Body["peer_address_mode"] != "tcp_peer" || status.Body["source_ip_warning"] == "" {
		t.Fatalf("source identity status = %#v", status.Body)
	}
}

func TestExpectedRevisionRejectsContradictoryHeaderAndBody(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	headers.Set("If-None-Match", "*")
	response := performJSONRequest(t, server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 2,
		"profile":           validProfilePayload(true),
	}, headers)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %#v", response)
	}
}

func TestLocalServerRoutingConfigAliasMutatesSharedProfile(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	read := performAuthedJSON(t, server, http.MethodGet, "/api/routing/config", nil)
	if read.Code != http.StatusOK || read.Body["profile_scope"] != "shared" || read.Body["revision"] != float64(1) {
		t.Fatalf("read = %#v", read)
	}
	update := performAuthedJSON(t, server, http.MethodPut, "/api/routing/config", map[string]any{
		"enabled":                     false,
		"listen":                      "",
		"default_strategy":            "stable",
		"use_default_rules":           false,
		"final_policy":                "DIRECT",
		"rules":                       []string{},
		"rule_providers":              []any{},
		"long_lived_min_uptime":       "2h",
		"long_lived_min_success_rate": 0.9,
		"session_ttl":                 "10m",
	})
	if update.Code != http.StatusOK || update.Body["profile_scope"] != "shared" || update.Body["revision"] != float64(2) || update.Body["need_reload"] != false {
		t.Fatalf("update = %#v", update)
	}
	shared := server.profileManagerSnapshot().SharedProfile()
	if shared == nil || shared.Revision() != 2 || shared.Enabled() || shared.Definition().FinalPolicy != "DIRECT" {
		t.Fatalf("shared = %#v", shared)
	}
}

func TestLocalServerRoutingConfigAliasPreservesProfileOnlyFields(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	profileBody := validProfilePayload(true)
	profileBody["node_filter"] = map[string]any{
		"countries":  []string{"US"},
		"regions":    []string{"north-america"},
		"long_lived": true,
	}
	updated := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{
		"expected_revision": 1,
		"profile":           profileBody,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("profile update = %#v", updated)
	}

	alias := performAuthedJSON(t, server, http.MethodGet, "/api/routing/config", nil)
	alias.Body["final_policy"] = "DIRECT"
	aliasUpdate := performAuthedJSON(t, server, http.MethodPut, "/api/routing/config", alias.Body)
	if aliasUpdate.Code != http.StatusOK {
		t.Fatalf("alias update = %#v", aliasUpdate)
	}
	definition := server.profileManagerSnapshot().SharedProfile().Definition()
	if len(definition.NodeFilter.Countries) != 1 || definition.NodeFilter.Countries[0] != "US" || len(definition.NodeFilter.Regions) != 1 || definition.NodeFilter.Regions[0] != "north-america" || definition.NodeFilter.LongLived == nil || !*definition.NodeFilter.LongLived {
		t.Fatalf("node filter was not preserved: %#v", definition.NodeFilter)
	}
}

func TestLocalServerRoutingConfigAliasRejectsStaleSharedRevision(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	stale := performAuthedJSON(t, server, http.MethodGet, "/api/routing/config", nil)
	if stale.Code != http.StatusOK || stale.Body["revision"] != float64(1) {
		t.Fatalf("stale snapshot = %#v", stale)
	}
	updated := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{
		"expected_revision": 1,
		"profile":           validProfilePayload(false),
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("shared update = %#v", updated)
	}

	stale.Body["final_policy"] = "DIRECT"
	conflict := performAuthedJSON(t, server, http.MethodPut, "/api/routing/config", stale.Body)
	if conflict.Code != http.StatusConflict || conflict.Body["current_revision"] != float64(2) {
		t.Fatalf("conflict = %#v", conflict)
	}
}

func TestLocalServerRoutingStatusUsesSharedProfileAndDispatcherReadiness(t *testing.T) {
	server := newLocalServerAPITestServer(t)
	server.SetRoutingController(&staticRoutingController{status: RoutingStatus{
		Enabled:         true,
		DispatcherReady: true,
		Listen:          "127.0.0.1:22323",
		FinalPolicy:     "PROXY",
	}})
	profileBody := validProfilePayload(false)
	profileBody["final_policy"] = "DIRECT"
	profileBody["rules"] = []string{"DOMAIN,example.com,DIRECT"}
	updated := performAuthedJSON(t, server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{
		"expected_revision": 1,
		"profile":           profileBody,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("update = %#v", updated)
	}
	status := performAuthedJSON(t, server, http.MethodGet, "/api/routing/status", nil)
	if status.Code != http.StatusOK || status.Body["enabled"] != false || status.Body["dispatcher_ready"] != true || status.Body["shared_revision"] != float64(2) || status.Body["profile_scope"] != "shared" {
		t.Fatalf("status = %#v", status)
	}
	if status.Body["final_policy"] != "DIRECT" || status.Body["default_strategy"] != "stable" || status.Body["rule_count"] == float64(0) {
		t.Fatalf("shared profile status = %#v", status.Body)
	}
}

func TestExpectedRevisionAcceptsQuotedIfMatch(t *testing.T) {
	headers := make(http.Header)
	headers.Set("If-Match", `"4"`)
	req, err := http.NewRequest(http.MethodPut, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = headers
	bodyRevision := int64(4)
	got, err := expectedRevision(req, &bodyRevision)
	if err != nil || got != 4 {
		t.Fatalf("expectedRevision = %d, %v", got, err)
	}

}

func TestLocalServerMutationsUseConfigBarrier(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	guard := &mutationGuardNodeManager{}
	harness.server.SetNodeManager(guard)
	profileUpdate := performAuthedJSON(t, harness.server, http.MethodPut, "/api/local-server/devices/laptop/profile", map[string]any{
		"expected_revision": 0,
		"profile":           validProfilePayload(true),
	})
	if profileUpdate.Code != http.StatusOK {
		t.Fatalf("profile update = %#v", profileUpdate)
	}
	configUpdate := performAuthedJSON(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"auth_password": "rotated-secret",
	})
	if configUpdate.Code != http.StatusOK {
		t.Fatalf("config update = %#v", configUpdate)
	}
	if guard.beginCalls.Load() != 2 || guard.releaseCalls.Load() != 2 {
		t.Fatalf("barrier calls = begin:%d release:%d", guard.beginCalls.Load(), guard.releaseCalls.Load())
	}
}
