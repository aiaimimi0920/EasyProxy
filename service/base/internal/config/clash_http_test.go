package config

import (
	"net/url"
	"testing"
)

func TestClashCandidatePreservesHTTPNodesAndTransport(t *testing.T) {
	nodes, err := parseClashYAML(`proxies:
  - {name: vmess-http, type: vmess, server: example.org, port: 80, uuid: 00000000-0000-0000-0000-000000000001, network: http, http-opts: {method: GET, path: [/tunnel], headers: {Host: [one.example, two.example], X-Test: [value]}}}
  - {name: anonymous-http, type: http, server: '2001:db8::1', port: 443, tls: true, skip-cert-verify: true, sni: proxy.example}
  - {name: authenticated-http, type: http, server: example.org, port: 443, tls: true, username: 'user@domain', password: 'p:a/ss'}
`)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("expected all three candidates, count=%d err=%v", len(nodes), err)
	}
	vmess, _ := url.Parse(nodes[0].URI)
	q := vmess.Query()
	if q.Get("type") != "http" || q.Get("path") != "/tunnel" || q.Get("method") != "GET" || len(q["host"]) != 2 || q.Get("httpHeaders") != `{"X-Test":["value"]}` {
		t.Fatalf("HTTP transport options were lost: %v", q)
	}
	anonymous, err := url.Parse(nodes[1].URI)
	if err != nil || anonymous.Scheme != "http" || anonymous.Host != "[2001:db8::1]:443" || anonymous.User != nil || anonymous.Query().Get("security") != "tls" || anonymous.Query().Get("insecure") != "1" || anonymous.Query().Get("sni") != "proxy.example" {
		t.Fatalf("anonymous TLS HTTP proxy was not preserved: %v", err)
	}
	authenticated, _ := url.Parse(nodes[2].URI)
	password, _ := authenticated.User.Password()
	if authenticated.User.Username() != "user@domain" || password != "p:a/ss" || authenticated.Query().Get("security") != "tls" || authenticated.Query().Get("insecure") != "" {
		t.Fatal("HTTP proxy authentication/TLS options were not preserved")
	}
}
