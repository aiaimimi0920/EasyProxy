package backup

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddFileRejectsContentChangedAfterInspection(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "changing.txt", "changed")
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	err := addFile(archive, "changing.txt", path, File{Size: 7, SHA256: strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "source file changed") {
		t.Fatalf("expected source mutation rejection, got %v", err)
	}
}

func writeFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEncryptedBackupRoundTripAndTamperRejection(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "backup.age")
	logicalChecksum := strings.Repeat("a", 64)
	input := Input{
		DatabasePath: writeFixture(t, dir, "source.sql", "CREATE TABLE data(value TEXT);\nINSERT INTO data VALUES ('secret-row');\n"),
		LogicalPath:  writeFixture(t, dir, "source.json", `{"data":{"settings":{"secret":"sensitive"}}}`),
		ManifestPath: writeFixture(t, dir, "manifest.json", `{"checksum":"manifest-checksum"}`),
		Metadata: Metadata{
			DeploymentName: "test", ApplicationVersion: "2.4.0", DatabaseSchemaVersion: 2,
			DatabaseName: "test-d1", DatabaseID: "database-id", DatabaseBinding: "MISUB_DB",
			Counts: map[string]int{"sources": 1}, LogicalDataSHA256: logicalChecksum,
			DatabaseRowsSHA256: strings.Repeat("c", 64), TableRows: map[string]int{"subscriptions": 1},
		},
	}
	if err := CreateEncrypted(output, "test passphrase", input); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sensitive") || strings.Contains(string(raw), "secret-row") {
		t.Fatal("encrypted backup contains plaintext data")
	}
	extracted, err := ExtractEncrypted(output, filepath.Join(dir, "restore"), "test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if extracted.Metadata.LogicalDataSHA256 != logicalChecksum {
		t.Fatalf("metadata checksum = %q", extracted.Metadata.LogicalDataSHA256)
	}
	if data, _ := os.ReadFile(extracted.Database); !strings.Contains(string(data), "secret-row") {
		t.Fatal("database export did not round-trip")
	}

	raw[len(raw)/2] ^= 0xff
	tampered := filepath.Join(dir, "tampered.age")
	if err := os.WriteFile(tampered, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractEncrypted(tampered, filepath.Join(dir, "tampered-restore"), "test passphrase"); err == nil {
		t.Fatal("tampered encrypted backup was accepted")
	}
}

func TestEncryptedBackupRejectsWrongPassphraseAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "backup.age")
	input := Input{
		DatabasePath: writeFixture(t, dir, "source.sql", "SELECT 1;"),
		LogicalPath:  writeFixture(t, dir, "source.json", `{}`),
		ManifestPath: writeFixture(t, dir, "manifest.json", `{}`),
		Metadata: Metadata{DeploymentName: "test", DatabaseName: "test-d1", DatabaseID: "id", DatabaseBinding: "MISUB_DB",
			Counts: map[string]int{}, LogicalDataSHA256: strings.Repeat("b", 64),
			DatabaseRowsSHA256: strings.Repeat("c", 64), TableRows: map[string]int{}},
	}
	if err := CreateEncrypted(output, "right", input); err != nil {
		t.Fatal(err)
	}
	if err := CreateEncrypted(output, "right", input); err == nil {
		t.Fatal("CreateEncrypted() overwrote an existing backup")
	}
	if _, err := ExtractEncrypted(output, filepath.Join(dir, "wrong"), "wrong"); err == nil {
		t.Fatal("ExtractEncrypted() accepted wrong passphrase")
	}
}
