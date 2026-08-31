package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type clashConfig struct {
	Proxies []clashProxy `yaml:"proxies"`
}

type clashProxy struct {
	Name              string                 `yaml:"name"`
	Type              string                 `yaml:"type"`
	Server            string                 `yaml:"server"`
	Port              int                    `yaml:"port"`
	UUID              string                 `yaml:"uuid"`
	Password          string                 `yaml:"password"`
	Cipher            string                 `yaml:"cipher"`
	AlterId           int                    `yaml:"alterId"`
	Network           string                 `yaml:"network"`
	TLS               bool                   `yaml:"tls"`
	SkipCertVerify    bool                   `yaml:"skip-cert-verify"`
	ServerName        string                 `yaml:"servername"`
	SNI               string                 `yaml:"sni"`
	Flow              string                 `yaml:"flow"`
	UDP               bool                   `yaml:"udp"`
	WSOpts            *clashWSOptions        `yaml:"ws-opts"`
	GrpcOpts          *clashGrpcOptions      `yaml:"grpc-opts"`
	RealityOpts       *clashRealityOptions   `yaml:"reality-opts"`
	ClientFingerprint string                 `yaml:"client-fingerprint"`
	ALPN              []string               `yaml:"alpn"`
	PacketEncoding    string                 `yaml:"packet-encoding"`
	UpMbps            int                    `yaml:"up-mbps"`
	DownMbps          int                    `yaml:"down-mbps"`
	Obfs              string                 `yaml:"obfs"`
	ObfsPassword      string                 `yaml:"obfs-password"`
	Plugin            string                 `yaml:"plugin"`
	PluginOpts        map[string]interface{} `yaml:"plugin-opts"`
}

type clashWSOptions struct {
	Path    string            `yaml:"path"`
	Headers map[string]string `yaml:"headers"`
}

type clashGrpcOptions struct {
	GrpcServiceName string `yaml:"grpc-service-name"`
}

type clashRealityOptions struct {
	PublicKey string `yaml:"public-key"`
	ShortID   string `yaml:"short-id"`
}

// parseClashYAML parses Clash YAML format and converts to NodeConfig
func parseClashYAML(content string) ([]NodeConfig, error) {
	var clash clashConfig
	if err := yaml.Unmarshal([]byte(content), &clash); err != nil {
		return nil, fmt.Errorf("parse clash yaml: %w", err)
	}

	var nodes []NodeConfig
	for _, proxy := range clash.Proxies {
		uri := convertClashProxyToURI(proxy)
		if uri != "" {
			nodes = append(nodes, NodeConfig{
				Name: proxy.Name,
				URI:  uri,
			})
		}
	}

	return nodes, nil
}

// convertClashProxyToURI converts a Clash proxy config to a standard URI
func convertClashProxyToURI(p clashProxy) string {
	switch strings.ToLower(p.Type) {
	case "vmess":
		return buildVMessURI(p)
	case "vless":
		return buildVLESSURI(p)
	case "trojan":
		return buildTrojanURI(p)
	case "ss", "shadowsocks":
		return buildShadowsocksURI(p)
	case "hysteria2", "hy2":
		return buildHysteria2URI(p)
	case "anytls":
		return buildAnyTLSURI(p)
	default:
		return ""
	}
}

func buildVMessURI(p clashProxy) string {
	params := url.Values{}
	if p.Cipher != "" {
		params.Set("encryption", p.Cipher)
	}
	if p.AlterId != 0 {
		params.Set("alterId", strconv.Itoa(p.AlterId))
	}
	if transport, ok := NormalizeV2RayTransport(p.Network); ok {
		if transport != "" {
			params.Set("type", transport)
		}
	} else {
		return ""
	}
	if p.TLS {
		params.Set("security", "tls")
		if p.ServerName != "" {
			params.Set("sni", p.ServerName)
		} else if p.SNI != "" {
			params.Set("sni", p.SNI)
		}
	}
	if p.WSOpts != nil {
		if p.WSOpts.Path != "" {
			params.Set("path", p.WSOpts.Path)
		}
		if host, ok := p.WSOpts.Headers["Host"]; ok {
			params.Set("host", host)
		}
	}
	if p.GrpcOpts != nil && p.GrpcOpts.GrpcServiceName != "" {
		params.Set("serviceName", p.GrpcOpts.GrpcServiceName)
	}
	if p.PacketEncoding != "" {
		params.Set("packetEncoding", p.PacketEncoding)
	}
	if len(p.ALPN) > 0 {
		params.Set("alpn", strings.Join(p.ALPN, ","))
	}
	if p.ClientFingerprint != "" {
		params.Set("fp", p.ClientFingerprint)
	}

	query := ""
	if len(params) > 0 {
		query = "?" + params.Encode()
	}

	return fmt.Sprintf("vmess://%s@%s:%d%s#%s", p.UUID, p.Server, p.Port, query, url.QueryEscape(p.Name))
}

func buildVLESSURI(p clashProxy) string {
	params := url.Values{}
	params.Set("encryption", "none")

	if transport, ok := NormalizeV2RayTransport(p.Network); ok {
		if transport != "" {
			params.Set("type", transport)
		}
	} else {
		return ""
	}
	if p.Flow != "" {
		params.Set("flow", NormalizeVLESSFlow(p.Flow))
	}
	if p.TLS {
		params.Set("security", "tls")
		if p.ServerName != "" {
			params.Set("sni", p.ServerName)
		} else if p.SNI != "" {
			params.Set("sni", p.SNI)
		}
	}
	if p.RealityOpts != nil {
		params.Set("security", "reality")
		if p.RealityOpts.PublicKey != "" {
			params.Set("pbk", p.RealityOpts.PublicKey)
		}
		if p.RealityOpts.ShortID != "" {
			params.Set("sid", p.RealityOpts.ShortID)
		}
		if p.ServerName != "" {
			params.Set("sni", p.ServerName)
		}
	}
	if p.WSOpts != nil {
		if p.WSOpts.Path != "" {
			params.Set("path", p.WSOpts.Path)
		}
		if host, ok := p.WSOpts.Headers["Host"]; ok {
			params.Set("host", host)
		}
	}
	if p.GrpcOpts != nil && p.GrpcOpts.GrpcServiceName != "" {
		params.Set("serviceName", p.GrpcOpts.GrpcServiceName)
	}
	if p.PacketEncoding != "" {
		params.Set("packetEncoding", p.PacketEncoding)
	}
	if len(p.ALPN) > 0 {
		params.Set("alpn", strings.Join(p.ALPN, ","))
	}
	if p.ClientFingerprint != "" {
		params.Set("fp", p.ClientFingerprint)
	}

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", p.UUID, p.Server, p.Port, params.Encode(), url.QueryEscape(p.Name))
}

func buildTrojanURI(p clashProxy) string {
	params := url.Values{}
	if transport, ok := NormalizeV2RayTransport(p.Network); ok {
		if transport != "" {
			params.Set("type", transport)
		}
	} else {
		return ""
	}
	if p.ServerName != "" {
		params.Set("sni", p.ServerName)
	} else if p.SNI != "" {
		params.Set("sni", p.SNI)
	}
	if p.SkipCertVerify {
		params.Set("allowInsecure", "1")
	}
	if p.WSOpts != nil {
		if p.WSOpts.Path != "" {
			params.Set("path", p.WSOpts.Path)
		}
		if host, ok := p.WSOpts.Headers["Host"]; ok {
			params.Set("host", host)
		}
	}
	if p.GrpcOpts != nil && p.GrpcOpts.GrpcServiceName != "" {
		params.Set("serviceName", p.GrpcOpts.GrpcServiceName)
	}
	if len(p.ALPN) > 0 {
		params.Set("alpn", strings.Join(p.ALPN, ","))
	}
	if p.ClientFingerprint != "" {
		params.Set("fp", p.ClientFingerprint)
	}

	uri := &url.URL{
		Scheme: "trojan",
		User:   url.User(p.Password),
		Host:   netJoinHostPort(p.Server, p.Port),
	}
	if len(params) > 0 {
		uri.RawQuery = params.Encode()
	}
	uri.Fragment = p.Name
	return uri.String()
}

func buildShadowsocksURI(p clashProxy) string {
	// Encode method:password in base64
	userInfo := base64.StdEncoding.EncodeToString([]byte(p.Cipher + ":" + p.Password))
	params := url.Values{}
	if plugin, pluginOptions := serializeClashShadowsocksPlugin(p.Plugin, p.PluginOpts); plugin != "" {
		params.Set("plugin", plugin)
		if pluginOptions != "" {
			params.Set("plugin-opts", pluginOptions)
		}
	}

	query := ""
	if len(params) > 0 {
		query = "?" + params.Encode()
	}

	return fmt.Sprintf("ss://%s@%s:%d%s#%s", userInfo, p.Server, p.Port, query, url.QueryEscape(p.Name))
}

func serializeClashShadowsocksPlugin(plugin string, pluginOpts map[string]interface{}) (string, string) {
	normalizedPlugin := strings.TrimSpace(strings.ToLower(plugin))
	if normalizedPlugin == "" {
		return "", ""
	}

	switch normalizedPlugin {
	case "obfs":
		normalizedPlugin = "obfs-local"
	}

	if len(pluginOpts) == 0 {
		return normalizedPlugin, ""
	}

	pairs := make([]string, 0, len(pluginOpts))
	keys := make([]string, 0, len(pluginOpts))
	for key := range pluginOpts {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		keys = append(keys, trimmed)
	}
	sort.Strings(keys)

	for _, key := range keys {
		rawValue, ok := pluginOpts[key]
		if !ok || rawValue == nil {
			continue
		}
		value := strings.TrimSpace(fmt.Sprint(rawValue))
		if value == "" {
			continue
		}

		normalizedKey := key
		if normalizedPlugin == "obfs-local" {
			switch strings.ToLower(key) {
			case "mode":
				normalizedKey = "obfs"
			case "host":
				normalizedKey = "obfs-host"
			}
		}
		pairs = append(pairs, normalizedKey+"="+value)
	}

	return normalizedPlugin, strings.Join(pairs, ";")
}

func buildHysteria2URI(p clashProxy) string {
	params := url.Values{}
	if p.ServerName != "" {
		params.Set("sni", p.ServerName)
	} else if p.SNI != "" {
		params.Set("sni", p.SNI)
	}
	if p.SkipCertVerify {
		params.Set("insecure", "1")
	}
	if len(p.ALPN) > 0 {
		params.Set("alpn", strings.Join(p.ALPN, ","))
	}
	if p.UpMbps > 0 {
		params.Set("upMbps", strconv.Itoa(p.UpMbps))
	}
	if p.DownMbps > 0 {
		params.Set("downMbps", strconv.Itoa(p.DownMbps))
	}
	if p.Obfs != "" {
		params.Set("obfs", p.Obfs)
		if p.ObfsPassword != "" {
			params.Set("obfs-password", p.ObfsPassword)
		}
	}

	u := &url.URL{
		Scheme:   "hysteria2",
		User:     url.User(p.Password),
		Host:     netJoinHostPort(p.Server, p.Port),
		RawQuery: params.Encode(),
		Fragment: p.Name,
	}
	return u.String()
}

func buildAnyTLSURI(p clashProxy) string {
	params := url.Values{}
	if p.ServerName != "" {
		params.Set("sni", p.ServerName)
	} else if p.SNI != "" {
		params.Set("sni", p.SNI)
	}
	if p.SkipCertVerify {
		params.Set("insecure", "1")
	}
	if len(p.ALPN) > 0 {
		params.Set("alpn", strings.Join(p.ALPN, ","))
	}

	u := &url.URL{
		Scheme:   "anytls",
		User:     url.User(p.Password),
		Host:     netJoinHostPort(p.Server, p.Port),
		RawQuery: params.Encode(),
		Fragment: p.Name,
	}
	return u.String()
}

// RLock acquires a read lock on the config.
