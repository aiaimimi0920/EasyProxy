package cloudflare

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func completeImage() PreservationImage {
	image := PreservationImage{Objects: map[string]string{}, Tables: map[string][]string{}}
	for _, table := range []string{"subscriptions", "profiles", "settings", "cron_executions", "schema_migrations", "user_table"} {
		image.Objects["table:"+table] = "unchanged schema"
		image.Tables[table] = []string{}
	}
	image.Tables["schema_migrations"] = []string{`{"migration_id":1,"name":"0001_initial","applied_at":"same"}`, `{"migration_id":2,"name":"0002_cron_executions","applied_at":"same"}`}
	image.Tables["user_table"] = []string{`{"id":1,"value":"retained"}`}
	image.Tables["subscriptions"] = []string{`{"id":"main","data":"[]"}`}
	return image
}

func copyImage(image PreservationImage) PreservationImage {
	body, _ := json.Marshal(image)
	var value PreservationImage
	_ = json.Unmarshal(body, &value)
	return value
}

func TestVerifyExpansionPreservesEveryExistingTableAndSchema(t *testing.T) {
	before := completeImage()
	delete(before.Objects, "table:cron_executions")
	delete(before.Tables, "cron_executions")
	before.Tables["schema_migrations"] = before.Tables["schema_migrations"][:1]
	after := completeImage()
	if err := VerifyExpansion(before, after); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"business-row", "custom-row", "table", "index", "old-migration", "unknown-migration", "missing-migration", "new-populated-table", "new-trigger"} {
		t.Run(change, func(t *testing.T) {
			candidate := copyImage(after)
			switch change {
			case "business-row":
				candidate.Tables["subscriptions"] = []string{}
			case "custom-row":
				candidate.Tables["user_table"] = []string{}
			case "table":
				delete(candidate.Objects, "table:profiles")
			case "index":
				candidate.Objects["table:settings"] = "changed schema"
			case "old-migration":
				candidate.Tables["schema_migrations"][0] = `{"migration_id":1,"name":"0001_initial","applied_at":"changed"}`
			case "unknown-migration":
				candidate.Tables["schema_migrations"] = append(candidate.Tables["schema_migrations"], `{"migration_id":3,"name":"new"}`)
			case "missing-migration":
				candidate.Tables["schema_migrations"] = candidate.Tables["schema_migrations"][:1]
			case "new-populated-table":
				candidate.Tables["cron_executions"] = []string{`{"id":"unexpected"}`}
			case "new-trigger":
				candidate.Objects["trigger:surprise"] = "trigger"
			}
			if err := VerifyExpansion(before, candidate); err == nil {
				t.Fatal("unsafe mutation accepted")
			}
		})
	}
}

func TestCapturePreservationQuotesTableNamesAndCanonicalizesRows(t *testing.T) {
	var commands []string
	runner := RunnerFunc(func(_ context.Context, args ...string) ([]byte, error) {
		command := args[len(args)-2]
		commands = append(commands, command)
		if len(commands) == 1 {
			return []byte(`[{"results":[{"type":"table","name":"custom\"table","tbl_name":"custom\"table","sql":"CREATE TABLE"},{"type":"table","name":"_cf_KV","sql":"private"}]}]`), nil
		}
		return []byte(`[{"results":[{"id":2},{"id":1}]}]`), nil
	})
	image, err := (D1{Runner: runner}).CapturePreservation(context.Background(), "db")
	if err != nil || len(image.Tables) != 1 || image.Tables[`custom"table`][0] != `{"id":1}` || commands[1] != `SELECT * FROM "custom""table"` {
		t.Fatalf("capture failed: %v calls=%v", err, commands)
	}
	if len(image.Digest()) != 64 || strings.Contains(image.Digest(), "private") {
		t.Fatal("invalid public digest")
	}
}

func TestCapturePreservationIncludesAutoincrementSequence(t *testing.T) {
	runner := &d1Runner{responses: [][]byte{
		[]byte(`[{"results":[{"type":"table","name":"sqlite_sequence","sql":"CREATE TABLE sqlite_sequence(name,seq)"}]}]`),
		[]byte(`[{"results":[{"name":"custom_data","seq":42}]}]`),
	}}
	image, err := (D1{Runner: runner}).CapturePreservation(context.Background(), "db")
	if err != nil || len(image.Tables["sqlite_sequence"]) != 1 {
		t.Fatal("stateful autoincrement counter was excluded")
	}
	changed := copyImage(image)
	changed.Tables["sqlite_sequence"] = []string{`{"name":"custom_data","seq":43}`}
	if VerifyExpansion(image, changed) == nil {
		t.Fatal("autoincrement counter change accepted")
	}
}
