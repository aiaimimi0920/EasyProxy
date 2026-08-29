package subscription

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

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
