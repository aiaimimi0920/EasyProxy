package builder

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"easy_proxies/internal/config"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func buildV2RayTransport(query url.Values) (*option.V2RayTransportOptions, error) {
	transportType, ok := config.NormalizeV2RayTransport(query.Get("type"))
	if !ok {
		return nil, fmt.Errorf("unsupported transport type %q", strings.TrimSpace(query.Get("type")))
	}
	if transportType == "" {
		return nil, nil
	}
	options := &option.V2RayTransportOptions{Type: transportType}
	switch transportType {
	case C.V2RayTransportTypeWebsocket:
		wsPath, earlyDataSize, earlyDataHeader := parseWebSocketPathAndEarlyData(query)
		options.WebsocketOptions.Path = wsPath
		if earlyDataSize > 0 {
			options.WebsocketOptions.MaxEarlyData = earlyDataSize
			options.WebsocketOptions.EarlyDataHeaderName = earlyDataHeader
		}
		headers := badoption.HTTPHeader{}
		if host := query.Get("host"); host != "" {
			headers["Host"] = []string{host}
		}
		if userAgent := websocketDefaultUserAgent(query.Get("fp")); userAgent != "" {
			headers["User-Agent"] = []string{userAgent}
		}
		if len(headers) > 0 {
			options.WebsocketOptions.Headers = headers
		}
	case C.V2RayTransportTypeHTTP:
		options.HTTPOptions.Path = query.Get("path")
		if host := query.Get("host"); host != "" {
			options.HTTPOptions.Host = badoption.Listable[string]{host}
		}
	case C.V2RayTransportTypeGRPC:
		options.GRPCOptions.ServiceName = query.Get("serviceName")
	case C.V2RayTransportTypeHTTPUpgrade:
		options.HTTPUpgradeOptions.Path = query.Get("path")
		if host := query.Get("host"); host != "" {
			options.HTTPUpgradeOptions.Headers = badoption.HTTPHeader{"Host": {host}}
		}
	default:
		return nil, fmt.Errorf("unsupported transport type %q", transportType)
	}
	return options, nil
}

func parseWebSocketPathAndEarlyData(query url.Values) (string, uint32, string) {
	rawPath := strings.TrimSpace(query.Get("path"))
	if rawPath == "" {
		rawPath = "/"
	} else if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}

	earlyDataHeader := strings.TrimSpace(query.Get("eh"))
	var embeddedQuery url.Values
	if idx := strings.Index(rawPath, "?"); idx != -1 && idx+1 < len(rawPath) {
		parsedQuery, err := url.ParseQuery(rawPath[idx+1:])
		if err == nil {
			embeddedQuery = parsedQuery
			if earlyDataHeader == "" {
				earlyDataHeader = strings.TrimSpace(parsedQuery.Get("eh"))
			}
		}
	}
	if earlyDataHeader == "" {
		earlyDataHeader = "Sec-WebSocket-Protocol"
	}

	earlyDataValue := strings.TrimSpace(query.Get("ed"))
	if earlyDataValue == "" && embeddedQuery != nil {
		earlyDataValue = strings.TrimSpace(embeddedQuery.Get("ed"))
	}
	if earlyDataValue == "" {
		return rawPath, 0, earlyDataHeader
	}

	earlyDataSize, err := strconv.Atoi(earlyDataValue)
	if err != nil || earlyDataSize <= 0 {
		return rawPath, 0, earlyDataHeader
	}

	return rawPath, uint32(earlyDataSize), earlyDataHeader
}

func websocketDefaultUserAgent(fingerprint string) string {
	switch strings.ToLower(strings.TrimSpace(fingerprint)) {
	case "firefox":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:137.0) Gecko/20100101 Firefox/137.0"
	case "safari":
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Safari/605.1.15"
	case "edge":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36 Edg/135.0.0.0"
	case "ios":
		return "Mozilla/5.0 (iPhone; CPU iPhone OS 18_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Mobile/15E148 Safari/604.1"
	case "android":
		return "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Mobile Safari/537.36"
	case "chrome", "random", "randomized", "":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
	default:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
	}
}
