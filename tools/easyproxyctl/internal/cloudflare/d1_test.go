package cloudflare

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type d1Runner struct {
	responses [][]byte
	calls     [][]string
}

func (runner *d1Runner) Run(_ context.Context, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, append([]string{}, args...))
	result := runner.responses[0]
	runner.responses = runner.responses[1:]
	return result, nil
}

func TestD1SnapshotSupportsLegacyAndMigratedTables(t *testing.T) {
	runner := &d1Runner{responses: [][]byte{
		[]byte(`[{"results":[{"name":"profiles"},{"name":"schema_migrations"},{"name":"subscriptions"}]}]`),
		[]byte(`[{"results":[{"id":"profiles","data":"[]"}]}]`),
		[]byte(`[{"results":[{"migration_id":1,"name":"0001"},{"migration_id":2,"name":"0002"}]}]`),
		[]byte(`[{"results":[{"id":"subscriptions","data":"[]"}]}]`),
	}}
	result, err := (D1{Runner: runner}).Snapshot(context.Background(), "demo-db")
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 2 || result.Rows["subscriptions"] != 1 || len(runner.calls) != 4 {
		t.Fatalf("unexpected snapshot: %+v calls=%d", result, len(runner.calls))
	}
	if !strings.Contains(strings.Join(runner.calls[2], " "), "ORDER BY migration_id") {
		t.Fatalf("migration query must be deterministic: %v", runner.calls[2])
	}
}

func TestD1ExportRequiresCreatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "database.sql")
	runner := RunnerFunc(func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, os.WriteFile(path, []byte("CREATE TABLE x(y);\n"), 0o600)
	})
	if err := (D1{Runner: runner}).Export(context.Background(), "db", path); err != nil {
		t.Fatal(err)
	}
}

type RunnerFunc func(context.Context, ...string) ([]byte, error)

func (function RunnerFunc) Run(ctx context.Context, args ...string) ([]byte, error) {
	return function(ctx, args...)
}

func TestEqualSnapshotChecksRows(t *testing.T) {
	left := D1Snapshot{SHA256: "a", SchemaVersion: 2, Rows: map[string]int{"profiles": 1}}
	right := D1Snapshot{SHA256: "a", SchemaVersion: 2, Rows: map[string]int{"profiles": 2}}
	if EqualSnapshot(left, right) {
		t.Fatal("different row counts must not compare equal")
	}
}

func TestD1ReadMiSubStorageDefaultsMissingValues(t *testing.T) {
	runner := &d1Runner{responses: [][]byte{
		[]byte(`[{"results":[{"name":"subscriptions"},{"name":"profiles"},{"name":"settings"},{"name":"cron_executions"}]}]`),
		[]byte(`[{"results":[{"data":"[{\"id\":\"source\"}]"}]}]`),
		[]byte(`[{"results":[]}]`),
		[]byte(`[{"results":[{"value":"{\"mytoken\":\"x\"}"}]}]`),
		[]byte(`[{"results":[]}]`),
	}}
	value, err := (D1{Runner: runner}).ReadMiSubStorage(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if string(value.Profiles) != "[]" || string(value.Cron) != "" || !strings.Contains(string(value.Sources), "source") || !value.SourcesPresent || value.ProfilesPresent {
		t.Fatalf("unexpected storage: %+v", value)
	}
}

func TestD1ReadMiSubStorageDoesNotIgnoreCronQueryFailure(t *testing.T) {
	calls := 0
	runner := RunnerFunc(func(_ context.Context, _ ...string) ([]byte, error) {
		calls++
		if calls == 5 {
			return nil, errors.New("permission denied")
		}
		responses := [][]byte{
			[]byte(`[{"results":[{"name":"subscriptions"},{"name":"profiles"},{"name":"settings"},{"name":"cron_executions"}]}]`),
			[]byte(`[{"results":[]}]`), []byte(`[{"results":[]}]`), []byte(`[{"results":[]}]`),
		}
		return responses[calls-1], nil
	})
	if _, err := (D1{Runner: runner}).ReadMiSubStorage(context.Background(), "db"); err == nil {
		t.Fatal("cron query error was ignored")
	}
}
