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
	SHA256             string         `json:"sha256"`
	SchemaVersion      int            `json:"schema_version"`
	Rows               map[string]int `json:"rows"`
	PreservationSHA256 string         `json:"preservation_sha256,omitempty"`
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
		{table: "settings", keyColumn: "key", key: "main", valueColumn: "value"},
		{table: "cron_executions", keyColumn: "id", key: "last", valueColumn: "data"},
	}
	storage := MiSubStorage{}
	// 当前 MiSub 按条目存储；旧版本的 main 行仍可能包含整个数组。
	for _, collection := range []struct {
		table   string
		target  *json.RawMessage
		present *bool
	}{{"subscriptions", &storage.Sources, &storage.SourcesPresent}, {"profiles", &storage.Profiles, &storage.ProfilesPresent}} {
		if !existing[collection.table] {
			return MiSubStorage{}, fmt.Errorf("D1 is missing required table %s", collection.table)
		}
		rows, err := d.query(ctx, database, "SELECT data FROM "+collection.table+" ORDER BY id")
		if err != nil {
			return MiSubStorage{}, err
		}
		value, err := collectionData(rows)
		if err != nil {
			return MiSubStorage{}, fmt.Errorf("D1 %s collection is invalid: %w", collection.table, err)
		}
		*collection.target, *collection.present = value, len(rows) > 0
	}
	queries[0].target = &storage.Settings
	queries[0].present = &storage.SettingsPresent
	queries[1].target = &storage.Cron
	queries[1].present = &storage.CronPresent
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

// 与 MiSub collection reader 一样合并逐项对象与旧数组；备份不能静默丢弃
// 无效项或冲突副本。全库 SQL 备份仍保存每一物理行，此处仅构造逻辑集合。
func collectionData(rows []map[string]interface{}) (json.RawMessage, error) {
	type entry struct {
		order float64
		body  json.RawMessage
	}
	items := []entry{}
	seen := map[string]json.RawMessage{}
	for _, row := range rows {
		raw, ok := row["data"].(string)
		if !ok {
			return nil, errors.New("data is not JSON text")
		}
		var value interface{}
		decoder := json.NewDecoder(bytes.NewBufferString(raw))
		decoder.UseNumber()
		if !json.Valid([]byte(raw)) || decoder.Decode(&value) != nil {
			return nil, errors.New("invalid JSON")
		}
		values, array := value.([]interface{})
		if !array {
			values = []interface{}{value}
		}
		for _, value := range values {
			object, ok := value.(map[string]interface{})
			if !ok {
				return nil, errors.New("item is not an object")
			}
			id, ok := object["id"].(string)
			if !ok || id == "" {
				return nil, errors.New("item has no ID")
			}
			body, err := json.Marshal(object)
			if err != nil {
				return nil, err
			}
			if prior, exists := seen[id]; exists {
				if !bytes.Equal(prior, body) {
					return nil, errors.New("conflicting duplicate ID")
				}
				continue
			}
			order := 0.0
			if rawOrder, exists := object["sortIndex"]; exists {
				number, ok := rawOrder.(json.Number)
				if !ok {
					return nil, errors.New("invalid sortIndex")
				}
				order, err = number.Float64()
				if err != nil {
					return nil, errors.New("invalid sortIndex")
				}
			}
			seen[id] = body
			items = append(items, entry{order, body})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].order < items[j].order })
	result := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		result = append(result, item.body)
	}
	return json.Marshal(result)
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
