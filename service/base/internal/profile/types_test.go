package profile

import (
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestCompileRejectsInvalidRuleInsteadOfSilentlyDroppingIt(t *testing.T) {
	_, err := Compile("shared", KindShared, 1, Definition{
		SchemaVersion: 1,
		Enabled:       true,
		FinalPolicy:   "PROXY",
		Rules:         []string{"NOT-A-RULE"},
	}, nil)
	if err == nil {
		t.Fatal("invalid rule was accepted")
	}
}

func TestDefinitionRoutingRoundTripPreservesListen(t *testing.T) {
	useDefaults := false
	longLivedOnly := true
	routing := config.RoutingConfig{
		Enabled:         true,
		Listen:          "127.0.0.1:22324",
		DefaultStrategy: "session",
		UseDefaultRules: &useDefaults,
		FinalPolicy:     "DIRECT",
		Rules:           []string{"DOMAIN,example.com,DIRECT"},
		RuleProviders: []config.RuleProvider{{
			URL:      "https://example.com/rules.txt",
			Policy:   "PROXY",
			Behavior: "classical",
			Interval: time.Hour,
		}},
		NodeFilter: config.RoutingNodeFilterConfig{
			Countries: []string{"us"},
			Regions:   []string{"HK"},
			LongLived: &longLivedOnly,
		},
		LongLived: config.LongLivedConfig{
			MinUptime:      30 * time.Minute,
			MinSuccessRate: 0.7,
		},
		Session: config.SessionConfig{
			TTL: 5 * time.Minute,
		},
	}

	definition := DefinitionFromRouting(routing)
	target := config.RoutingConfig{
		Listen: "127.0.0.1:39999",
	}
	if err := ApplyDefinitionToRouting(definition, &target); err != nil {
		t.Fatalf("ApplyDefinitionToRouting returned error: %v", err)
	}

	if got, want := target.Listen, "127.0.0.1:39999"; got != want {
		t.Fatalf("routing.listen = %q, want preserved %q", got, want)
	}
	if got, want := target.DefaultStrategy, "session"; got != want {
		t.Fatalf("routing.default_strategy = %q, want %q", got, want)
	}
	if target.UseDefaultRules == nil || *target.UseDefaultRules != false {
		t.Fatalf("routing.use_default_rules = %v, want false", target.UseDefaultRules)
	}
	if got, want := target.FinalPolicy, "DIRECT"; got != want {
		t.Fatalf("routing.final_policy = %q, want %q", got, want)
	}
	if got, want := target.NodeFilter.Countries[0], "US"; got != want {
		t.Fatalf("routing.node_filter.countries[0] = %q, want %q", got, want)
	}
	if got, want := target.NodeFilter.Regions[0], "hk"; got != want {
		t.Fatalf("routing.node_filter.regions[0] = %q, want %q", got, want)
	}
}

func TestCloneDefinitionDeepCopiesReferenceFields(t *testing.T) {
	longLivedOnly := true
	original := Definition{
		SchemaVersion:    1,
		Enabled:          true,
		DefaultStrategy:  "stable",
		UseDefaultRules:  true,
		FinalPolicy:      "PROXY",
		Rules:            []string{"DOMAIN-SUFFIX,example.com,PROXY"},
		RuleProviders:    []RuleProvider{{URL: "https://example.com/rules.txt", Policy: "DIRECT", Behavior: "domain", Interval: "1h"}},
		NodeFilter:       NodeFilter{Countries: []string{"US"}, Regions: []string{"hk"}, LongLived: &longLivedOnly},
		LongLived:        LongLivedPolicy{MinUptime: "1h", MinSuccessRate: 0.8},
		Session:          SessionPolicy{TTL: "10m"},
	}

	cloned := cloneDefinition(original)
	cloned.Rules[0] = "DOMAIN,changed.example,DIRECT"
	cloned.RuleProviders[0].URL = "https://changed.example/rules.txt"
	cloned.NodeFilter.Countries[0] = "JP"
	cloned.NodeFilter.Regions[0] = "sg"
	*cloned.NodeFilter.LongLived = false

	if got := original.Rules[0]; got != "DOMAIN-SUFFIX,example.com,PROXY" {
		t.Fatalf("original rules mutated through clone: %q", got)
	}
	if got := original.RuleProviders[0].URL; got != "https://example.com/rules.txt" {
		t.Fatalf("original provider URL mutated through clone: %q", got)
	}
	if got := original.NodeFilter.Countries[0]; got != "US" {
		t.Fatalf("original node_filter country mutated through clone: %q", got)
	}
	if got := original.NodeFilter.Regions[0]; got != "hk" {
		t.Fatalf("original node_filter region mutated through clone: %q", got)
	}
	if !*original.NodeFilter.LongLived {
		t.Fatal("original node_filter long_lived mutated through clone")
	}
}
