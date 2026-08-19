package routerule

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadLocalRuleFiles(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "first.txt")
	yamlPath := filepath.Join(dir, "second.yaml")
	if err := os.WriteFile(textPath, []byte("# comment\nDOMAIN-SUFFIX,first.example,DIRECT\n\n// comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, []byte("payload:\n  - DOMAIN-KEYWORD,second,PROXY\n  - IP-CIDR,203.0.113.0/24,DIRECT\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadLocalRuleFiles([]string{textPath, yamlPath})
	if err != nil {
		t.Fatalf("LoadLocalRuleFiles() error = %v", err)
	}
	want := []string{
		"DOMAIN-SUFFIX,first.example,DIRECT",
		"DOMAIN-KEYWORD,second,PROXY",
		"IP-CIDR,203.0.113.0/24,DIRECT",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadLocalRuleFiles() = %#v, want %#v", got, want)
	}
}

func TestLoadLocalRuleFilesReturnsPathQualifiedValidationError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.txt")
	if err := os.WriteFile(path, []byte("NOT-A-RULE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadLocalRuleFiles([]string{path})
	if err == nil {
		t.Fatal("LoadLocalRuleFiles() unexpectedly accepted an invalid rule")
	}
	if !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Fatalf("LoadLocalRuleFiles() error = %q, want path %q", err, path)
	}
}
