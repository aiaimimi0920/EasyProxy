package app

import (
	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"testing"
)

func TestClaudeRouteChangesRequireDispatcherRebuild(t *testing.T) {
	for _, local := range []bool{false, true} {
		from := &config.Config{ClaudeRoute: config.ClaudeRoute{Enabled: true, NodeTag: "fixed", NodeFingerprint: config.NodeFingerprint("uri")}}
		from.Routing.Enabled = true
		from.LocalServer.Enabled = local
		for _, change := range []string{"enabled", "tag", "fingerprint", "shared_domains"} {
			to := from.Clone()
			switch change {
			case "shared_domains":
				to.ClaudeRoute.IncludeSharedDependencies = true
			case "enabled":
				to.ClaudeRoute.Enabled = false
			case "tag":
				to.ClaudeRoute.NodeTag = "other"
			case "fingerprint":
				to.ClaudeRoute.NodeFingerprint = config.NodeFingerprint("changed")
			}
			if routingTopologyFor(boxmgr.ReloadState{Config: from}) == routingTopologyFor(boxmgr.ReloadState{Config: to}) {
				t.Fatalf("%s did not change topology", change)
			}
			if routingHotApplyCompatible(from, to) {
				t.Fatalf("%s wrongly hot applied", change)
			}
		}
	}
}
