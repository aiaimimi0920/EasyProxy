package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/discovery"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/manifest"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/naming"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/topology"
)

type lifecycleProvider struct {
	resources map[string]discovery.Resource
	creates   int
}

func (p *lifecycleProvider) FindExact(_ context.Context, kind, name string) ([]discovery.Resource, error) {
	resource, ok := p.resources[kind]
	if !ok || resource.Name != name {
		return nil, nil
	}
	return []discovery.Resource{resource}, nil
}

func (p *lifecycleProvider) Create(_ context.Context, kind, name string) (discovery.Resource, error) {
	p.creates++
	resource := discovery.Resource{Kind: kind, Name: name, ID: kind + "-id"}
	p.resources[kind] = resource
	return resource, nil
}

func testTopology(t *testing.T) *topology.Topology {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "topology.example.yaml")
	loaded, err := topology.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestBootstrapIsIdempotentAndUpdateNeverCreates(t *testing.T) {
	loaded := testTopology(t)
	provider := &lifecycleProvider{resources: make(map[string]discovery.Resource)}
	first, err := ResolveMiSub(context.Background(), provider, discovery.ModeBootstrap, loaded)
	if err != nil || provider.creates != 2 || !first.Pages.Created || !first.D1.Created {
		t.Fatalf("first bootstrap = %#v, %v, creates=%d", first, err, provider.creates)
	}
	second, err := ResolveMiSub(context.Background(), provider, discovery.ModeBootstrap, loaded)
	if err != nil || provider.creates != 2 || second.Pages.Created || second.D1.Created || second.D1.ID != first.D1.ID {
		t.Fatalf("second bootstrap = %#v, %v, creates=%d", second, err, provider.creates)
	}
	delete(provider.resources, "d1")
	if _, err := ResolveMiSub(context.Background(), provider, discovery.ModeUpdate, loaded); err == nil || provider.creates != 2 {
		t.Fatalf("update error = %v, creates=%d", err, provider.creates)
	}
}

func TestManifestAndBindingMustMatchResolvedIdentity(t *testing.T) {
	loaded := testTopology(t)
	names := naming.Resolve(loaded)
	state := State{SchemaVersion: 1, DeploymentName: loaded.DeploymentName, D1Binding: "MISUB_DB",
		Pages: ResourceState{Name: names.PagesProject, ID: "pages-id"}, D1: ResourceState{Name: names.D1Database, ID: "d1-id"}}
	canonical, _ := loaded.CanonicalSHA256Input()
	digest := sha256.Sum256(canonical)
	value := &manifest.Manifest{SchemaVersion: 1, DeploymentName: loaded.DeploymentName, ReleaseChannel: loaded.ReleaseChannel,
		GeneratedAt: time.Now().UTC(), TopologySHA256: hex.EncodeToString(digest[:]), Components: []string{"misub"},
		Resources: []manifest.Resource{{Kind: "pages", Name: state.Pages.Name, ID: state.Pages.ID}, {Kind: "d1", Name: state.D1.Name, ID: state.D1.ID}},
		Source:    manifest.Source{RootCommit: strings.Repeat("a", 40), SubmoduleCommits: map[string]string{}}, Images: map[string]string{}}
	if err := value.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(loaded, state, value); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "wrangler.json")
	config := `{"d1_databases":[{"binding":"MISUB_DB","database_name":"` + state.D1.Name + `","database_id":"d1-id"}]}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWranglerConfig(configPath, state); err != nil {
		t.Fatal(err)
	}
	value.Resources[0].ID = "wrong"
	if err := VerifyManifest(loaded, state, value); err == nil {
		t.Fatal("VerifyManifest() accepted tampered identity")
	}
	wrong := strings.Replace(config, "MISUB_DB", "OTHER_DB", 1)
	if err := os.WriteFile(configPath, []byte(wrong), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWranglerConfig(configPath, state); err == nil {
		t.Fatal("VerifyWranglerConfig() accepted wrong binding")
	}
}
