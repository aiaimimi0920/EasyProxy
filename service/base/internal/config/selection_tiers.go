package config

import (
	"fmt"
	"strings"
)

// SelectionTier ranks eligible nodes. The final general-pool tier is implicit;
// legacy hard filters remain constraints at every tier.
type SelectionTier struct {
	Kind    string `json:"kind" yaml:"kind"`
	Country string `json:"country,omitempty" yaml:"country,omitempty"`
}

// NormalizeSelectionTiers validates a bounded, ordered selection chain without
// sorting it. Nil and empty chains retain legacy selection behavior.
func NormalizeSelectionTiers(tiers []SelectionTier) ([]SelectionTier, error) {
	if len(tiers) > 2 {
		return nil, fmt.Errorf("selection_tiers allows at most two configured tiers")
	}
	if tiers == nil {
		return nil, nil
	}
	out := make([]SelectionTier, len(tiers))
	for i, tier := range tiers {
		tier.Kind = strings.ToLower(strings.TrimSpace(tier.Kind))
		tier.Country = strings.ToUpper(strings.TrimSpace(tier.Country))
		switch tier.Kind {
		case "country":
			if len(tier.Country) != 2 || tier.Country[0] < 'A' || tier.Country[0] > 'Z' || tier.Country[1] < 'A' || tier.Country[1] > 'Z' {
				return nil, fmt.Errorf("selection_tiers[%d].country must be a two-letter country code", i)
			}
		case "latency", "stability":
			if tier.Country != "" {
				return nil, fmt.Errorf("selection_tiers[%d].country requires kind country", i)
			}
		default:
			return nil, fmt.Errorf("selection_tiers[%d].kind must be country, latency, or stability", i)
		}
		out[i] = tier
	}
	return out, nil
}

func CloneSelectionTiers(tiers []SelectionTier) []SelectionTier {
	if tiers == nil {
		return nil
	}
	out := make([]SelectionTier, len(tiers))
	copy(out, tiers)
	return out
}
