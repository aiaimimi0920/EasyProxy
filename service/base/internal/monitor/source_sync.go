package monitor

import "time"

// SourceSyncStatus reports the health and current activation state of runtime source sync.
type SourceSyncStatus struct {
	Enabled                bool      `json:"enabled"`
	ManifestURL            string    `json:"manifest_url"`
	ManifestHealthy        bool      `json:"manifest_healthy"`
	LastSync               time.Time `json:"last_sync"`
	LastSuccess            time.Time `json:"last_success"`
	LastError              string    `json:"last_error,omitempty"`
	FallbackActive         bool      `json:"fallback_active"`
	LocalSourceCount       int       `json:"local_source_count"`
	ManifestSourceCount    int       `json:"manifest_source_count"`
	FallbackSourceCount    int       `json:"fallback_source_count"`
	ConnectorSourceCount   int       `json:"connector_source_count"`
	ConnectorInstanceCount int       `json:"connector_instance_count"`
}
