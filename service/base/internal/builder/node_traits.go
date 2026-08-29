package builder

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"easy_proxies/internal/config"
)

type nodeRoutingTraits struct {
	ProtocolFamily string
	NodeMode       string
	DomainFamily   string
}

func describeNodeRoutingTraits(rawURI string) nodeRoutingTraits {
	trimmed := strings.TrimSpace(rawURI)
	if trimmed == "" {
		return nodeRoutingTraits{}
	}

	protocol := normalizeProtocolFamily(uriScheme(trimmed))
	traits := nodeRoutingTraits{ProtocolFamily: protocol}
	if protocol == "vmess" {
		if parsed, ok := parseVMessTraits(trimmed); ok {
			return parsed
		}
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		traits.NodeMode = defaultNodeMode(protocol)
		return traits
	}

	query := parsed.Query()
	server := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	security := normalizeSecurity(protocol, strings.TrimSpace(query.Get("security")))
	transport := normalizeTransport(protocol, strings.TrimSpace(query.Get("type")), strings.TrimSpace(query.Get("network")))
	hostHint := firstNonEmpty(query.Get("sni"), query.Get("peer"), query.Get("host"), server)

	traits.NodeMode = buildNodeMode(security, transport)
	traits.DomainFamily = domainFamily(hostHint)
	return traits
}

func parseVMessTraits(rawURI string) (nodeRoutingTraits, bool) {
	traits := nodeRoutingTraits{ProtocolFamily: "vmess"}
	encoded := strings.TrimPrefix(strings.TrimSpace(rawURI), "vmess://")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return nodeRoutingTraits{}, false
		}
	}

	var vmess vmessJSON
	if err := json.Unmarshal(decoded, &vmess); err != nil {
		return nodeRoutingTraits{}, false
	}

	security := "plain"
	if strings.EqualFold(strings.TrimSpace(vmess.TLS), "tls") {
		security = "tls"
	}
	transport := normalizeTransport("vmess", strings.TrimSpace(vmess.Net), "")
	hostHint := firstNonEmpty(vmess.SNI, vmess.Host, vmess.Add)

	traits.NodeMode = buildNodeMode(security, transport)
	traits.DomainFamily = domainFamily(hostHint)
	return traits, true
}

func uriScheme(rawURI string) string {
	if idx := strings.Index(rawURI, "://"); idx > 0 {
		return strings.ToLower(strings.TrimSpace(rawURI[:idx]))
	}
	return ""
}

func normalizeProtocolFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hy2":
		return "hysteria2"
	case "shadowsocks":
		return "ss"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeSecurity(protocol string, value string) string {
	security := strings.ToLower(strings.TrimSpace(value))
	if security != "" && security != "none" {
		return security
	}
	switch protocol {
	case "trojan", "anytls", "hysteria2":
		return "tls"
	default:
		return "plain"
	}
}

func normalizeTransport(protocol string, primary string, fallback string) string {
	if transport, ok := config.NormalizeV2RayTransport(primary); ok && transport != "" {
		return transport
	}
	if transport, ok := config.NormalizeV2RayTransport(fallback); ok && transport != "" {
		return transport
	}
	switch protocol {
	case "hysteria2":
		return "udp"
	default:
		return "tcp"
	}
}

func buildNodeMode(security string, transport string) string {
	left := strings.TrimSpace(strings.ToLower(security))
	if left == "" {
		left = "plain"
	}
	right := strings.TrimSpace(strings.ToLower(transport))
	if right == "" {
		right = "tcp"
	}
	return left + "/" + right
}

func defaultNodeMode(protocol string) string {
	return buildNodeMode(normalizeSecurity(protocol, ""), normalizeTransport(protocol, "", ""))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func domainFamily(host string) string {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	if trimmed == "" {
		return ""
	}
	if parsed := net.ParseIP(trimmed); parsed != nil {
		if ipv4 := parsed.To4(); ipv4 != nil {
			return fmt.Sprintf("%d.%d.%d.0/24", ipv4[0], ipv4[1], ipv4[2])
		}
		return parsed.String()
	}
	labels := strings.Split(trimmed, ".")
	if len(labels) <= 2 {
		return trimmed
	}
	suffix := labels[len(labels)-2] + "." + labels[len(labels)-1]
	if len(labels) >= 3 && usesThreeLabelSuffix(suffix) {
		return labels[len(labels)-3] + "." + suffix
	}
	return suffix
}

func usesThreeLabelSuffix(value string) bool {
	switch value {
	case "co.uk", "org.uk", "ac.uk", "gov.uk", "com.cn", "net.cn", "org.cn", "com.hk", "com.tw", "co.jp":
		return true
	default:
		return false
	}
}

// printProxyLinks prints all proxy connection information at startup
