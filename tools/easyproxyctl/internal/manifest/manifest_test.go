package manifest

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture() *Manifest {
	return &Manifest{
		SchemaVersion:  SchemaVersion,
		DeploymentName: "demo",
		ReleaseChannel: "stable",
		GeneratedAt:    time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC),
		TopologySHA256: strings.Repeat("a", 64),
		Components:     []string{"misub", "ech_worker"},
		Resources: []Resource{
			{Kind: "worker", Name: "demo-ech-worker", ID: "worker-1"},
			{Kind: "d1", Name: "demo-misub-d1", ID: "db-1"},
		},
		Source: Source{
			RootCommit:       strings.Repeat("b", 40),
			SubmoduleCommits: map[string]string{"upstreams/misub": strings.Repeat("c", 40)},
		},
		Images: map[string]string{"service": "sha256:123"},
	}
}

func TestSealAndVerify(t *testing.T) {
	value := fixture()
	if err := value.Seal(); err != nil {
		t.Fatal(err)
	}
	if len(value.Checksum) != 64 {
		t.Fatalf("checksum = %q", value.Checksum)
	}
	if err := value.Verify(); err != nil {
		t.Fatal(err)
	}
	if value.Resources[0].Kind != "d1" {
		t.Fatalf("resources were not sorted: %#v", value.Resources)
	}
}

func TestVerifyDetectsMutation(t *testing.T) {
	value := fixture()
	if err := value.Seal(); err != nil {
		t.Fatal(err)
	}
	value.Resources[0].ID = "changed"
	if err := value.Verify(); err == nil {
		t.Fatal("Verify() succeeded after mutation")
	}
}

func TestWriteAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "deployment-manifest.json")
	if err := Write(path, fixture()); err != nil {
		t.Fatal(err)
	}
	loaded, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeploymentName != "demo" {
		t.Fatalf("deployment_name = %q", loaded.DeploymentName)
	}
}
