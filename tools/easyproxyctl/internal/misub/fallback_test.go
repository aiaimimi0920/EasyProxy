package misub

import (
	"encoding/json"
	"testing"
)

func TestBuildExportFromStoragePreservesAllSourceKinds(t *testing.T) {
	value, err := BuildExportFromStorage(StorageData{
		Sources:  json.RawMessage(`[{"id":"s"},{"id":"p","kind":"proxy_uri"},{"id":"c","kind":"connector"}]`),
		Profiles: json.RawMessage(`[{"id":"profile"}]`), Settings: json.RawMessage(`{"mytoken":"x"}`),
	}, 1, ResourceIdentity{DeploymentName: "demo", PagesProject: "pages", D1DatabaseID: "db", D1Binding: "MISUB_DB"})
	if err != nil {
		t.Fatal(err)
	}
	if value.Counts["sources"] != 3 || value.Counts["subscriptions"] != 1 || value.Counts["proxyUris"] != 1 || value.Counts["connectors"] != 1 {
		t.Fatalf("unexpected counts: %+v", value.Counts)
	}
	if value.D1DatabaseID != "db" || value.LogicalDataSHA256 == "" {
		t.Fatalf("unexpected identity/checksum: %+v", value)
	}
}
