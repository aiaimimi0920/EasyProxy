package subscription

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

const connectorTypeECHWorker = "ech_worker"
const connectorTypeZenProxyClient = "zenproxy_client"

const (
	zenProxyFetchMaxAttempts    = 3
	zenProxyFetchRetryBaseDelay = 100 * time.Millisecond
	zenProxyFetchBodyLimit      = 2 * 1024 * 1024
)

type preferredIPRuntimeSelector func(context.Context, string, config.ConnectorRuntimeConfig, config.ConnectorSourceConfig, monitor.PreferredIPRefreshOptions) ([]preferredIPResultRow, string, string, error)

type connectorRuntimeManager struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	logger              Logger
	httpClient          *http.Client
	instances           map[string]*connectorInstance
	fanoutCache         map[string][]RuntimeSource
	preferredIPSelector preferredIPRuntimeSelector
}

type connectorInstance struct {
	spec   connectorSpec
	cancel context.CancelFunc
	cmd    *exec.Cmd
	done   chan error
}

type connectorSpec struct {
	Key           string
	Fingerprint   string
	DisplayName   string
	LocalProtocol string
	ListenHost    string
	ListenPort    uint16
	ListenAddr    string
	LocalURI      string
	BinaryPath    string
	WorkingDir    string
	Args          []string
}

type echWorkerConnectorConfig struct {
	LocalProtocol string
	AccessToken   string
	Path          string
	ProxyIP       string
	ServerIP      string
	DNSServer     string
	ECHDomain     string
}

type zenProxyConnectorConfig struct {
	APIKey      string
	FetchPath   string
	Count       int
	Country     string
	ProxyType   string
	ProxyID     string
	ChatGPT     bool
	Google      bool
	Residential bool
	RiskMax     *float64
	AuthInQuery bool
}

type zenProxyFetchResponse struct {
	Proxies []zenProxyFetchedProxy `json:"proxies"`
	Count   int                    `json:"count"`
	Message string                 `json:"message"`
}

type zenProxyFetchedProxy struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Server   string         `json:"server"`
	Port     int            `json:"port"`
	Outbound map[string]any `json:"outbound"`
}

func newConnectorRuntimeManager(parent context.Context, logger Logger) ConnectorRuntime {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if logger == nil {
		logger = defaultLogger{}
	}
	return &connectorRuntimeManager{
		ctx:                 ctx,
		cancel:              cancel,
		logger:              logger,
		httpClient:          newConnectorHTTPClient(),
		instances:           make(map[string]*connectorInstance),
		fanoutCache:         make(map[string][]RuntimeSource),
		preferredIPSelector: runPreferredIPSelection,
	}
}

func (m *connectorRuntimeManager) StopAll() error {
	m.cancel()

	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	for key, instance := range m.instances {
		if err := m.stopInstance(instance); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, err))
		}
	}
	m.instances = make(map[string]*connectorInstance)
	m.fanoutCache = make(map[string][]RuntimeSource)

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *connectorRuntimeManager) Reconcile(cfg *config.Config, sources []RuntimeSource) ([]RuntimeSource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cfg == nil {
		return nil, nil
	}
	if !cfg.ConnectorRuntimeEnabled() || len(sources) == 0 {
		return m.stopAllLocked()
	}

	specs, err := m.buildConnectorSpecs(cfg, sources)
	if err != nil {
		return nil, err
	}

	desired := make(map[string]connectorSpec, len(specs))
	for _, spec := range specs {
		desired[spec.Key] = spec
	}

	var errs []string
	for key, instance := range m.instances {
		spec, ok := desired[key]
		if !ok || instance.spec.Fingerprint != spec.Fingerprint {
			if err := m.stopInstance(instance); err != nil {
				errs = append(errs, fmt.Sprintf("stop %s: %v", key, err))
			}
			delete(m.instances, key)
		}
	}

	for _, spec := range specs {
		instance, ok := m.instances[spec.Key]
		if ok && instance.isRunning() {
			continue
		}
		if ok {
			if err := m.stopInstance(instance); err != nil {
				errs = append(errs, fmt.Sprintf("restart %s: %v", spec.Key, err))
			}
			delete(m.instances, spec.Key)
		}

		instance, err := m.startInstance(spec, connectorStartupTimeout(cfg))
		if err != nil {
			errs = append(errs, fmt.Sprintf("start %s: %v", spec.DisplayName, err))
			continue
		}
		m.instances[spec.Key] = instance
	}

	var runtimeSources []RuntimeSource
	for _, spec := range specs {
		instance, ok := m.instances[spec.Key]
		if !ok || !instance.isRunning() {
			continue
		}
		runtimeSources = append(runtimeSources, RuntimeSource{
			ID:     spec.Key,
			Kind:   SourceKindProxyURI,
			Name:   spec.DisplayName,
			Input:  spec.LocalURI,
			Origin: "manifest",
			Options: map[string]any{
				"connector_key":  spec.Key,
				"connector_type": connectorTypeECHWorker,
			},
		})
	}

	zenProxySources, zenErr := m.fetchZenProxyRuntimeSources(cfg, sources)
	if len(zenProxySources) > 0 {
		runtimeSources = dedupeSourcesWithPrecedence(runtimeSources, zenProxySources)
	}
	if zenErr != nil {
		errs = append(errs, zenErr.Error())
	}

	if len(errs) > 0 {
		return runtimeSources, errors.New(strings.Join(errs, "; "))
	}
	return runtimeSources, nil
}

func (m *connectorRuntimeManager) stopAllLocked() ([]RuntimeSource, error) {
	var errs []string
	for key, instance := range m.instances {
		if err := m.stopInstance(instance); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, err))
		}
	}
	m.instances = make(map[string]*connectorInstance)
	m.fanoutCache = make(map[string][]RuntimeSource)
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return nil, nil
}

func (m *connectorRuntimeManager) buildConnectorSpecs(cfg *config.Config, sources []RuntimeSource) ([]connectorSpec, error) {
	hasECHSource := false
	for _, source := range sources {
		if source.Kind != SourceKindConnector {
			continue
		}
		if extractStringOption(source.Options, "connector_type") == connectorTypeECHWorker {
			hasECHSource = true
			break
		}
	}
	if !hasECHSource {
		return nil, nil
	}

	binaryPath, err := resolveConnectorBinary(strings.TrimSpace(cfg.SourceSync.ConnectorRuntime.BinaryPath))
	if err != nil {
		return nil, err
	}

	workingDir := connectorWorkingDirectory(cfg)
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, fmt.Errorf("create connector working directory: %w", err)
	}

	usedPorts := make(map[uint16]struct{})
	for _, instance := range m.instances {
		if instance.isRunning() {
			usedPorts[instance.spec.ListenPort] = struct{}{}
		}
	}

	expandedSources, err := m.expandConnectorSources(cfg, sources)
	if err != nil {
		return nil, err
	}

	specs := make([]connectorSpec, 0, len(expandedSources))
	for idx, source := range expandedSources {
		if source.Kind != SourceKindConnector {
			continue
		}
		if extractStringOption(source.Options, "connector_type") != connectorTypeECHWorker {
			continue
		}
		spec, err := buildECHWorkerConnectorSpec(cfg, source, idx, binaryPath, workingDir)
		if err != nil {
			return nil, err
		}
		if existing, ok := m.instances[spec.Key]; ok && existing.spec.Fingerprint == spec.Fingerprint && existing.isRunning() {
			spec.ListenPort = existing.spec.ListenPort
			spec.ListenAddr = existing.spec.ListenAddr
			spec.LocalURI = buildConnectorLocalURI(spec.LocalProtocol, spec.ListenHost, spec.ListenPort)
			usedPorts[spec.ListenPort] = struct{}{}
		} else {
			port, err := nextAvailableConnectorPort(spec.ListenHost, cfg.SourceSync.ConnectorRuntime.ListenStartPort, usedPorts)
			if err != nil {
				return nil, err
			}
			spec.ListenPort = port
			spec.ListenAddr = net.JoinHostPort(spec.ListenHost, strconv.Itoa(int(port)))
			spec.LocalURI = buildConnectorLocalURI(spec.LocalProtocol, spec.ListenHost, spec.ListenPort)
			spec.Args = upsertArgValue(spec.Args, "-l", spec.ListenAddr)
			usedPorts[port] = struct{}{}
		}
		specs = append(specs, spec)
	}

	return specs, nil
}
