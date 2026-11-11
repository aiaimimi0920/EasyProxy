package monitor

import (
	"net/http"
	"testing"
)

// Unlike the legacy routing alias, full Profile PUT replaces its definition.
func TestSharedProfileFullReplacementTierContract(t *testing.T) {
	for _, explicitEmpty := range []bool{false, true} {
		h := newLocalServerMonitor(t, "easyproxy", "secret", 1)
		p := validProfilePayload(false)
		p["selection_tiers"] = []any{map[string]any{"kind": "country", "country": "US"}}
		result := performAuthedJSON(t, h.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{"expected_revision": 1, "profile": p})
		if result.Code != http.StatusOK || len(h.profiles.SharedProfile().Selection().SelectionTiers) != 1 {
			t.Fatalf("set failed: %#v", result)
		}
		p = validProfilePayload(false)
		if explicitEmpty {
			p["selection_tiers"] = []any{}
		}
		result = performAuthedJSON(t, h.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{"expected_revision": 2, "profile": p})
		if result.Code != http.StatusOK || len(h.profiles.SharedProfile().Selection().SelectionTiers) != 0 || h.profiles.SharedProfile().Revision() != 3 {
			t.Fatalf("full replacement failed: %#v", result)
		}
		stale := performAuthedJSON(t, h.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{"expected_revision": 2, "profile": p})
		if stale.Code != http.StatusConflict {
			t.Fatalf("stale replacement accepted: %#v", stale)
		}
	}
}
