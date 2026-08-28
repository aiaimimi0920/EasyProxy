package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/cloudflare"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/misub"
)

type restoreRunner struct {
	output []byte
	err    error
}

func (runner restoreRunner) Run(_ context.Context, _ ...string) ([]byte, error) {
	return runner.output, runner.err
}

func restoreCloud(output string) protectedCloud {
	state := cloudflare.State{DeploymentName: "demo", D1Binding: "MISUB_DB",
		Pages: cloudflare.ResourceState{Name: "demo-pages", ID: "demo-pages"},
		D1:    cloudflare.ResourceState{Name: "demo-d1", ID: "source-id"}}
	return protectedCloud{State: state, Provider: cloudflare.Provider{Runner: restoreRunner{output: []byte(output)}}}
}

func TestValidateRestoreTargetSeparatesDrillAndProduction(t *testing.T) {
	drill := restoreCloud(`[{"name":"demo-d1-restore-drill-42","uuid":"drill-id"}]`)
	if err := validateRestoreTarget(context.Background(), drill, "demo-d1-restore-drill-42", "drill-id", "drill-id", true, false); err != nil {
		t.Fatal(err)
	}
	production := restoreCloud(`[{"name":"demo-d1","uuid":"source-id"}]`)
	if err := validateRestoreTarget(context.Background(), production, "demo-d1", "source-id", "source-id", false, true); err != nil {
		t.Fatal(err)
	}
	if err := validateRestoreTarget(context.Background(), production, "demo-d1", "source-id", "wrong", false, true); err == nil {
		t.Fatal("restore accepted a mismatched database confirmation")
	}
}

func TestValidateRestoreTargetRejectsAmbiguityAndProductionAsDrill(t *testing.T) {
	ambiguous := restoreCloud(`[{"name":"demo-d1-restore-drill-42","uuid":"a"},{"name":"demo-d1-restore-drill-42","uuid":"b"}]`)
	if err := validateRestoreTarget(context.Background(), ambiguous, "demo-d1-restore-drill-42", "a", "a", true, false); err == nil {
		t.Fatal("restore accepted ambiguous target")
	}
	production := restoreCloud(`[{"name":"demo-d1","uuid":"source-id"}]`)
	if err := validateRestoreTarget(context.Background(), production, "demo-d1", "source-id", "source-id", true, false); err == nil {
		t.Fatal("restore accepted production as a drill target")
	}
}

func TestVerifyLogicalIdentityRequiresExactCompleteIdentity(t *testing.T) {
	state := cloudflare.State{DeploymentName: "demo", D1Binding: "MISUB_DB",
		Pages: cloudflare.ResourceState{Name: "pages"}, D1: cloudflare.ResourceState{ID: "db"}}
	if err := verifyLogicalIdentity(misub.Export{}, state); err == nil {
		t.Fatal("empty logical identity was accepted")
	}
	valid := misub.Export{DeploymentName: "demo", PagesProject: "pages", D1DatabaseID: "db", D1Binding: "MISUB_DB"}
	if err := verifyLogicalIdentity(valid, state); err != nil {
		t.Fatal(err)
	}
	if err := verifyLogicalIdentity(misub.Export{D1DatabaseID: "other"}, state); err == nil {
		t.Fatal("logical identity mismatch was accepted")
	}
}

func TestValidateRestoreTargetPropagatesProviderFailure(t *testing.T) {
	cloud := restoreCloud(`[]`)
	cloud.Provider.Runner = restoreRunner{err: errors.New("provider failed")}
	if err := validateRestoreTarget(context.Background(), cloud, "x", "y", "y", true, false); err == nil {
		t.Fatal("provider failure was ignored")
	}
}

func TestValidateStoragePresenceRejectsTruncatedDirectExport(t *testing.T) {
	storage := cloudflare.MiSubStorage{ProfilesPresent: true, SettingsPresent: true}
	snapshot := cloudflare.D1Snapshot{Rows: map[string]int{"subscriptions": 1, "profiles": 1, "settings": 1}}
	if err := validateStoragePresence(storage, snapshot); err == nil {
		t.Fatal("non-empty subscriptions table without canonical row was accepted")
	}
}
