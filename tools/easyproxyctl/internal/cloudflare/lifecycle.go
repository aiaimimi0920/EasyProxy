package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/discovery"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/manifest"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/naming"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/topology"
)

const StateSchemaVersion = 1

type ResourceState struct {
	Name    string `json:"name"`
	ID      string `json:"id"`
	Created bool   `json:"created"`
}

type State struct {
	SchemaVersion  int           `json:"schema_version"`
	DeploymentName string        `json:"deployment_name"`
	Pages          ResourceState `json:"pages"`
	D1             ResourceState `json:"d1"`
	D1Binding      string        `json:"d1_binding"`
}

func ResolveMiSub(ctx context.Context, provider discovery.Provider, mode discovery.Mode, loaded *topology.Topology) (State, error) {
	if !loaded.Components.MiSubEnabled() {
		return State{}, errors.New("MiSub is disabled in topology")
	}
	names := naming.Resolve(loaded)
	pages, pagesCreated, err := discovery.Reconcile(ctx, provider, mode, "pages", names.PagesProject)
	if err != nil {
		return State{}, err
	}
	d1, d1Created, err := discovery.Reconcile(ctx, provider, mode, "d1", names.D1Database)
	if err != nil {
		return State{}, err
	}
	return State{
		SchemaVersion:  StateSchemaVersion,
		DeploymentName: loaded.DeploymentName,
		Pages:          ResourceState{Name: pages.Name, ID: pages.ID, Created: pagesCreated},
		D1:             ResourceState{Name: d1.Name, ID: d1.ID, Created: d1Created},
		D1Binding:      "MISUB_DB",
	}, nil
}

func VerifyManifest(loaded *topology.Topology, state State, value *manifest.Manifest) error {
	if err := value.Verify(); err != nil {
		return err
	}
	if value.DeploymentName != loaded.DeploymentName || state.DeploymentName != loaded.DeploymentName {
		return errors.New("deployment name mismatch between topology, state, and manifest")
	}
	canonical, err := loaded.CanonicalSHA256Input()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if value.TopologySHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("manifest topology checksum does not match topology")
	}
	want := map[string]ResourceState{"pages": state.Pages, "d1": state.D1}
	seen := make(map[string]bool, len(want))
	for _, resource := range value.Resources {
		expected, ok := want[resource.Kind]
		if !ok {
			continue
		}
		if seen[resource.Kind] {
			return fmt.Errorf("manifest contains duplicate %s resource", resource.Kind)
		}
		seen[resource.Kind] = true
		if resource.Name != expected.Name || resource.ID != expected.ID {
			return fmt.Errorf("manifest %s identity mismatch", resource.Kind)
		}
	}
	for kind := range want {
		if !seen[kind] {
			return fmt.Errorf("manifest is missing %s resource identity", kind)
		}
	}
	return nil
}

func VerifyWranglerConfig(path string, state State) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Wrangler config: %w", err)
	}
	var config struct {
		D1 []struct {
			Binding string `json:"binding"`
			Name    string `json:"database_name"`
			ID      string `json:"database_id"`
		} `json:"d1_databases"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("decode Wrangler config: %w", err)
	}
	if len(config.D1) != 1 {
		return fmt.Errorf("Wrangler config must contain exactly one D1 binding, got %d", len(config.D1))
	}
	binding := config.D1[0]
	if binding.Binding != state.D1Binding || binding.Name != state.D1.Name || binding.ID != state.D1.ID {
		return errors.New("Wrangler MISUB_DB binding does not match resolved D1 identity")
	}
	return nil
}

func WriteState(path string, state State) error {
	if state.SchemaVersion != StateSchemaVersion || state.DeploymentName == "" || state.Pages.ID == "" || state.D1.ID == "" {
		return errors.New("Cloudflare resource state is incomplete")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
