package subscription

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

const connectorTypeECHWorker = "ech_worker"
const connectorTypeZenProxyClient = "zenproxy_client"

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

func (m *connectorRuntimeManager) fetchZenProxyRuntimeSources(cfg *config.Config, sources []RuntimeSource) ([]RuntimeSource, error) {
	if cfg == nil || len(sources) == 0 {
		return nil, nil
	}

	var runtimeSources []RuntimeSource
	var errs []string
	timeout := cfg.SourceSync.RequestTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	for _, source := range sources {
		if source.Kind != SourceKindConnector {
			continue
		}
		if extractStringOption(source.Options, "connector_type") != connectorTypeZenProxyClient {
			continue
		}

		connectorCfg, err := parseZenProxyConnectorConfig(extractMapOption(source.Options, "connector_config"))
		if err != nil {
			errs = append(errs, fmt.Sprintf("zenproxy connector %s config: %v", source.Name, err))
			continue
		}

		fetched, err := m.fetchZenProxyConnectorSource(cfg, source, connectorCfg, timeout)
		if err != nil {
			errs = append(errs, fmt.Sprintf("zenproxy connector %s fetch: %v", source.Name, err))
			continue
		}
		runtimeSources = append(runtimeSources, fetched...)
	}

	if len(errs) > 0 {
		return runtimeSources, errors.New(strings.Join(errs, "; "))
	}
	return runtimeSources, nil
}

func (m *connectorRuntimeManager) fetchZenProxyConnectorSource(cfg *config.Config, source RuntimeSource, connectorCfg zenProxyConnectorConfig, timeout time.Duration) ([]RuntimeSource, error) {
	endpoint, err := buildZenProxyFetchURL(source.Input, connectorCfg.FetchPath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if connectorCfg.APIKey != "" && !connectorCfg.AuthInQuery {
		req.Header.Set("Authorization", "Bearer "+connectorCfg.APIKey)
	}

	values := req.URL.Query()
	if connectorCfg.AuthInQuery && connectorCfg.APIKey != "" {
		values.Set("api_key", connectorCfg.APIKey)
	}
	if connectorCfg.Count > 0 {
		values.Set("count", strconv.Itoa(connectorCfg.Count))
	}
	if connectorCfg.Country != "" {
		values.Set("country", connectorCfg.Country)
	}
	if connectorCfg.ProxyType != "" {
		values.Set("type", connectorCfg.ProxyType)
	}
	if connectorCfg.ProxyID != "" {
		values.Set("proxy_id", connectorCfg.ProxyID)
	}
	if connectorCfg.ChatGPT {
		values.Set("chatgpt", "true")
	}
	if connectorCfg.Google {
		values.Set("google", "true")
	}
	if connectorCfg.Residential {
		values.Set("residential", "true")
	}
	if connectorCfg.RiskMax != nil {
		values.Set("risk_max", strconv.FormatFloat(*connectorCfg.RiskMax, 'f', -1, 64))
	}
	req.URL.RawQuery = values.Encode()

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload zenProxyFetchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(payload.Proxies) == 0 {
		if strings.TrimSpace(payload.Message) != "" {
			return nil, errors.New(strings.TrimSpace(payload.Message))
		}
		return nil, fmt.Errorf("no proxies returned")
	}

	providerRef := strings.TrimSpace(source.ID)
	if providerRef == "" {
		providerRef = sourceKey(source)
	}

	runtimeSources := make([]RuntimeSource, 0, len(payload.Proxies))
	var conversionErrs []string
	for idx, item := range payload.Proxies {
		uri, err := config.BuildURIFromSingboxOutbound(item.Name, item.Outbound)
		if err != nil {
			conversionErrs = append(conversionErrs, fmt.Sprintf("%s: %v", firstNonEmpty(strings.TrimSpace(item.Name), strings.TrimSpace(item.ID), fmt.Sprintf("proxy-%d", idx+1)), err))
			continue
		}

		displayName := buildZenProxyRuntimeDisplayName(source.Name, item.Name, item.Server, item.Port, idx)
		runtimeSources = append(runtimeSources, RuntimeSource{
			ID:     providerRef,
			Kind:   SourceKindProxyURI,
			Name:   displayName,
			Input:  uri,
			Origin: firstNonEmpty(strings.TrimSpace(source.Origin), "manifest"),
			Options: map[string]any{
				"connector_type":       connectorTypeZenProxyClient,
				"connector_proxy_id":   strings.TrimSpace(item.ID),
				"connector_proxy_type": firstNonEmpty(strings.TrimSpace(item.Type), strings.TrimSpace(extractStringOption(item.Outbound, "type"))),
				"connector_provider":   firstNonEmpty(strings.TrimSpace(source.Name), "ZenProxy"),
			},
		})
	}

	if len(runtimeSources) == 0 && len(conversionErrs) > 0 {
		return nil, fmt.Errorf("all returned proxies failed conversion: %s", strings.Join(conversionErrs, "; "))
	}
	if len(conversionErrs) > 0 {
		return runtimeSources, fmt.Errorf("partial conversion failures: %s", strings.Join(conversionErrs, "; "))
	}
	return runtimeSources, nil
}

func (m *connectorRuntimeManager) expandConnectorSources(cfg *config.Config, sources []RuntimeSource) ([]RuntimeSource, error) {
	if cfg == nil || len(sources) == 0 {
		return sources, nil
	}

	fanoutCount := cfg.SourceSync.ConnectorRuntime.PreferredIP.FanoutCount
	if fanoutCount <= 1 {
		return sources, nil
	}

	expanded := make([]RuntimeSource, 0, len(sources)*fanoutCount)
	nextCache := make(map[string][]RuntimeSource, len(sources))
	for _, source := range sources {
		if source.Kind != SourceKindConnector {
			expanded = append(expanded, source)
			continue
		}

		connectorType := extractStringOption(source.Options, "connector_type")
		connectorCfg := extractMapOption(source.Options, "connector_config")
		serverIP := strings.TrimSpace(extractStringOption(connectorCfg, "server_ip"))
		if connectorType != connectorTypeECHWorker || serverIP != "" {
			expanded = append(expanded, source)
			continue
		}

		cacheKey := sourceKey(source)
		if cached, ok := m.fanoutCache[cacheKey]; ok && len(cached) > 0 {
			cloned := cloneRuntimeSources(cached)
			nextCache[cacheKey] = cloned
			expanded = append(expanded, cloned...)
			continue
		}

		template := config.ConnectorSourceConfig{
			Name:            strings.TrimSpace(source.Name),
			Input:           strings.TrimSpace(source.Input),
			Enabled:         true,
			ConnectorType:   connectorTypeECHWorker,
			ConnectorConfig: cloneConnectorOptions(connectorCfg),
		}
		selected, _, _, err := m.preferredIPSelector(
			m.ctx,
			cfg.FilePath(),
			cfg.SourceSync.ConnectorRuntime,
			template,
			monitor.PreferredIPRefreshOptions{TopCount: fanoutCount},
		)
		if err != nil {
			m.logger.Warnf("preferred IP fanout failed for connector %s, using single connector: %v", source.Name, err)
			expanded = append(expanded, source)
			continue
		}
		generated := buildPreferredRuntimeSources(source, selected)
		if len(generated) == 0 {
			expanded = append(expanded, source)
			continue
		}
		nextCache[cacheKey] = cloneRuntimeSources(generated)
		expanded = append(expanded, generated...)
	}

	m.fanoutCache = nextCache
	return expanded, nil
}

func buildPreferredRuntimeSources(source RuntimeSource, selected []preferredIPResultRow) []RuntimeSource {
	generated := make([]RuntimeSource, 0, len(selected))
	for idx, item := range selected {
		clone := RuntimeSource{
			ID:     strings.TrimSpace(source.ID),
			Kind:   source.Kind,
			Name:   strings.TrimSpace(source.Name),
			Input:  strings.TrimSpace(source.Input),
			Origin: strings.TrimSpace(source.Origin),
		}
		if source.Options != nil {
			clone.Options = cloneConnectorOptions(source.Options)
		} else {
			clone.Options = map[string]any{}
		}

		connectorCfg := extractMapOption(clone.Options, "connector_config")
		if connectorCfg == nil {
			connectorCfg = map[string]any{}
		}
		connectorCfg["server_ip"] = item.IP
		clone.Options["connector_config"] = connectorCfg
		if strings.TrimSpace(clone.ID) == "" {
			clone.ID = fmt.Sprintf("connector-pref-%d", idx+1)
		} else {
			clone.ID = fmt.Sprintf("%s-pref-%d", clone.ID, idx+1)
		}
		if strings.TrimSpace(clone.Name) == "" {
			clone.Name = fmt.Sprintf("Connector Preferred %d", idx+1)
		} else {
			clone.Name = fmt.Sprintf("%s Preferred %d", clone.Name, idx+1)
		}
		generated = append(generated, clone)
	}
	return generated
}

func cloneRuntimeSources(input []RuntimeSource) []RuntimeSource {
	if len(input) == 0 {
		return nil
	}
	cloned := make([]RuntimeSource, 0, len(input))
	for _, item := range input {
		copied := RuntimeSource{
			ID:     item.ID,
			Kind:   item.Kind,
			Name:   item.Name,
			Input:  item.Input,
			Origin: item.Origin,
		}
		if item.Options != nil {
			copied.Options = cloneConnectorOptions(item.Options)
		}
		cloned = append(cloned, copied)
	}
	return cloned
}

func buildECHWorkerConnectorSpec(cfg *config.Config, source RuntimeSource, index int, binaryPath string, workingDir string) (connectorSpec, error) {
	connectorType := extractStringOption(source.Options, "connector_type")
	if connectorType == "" {
		return connectorSpec{}, fmt.Errorf("connector %s missing connector_type", source.Name)
	}
	if connectorType != connectorTypeECHWorker {
		return connectorSpec{}, fmt.Errorf("connector %s has unsupported type %q", source.Name, connectorType)
	}

	connectorCfg := extractMapOption(source.Options, "connector_config")
	echCfg := echWorkerConnectorConfig{
		LocalProtocol: normalizeConnectorLocalProtocol(extractStringOption(connectorCfg, "local_protocol")),
		AccessToken:   strings.TrimSpace(extractStringOption(connectorCfg, "access_token")),
		Path:          strings.TrimSpace(extractStringOption(connectorCfg, "path")),
		ProxyIP:       strings.TrimSpace(extractStringOption(connectorCfg, "proxy_ip")),
		ServerIP:      strings.TrimSpace(extractStringOption(connectorCfg, "server_ip")),
		DNSServer:     strings.TrimSpace(extractStringOption(connectorCfg, "dns_server")),
		ECHDomain:     strings.TrimSpace(extractStringOption(connectorCfg, "ech_domain")),
	}

	serverAddr, err := buildECHWorkerServerAddr(source.Input, echCfg.Path)
	if err != nil {
		return connectorSpec{}, fmt.Errorf("connector %s server address: %w", source.Name, err)
	}

	key := strings.TrimSpace(source.ID)
	if key == "" {
		key = sourceKey(source)
	}
	displayName := strings.TrimSpace(source.Name)
	if displayName == "" {
		displayName = fmt.Sprintf("connector-%d", index+1)
	}
	fingerprint := strings.Join([]string{
		key,
		serverAddr,
		echCfg.AccessToken,
		echCfg.ProxyIP,
		echCfg.ServerIP,
		echCfg.DNSServer,
		echCfg.ECHDomain,
		echCfg.LocalProtocol,
		binaryPath,
	}, "|")

	args := []string{"-f", serverAddr}
	if echCfg.AccessToken != "" {
		args = append(args, "-token", echCfg.AccessToken)
	}
	if echCfg.ProxyIP != "" {
		args = append(args, "-pyip", echCfg.ProxyIP)
	}
	if echCfg.ServerIP != "" {
		args = append(args, "-ip", echCfg.ServerIP)
	}
	if echCfg.DNSServer != "" {
		args = append(args, "-dns", echCfg.DNSServer)
	}
	if echCfg.ECHDomain != "" {
		args = append(args, "-ech", echCfg.ECHDomain)
	}

	return connectorSpec{
		Key:           key,
		Fingerprint:   fingerprint,
		DisplayName:   displayName,
		LocalProtocol: echCfg.LocalProtocol,
		ListenHost:    strings.TrimSpace(cfg.SourceSync.ConnectorRuntime.ListenHost),
		BinaryPath:    binaryPath,
		WorkingDir:    workingDir,
		Args:          args,
	}, nil
}

func buildECHWorkerServerAddr(input string, pathOverride string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", errors.New("connector input is empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid connector input: %w", err)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", errors.New("missing connector host")
	}
	port := parsed.Port()
	if port == "" {
		if strings.EqualFold(parsed.Scheme, "http") {
			port = "80"
		} else {
			port = "443"
		}
	}

	pathValue := parsed.EscapedPath()
	if pathValue == "" || pathValue == "/" {
		pathValue = normalizeConnectorPath(pathOverride)
	}
	if pathValue == "" {
		pathValue = "/"
	}
	if parsed.RawQuery != "" {
		pathValue = pathValue + "?" + parsed.RawQuery
	}
	return net.JoinHostPort(host, port) + pathValue, nil
}

func (m *connectorRuntimeManager) startInstance(spec connectorSpec, startupTimeout time.Duration) (*connectorInstance, error) {
	ctx, cancel := context.WithCancel(m.ctx)
	cmd := exec.CommandContext(ctx, spec.BinaryPath, spec.Args...)
	cmd.Dir = spec.WorkingDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start process: %w", err)
	}

	instance := &connectorInstance{
		spec:   spec,
		cancel: cancel,
		cmd:    cmd,
		done:   make(chan error, 1),
	}

	go m.pipeLogs(spec.DisplayName, "stdout", stdout)
	go m.pipeLogs(spec.DisplayName, "stderr", stderr)
	go func() {
		instance.done <- cmd.Wait()
		close(instance.done)
	}()

	if err := waitForConnectorListen(spec.ListenAddr, startupTimeout); err != nil {
		_ = m.stopInstance(instance)
		return nil, err
	}

	m.logger.Infof("started connector %s on %s", spec.DisplayName, spec.ListenAddr)
	return instance, nil
}

func (m *connectorRuntimeManager) stopInstance(instance *connectorInstance) error {
	if instance == nil {
		return nil
	}

	instance.cancel()
	if instance.cmd != nil && instance.cmd.Process != nil {
		_ = instance.cmd.Process.Kill()
	}

	select {
	case err := <-instance.done:
		if err != nil && !errors.Is(err, context.Canceled) && !isKilledProcessError(err) {
			return err
		}
	case <-time.After(2 * time.Second):
		return fmt.Errorf("timeout waiting for connector %s to stop", instance.spec.DisplayName)
	}

	m.logger.Infof("stopped connector %s", instance.spec.DisplayName)
	return nil
}

func (i *connectorInstance) isRunning() bool {
	if i == nil {
		return false
	}
	select {
	case <-i.done:
		return false
	default:
		return true
	}
}

func (m *connectorRuntimeManager) pipeLogs(name string, stream string, reader interface{ Read([]byte) (int, error) }) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		m.logger.Infof("[connector:%s:%s] %s", name, stream, line)
	}
}

func waitForConnectorListen(addr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("connector listen timeout on %s", addr)
}

func resolveConnectorBinary(configuredPath string) (string, error) {
	if configuredPath != "" {
		path, err := exec.LookPath(configuredPath)
		if err == nil {
			return path, nil
		}
		if filepath.IsAbs(configuredPath) {
			if _, statErr := os.Stat(configuredPath); statErr == nil {
				return configuredPath, nil
			}
		}
		return "", fmt.Errorf("connector binary %q not found", configuredPath)
	}

	candidates := []string{"ech-workers", "ech-win"}
	if runtime.GOOS == "windows" {
		candidates = []string{"ech-workers.exe", "ech-win.exe", "ech-workers", "ech-win"}
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("ech-workers binary not found in PATH")
}

func newConnectorHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
		Timeout: 30 * time.Second,
	}
}

func connectorWorkingDirectory(cfg *config.Config) string {
	if cfg == nil {
		return filepath.Join("data", "connectors")
	}
	if workingDir := strings.TrimSpace(cfg.SourceSync.ConnectorRuntime.WorkingDirectory); workingDir != "" {
		return workingDir
	}
	baseDir := filepath.Dir(cfg.FilePath())
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, "data", "connectors")
}

func connectorStartupTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.SourceSync.ConnectorRuntime.StartupTimeout <= 0 {
		return 10 * time.Second
	}
	return cfg.SourceSync.ConnectorRuntime.StartupTimeout
}

func nextAvailableConnectorPort(host string, start uint16, used map[uint16]struct{}) (uint16, error) {
	if start == 0 {
		start = 30000
	}
	for port := start; port < 65535; port++ {
		if _, exists := used[port]; exists {
			continue
		}
		addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, errors.New("no connector listen ports available")
}

func normalizeConnectorLocalProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "http":
		return "http"
	case "socks", "socks5":
		return "socks5"
	default:
		return "socks5"
	}
}

func normalizeConnectorPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "/" + trimmed
	}
	return trimmed
}

func buildConnectorLocalURI(protocol string, host string, port uint16) string {
	return fmt.Sprintf("%s://%s", normalizeConnectorLocalProtocol(protocol), net.JoinHostPort(host, strconv.Itoa(int(port))))
}

func upsertArgValue(args []string, key string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return args
	}
	for idx := 0; idx < len(args)-1; idx++ {
		if args[idx] == key {
			args[idx+1] = value
			return args
		}
	}
	return append(args, key, value)
}

func extractMapOption(options map[string]any, key string) map[string]any {
	if options == nil {
		return map[string]any{}
	}
	if value, ok := options[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func extractStringOption(options map[string]any, key string) string {
	if options == nil {
		return ""
	}
	value, ok := options[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func extractBoolOption(options map[string]any, key string) bool {
	if options == nil {
		return false
	}
	value, ok := options[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}

func extractIntOption(options map[string]any, key string, fallback int) int {
	if options == nil {
		return fallback
	}
	value, ok := options[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func extractFloat64Option(options map[string]any, key string) *float64 {
	if options == nil {
		return nil
	}
	value, ok := options[key]
	if !ok || value == nil {
		return nil
	}
	var parsed float64
	switch typed := value.(type) {
	case float64:
		parsed = typed
	case float32:
		parsed = float64(typed)
	case int:
		parsed = float64(typed)
	case int8:
		parsed = float64(typed)
	case int16:
		parsed = float64(typed)
	case int32:
		parsed = float64(typed)
	case int64:
		parsed = float64(typed)
	case uint:
		parsed = float64(typed)
	case uint8:
		parsed = float64(typed)
	case uint16:
		parsed = float64(typed)
	case uint32:
		parsed = float64(typed)
	case uint64:
		parsed = float64(typed)
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return nil
		}
		parsed = value
	default:
		return nil
	}
	return &parsed
}

func parseZenProxyConnectorConfig(options map[string]any) (zenProxyConnectorConfig, error) {
	cfg := zenProxyConnectorConfig{
		APIKey:      strings.TrimSpace(extractStringOption(options, "api_key")),
		FetchPath:   strings.TrimSpace(extractStringOption(options, "fetch_path")),
		Count:       extractIntOption(options, "count", 10),
		Country:     strings.TrimSpace(extractStringOption(options, "country")),
		ProxyType:   strings.TrimSpace(extractStringOption(options, "type")),
		ProxyID:     strings.TrimSpace(extractStringOption(options, "proxy_id")),
		ChatGPT:     extractBoolOption(options, "chatgpt"),
		Google:      extractBoolOption(options, "google"),
		Residential: extractBoolOption(options, "residential"),
		RiskMax:     extractFloat64Option(options, "risk_max"),
		AuthInQuery: extractBoolOption(options, "auth_in_query"),
	}
	if cfg.APIKey == "" {
		return zenProxyConnectorConfig{}, fmt.Errorf("missing api_key")
	}
	if cfg.Count <= 0 {
		cfg.Count = 10
	}
	return cfg, nil
}

func buildZenProxyFetchURL(rawInput string, fetchPath string) (string, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return "", fmt.Errorf("missing zenproxy input url")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid zenproxy input url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("zenproxy input must be http(s)")
	}

	path := strings.TrimSpace(fetchPath)
	if path == "" {
		path = "/api/client/fetch"
	}
	if strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/api/client/fetch") {
		return parsed.String(), nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String(), nil
}

func buildZenProxyRuntimeDisplayName(sourceName string, proxyName string, server string, port int, index int) string {
	remote := strings.TrimSpace(proxyName)
	if remote == "" && strings.TrimSpace(server) != "" && port > 0 {
		remote = fmt.Sprintf("%s:%d", strings.TrimSpace(server), port)
	}
	if remote == "" {
		remote = fmt.Sprintf("proxy-%d", index+1)
	}
	base := strings.TrimSpace(sourceName)
	if base == "" {
		return remote
	}
	return fmt.Sprintf("%s | %s", base, remote)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isKilledProcessError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "killed") || strings.Contains(text, "terminated")
}
