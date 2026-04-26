// Package store defines the data persistence layer for easy_proxies.
// It abstracts all storage operations behind the Store interface,
// allowing different backend implementations (e.g., SQLite).
package store

import (
	"context"
	"time"
)

// Store defines all data storage operations.
type Store interface {
	// --- Node operations ---

	// ListNodes returns nodes matching the given filter.
	ListNodes(ctx context.Context, filter NodeFilter) ([]Node, error)

	// GetNode returns a node by its ID.
	GetNode(ctx context.Context, id int64) (*Node, error)

	// GetNodeByURI returns a node by its URI.
	GetNodeByURI(ctx context.Context, uri string) (*Node, error)

	// GetNodeByName returns a node by its name.
	GetNodeByName(ctx context.Context, name string) (*Node, error)

	// CreateNode inserts a new node and returns its assigned ID.
	CreateNode(ctx context.Context, node *Node) error

	// UpdateNode updates an existing node by ID.
	UpdateNode(ctx context.Context, node *Node) error

	// DeleteNode removes a node by ID (cascading to stats/timeline).
	DeleteNode(ctx context.Context, id int64) error

	// DeleteNodesBySource removes all nodes with the given source.
	// Returns the number of deleted rows.
	DeleteNodesBySource(ctx context.Context, source string) (int64, error)

	// BulkUpsertNodes inserts or updates nodes in a single transaction.
	// Nodes are matched by URI for upsert logic.
	BulkUpsertNodes(ctx context.Context, nodes []Node) error

	// CountNodes returns the total number of nodes matching the filter.
	CountNodes(ctx context.Context, filter NodeFilter) (int64, error)

	// --- Node stats ---

	// GetNodeStats returns runtime statistics for a node.
	GetNodeStats(ctx context.Context, nodeID int64) (*NodeStats, error)

	// UpsertNodeStats creates or updates node statistics.
	UpsertNodeStats(ctx context.Context, stats *NodeStats) error

	// RecordSuccess increments success count and updates latency.
	RecordSuccess(ctx context.Context, nodeID int64, latencyMs int64) error

	// RecordFailure increments failure count and records the error.
	RecordFailure(ctx context.Context, nodeID int64, errMsg string) error

	// SetBlacklist marks a node as blacklisted until the given time.
	SetBlacklist(ctx context.Context, nodeID int64, until time.Time) error

	// ClearBlacklist removes the blacklist flag for a node.
	ClearBlacklist(ctx context.Context, nodeID int64) error

	// ClearAllBlacklists removes all blacklist flags.
	ClearAllBlacklists(ctx context.Context) error

	// BatchUpdateStats applies multiple stat updates in a single transaction.
	BatchUpdateStats(ctx context.Context, updates []StatsUpdate) error

	// GetAllNodeStats returns stats for all nodes (used for bulk restore).
	GetAllNodeStats(ctx context.Context) (map[int64]*NodeStats, error)

	// --- Timeline ---

	// AppendTimeline adds an event to a node's timeline.
	AppendTimeline(ctx context.Context, nodeID int64, event TimelineEvent) error

	// GetTimeline returns the most recent events for a node.
	GetTimeline(ctx context.Context, nodeID int64, limit int) ([]TimelineEvent, error)

	// CleanupTimeline removes old timeline events, keeping only the most recent per node.
	CleanupTimeline(ctx context.Context, keepPerNode int) error

	// --- Sessions ---

	// CreateSession stores a new session.
	CreateSession(ctx context.Context, session *Session) error

	// GetSession retrieves a session by token.
	GetSession(ctx context.Context, token string) (*Session, error)

	// DeleteSession removes a session by token.
	DeleteSession(ctx context.Context, token string) error

	// CleanupExpiredSessions removes all expired sessions.
	CleanupExpiredSessions(ctx context.Context) error

	// --- Subscription status ---

	// GetSubscriptionStatus returns the current subscription refresh status.
	GetSubscriptionStatus(ctx context.Context) (*SubscriptionStatus, error)

	// UpdateSubscriptionStatus creates or updates the subscription status.
	UpdateSubscriptionStatus(ctx context.Context, status *SubscriptionStatus) error

	// --- Lifecycle ---

	// Close releases all resources held by the store.
	Close() error

	// WithTx executes fn within a transaction. If fn returns an error,
	// the transaction is rolled back; otherwise it is committed.
	WithTx(ctx context.Context, fn func(tx Store) error) error
}

// --- Data models ---

// Node represents a proxy node stored in the database.
type Node struct {
	ID        int64     `json:"id"`
	URI       string    `json:"uri"`
	Name      string    `json:"name"`
	Source    string    `json:"source"` // inline, nodes_file, subscription, manual
	Port      uint16    `json:"port"`
	Username  string    `json:"username,omitempty"`
	Password  string    `json:"password,omitempty"`
	Region    string    `json:"region,omitempty"`
	Country   string    `json:"country,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NodeFilter specifies criteria for listing nodes.
type NodeFilter struct {
	Source  string // Filter by source (empty = all)
	Region  string // Filter by region (empty = all)
	Enabled *bool  // Filter by enabled status (nil = all)
	Limit   int    // Max results (0 = no limit)
	Offset  int    // Pagination offset
}

// NodeStats holds runtime statistics for a node.
type NodeStats struct {
	NodeID               int64     `json:"node_id"`
	FailureCount         int       `json:"failure_count"`
	SuccessCount         int64     `json:"success_count"`
	TrafficSuccessCount  int64     `json:"traffic_success_count"`
	Blacklisted          bool      `json:"blacklisted"`
	BlacklistedUntil     time.Time `json:"blacklisted_until"`
	LastError            string    `json:"last_error"`
	LastFailureAt        time.Time `json:"last_failure_at"`
	LastSuccessAt        time.Time `json:"last_success_at"`
	LastTrafficSuccessAt time.Time `json:"last_traffic_success_at"`
	LastProbeAt          time.Time `json:"last_probe_at"`
	LastProbeSuccessAt   time.Time `json:"last_probe_success_at"`
	LastLatencyMs        int64     `json:"last_latency_ms"` // -1 = untested
	Available            bool      `json:"available"`
	InitialCheckDone     bool      `json:"initial_check_done"`
	TotalUploadBytes     int64     `json:"total_upload_bytes"`
	TotalDownloadBytes   int64     `json:"total_download_bytes"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// StatsUpdate represents a batch update for node statistics.
type StatsUpdate struct {
	NodeID               int64
	FailureCount         int
	SuccessCount         int64
	TrafficSuccessCount  int64
	Blacklisted          bool
	BlacklistedUntil     time.Time
	LastError            string
	LastFailureAt        time.Time
	LastSuccessAt        time.Time
	LastTrafficSuccessAt time.Time
	LastProbeAt          time.Time
	LastProbeSuccessAt   time.Time
	LastLatencyMs        int64
	Available            bool
	InitialCheckDone     bool
	TotalUploadBytes     int64
	TotalDownloadBytes   int64
}

// TimelineEvent represents a single usage event for debug tracking.
type TimelineEvent struct {
	ID        int64     `json:"id"`
	NodeID    int64     `json:"node_id"`
	Success   bool      `json:"success"`
	LatencyMs int64     `json:"latency_ms"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Session represents a user authentication session.
type Session struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SubscriptionStatus represents subscription refresh status.
type SubscriptionStatus struct {
	LastRefresh  time.Time `json:"last_refresh"`
	NextRefresh  time.Time `json:"next_refresh"`
	NodeCount    int       `json:"node_count"`
	LastError    string    `json:"last_error"`
	RefreshCount int       `json:"refresh_count"`
	IsRefreshing bool      `json:"is_refreshing"`
	NodesHash    string    `json:"nodes_hash"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Node source constants (matching config.NodeSource values).
const (
	NodeSourceInline       = "inline"
	NodeSourceFile         = "nodes_file"
	NodeSourceSubscription = "subscription"
	NodeSourceManifest     = "manifest"
	NodeSourceFallback     = "fallback"
	NodeSourceManual       = "manual"
)

// IsPersistentNodeSource reports whether a stored node belongs to the local
// persisted config surface rather than the runtime-generated source-sync set.
func IsPersistentNodeSource(source string) bool {
	switch source {
	case "", NodeSourceInline, NodeSourceFile, NodeSourceManual:
		return true
	default:
		return false
	}
}

// IsRuntimeNodeSource reports whether a stored node is generated from
// subscriptions, manifest sources, or fallback sources at runtime.
func IsRuntimeNodeSource(source string) bool {
	switch source {
	case NodeSourceSubscription, NodeSourceManifest, NodeSourceFallback:
		return true
	default:
		return false
	}
}
