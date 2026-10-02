package pool

import (
	"context"
	"testing"
)

func TestRequiredTagIsConsumedBeforeNodeBootstrapDial(t *testing.T) {
	directive := &SelectionDirective{RequiredTag: "outer-node", Strategy: StrategySession, SessionKey: "lease", ProfileID: "device"}
	ctx, cancel := context.WithCancel(WithDirective(context.Background(), directive))
	nested := memberDialContext(ctx)
	got := DirectiveFrom(nested)
	if got == nil || got.RequiredTag != "" || got.Strategy != directive.Strategy || got.SessionKey != "lease" || got.ProfileID != "device" {
		t.Fatalf("unexpected nested directive: %#v", got)
	}
	if directive.RequiredTag != "outer-node" {
		t.Fatal("outer lease binding was mutated")
	}
	cancel()
	if nested.Err() != context.Canceled {
		t.Fatal("nested context lost cancellation")
	}
	ordinary := context.Background()
	if memberDialContext(ordinary) != ordinary {
		t.Fatal("ordinary requests changed")
	}
}

func TestRequiredTagNeverFallsBackDuringCandidateRelaxation(t *testing.T) {
	p := &poolOutbound{mode: modeSequential, members: []*memberState{
		{tag: "other", outbound: failingOutbound{}},
		{tag: "required", outbound: failingOutbound{}},
	}}
	directive := &SelectionDirective{RequiredTag: "required"}
	got, err := p.pickMember("tcp", nil, directive)
	if err != nil || got == nil || got.tag != "required" {
		t.Fatalf("strict selection = %v, %v", got, err)
	}
	// Dial retries exclude the failed member. Every health/source fallback pass
	// must keep the strict tag constraint rather than selecting another node.
	got, err = p.pickMember("tcp", map[string]struct{}{"required": {}}, directive)
	if err == nil || got != nil {
		t.Fatalf("failed strict route escaped to another member: %v, %v", got, err)
	}
	directive.RequiredTag = "missing"
	got, err = p.pickMember("tcp", nil, directive)
	if err == nil || got != nil {
		t.Fatalf("unknown strict tag must fail closed: %v, %v", got, err)
	}
	// Ordinary pool clients keep their original failover behavior.
	got, err = p.pickMember("tcp", map[string]struct{}{"required": {}}, nil)
	if err != nil || got == nil || got.tag != "other" {
		t.Fatalf("ordinary pool fallback changed: %v, %v", got, err)
	}
}
