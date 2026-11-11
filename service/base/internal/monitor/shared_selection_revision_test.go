package monitor

import (
	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
	"net/http"
	"testing"
)

type sharedPublishingRouting struct {
	staticRoutingController
	manager *profile.Manager
}

func (r *sharedPublishingRouting) ApplyHot(c *config.Config) bool {
	return r.manager.PublishConfigSnapshot(c) == nil
}
func TestLegacyRoutingEditInvalidatesSharedCAS(t *testing.T) {
	h := newLocalServerMonitorWithEnabled(t, "easyproxy", "secret", 1, false)
	h.config.Routing.Enabled = false
	h.server.SetRoutingController(&sharedPublishingRouting{manager: h.profiles})
	req := routingConfigPayload{Enabled: false, FinalPolicy: "PROXY", DefaultStrategy: "stable", UseDefaultRules: true, SelectionTiers: []config.SelectionTier{{Kind: "country", Country: "US"}}}
	if _, err := h.server.updateRoutingConfig(req); err != nil {
		t.Fatal(err)
	}
	if h.profiles.SharedProfile().Revision() != 2 {
		t.Fatal("legacy shared revision not advanced")
	}
	response := performAuthedJSON(t, h.server, http.MethodPut, "/api/local-server/profiles/shared", map[string]any{"expected_revision": 1, "profile": validProfilePayload(false)})
	if response.Code != http.StatusConflict {
		t.Fatalf("stale edit accepted: %#v", response)
	}
}
