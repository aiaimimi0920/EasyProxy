package misub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type StorageData struct {
	Sources  json.RawMessage
	Profiles json.RawMessage
	Settings json.RawMessage
	Cron     json.RawMessage
}

type ResourceIdentity struct {
	DeploymentName string
	PagesProject   string
	D1DatabaseID   string
	D1Binding      string
}

func BuildExportFromStorage(storage StorageData, schemaVersion int, identity ResourceIdentity) (Export, error) {
	var sources, profiles []interface{}
	var settings map[string]interface{}
	if err := json.Unmarshal(storage.Sources, &sources); err != nil {
		return Export{}, errors.New("D1 sources value is not a JSON array")
	}
	if err := json.Unmarshal(storage.Profiles, &profiles); err != nil {
		return Export{}, errors.New("D1 profiles value is not a JSON array")
	}
	if err := json.Unmarshal(storage.Settings, &settings); err != nil {
		return Export{}, errors.New("D1 settings value is not a JSON object")
	}
	var cron interface{}
	if len(storage.Cron) != 0 {
		if err := json.Unmarshal(storage.Cron, &cron); err != nil {
			return Export{}, errors.New("D1 cron value is not valid JSON")
		}
	}
	subscriptions, proxyURIs, connectors := classifySources(sources)
	manualNodes := append(append([]interface{}{}, proxyURIs...), connectors...)
	data := map[string]interface{}{
		"sources": sources, "subscriptions": subscriptions, "proxyUris": proxyURIs,
		"connectors": connectors, "manualNodes": manualNodes, "profiles": profiles,
		"settings": settings, "cron": map[string]interface{}{"lastExecution": cron},
	}
	canonical, err := json.Marshal(data)
	if err != nil {
		return Export{}, err
	}
	digest := sha256.Sum256(canonical)
	checksum := hex.EncodeToString(digest[:])
	counts := map[string]int{
		"sources": len(sources), "subscriptions": len(subscriptions), "proxyUris": len(proxyURIs),
		"connectors": len(connectors), "profiles": len(profiles), "settings": len(settings),
	}
	if cron != nil {
		counts["cronExecutions"] = 1
	} else {
		counts["cronExecutions"] = 0
	}
	payload := map[string]interface{}{
		"format": "misub-logical-backup", "formatVersion": 1, "applicationVersion": "d1-direct-export",
		"schemaVersion": schemaVersion, "storageType": "d1", "containsSensitiveData": true,
		"resourceIdentity": map[string]interface{}{
			"deploymentName": identity.DeploymentName, "pagesProject": identity.PagesProject,
			"d1DatabaseId": identity.D1DatabaseID, "d1Binding": identity.D1Binding,
		},
		"counts": counts, "integrity": map[string]string{"algorithm": "SHA-256", "canonicalDataSha256": checksum}, "data": data,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Export{}, err
	}
	return ValidateExport(raw)
}

func classifySources(sources []interface{}) (subscriptions, proxyURIs, connectors []interface{}) {
	for _, source := range sources {
		kind := ""
		if object, ok := source.(map[string]interface{}); ok {
			kind, _ = object["kind"].(string)
		}
		switch kind {
		case "connector":
			connectors = append(connectors, source)
		case "proxy_uri":
			proxyURIs = append(proxyURIs, source)
		default:
			subscriptions = append(subscriptions, source)
		}
	}
	return subscriptions, proxyURIs, connectors
}
