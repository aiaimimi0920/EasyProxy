package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// BuildURIFromSingboxOutbound converts a sing-box outbound JSON object into the
// URI format understood by the EasyProxy runtime builder.
func BuildURIFromSingboxOutbound(name string, outbound map[string]any) (string, error) {
	if len(outbound) == 0 {
		return "", fmt.Errorf("outbound is empty")
	}

	proxyType := strings.ToLower(strings.TrimSpace(singboxString(outbound, "type")))
	if proxyType == "" {
		return "", fmt.Errorf("outbound missing type")
	}

	switch proxyType {
	case "vmess":
		return buildVMessURIFromSingbox(name, outbound)
	case "vless":
		return buildVLESSURIFromSingbox(name, outbound)
	case "trojan":
		return buildTrojanURIFromSingbox(name, outbound)
	case "shadowsocks":
		return buildShadowsocksURIFromSingbox(name, outbound)
	case "hysteria2":
		return buildHysteria2URIFromSingbox(name, outbound)
	case "socks":
		return buildSOCKSURIFromSingbox(name, outbound)
	case "http":
		return buildHTTPURIFromSingbox(name, outbound)
	default:
		return "", fmt.Errorf("unsupported sing-box outbound type %q", proxyType)
	}
}

func buildVMessURIFromSingbox(name string, outbound map[string]any) (string, error) {
	proxy, err := buildClashProxyFromSingbox(name, outbound)
	if err != nil {
		return "", err
	}
	proxy.Type = "vmess"
	proxy.UUID = singboxString(outbound, "uuid")
	proxy.AlterId = singboxInt(outbound, "alter_id")
	proxy.Cipher = singboxStringDefault(outbound, "security", "auto")
	if proxy.UUID == "" {
		return "", fmt.Errorf("vmess outbound missing uuid")
	}

	uri := buildVMessURI(proxy)
	if uri == "" {
		return "", fmt.Errorf("failed to encode vmess outbound as uri")
	}
	return withExtraTLSQuery(uri, proxy), nil
}

func buildVLESSURIFromSingbox(name string, outbound map[string]any) (string, error) {
	proxy, err := buildClashProxyFromSingbox(name, outbound)
	if err != nil {
		return "", err
	}
	proxy.Type = "vless"
	proxy.UUID = singboxString(outbound, "uuid")
	proxy.Flow = singboxString(outbound, "flow")
	if proxy.UUID == "" {
		return "", fmt.Errorf("vless outbound missing uuid")
	}

	uri := buildVLESSURI(proxy)
	if uri == "" {
		return "", fmt.Errorf("failed to encode vless outbound as uri")
	}
	return withExtraTLSQuery(uri, proxy), nil
}

func buildTrojanURIFromSingbox(name string, outbound map[string]any) (string, error) {
	proxy, err := buildClashProxyFromSingbox(name, outbound)
	if err != nil {
		return "", err
	}
	proxy.Type = "trojan"
	proxy.Password = singboxString(outbound, "password")
	if proxy.Password == "" {
		return "", fmt.Errorf("trojan outbound missing password")
	}

	uri := buildTrojanURI(proxy)
	if uri == "" {
		return "", fmt.Errorf("failed to encode trojan outbound as uri")
	}
	return withExtraTLSQuery(uri, proxy), nil
}

func buildShadowsocksURIFromSingbox(name string, outbound map[string]any) (string, error) {
	proxy, err := buildClashProxyFromSingbox(name, outbound)
	if err != nil {
		return "", err
	}
	proxy.Type = "shadowsocks"
	proxy.Cipher = singboxString(outbound, "method")
	proxy.Password = singboxString(outbound, "password")
	proxy.Plugin = singboxString(outbound, "plugin")
	proxy.PluginOpts = singboxInterfaceMap(outbound, "plugin_opts")
	if proxy.Cipher == "" || proxy.Password == "" {
		return "", fmt.Errorf("shadowsocks outbound missing method or password")
	}

	uri := buildShadowsocksURI(proxy)
	if uri == "" {
		return "", fmt.Errorf("failed to encode shadowsocks outbound as uri")
	}
	return uri, nil
}

func buildHysteria2URIFromSingbox(name string, outbound map[string]any) (string, error) {
	proxy, err := buildClashProxyFromSingbox(name, outbound)
	if err != nil {
		return "", err
	}
	proxy.Type = "hysteria2"
	proxy.Password = singboxString(outbound, "password")
	if proxy.Password == "" {
		return "", fmt.Errorf("hysteria2 outbound missing password")
	}

	uri := buildHysteria2URI(proxy)
	if uri == "" {
		return "", fmt.Errorf("failed to encode hysteria2 outbound as uri")
	}
	return withURIQuery(uri, func(values url.Values) {
		if obfs := singboxMap(outbound, "obfs"); len(obfs) > 0 {
			if obfsType := singboxString(obfs, "type"); obfsType != "" {
				values.Set("obfs", obfsType)
			}
			if obfsPassword := singboxString(obfs, "password"); obfsPassword != "" {
				values.Set("obfs-password", obfsPassword)
			}
		}
	}), nil
}

func buildSOCKSURIFromSingbox(name string, outbound map[string]any) (string, error) {
	server, port, err := singboxServerPort(outbound)
	if err != nil {
		return "", err
	}

	version := strings.TrimSpace(singboxStringDefault(outbound, "version", "5"))
	if version == "" {
		version = "5"
	}
	if version != "5" {
		return "", fmt.Errorf("unsupported socks version %q", version)
	}
	if tls := singboxMap(outbound, "tls"); singboxBool(tls, "enabled") {
		return "", fmt.Errorf("secure socks outbounds are not supported by uri builder")
	}

	uri := (&url.URL{
		Scheme: "socks5",
		Host:   netJoinHostPort(server, port),
	}).String()

	username := singboxString(outbound, "username")
	password := singboxString(outbound, "password")
	if username != "" || password != "" {
		u, _ := url.Parse(uri)
		u.User = url.UserPassword(username, password)
		uri = u.String()
	}
	return appendFragment(uri, defaultOutboundDisplayName(name, server, port)), nil
}

func buildHTTPURIFromSingbox(name string, outbound map[string]any) (string, error) {
	server, port, err := singboxServerPort(outbound)
	if err != nil {
		return "", err
	}

	u := &url.URL{
		Scheme: "http",
		Host:   netJoinHostPort(server, port),
	}
	username := singboxString(outbound, "username")
	password := singboxString(outbound, "password")
	if username != "" || password != "" {
		u.User = url.UserPassword(username, password)
	}

	uri := withURIQuery(u.String(), func(values url.Values) {
		tls := singboxMap(outbound, "tls")
		if singboxBool(tls, "enabled") {
			values.Set("security", "tls")
			if serverName := singboxString(tls, "server_name"); serverName != "" {
				values.Set("sni", serverName)
			}
			if singboxBool(tls, "insecure") {
				values.Set("insecure", "1")
			}
			if fingerprint := singboxString(singboxMap(tls, "utls"), "fingerprint"); fingerprint != "" {
				values.Set("fp", fingerprint)
			}
		}
	})
	return appendFragment(uri, defaultOutboundDisplayName(name, server, port)), nil
}

func buildClashProxyFromSingbox(name string, outbound map[string]any) (clashProxy, error) {
	server, port, err := singboxServerPort(outbound)
	if err != nil {
		return clashProxy{}, err
	}

	proxy := clashProxy{
		Name:   defaultOutboundDisplayName(name, server, port),
		Server: server,
		Port:   port,
	}

	transport := singboxMap(outbound, "transport")
	switch strings.ToLower(strings.TrimSpace(singboxString(transport, "type"))) {
	case "", "tcp":
	case "ws":
		proxy.Network = "ws"
		proxy.WSOpts = &clashWSOptions{
			Path:    singboxString(transport, "path"),
			Headers: map[string]string{},
		}
		if host := singboxTransportHost(transport); host != "" {
			proxy.WSOpts.Headers["Host"] = host
		}
	case "grpc":
		proxy.Network = "grpc"
		proxy.GrpcOpts = &clashGrpcOptions{
			GrpcServiceName: singboxString(transport, "service_name"),
		}
	case "http", "h2":
		proxy.Network = "http"
		proxy.WSOpts = &clashWSOptions{
			Path:    singboxString(transport, "path"),
			Headers: map[string]string{},
		}
		if host := singboxTransportHost(transport); host != "" {
			proxy.WSOpts.Headers["Host"] = host
		}
	case "httpupgrade":
		proxy.Network = "httpupgrade"
		proxy.WSOpts = &clashWSOptions{
			Path:    singboxString(transport, "path"),
			Headers: map[string]string{},
		}
		if host := singboxTransportHost(transport); host != "" {
			proxy.WSOpts.Headers["Host"] = host
		}
	default:
		return clashProxy{}, fmt.Errorf("unsupported transport type %q", singboxString(transport, "type"))
	}

	tls := singboxMap(outbound, "tls")
	if singboxBool(tls, "enabled") {
		proxy.TLS = true
		proxy.SkipCertVerify = singboxBool(tls, "insecure")
		proxy.ServerName = singboxString(tls, "server_name")
		proxy.SNI = proxy.ServerName
		proxy.ClientFingerprint = singboxString(singboxMap(tls, "utls"), "fingerprint")
		if reality := singboxMap(tls, "reality"); singboxBool(reality, "enabled") {
			proxy.RealityOpts = &clashRealityOptions{
				PublicKey: singboxString(reality, "public_key"),
				ShortID:   singboxString(reality, "short_id"),
			}
		}
	}

	return proxy, nil
}

func withExtraTLSQuery(rawURI string, proxy clashProxy) string {
	return withURIQuery(rawURI, func(values url.Values) {
		if proxy.SkipCertVerify {
			values.Set("insecure", "1")
		}
		if proxy.ClientFingerprint != "" {
			values.Set("fp", proxy.ClientFingerprint)
		}
	})
}

func withURIQuery(rawURI string, mutate func(url.Values)) string {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return rawURI
	}
	values := parsed.Query()
	mutate(values)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func appendFragment(rawURI string, name string) string {
	if strings.TrimSpace(name) == "" {
		return rawURI
	}
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return rawURI
	}
	parsed.Fragment = name
	return parsed.String()
}

func defaultOutboundDisplayName(name string, server string, port int) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	if server == "" || port <= 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", server, port)
}

func singboxServerPort(outbound map[string]any) (string, int, error) {
	server := singboxString(outbound, "server")
	port := singboxInt(outbound, "server_port")
	if server == "" || port <= 0 {
		return "", 0, fmt.Errorf("outbound missing server or server_port")
	}
	return server, port, nil
}

func singboxString(values map[string]any, key string) string {
	return strings.TrimSpace(singboxStringDefault(values, key, ""))
}

func singboxStringDefault(values map[string]any, key string, fallback string) string {
	if values == nil {
		return fallback
	}
	value, ok := values[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func singboxInt(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	value, ok := values[key]
	if !ok || value == nil {
		return 0
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
	return 0
}

func singboxBool(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, ok := values[key]
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

func singboxMap(values map[string]any, key string) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	value, ok := values[key]
	if !ok || value == nil {
		return map[string]any{}
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func singboxInterfaceMap(values map[string]any, key string) map[string]interface{} {
	raw := singboxMap(values, key)
	if len(raw) == 0 {
		return nil
	}
	converted := make(map[string]interface{}, len(raw))
	for itemKey, itemValue := range raw {
		converted[itemKey] = itemValue
	}
	return converted
}

func singboxTransportHost(transport map[string]any) string {
	if transport == nil {
		return ""
	}
	headers := singboxMap(transport, "headers")
	if host := singboxString(headers, "Host"); host != "" {
		return host
	}
	if host := singboxString(headers, "host"); host != "" {
		return host
	}
	if values, ok := transport["host"].([]any); ok && len(values) > 0 {
		return strings.TrimSpace(fmt.Sprint(values[0]))
	}
	if values, ok := transport["host"].([]string); ok && len(values) > 0 {
		return strings.TrimSpace(values[0])
	}
	return singboxString(transport, "host")
}

func netJoinHostPort(server string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(server), strconv.Itoa(port))
}
