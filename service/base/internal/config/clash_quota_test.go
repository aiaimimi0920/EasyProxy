package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestClashQuotaMarkerSurvivesURIConversion(t *testing.T) {
	marker := "[epq:" + strings.Repeat("a", 64) + "," + strings.Repeat("b", 64) + "]"
	for _, protocol := range []string{"http", "vmess", "vless", "trojan", "ss", "hysteria2", "anytls"} {
		t.Run(protocol, func(t *testing.T) {
			input := "proxies:\n  - name: '" + marker + " 中文 + name'\n" +
				"    type: " + protocol + "\n    server: 192.0.2.1\n    port: 443\n" +
				"    uuid: 00000000-0000-4000-8000-000000000001\n    password: fixture\n    cipher: aes-128-gcm\n"
			nodes, err := parseClashYAML(input)
			if err != nil || len(nodes) != 1 {
				t.Fatalf("expected one converted node: count=%d err=%v", len(nodes), err)
			}
			_, fragment, found := strings.Cut(nodes[0].URI, "#")
			decoded, err := url.QueryUnescape(fragment)
			if !found || err != nil || !strings.HasPrefix(decoded, marker+" ") {
				t.Fatal("quota provenance marker was lost during URI conversion")
			}
		})
	}
}
