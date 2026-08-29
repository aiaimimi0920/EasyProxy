package subscription

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"easy_proxies/internal/config"
)

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
