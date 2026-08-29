package monitor

import (
	"errors"
	"sync"
	"time"
)

type proxyCompatCatalog struct {
	ProviderTypes         []proxyCompatProviderType     `json:"providerTypes"`
	RuntimeTemplates      []any                         `json:"runtimeTemplates"`
	StrategyProfiles      []proxyCompatStrategyProfile  `json:"strategyProfiles"`
	ProviderGroups        []proxyCompatProviderGroup    `json:"providerGroups"`
	BusinessStrategies    []proxyCompatBusinessStrategy `json:"businessStrategies"`
	DefaultStrategyModeID string                        `json:"defaultStrategyModeId,omitempty"`
	DefaultStrategyMode   *proxyCompatStrategyMode      `json:"defaultStrategyMode,omitempty"`
	SupportsStrategyMode  bool                          `json:"supportsStrategyMode"`
}

type proxyCompatSnapshot struct {
	ProviderTypes      []proxyCompatProviderType     `json:"providerTypes"`
	RuntimeTemplates   []any                         `json:"runtimeTemplates"`
	Instances          []proxyCompatProviderInstance `json:"instances"`
	Bindings           []proxyCompatBinding          `json:"bindings"`
	Strategies         []proxyCompatStrategyProfile  `json:"strategies"`
	CredentialSets     []any                         `json:"credentialSets"`
	CredentialBindings []any                         `json:"credentialBindings"`
	Leases             []proxyCompatLease            `json:"leases"`
	UsageRecords       []proxyCompatUsageRecord      `json:"usageRecords"`
	ServiceFeedback    []proxyCompatServiceFeedback  `json:"serviceFeedback"`
	UsageStats         []proxyCompatUsageStats       `json:"usageStats"`
}

type proxyCompatProviderType struct {
	Key                         string   `json:"key"`
	DisplayName                 string   `json:"displayName"`
	Description                 string   `json:"description"`
	SupportsDynamicProvisioning bool     `json:"supportsDynamicProvisioning"`
	DefaultStrategyKey          string   `json:"defaultStrategyKey"`
	Tags                        []string `json:"tags"`
}

type proxyCompatProviderGroup struct {
	Key              string   `json:"key"`
	DisplayName      string   `json:"displayName"`
	ProviderTypeKeys []string `json:"providerTypeKeys"`
	Description      string   `json:"description"`
}

type proxyCompatBusinessStrategy struct {
	ID                  string   `json:"id"`
	DisplayName         string   `json:"displayName"`
	Description         string   `json:"description"`
	ProviderGroupOrder  []string `json:"providerGroupOrder,omitempty"`
	FallbackProfileID   string   `json:"fallbackProfileId,omitempty"`
	FallbackStrategyKey string   `json:"fallbackStrategyKey,omitempty"`
}

type proxyCompatStrategyProfile struct {
	ID          string            `json:"id"`
	Key         string            `json:"key"`
	DisplayName string            `json:"displayName"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
}

type proxyCompatStrategyMode struct {
	Service                string   `json:"service"`
	ModeID                 string   `json:"modeId"`
	ProviderSelections     []string `json:"providerSelections"`
	EligibleProviderGroups []string `json:"eligibleProviderGroups"`
	ProviderGroupOrder     []string `json:"providerGroupOrder"`
	StrategyKey            string   `json:"strategyKey,omitempty"`
	Warnings               []string `json:"warnings"`
	Explain                []string `json:"explain"`
}

type proxyCompatProviderInstance struct {
	ID               string            `json:"id"`
	ProviderTypeKey  string            `json:"providerTypeKey"`
	DisplayName      string            `json:"displayName"`
	Status           string            `json:"status"`
	RuntimeKind      string            `json:"runtimeKind"`
	ConnectorKind    string            `json:"connectorKind"`
	Shared           bool              `json:"shared"`
	CostTier         string            `json:"costTier"`
	HealthScore      float64           `json:"healthScore"`
	AverageLatencyMs int64             `json:"averageLatencyMs"`
	ConnectionRef    string            `json:"connectionRef"`
	HostBindings     []string          `json:"hostBindings"`
	GroupKeys        []string          `json:"groupKeys"`
	Metadata         map[string]string `json:"metadata"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt"`
}

type proxyCompatBinding struct {
	HostID          string `json:"hostId"`
	ProviderTypeKey string `json:"providerTypeKey"`
	BindingMode     string `json:"bindingMode"`
	InstanceID      string `json:"instanceId"`
	GroupKey        string `json:"groupKey,omitempty"`
	UpdatedAt       string `json:"updatedAt"`
}

type proxyCompatCheckoutRequest struct {
	HostID                  string            `json:"hostId"`
	ProviderTypeKey         string            `json:"providerTypeKey,omitempty"`
	ProvisionMode           string            `json:"provisionMode"`
	BindingMode             string            `json:"bindingMode"`
	StrategyProfileID       string            `json:"strategyProfileId,omitempty"`
	ProviderStrategyModeID  string            `json:"providerStrategyModeId,omitempty"`
	ProviderGroupSelections []string          `json:"providerGroupSelections,omitempty"`
	PreferredInstanceID     string            `json:"preferredInstanceId,omitempty"`
	RuntimeTemplateID       string            `json:"runtimeTemplateId,omitempty"`
	GroupKey                string            `json:"groupKey,omitempty"`
	Protocol                string            `json:"protocol,omitempty"`
	TTLMinutes              int               `json:"ttlMinutes,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

type proxyCompatPlanResult struct {
	Request               proxyCompatCheckoutRequest  `json:"request"`
	ProviderType          proxyCompatProviderType     `json:"providerType"`
	Instance              proxyCompatProviderInstance `json:"instance"`
	Binding               proxyCompatBinding          `json:"binding"`
	StrategyProfile       *proxyCompatStrategyProfile `json:"strategyProfile,omitempty"`
	ReusedExistingBinding bool                        `json:"reusedExistingBinding"`
	RequiresProvisioning  bool                        `json:"requiresProvisioning"`
	StrategyMode          *proxyCompatStrategyMode    `json:"strategyMode,omitempty"`
}

type proxyCompatCheckoutResult struct {
	Lease        proxyCompatLease            `json:"lease"`
	Instance     proxyCompatProviderInstance `json:"instance"`
	Binding      proxyCompatBinding          `json:"binding"`
	StrategyMode *proxyCompatStrategyMode    `json:"strategyMode,omitempty"`
}

type proxyCompatLease struct {
	ID                 string            `json:"id"`
	HostID             string            `json:"hostId"`
	ProviderTypeKey    string            `json:"providerTypeKey"`
	ProviderInstanceID string            `json:"providerInstanceId"`
	ProxyURL           string            `json:"proxyUrl"`
	Host               string            `json:"host"`
	Port               int               `json:"port"`
	Protocol           string            `json:"protocol"`
	Username           string            `json:"username,omitempty"`
	Password           string            `json:"password,omitempty"`
	Status             string            `json:"status"`
	CreatedAt          string            `json:"createdAt"`
	ExpiresAt          string            `json:"expiresAt,omitempty"`
	ReleasedAt         string            `json:"releasedAt,omitempty"`
	Metadata           map[string]string `json:"metadata"`
}

type proxyCompatUsageReport struct {
	LeaseID         string `json:"leaseId"`
	Success         bool   `json:"success"`
	LatencyMs       int64  `json:"latencyMs,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ReportedAt      string `json:"reportedAt,omitempty"`
	ServiceKey      string `json:"serviceKey,omitempty"`
	Stage           string `json:"stage,omitempty"`
	FailureClass    string `json:"failureClass,omitempty"`
	RouteConfidence string `json:"routeConfidence,omitempty"`
}

type proxyCompatUsageRecord struct {
	ID                 string `json:"id"`
	LeaseID            string `json:"leaseId"`
	ProviderInstanceID string `json:"providerInstanceId"`
	SelectedNodeTag    string `json:"selectedNodeTag,omitempty"`
	ReportedAt         string `json:"reportedAt"`
	Success            bool   `json:"success"`
	LatencyMs          int64  `json:"latencyMs,omitempty"`
	ErrorCode          string `json:"errorCode,omitempty"`
	ServiceKey         string `json:"serviceKey,omitempty"`
	Stage              string `json:"stage,omitempty"`
	FailureClass       string `json:"failureClass,omitempty"`
	RouteConfidence    string `json:"routeConfidence,omitempty"`
}

type proxyCompatServiceFeedback struct {
	HostID              string `json:"hostId"`
	NodeTag             string `json:"nodeTag"`
	FeedbackKey         string `json:"feedbackKey,omitempty"`
	ScopeKind           string `json:"scopeKind,omitempty"`
	ScopeValue          string `json:"scopeValue,omitempty"`
	Penalty             int    `json:"penalty"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	CooldownUntil       string `json:"cooldownUntil,omitempty"`
	LastErrorClass      string `json:"lastErrorClass,omitempty"`
	LastErrorCode       string `json:"lastErrorCode,omitempty"`
	LastReportedAt      string `json:"lastReportedAt,omitempty"`
}

type proxyCompatUsageStats struct {
	ServiceKey                 string  `json:"serviceKey"`
	Stage                      string  `json:"stage"`
	NodeTag                    string  `json:"nodeTag,omitempty"`
	ServiceTotal               int     `json:"serviceTotal"`
	ServiceFailures            int     `json:"serviceFailures"`
	ServiceFailureRate         float64 `json:"serviceFailureRate"`
	ServiceSentinelFailures    int     `json:"serviceSentinelFailures"`
	ServiceSentinelFailureRate float64 `json:"serviceSentinelFailureRate"`
	NodeTotal                  int     `json:"nodeTotal"`
	NodeFailures               int     `json:"nodeFailures"`
	NodeSuccesses              int     `json:"nodeSuccesses"`
	NodeSuccessRate            float64 `json:"nodeSuccessRate"`
	NodeFailureRate            float64 `json:"nodeFailureRate"`
	NodeSentinelFailures       int     `json:"nodeSentinelFailures"`
	NodeSentinelFailureRate    float64 `json:"nodeSentinelFailureRate"`
}

type proxyCompatState struct {
	mu               sync.RWMutex
	leases           map[string]*proxyCompatLeaseState
	usageRecords     []proxyCompatUsageRecord
	nodeReservations map[string]int
	serviceFeedback  map[string]map[string]*proxyCompatServiceFeedback
}

type proxyCompatLeaseState struct {
	Lease           proxyCompatLease
	SelectedNodeTag string
}

type proxyCompatRuntime struct {
	SharedHost              string
	SharedPort              int
	SharedProtocol          string
	SharedUsername          string
	SharedPassword          string
	AllowSharedPoolFallback bool
	NodeProtocol            string
	NodeUsername            string
	NodePassword            string
	ManagementPort          int
	ConnectionRef           string
	ManagementURL           string
	ProviderInstanceID      string
	ProviderDisplayName     string
	CreatedAt               string
	UpdatedAt               string
}

type proxyCompatCandidate struct {
	Snapshot             Snapshot
	ReservationCount     int
	ServiceLeaseCount    int
	ServicePenalty       int
	ServiceCooling       bool
	UsageStats           proxyCompatUsageStats
	RecentSuccessCount   int
	RecentSuccessStreak  int
	RecentSuccessPenalty int
	SelectionTier        string
	EndpointHost         string
	EndpointPort         int
	Protocol             string
	Username             string
	Password             string
	EndpointMode         string
}

type proxyCompatRecentSuccessReusePreference struct {
	Enabled   bool
	Threshold int
	Window    time.Duration
}

type proxyCompatMaintenanceResult struct {
	expired []string
	cleaned []string
}

var (
	errProxyCompatUnsupportedProvider = errors.New("requested provider is not supported by the EasyProxy compatibility layer")
	errProxyCompatNoNodes             = errors.New("no effective EasyProxy nodes are currently available")
)

type proxyCompatUsageFailureScope string

const (
	proxyCompatUsageFailureNone    proxyCompatUsageFailureScope = "none"
	proxyCompatUsageFailureGlobal  proxyCompatUsageFailureScope = "global"
	proxyCompatUsageFailureService proxyCompatUsageFailureScope = "service"
)

const (
	proxyCompatFailureClassNone         = "none"
	proxyCompatFailureClassUnknown      = "unknown"
	proxyCompatFailureClassRouteFailure = "route_failure"
	proxyCompatFailureClassBusinessRisk = "business_risk"
	proxyCompatFailureClassAccountAuth  = "account_or_auth"
)

const (
	proxyCompatRouteConfidenceLow    = "low"
	proxyCompatRouteConfidenceMedium = "medium"
	proxyCompatRouteConfidenceHigh   = "high"
)

const (
	proxyCompatFeedbackScopeNode           = "node"
	proxyCompatFeedbackScopeProtocolFamily = "protocol_family"
	proxyCompatFeedbackScopeNodeMode       = "node_mode"
	proxyCompatFeedbackScopeDomainFamily   = "domain_family"
)

type proxyCompatServiceFeedbackRef struct {
	Key        string
	ScopeKind  string
	ScopeValue string
}

type proxyCompatUsageFeedbackDecision struct {
	Scope              proxyCompatUsageFailureScope
	ErrorClass         string
	Penalty            int
	CooldownBase       time.Duration
	CooldownEscalated  time.Duration
	EscalateAfterCount int
}
