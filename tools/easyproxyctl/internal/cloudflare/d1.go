package cloudflare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
)

var snapshotTables = []string{"cron_executions", "profiles", "schema_migrations", "settings", "subscriptions"}

type D1Snapshot struct {
	SHA256        string         `json:"sha256"`
	SchemaVersion int            `json:"schema_version"`
	Rows          map[string]int `json:"rows"`
}

type D1 struct {
	Runner Runner
}

type MiSubStorage struct {
	Sources         json.RawMessage
	Profiles        json.RawMessage
	Settings        json.RawMessage
	Cron            json.RawMessage
	SourcesPresent  bool
	ProfilesPresent bool
	SettingsPresent bool
	CronPresent     bool
}

func (d D1) Export(ctx context.Context, database, output string) error {
	if database == "" || output == "" {
		return errors.New("D1 database and export output are required")
	}
	if _, err := d.Runner.Run(ctx, "d1", "export", database, "--remote", "--output", output); err != nil {
		return err
	}
	info, err := os.Stat(output)
	if err != nil {
		return fmt.Errorf("D1 export did not create output: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("D1 export created an empty SQL file")
	}
	return nil
}

func (d D1) Restore(ctx context.Context, database, input string) error {
	if database == "" || input == "" {
		return errors.New("D1 database and restore input are required")
	}
	_, err := d.Runner.Run(ctx, "d1", "execute", database, "--remote", "--file", input)
	return err
}

func (d D1) ApplyMigrations(ctx context.Context, database string) error {
	if database == "" {
		return errors.New("D1 database is required")
	}
	_, err := d.Runner.Run(ctx, "d1", "migrations", "apply", database, "--remote")
	return err
}

func (d D1) Snapshot(ctx context.Context, database string) (D1Snapshot, error) {
	tableRows, err := d.query(ctx, database, "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return D1Snapshot{}, err
	}
	existing := make(map[string]bool, len(tableRows))
	for _, row := range tableRows {
		if name, ok := row["name"].(string); ok {
			existing[name] = true
		}
	}
	content := make(map[string][]map[string]interface{})
	counts := make(map[string]int)
	schemaVersion := 0
	for _, table := range snapshotTables {
		if !existing[table] {
			continue
		}
		rows, err := d.query(ctx, database, snapshotQuery(table))
		if err != nil {
			return D1Snapshot{}, err
		}
		content[table] = rows
		counts[table] = len(rows)
		if table == "schema_migrations" {
			for _, row := range rows {
				if value := integerValue(row["migration_id"]); value > schemaVersion {
					schemaVersion = value
				}
			}
		}
	}
	canonical, err := json.Marshal(content)
	if err != nil {
		return D1Snapshot{}, err
	}
	digest := sha256.Sum256(canonical)
	return D1Snapshot{SHA256: hex.EncodeToString(digest[:]), SchemaVersion: schemaVersion, Rows: counts}, nil
}

func (d D1) ReadMiSubStorage(ctx context.Context, database string) (MiSubStorage, error) {
	tableRows, err := d.query(ctx, database, "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return MiSubStorage{}, err
	}
	existing := make(map[string]bool, len(tableRows))
	for _, row := range tableRows {
		if name, ok := row["name"].(string); ok {
			existing[name] = true
		}
	}
	queries := []struct {
		table, keyColumn, key, valueColumn string
		target                             *json.RawMessage
		present                            *bool
	}{
		{table: "subscriptions", keyColumn: "id", key: "main", valueColumn: "data"},
		{table: "profiles", keyColumn: "id", key: "main", valueColumn: "data"},
		{table: "settings", keyColumn: "key", key: "main", valueColumn: "value"},
		{table: "cron_executions", keyColumn: "id", key: "last", valueColumn: "data"},
	}
	storage := MiSubStorage{}
	queries[0].target = &storage.Sources
	queries[0].present = &storage.SourcesPresent
	queries[1].target = &storage.Profiles
	queries[1].present = &storage.ProfilesPresent
	queries[2].target = &storage.Settings
	queries[2].present = &storage.SettingsPresent
	queries[3].target = &storage.Cron
	queries[3].present = &storage.CronPresent
	for _, query := range queries {
		if !existing[query.table] {
			if query.table == "cron_executions" {
				continue
			}
			return MiSubStorage{}, fmt.Errorf("D1 is missing required table %s", query.table)
		}
		statement := fmt.Sprintf("SELECT %s FROM %s WHERE %s = '%s'", query.valueColumn, query.table, query.keyColumn, query.key)
		rows, err := d.query(ctx, database, statement)
		if err != nil {
			return MiSubStorage{}, err
		}
		if len(rows) == 0 {
			continue
		}
		value, ok := rows[0][query.valueColumn].(string)
		if !ok || !json.Valid([]byte(value)) {
			return MiSubStorage{}, fmt.Errorf("D1 %s value is not valid JSON", query.table)
		}
		*query.target = json.RawMessage(value)
		*query.present = true
	}
	if len(storage.Sources) == 0 {
		storage.Sources = json.RawMessage(`[]`)
	}
	if len(storage.Profiles) == 0 {
		storage.Profiles = json.RawMessage(`[]`)
	}
	if len(storage.Settings) == 0 {
		storage.Settings = json.RawMessage(`{}`)
	}
	return storage, nil
}

func (d D1) query(ctx context.Context, database, statement string) ([]map[string]interface{}, error) {
	output, err := d.Runner.Run(ctx, "d1", "execute", database, "--remote", "--command", statement, "--json")
	if err != nil {
		return nil, err
	}
	rows, ok := findRows(output)
	if !ok {
		return nil, errors.New("Wrangler D1 response does not contain result rows")
	}
	return rows, nil
}

func findRows(data []byte) ([]map[string]interface{}, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return rowsIn(value)
}

func rowsIn(value interface{}) ([]map[string]interface{}, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		if results, exists := typed["results"]; exists {
			if rows, ok := objectRows(results); ok {
				return rows, true
			}
		}
		for _, key := range []string{"result", "data"} {
			if nested, exists := typed[key]; exists {
				if rows, ok := rowsIn(nested); ok {
					return rows, true
				}
			}
		}
	case []interface{}:
		for _, nested := range typed {
			if rows, ok := rowsIn(nested); ok {
				return rows, true
			}
		}
	}
	return nil, false
}

func objectRows(value interface{}) ([]map[string]interface{}, bool) {
	items, ok := value.([]interface{})
	if !ok {
		return nil, false
	}
	rows := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok {
			return nil, false
		}
		rows = append(rows, row)
	}
	return rows, true
}

func snapshotQuery(table string) string {
	keys := map[string]string{"schema_migrations": "migration_id", "settings": "key"}
	key := keys[table]
	if key == "" {
		key = "id"
	}
	return fmt.Sprintf("SELECT * FROM %s ORDER BY %s", table, key)
}

func integerValue(value interface{}) int {
	switch typed := value.(type) {
	case json.Number:
		result, _ := strconv.Atoi(string(typed))
		return result
	case float64:
		return int(typed)
	case string:
		result, _ := strconv.Atoi(typed)
		return result
	}
	return 0
}

func EqualSnapshot(left, right D1Snapshot) bool {
	if left.SHA256 != right.SHA256 || left.SchemaVersion != right.SchemaVersion || len(left.Rows) != len(right.Rows) {
		return false
	}
	keys := make([]string, 0, len(left.Rows))
	for key := range left.Rows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if left.Rows[key] != right.Rows[key] {
			return false
		}
	}
	return true
}
