package boxmgr

import (
	"easy_proxies/internal/config"
	"testing"
)

func TestLegacySharedSelectionRollbackSnapshot(t *testing.T) {
	base := &config.Config{Mode: "pool"}
	hot := base.Clone()
	hot.Routing.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "US"}}
	hot.Routing.NodeFilter.Countries = []string{"US", "JP"}
	hot.LocalServer.SharedRevision = 9
	got := mergeHotAppliedConfig(base, hot)
	hot.Routing.SelectionTiers[0].Country = "DE"
	hot.Routing.NodeFilter.Countries[0] = "DE"
	if got.Routing.SelectionTiers[0].Country != "US" || got.Routing.NodeFilter.Countries[0] != "US" || got.LocalServer.SharedRevision != 9 {
		t.Fatal("legacy hot selection snapshot missing or aliased")
	}
	hot.Routing.SelectionTiers = nil
	if len(mergeHotAppliedConfig(got, hot).Routing.SelectionTiers) != 0 {
		t.Fatal("clear lost in rollback snapshot")
	}
}
