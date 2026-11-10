package monitor

import (
	"easy_proxies/internal/config"
	"net/http"
	"reflect"
	"testing"
)

func TestSharedTierAPIValidationPersistenceAndLegacyAlias(t *testing.T) {
	h := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	profile := validProfilePayload(true)
	profile["selection_tiers"] = []any{map[string]any{"kind": "country", "country": "us"}, map[string]any{"kind": "latency"}}
	result := performAuthedJSON(t, h.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{"expected_revision": 1, "profile": profile})
	if result.Code != http.StatusOK {
		t.Fatalf("save=%#v", result)
	}
	want := []config.SelectionTier{{Kind: "country", Country: "US"}, {Kind: "latency"}}
	if !reflect.DeepEqual(h.config.Routing.SelectionTiers, want) || !reflect.DeepEqual(h.profiles.SharedProfile().Selection().SelectionTiers, want) {
		t.Fatal("save lost tiers")
	}
	read := performAuthedJSON(t, h.server, http.MethodGet, "/api/routing/config", nil)
	if read.Code != http.StatusOK || len(read.Body["selection_tiers"].([]any)) != 2 {
		t.Fatalf("alias get=%#v", read)
	}
	delete(read.Body, "selection_tiers")
	preserved := performAuthedJSON(t, h.server, http.MethodPut, "/api/routing/config", read.Body)
	if preserved.Code != http.StatusOK || !reflect.DeepEqual(h.profiles.SharedProfile().Selection().SelectionTiers, want) {
		t.Fatalf("old client cleared tiers: %#v", preserved)
	}
	clear := performAuthedJSON(t, h.server, http.MethodGet, "/api/routing/config", nil)
	clear.Body["selection_tiers"] = []any{}
	cleared := performAuthedJSON(t, h.server, http.MethodPut, "/api/routing/config", clear.Body)
	if cleared.Code != http.StatusOK || len(h.profiles.SharedProfile().Selection().SelectionTiers) != 0 {
		t.Fatalf("explicit clear failed: %#v", cleared)
	}
	invalid := validProfilePayload(true)
	invalid["selection_tiers"] = []any{map[string]any{"kind": "invalid"}}
	before := h.profiles.SharedProfile().Revision()
	rejected := performAuthedJSON(t, h.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{"expected_revision": before, "profile": invalid})
	if rejected.Code != http.StatusUnprocessableEntity || h.profiles.SharedProfile().Revision() != before {
		t.Fatalf("invalid chain changed state: %#v", rejected)
	}
}
