package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/backup"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/cloudflare"
	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/topology"
)

// Exercise real SQLite and age, not precomputed snapshot responses. Python is
// already a supported root-test/runtime dependency; no new Go driver is added.
const sqliteProgram = `import json,pathlib,sqlite3,sys
connection=sqlite3.connect(sys.argv[1])
connection.row_factory=sqlite3.Row
mode,value=sys.argv[2:4]
if mode=='query':
 rows=[dict(row) for row in connection.execute(value)]
 print(json.dumps([{'results':rows}],ensure_ascii=False))
elif mode=='export':
 pathlib.Path(value).write_text('\n'.join(connection.iterdump()),encoding='utf-8')
elif mode=='execute':
 connection.executescript(value)
 connection.commit()
connection.close()
`

type sqliteFixture struct {
	python, database string
	writeCalls       int
	outcome          string
}

func archiveSHA(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func (fixture *sqliteFixture) execute(ctx context.Context, mode, value string) ([]byte, error) {
	return exec.CommandContext(ctx, fixture.python, "-X", "utf8", "-c", sqliteProgram, fixture.database, mode, value).Output()
}

func (fixture *sqliteFixture) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 1 && args[1] == "export" {
		return fixture.execute(ctx, "export", args[len(args)-1])
	}
	return fixture.execute(ctx, "query", args[len(args)-2])
}

func (fixture *sqliteFixture) Apply(ctx context.Context, _ string, plan cloudflare.MigrationPlan) error {
	fixture.writeCalls++
	if _, err := fixture.execute(ctx, "execute", strings.Join(plan.Statements, ";\n")+";"); err != nil {
		return err
	}
	if fixture.outcome == "lost" {
		return errors.New("response lost after commit; sensitive diagnostic")
	}
	if fixture.outcome == "corrupt" {
		_, err := fixture.execute(ctx, "execute", "DELETE FROM custom_data;")
		return err
	}
	return nil
}

func protectedFixture(t *testing.T) (protectedCloud, *sqliteFixture, cloudflare.MigrationPlan, string) {
	t.Helper()
	python := os.Getenv("EASYPROXY_TEST_PYTHON")
	if python == "" {
		var err error
		python, err = exec.LookPath("python")
		if err != nil {
			t.Skip("real SQLite integration requires EASYPROXY_TEST_PYTHON or python on PATH")
		}
	}
	root := filepath.Join("..", "..", "..", "..")
	loaded, err := topology.Load(filepath.Join(root, "topology.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	fixture := &sqliteFixture{python: python, database: filepath.Join(directory, "source.sqlite")}
	initial, err := os.ReadFile(filepath.Join(root, "upstreams", "misub", "migrations", "0001_initial.sql"))
	if err != nil {
		t.Fatal(err)
	}
	seed := `INSERT INTO subscriptions(id,data) VALUES('main','[{"id":"user-source","kind":"connector"}]');
INSERT INTO profiles(id,data) VALUES('main','[{"id":"user-profile"}]');
INSERT INTO settings(key,value) VALUES('main','{"mytoken":"synthetic"}');
CREATE TABLE custom_data(id INTEGER PRIMARY KEY, value TEXT);
INSERT INTO custom_data VALUES(1,'中文数据'),(2,'must survive');`
	if _, err := fixture.execute(context.Background(), "execute", string(initial)+seed); err != nil {
		t.Fatal("SQLite fixture initialization failed: ", err)
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	args := []string{"manifest", "build", "--topology", filepath.Join(root, "topology.example.yaml"),
		"--output", manifestPath, "--repo-root", directory, "--root-commit", strings.Repeat("a", 40),
		"--submodule-commit", "upstreams/misub=" + strings.Repeat("b", 40),
		"--submodule-commit", "upstreams/aggregator=" + strings.Repeat("c", 40),
		"--submodule-commit", "upstreams/ech-workers=" + strings.Repeat("d", 40),
		"--resource-id", "pages=easyproxy-misub-pages", "--resource-id", "d1=11111111-2222-3333-4444-555555555555"}
	var stdout, stderr bytes.Buffer
	if Run(args, &stdout, &stderr) != 0 {
		t.Fatal("fixture manifest build failed: ", stderr.String())
	}
	plan, err := cloudflare.LoadReviewedMigrations(filepath.Join(root, "upstreams", "misub", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EASYPROXY_BACKUP_PASSPHRASE", "integration-test-passphrase")
	t.Setenv("MISUB_ADMIN_PASSWORD", "") // The test explicitly authorizes D1 fallback.
	state := cloudflare.State{SchemaVersion: 1, DeploymentName: loaded.DeploymentName, D1Binding: "MISUB_DB",
		Pages: cloudflare.ResourceState{Name: "easyproxy-misub-pages", ID: "easyproxy-misub-pages"},
		D1:    cloudflare.ResourceState{Name: "easyproxy-misub-d1", ID: "11111111-2222-3333-4444-555555555555"}}
	return protectedCloud{Topology: loaded, State: state, D1: cloudflare.D1{Runner: fixture}}, fixture, plan, manifestPath
}

func TestProtectedMigrateRealSQLiteAgeAndRestore(t *testing.T) {
	cloud, fixture, plan, manifestPath := protectedFixture(t)
	output := filepath.Join(t.TempDir(), "before.age")
	before, err := cloud.D1.Snapshot(context.Background(), cloud.State.D1.Name)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := protectedMigrate(context.Background(), cloud, manifestPath, "", output, true, plan, fixture, "")
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.BackupCreated || !evidence.DataPreserved || !evidence.Verified || fixture.writeCalls != 1 ||
		len(evidence.BackupSHA256) != 64 || evidence.BeforeSHA256 == evidence.AfterSHA256 {
		t.Fatal("protected migration did not produce complete safe evidence")
	}
	body, _ := json.Marshal(evidence)
	if bytes.Contains(body, []byte("mytoken")) || bytes.Contains(body, []byte("中文")) {
		t.Fatal("business data leaked into evidence")
	}
	extracted, err := backup.ExtractEncrypted(output, t.TempDir(), os.Getenv("EASYPROXY_BACKUP_PASSPHRASE"))
	if err != nil || verifyExtractedBackup(cloud, extracted) != nil {
		t.Fatal("existing backup format/identity verification failed: ", err)
	}
	restored := &sqliteFixture{python: fixture.python, database: filepath.Join(t.TempDir(), "restored.sqlite")}
	sql, err := os.ReadFile(extracted.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.execute(context.Background(), "execute", string(sql)); err != nil {
		t.Fatal("real restore failed: ", err)
	}
	after, err := (cloudflare.D1{Runner: restored}).Snapshot(context.Background(), "restored")
	if err != nil || !cloudflare.EqualSnapshot(before, after) {
		t.Fatal("encrypted pre-migration backup did not restore its original snapshot")
	}
	image, err := (cloudflare.D1{Runner: restored}).CapturePreservation(context.Background(), "restored")
	if err != nil || len(image.Tables["custom_data"]) != 2 {
		t.Fatal("custom table or Unicode data was lost from backup")
	}
}

func TestProtectedMigrateBackupFailurePreventsMutation(t *testing.T) {
	cloud, fixture, plan, manifestPath := protectedFixture(t)
	t.Setenv("EASYPROXY_BACKUP_PASSPHRASE", "")
	_, err := protectedMigrate(context.Background(), cloud, manifestPath, "", filepath.Join(t.TempDir(), "before.age"), true, plan, fixture, "")
	if err == nil || err.Error() != "EASYPROXY_MIGRATION_BACKUP_FAILED" || fixture.writeCalls != 0 {
		t.Fatalf("backup failure did not gate mutations: %v calls=%d", err, fixture.writeCalls)
	}
}

func TestProtectedMigrateConsumesRetainedBackupWithoutCreatingAnother(t *testing.T) {
	cloud, fixture, plan, manifestPath := protectedFixture(t)
	output := filepath.Join(t.TempDir(), "retained.age")
	if err := createCloudBackup(context.Background(), cloud, manifestPath, "", output, true); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(output)
	evidence, err := protectedMigrate(context.Background(), cloud, manifestPath, "", output, false, plan, fixture, archiveSHA(t, output))
	if err != nil || !evidence.Verified || fixture.writeCalls != 1 {
		t.Fatalf("retained backup path failed: %v", err)
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("migration rewrote a retained backup")
	}
}

func TestProtectedMigrateRejectsStaleRetainedCustomTableData(t *testing.T) {
	cloud, fixture, plan, manifestPath := protectedFixture(t)
	output := filepath.Join(t.TempDir(), "stale.age")
	if err := createCloudBackup(context.Background(), cloud, manifestPath, "", output, true); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.execute(context.Background(), "execute", "INSERT INTO custom_data VALUES(3,'changed after backup');"); err != nil {
		t.Fatal(err)
	}
	_, err := protectedMigrate(context.Background(), cloud, manifestPath, "", output, false, plan, fixture, archiveSHA(t, output))
	if err == nil || err.Error() != "EASYPROXY_MIGRATION_SOURCE_CHANGED" || fixture.writeCalls != 0 {
		t.Fatalf("stale custom-table backup allowed writes: %v", err)
	}
}

func TestProtectedMigrateRejectsReplacementArchiveBeforeWriting(t *testing.T) {
	cloud, fixture, plan, manifestPath := protectedFixture(t)
	directory := t.TempDir()
	first, second := filepath.Join(directory, "retained.age"), filepath.Join(directory, "replacement.age")
	for _, path := range []string{first, second} {
		if err := createCloudBackup(context.Background(), cloud, manifestPath, "", path, true); err != nil {
			t.Fatal(err)
		}
	}
	expected := archiveSHA(t, first)
	replacement, _ := os.ReadFile(second)
	if err := os.WriteFile(first, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := protectedMigrate(context.Background(), cloud, manifestPath, "", first, false, plan, fixture, expected)
	if err == nil || err.Error() != "EASYPROXY_MIGRATION_BACKUP_FAILED" || fixture.writeCalls != 0 {
		t.Fatalf("unretained replacement allowed writes: %v calls=%d", err, fixture.writeCalls)
	}
}

func TestProtectedMigrateUnknownAndDataLossNeverReplayOrRestore(t *testing.T) {
	for _, outcome := range []string{"lost", "corrupt"} {
		t.Run(outcome, func(t *testing.T) {
			cloud, fixture, plan, manifestPath := protectedFixture(t)
			fixture.outcome = outcome
			output := filepath.Join(t.TempDir(), "before.age")
			_, err := protectedMigrate(context.Background(), cloud, manifestPath, "", output, true, plan, fixture, "")
			want := "EASYPROXY_MIGRATION_WRITE_UNKNOWN"
			if outcome == "corrupt" {
				want = "EASYPROXY_MIGRATION_DATA_NOT_PRESERVED"
			}
			if err == nil || err.Error() != want || fixture.writeCalls != 1 {
				t.Fatalf("unexpected failure semantics: %v calls=%d", err, fixture.writeCalls)
			}
			if _, err := os.Stat(output); err != nil {
				t.Fatal("failure discarded encrypted recovery backup")
			}
			image, err := cloud.D1.CapturePreservation(context.Background(), cloud.State.D1.Name)
			if err != nil || cloudflare.ValidateReviewedSchema(image, true) != nil {
				t.Fatal("unknown write was incorrectly rolled back/replayed")
			}
			if outcome == "corrupt" && len(image.Tables["custom_data"]) != 0 {
				t.Fatal("post-verification failure silently restored data")
			}
		})
	}
}

func TestCloudMigrateCLIRejectsIncompleteRequestBeforeProviderAccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if Run([]string{"cloud", "migrate"}, &stdout, &stderr) == 0 || !strings.Contains(stderr.String(), "EASYPROXY_MIGRATION_ARGUMENTS_REJECTED") {
		t.Fatal("incomplete migrate request accepted")
	}
}
