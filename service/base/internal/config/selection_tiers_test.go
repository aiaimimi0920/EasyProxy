package config

import (
	"gopkg.in/yaml.v3"
	"reflect"
	"testing"
)

func TestSelectionTierValidationAndOrder(t *testing.T) {
	good := []SelectionTier{{Kind: " country ", Country: "us"}, {Kind: "stability"}}
	got, err := NormalizeSelectionTiers(good)
	if err != nil || got[0].Country != "US" || got[0].Kind != "country" || got[1].Kind != "stability" {
		t.Fatalf("normalized=%+v err=%v", got, err)
	}
	if good[0].Country != "us" {
		t.Fatal("normalization mutated caller")
	}
	for _, bad := range [][]SelectionTier{{{Kind: "country"}}, {{Kind: "country", Country: "USA"}}, {{Kind: "country", Country: "1S"}}, {{Kind: "unknown"}}, {{Kind: "latency", Country: "US"}}, {{Kind: "stability", Country: "US"}}, {{Kind: "latency"}, {Kind: "stability"}, {Kind: "latency"}}} {
		if _, err := NormalizeSelectionTiers(bad); err == nil {
			t.Errorf("accepted invalid chain %+v", bad)
		}
	}
	for _, empty := range [][]SelectionTier{nil, {}} {
		got, err := NormalizeSelectionTiers(empty)
		if err != nil || !reflect.DeepEqual(got, empty) {
			t.Fatalf("empty compatibility: got=%#v err=%v", got, err)
		}
	}
}
func TestSelectionTiersYAMLRoundTripAndClone(t *testing.T) {
	original := &Config{Routing: RoutingConfig{SelectionTiers: []SelectionTier{{Kind: "country", Country: "US"}, {Kind: "latency"}}}}
	cloned := original.Clone()
	cloned.Routing.SelectionTiers[0].Country = "JP"
	if original.Routing.SelectionTiers[0].Country != "US" {
		t.Fatal("clone aliases chain")
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Routing.SelectionTiers, original.Routing.SelectionTiers) {
		t.Fatal("roundtrip lost tiers")
	}
	if err := decoded.normalizeInternal(true); err != nil {
		t.Fatal(err)
	}
	decoded.Routing.SelectionTiers = []SelectionTier{{Kind: "bad"}}
	if err := decoded.normalizeInternal(true); err == nil {
		t.Fatal("normalization accepted bad tier")
	}
}
