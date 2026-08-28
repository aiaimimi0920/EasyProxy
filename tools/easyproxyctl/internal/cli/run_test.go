package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/manifest"
)

func TestUnknownCommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"unknown"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
}

func TestCloudCommandRequiresKnownLifecycleSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"cloud", "destroy"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "unknown cloud subcommand") {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
}

func TestManifestVerifyRejectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployment-manifest.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"checksum":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"manifest", "verify", "--file", path}, &stdout, &stderr); code == 0 {
		t.Fatalf("Run() succeeded, stderr = %q", stderr.String())
	}
}

func TestManifestBuildRecordsEnabledComponentsAndProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployment-manifest.json")
	topologyPath := filepath.Join("..", "..", "..", "..", "topology.example.yaml")
	rootCommit := strings.Repeat("a", 40)
	args := []string{
		"manifest", "build",
		"--topology", topologyPath,
		"--output", path,
		"--repo-root", t.TempDir(),
		"--root-commit", rootCommit,
		"--submodule-commit", "upstreams/aggregator=" + strings.Repeat("b", 40),
		"--submodule-commit", "upstreams/misub=" + strings.Repeat("c", 40),
		"--submodule-commit", "upstreams/ech-workers=" + strings.Repeat("d", 40),
		"--resource-id", "d1=database-1",
		"--image", "easyproxy=ghcr.io/example/easyproxy@sha256:123",
	}
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	value, err := manifest.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Components) != 4 || value.Source.RootCommit != rootCommit {
		t.Fatalf("manifest components/source = %#v / %#v", value.Components, value.Source)
	}
	if value.Source.SubmoduleCommits["upstreams/misub"] == "" || value.Images["easyproxy"] == "" {
		t.Fatalf("manifest provenance = %#v / %#v", value.Source, value.Images)
	}
	foundD1 := false
	for _, resource := range value.Resources {
		if resource.Kind == "d1" && resource.ID == "database-1" {
			foundD1 = true
		}
	}
	if !foundD1 {
		t.Fatalf("manifest resources = %#v", value.Resources)
	}
}
