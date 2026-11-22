package pool

import "strings"

// This is a candidate constraint, not a node removal or health failure. Apply
// it before every health relaxation and tier so fallback cannot reintroduce a
// default-excluded node. Dedicated node/region and bootstrap pools opt out.
func (p *poolOutbound) memberAllowedByDefaultRegionPolicy(member *memberState, d *SelectionDirective) bool {
	if !p.options.DefaultRegionExclusions {
		return true
	}
	if d != nil && (d.ExplicitRegionSelection || d.RequiredTag == member.tag || d.PinnedTag == member.tag) {
		// Normal filters, health checks and strict-pin constraints still apply.
		// A soft pin exempts only its named node, not its fallback candidates.
		return true
	}
	meta := p.options.Metadata[member.tag]
	if iso := strings.TrimSpace(meta.CountryISO); iso != "" {
		return !defaultExcludedRegion(iso)
	}
	return !defaultExcludedRegion(meta.Country) && !defaultExcludedRegion(meta.Region)
}

func defaultExcludedRegion(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CN", "HK", "MO", "CHINA", "HONG KONG", "MACAO", "MACAU", "中国", "香港", "澳门":
		return true
	default:
		return false
	}
}
