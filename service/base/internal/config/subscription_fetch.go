package config

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func loadNodesFromFile(path string) ([]NodeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseNodesFromContent(string(data))
}

// FetchSubscriptionNodes fetches and parses nodes from a subscription URL.
// When a cache directory is provided, successful results are persisted and
// reused until cacheTTL expires so startup reloads do not hammer upstreams.
func FetchSubscriptionNodes(subURL string, timeout time.Duration, cacheDir string, cacheTTL time.Duration) ([]NodeConfig, error) {
	return FetchSubscriptionNodesWithClient(subURL, timeout, cacheDir, cacheTTL, nil)
}

// FetchSubscriptionNodesWithClient fetches and parses nodes from a subscription
// URL while allowing callers to reuse a shared HTTP client.
func FetchSubscriptionNodesWithClient(
	subURL string,
	timeout time.Duration,
	cacheDir string,
	cacheTTL time.Duration,
	client *http.Client,
) ([]NodeConfig, error) {
	if entry, err := readSubscriptionCache(cacheDir, subURL); err != nil {
		log.Printf("⚠️ Failed to read subscription cache for %q: %v", subURL, err)
	} else if entry != nil && cacheTTL > 0 && time.Since(entry.FetchedAt) < cacheTTL {
		if len(entry.Nodes) > 0 {
			log.Printf("♻️ Reusing cached subscription %q (age=%s)", subURL, time.Since(entry.FetchedAt).Round(time.Second))
			return cloneSubscriptionCacheNodes(entry.Nodes), nil
		}
		return nil, fmt.Errorf("subscription recently failed and is cooling down: %s", strings.TrimSpace(entry.LastError))
	}

	nodes, err := fetchSubscriptionNodesRemote(subURL, timeout, client)
	if err == nil {
		if cacheDir != "" {
			if writeErr := writeSubscriptionCache(cacheDir, subURL, nodes, ""); writeErr != nil {
				log.Printf("⚠️ Failed to persist subscription cache for %q: %v", subURL, writeErr)
			}
		}
		return nodes, nil
	}

	if entry, cacheErr := readSubscriptionCache(cacheDir, subURL); cacheErr != nil {
		log.Printf("⚠️ Failed to read stale subscription cache for %q: %v", subURL, cacheErr)
	} else if entry != nil {
		if cacheDir != "" {
			if writeErr := writeSubscriptionCache(cacheDir, subURL, entry.Nodes, err.Error()); writeErr != nil {
				log.Printf("⚠️ Failed to refresh subscription failure cache for %q: %v", subURL, writeErr)
			}
		}
		if len(entry.Nodes) == 0 {
			return nil, err
		}
		log.Printf(
			"⚠️ Subscription %q refresh failed: %v; using stale cache from %s",
			subURL,
			err,
			entry.FetchedAt.UTC().Format(time.RFC3339),
		)
		return cloneSubscriptionCacheNodes(entry.Nodes), nil
	}

	if cacheDir != "" {
		if writeErr := writeSubscriptionCache(cacheDir, subURL, nil, err.Error()); writeErr != nil {
			log.Printf("⚠️ Failed to persist subscription failure cache for %q: %v", subURL, writeErr)
		}
	}

	return nil, err
}

// loadNodesFromSubscription fetches and parses nodes from a subscription URL
// Supports multiple formats: base64 encoded, plain text, clash yaml, etc.
func loadNodesFromSubscription(subURL string, timeout time.Duration) ([]NodeConfig, error) {
	return FetchSubscriptionNodes(subURL, timeout, "", 0)
}

func fetchSubscriptionNodesRemote(subURL string, timeout time.Duration, client *http.Client) ([]NodeConfig, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if client == nil {
		transport := &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		}
		client = &http.Client{
			Transport: transport,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resp, err := fetchSubscriptionResponse(ctx, subURL, client)
	if err != nil {
		return nil, fmt.Errorf("fetch subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxSubscriptionBodyBytes {
		return nil, fmt.Errorf("subscription response exceeds %d bytes", maxSubscriptionBodyBytes)
	}

	content := string(body)

	// Try to detect and parse different formats
	nodes, err := ParseSubscriptionContent(content)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("subscription contained no usable nodes")
	}
	return nodes, nil
}

func fetchSubscriptionResponse(ctx context.Context, subURL string, client *http.Client) (*http.Response, error) {
	req, err := newSubscriptionFetchRequest(ctx, subURL)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err == nil {
		return resp, nil
	}
	if !isHTTP2ResponseHeaderTimeout(err) {
		return nil, err
	}

	req, err = newSubscriptionFetchRequest(ctx, subURL)
	if err != nil {
		return nil, err
	}
	resp, fallbackErr := newSubscriptionHTTP1Client().Do(req)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%w; HTTP/1.1 fallback failed: %v", err, fallbackErr)
	}
	return resp, nil
}

func newSubscriptionFetchRequest(ctx context.Context, subURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", subURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Set common headers to avoid being blocked.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "*/*")
	return req, nil
}

func newSubscriptionHTTP1Client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     false,
			TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
}

func isHTTP2ResponseHeaderTimeout(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "http2: timeout awaiting response headers")
}

func subscriptionCachePath(cacheDir string, subURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(subURL)))
	return filepath.Join(cacheDir, fmt.Sprintf("%x.json", sum))
}

func readSubscriptionCache(cacheDir string, subURL string) (*subscriptionCacheEntry, error) {
	if strings.TrimSpace(cacheDir) == "" || strings.TrimSpace(subURL) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(subscriptionCachePath(cacheDir, subURL))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entry subscriptionCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	if len(entry.Nodes) == 0 && strings.TrimSpace(entry.LastError) == "" {
		return nil, nil
	}
	return &entry, nil
}

func writeSubscriptionCache(cacheDir string, subURL string, nodes []NodeConfig, lastError string) error {
	if strings.TrimSpace(cacheDir) == "" || strings.TrimSpace(subURL) == "" {
		return nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	entry := subscriptionCacheEntry{
		URL:       strings.TrimSpace(subURL),
		FetchedAt: time.Now().UTC(),
		LastError: strings.TrimSpace(lastError),
		Nodes:     cloneSubscriptionCacheNodes(nodes),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	targetPath := subscriptionCachePath(cacheDir, subURL)
	tempPath := targetPath + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, targetPath)
}

func cloneSubscriptionCacheNodes(nodes []NodeConfig) []NodeConfig {
	if len(nodes) == 0 {
		return nil
	}
	cloned := make([]NodeConfig, 0, len(nodes))
	for _, node := range nodes {
		uri := strings.TrimSpace(node.URI)
		if uri == "" {
			continue
		}
		cloned = append(cloned, NodeConfig{
			Name: strings.TrimSpace(node.Name),
			URI:  uri,
		})
	}
	return cloned
}

// ParseSubscriptionContent tries to parse subscription content in various
// formats. It is shared by both config-time subscription loading and runtime
// source-sync/bootstrap refresh paths so manifest/fallback subscriptions are
// interpreted exactly the same way as local subscription URLs.
