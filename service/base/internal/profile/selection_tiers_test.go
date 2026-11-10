package profile

import (
	"context"
	"easy_proxies/internal/config"
	"reflect"
	"testing"
)

func TestProfileSelectionTiersRoundTripAndOwnedSnapshots(t *testing.T) {
	tiers := []config.SelectionTier{{Kind: "country", Country: "us"}, {Kind: "stability"}}
	compiled, err := Compile("device:laptop", KindDevice, 2, Definition{Enabled: true, SelectionTiers: tiers}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tiers[0].Country = "JP"
	got := compiled.Selection()
	if got.SelectionTiers[0].Country != "US" {
		t.Fatal("compile lost ownership")
	}
	got.SelectionTiers[0].Country = "KR"
	snapshot := compiled.Definition()
	snapshot.SelectionTiers[0].Country = "TW"
	if compiled.Selection().SelectionTiers[0].Country != "US" {
		t.Fatal("accessor aliases selection")
	}
	var routing config.RoutingConfig
	if err := ApplyDefinitionToRouting(compiled.Definition(), &routing); err != nil {
		t.Fatal(err)
	}
	converted := DefinitionFromRouting(routing)
	if !reflect.DeepEqual(converted.SelectionTiers, compiled.Definition().SelectionTiers) {
		t.Fatal("routing roundtrip lost tiers")
	}
	routing.SelectionTiers[0].Country = "JP"
	if converted.SelectionTiers[0].Country != "US" {
		t.Fatal("routing aliases slices")
	}
	if _, err := Compile("shared", KindShared, 1, Definition{SelectionTiers: []config.SelectionTier{{Kind: "bogus"}}}, nil); err == nil {
		t.Fatal("invalid tier accepted")
	}
}
func TestDeviceTierPersistenceAndIndependentSharedChanges(t *testing.T) {
	ctx := context.Background()
	st := openProfileTestStore(t)
	mgr := newProfileTestManager(t, ctx, st)
	def := mgr.SharedProfile().Definition()
	def.SelectionTiers = []config.SelectionTier{{Kind: "country", Country: "US"}, {Kind: "latency"}}
	created, err := mgr.PutDeviceProfile(ctx, "laptop", def, 0)
	if err != nil {
		t.Fatal(err)
	}
	def.SelectionTiers[0].Country = "JP"
	prepared, err := mgr.PrepareShared(def, mgr.SharedProfile().Revision())
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.ReserveShared(prepared); err != nil {
		t.Fatal(err)
	}
	mgr.PublishShared(prepared)
	current := mgr.Resolve(RequestIdentity{ExplicitDeviceID: "laptop"})
	if current.Profile.Selection().SelectionTiers[0].Country != "US" {
		t.Fatal("shared mutation changed independent profile")
	}
	reloaded := newProfileTestManager(t, ctx, st)
	loaded := reloaded.Resolve(RequestIdentity{ExplicitDeviceID: "laptop"})
	if !reflect.DeepEqual(loaded.Profile.Definition().SelectionTiers, created.Profile.Definition().SelectionTiers) {
		t.Fatal("persistent reload lost device tiers")
	}
	if _, err := mgr.DeleteDeviceProfile(ctx, "laptop", created.Revision); err != nil {
		t.Fatal(err)
	}
	if got := mgr.Resolve(RequestIdentity{ExplicitDeviceID: "laptop"}).Profile.Selection().SelectionTiers[0].Country; got != "JP" {
		t.Fatalf("delete did not return to shared: %s", got)
	}
}
