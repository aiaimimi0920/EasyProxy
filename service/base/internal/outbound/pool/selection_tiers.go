package pool

import (
	"fmt"
	"strings"
	"time"

	"easy_proxies/internal/monitor"
)

func hasSelectionTiers(d *SelectionDirective) bool { return d != nil && len(d.SelectionTiers) > 0 }

// selectTieredMember only receives candidates that passed all hard constraints.
// A failed dial excludes its tag before re-entering this function, exhausting
// each tier in order without allowing a sticky binding to cross tiers.
func (p *poolOutbound) selectTieredMember(candidates []*memberState, sources map[string]monitor.SourceSelectionState, secondary map[string]monitor.SecondarySelectionState, d *SelectionDirective) *memberState {
	if !hasSelectionTiers(d) {
		return p.selectMemberWithDirective(candidates, sources, secondary, d)
	}
	now := time.Now()
	for i, tier := range d.SelectionTiers {
		if i >= 2 {
			break
		}
		if d.tierAttempts != nil && d.tierAttempts[i] >= selectionTierAttempts {
			continue
		}
		if tier.Kind == "country" {
			subset := p.getCandidateBuffer()
			want := strings.TrimSpace(tier.Country)
			for _, member := range candidates {
				meta := p.options.Metadata[member.tag]
				if want != "" && (strings.EqualFold(want, strings.TrimSpace(meta.CountryISO)) || strings.EqualFold(want, strings.TrimSpace(meta.Country))) {
					subset = append(subset, member)
				}
			}
			if len(subset) > 0 {
				next := *d
				next.tierNamespace = fmt.Sprintf("tier:%d:country:%s", i, strings.ToUpper(want))
				member := p.selectMemberWithDirective(subset, sources, secondary, &next)
				p.putCandidateBuffer(subset)
				noteTierAttempt(d, i)
				return member
			}
			p.putCandidateBuffer(subset)
			continue
		}
		if tier.Kind != "latency" && tier.Kind != "stability" {
			continue
		}
		var best *memberState
		var bestValue time.Duration
		for _, member := range candidates {
			metrics := member.entry.ProbeMetrics(now)
			if metrics.Samples == 0 || metrics.Latency <= 0 {
				continue
			}
			value := metrics.Latency
			if tier.Kind == "stability" {
				if metrics.Samples < monitor.ProbeMetricMinJitterSamples {
					continue
				}
				value = metrics.Jitter
			}
			if best == nil || value < bestValue || (value == bestValue && member.tag < best.tag) {
				best, bestValue = member, value
			}
		}
		if best != nil {
			noteTierAttempt(d, i)
			return best
		}
	}
	if d.tierAttempts != nil && d.tierAttempts[2] >= selectionTierAttempts {
		return nil
	}
	noteTierAttempt(d, 2)
	next := *d
	next.tierNamespace = "tier:general"
	return p.selectMemberWithDirective(candidates, sources, secondary, &next)
}

func noteTierAttempt(d *SelectionDirective, tier int) {
	if d.tierAttempts != nil {
		d.tierAttempts[tier]++
	}
}
