package app

import (
	"context"
	"easy_proxies/internal/config"
	"errors"
	"testing"
)

type sharedPublisherStub struct {
	profileRuntimeStub
	cfg *config.Config
	err error
}

func (p *sharedPublisherStub) PublishConfigSnapshot(cfg *config.Config) error {
	p.cfg = cfg
	return p.err
}
func TestLegacyHotApplyPublishesSharedSelection(t *testing.T) {
	for _, fail := range []bool{false, true} {
		publisher := &sharedPublisherStub{}
		if fail {
			publisher.err = errors.New("invalid profile")
		}
		box := &recordingAppliedConfigBoxManager{}
		rc := NewRoutingController(context.Background(), box, WithProfileRuntime(publisher))
		cfg := &config.Config{Mode: "pool"}
		cfg.Routing.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "US"}}
		if rc.ApplyHot(cfg) == fail {
			t.Fatalf("hot apply fail=%v", fail)
		}
		if publisher.cfg == nil || publisher.cfg == cfg || publisher.cfg.Routing.SelectionTiers[0].Country != "US" {
			t.Fatal("shared snapshot not published immutably")
		}
		if fail && box.recordedConfig() != nil {
			t.Fatal("failed publication advanced rollback state")
		}
	}
}

func TestLegacySharedRevisionIsHotCompatible(t *testing.T) {
	old := &config.Config{Mode: "pool"}
	old.LocalServer.SharedRevision = 1
	next := old.Clone()
	next.LocalServer.SharedRevision = 2
	next.Routing.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "US"}}
	if !routingHotApplyCompatible(old, next) {
		t.Fatal("shared selection revision incorrectly requires reload")
	}
	rc := NewRoutingController(context.Background(), &recordingAppliedConfigBoxManager{}, WithProfileRuntime(&sharedPublisherStub{}))
	if !rc.ApplyHot(old) || !rc.ApplyHot(next) {
		t.Fatal("shared selection update rejected with existing applied state")
	}
}
