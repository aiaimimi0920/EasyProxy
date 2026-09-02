package pool

import "strings"

func (p *poolOutbound) memberMatchesRequiredAvailableProtocol(
	member *memberState,
	directive *SelectionDirective,
) bool {
	if directive == nil || !directive.RequireAvailablePreferred {
		return true
	}
	family := strings.ToLower(strings.TrimSpace(p.options.Metadata[member.tag].ProtocolFamily))
	matched := false
	for _, preferred := range directive.PreferredProtocolFamilies {
		if family == strings.ToLower(strings.TrimSpace(preferred)) {
			matched = true
			break
		}
	}
	if !matched || member.entry == nil {
		return false
	}
	return member.entry.Snapshot().EffectiveAvailable
}

func (p *poolOutbound) preferProtocolCandidates(
	candidates []*memberState,
	directive *SelectionDirective,
) ([]*memberState, []*memberState) {
	if directive == nil || len(directive.PreferredProtocolFamilies) == 0 {
		return candidates, nil
	}
	wanted := make(map[string]struct{}, len(directive.PreferredProtocolFamilies))
	for _, family := range directive.PreferredProtocolFamilies {
		if normalized := strings.ToLower(strings.TrimSpace(family)); normalized != "" {
			wanted[normalized] = struct{}{}
		}
	}
	preferred := p.getCandidateBuffer()
	for _, member := range candidates {
		family := strings.ToLower(strings.TrimSpace(p.options.Metadata[member.tag].ProtocolFamily))
		if _, ok := wanted[family]; ok {
			preferred = append(preferred, member)
		}
	}
	if len(preferred) == 0 {
		p.putCandidateBuffer(preferred)
		return candidates, nil
	}
	return preferred, preferred
}
