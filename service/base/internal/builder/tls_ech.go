package builder

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	mDNS "github.com/miekg/dns"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

func buildTLSOptions(query url.Values, skipCertVerify bool) (*option.OutboundTLSOptions, error) {
	security := strings.ToLower(query.Get("security"))
	if security == "" || security == "none" {
		return nil, nil
	}
	tlsOptions := &option.OutboundTLSOptions{Enabled: true, Insecure: skipCertVerify}
	if sni := query.Get("sni"); sni != "" {
		tlsOptions.ServerName = sni
	}
	insecure := query.Get("allowInsecure")
	if insecure == "" {
		insecure = query.Get("insecure")
	}
	if insecure != "" {
		tlsOptions.Insecure = insecure == "1" || strings.EqualFold(insecure, "true")
	}
	if alpn := query.Get("alpn"); alpn != "" {
		tlsOptions.ALPN = badoption.Listable[string](strings.Split(alpn, ","))
	}
	fp := query.Get("fp")
	if fp != "" {
		tlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fp}
	}
	if echValue := query.Get("ech"); echValue != "" {
		echOptions := &option.OutboundECHOptions{Enabled: true}
		configPEM, err := resolveECHConfigPEM(echValue)
		if err != nil {
			log.Printf("⚠️  Failed to prefetch ECH config for %q: %v (falling back to runtime DNS)", echValue, err)
		} else if strings.TrimSpace(configPEM) != "" {
			echOptions.Config = badoption.Listable[string](splitNonEmptyLines(configPEM))
		}
		tlsOptions.ECH = echOptions
	}
	if security == "reality" {
		publicKey := strings.TrimSpace(query.Get("pbk"))
		if publicKey == "" {
			return nil, errors.New("reality missing public key")
		}
		shortID, err := normalizeRealityShortID(query.Get("sid"))
		if err != nil {
			return nil, err
		}
		tlsOptions.Reality = &option.OutboundRealityOptions{Enabled: true, PublicKey: publicKey, ShortID: shortID}
		// Reality requires uTLS; use default fingerprint if not specified
		if tlsOptions.UTLS == nil {
			if fp == "" {
				fp = "chrome"
			}
			tlsOptions.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fp}
		}
	}
	return tlsOptions, nil
}

func normalizeRealityShortID(value string) (string, error) {
	shortID := strings.TrimSpace(value)
	if shortID == "" {
		return "", nil
	}
	if _, err := hex.DecodeString(shortID); err != nil {
		return "", fmt.Errorf("invalid reality short_id %q: %w", shortID, err)
	}
	return strings.ToLower(shortID), nil
}

func splitNonEmptyLines(content string) []string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func parseECHQueryValue(value string) (string, string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "+", 2)
	queryServerName := strings.TrimSpace(parts[0])
	if queryServerName == "" {
		return "", "", false
	}
	dohURL := ""
	if len(parts) == 2 {
		dohURL = strings.TrimSpace(parts[1])
	}
	return queryServerName, dohURL, true
}

func resolveECHConfigPEMFromQuery(value string) (string, error) {
	queryServerName, dohURL, ok := parseECHQueryValue(value)
	if !ok {
		return "", fmt.Errorf("invalid ech query value %q", value)
	}
	if dohURL == "" {
		return "", nil
	}
	cacheKey := queryServerName + "|" + dohURL
	if cached, ok := echConfigCache.Load(cacheKey); ok {
		return cached.(string), nil
	}
	configList, err := fetchECHConfigList(queryServerName, dohURL, 15*time.Second)
	if err != nil {
		return "", err
	}
	configPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "ECH CONFIGS",
		Bytes: configList,
	}))
	echConfigCache.Store(cacheKey, configPEM)
	return configPEM, nil
}

func fetchECHConfigList(queryServerName string, dohURL string, timeout time.Duration) ([]byte, error) {
	message := new(mDNS.Msg)
	message.SetQuestion(mDNS.Fqdn(queryServerName), mDNS.TypeHTTPS)
	message.RecursionDesired = true
	wire, err := message.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack HTTPS RR query: %w", err)
	}
	request, err := http.NewRequest(http.MethodPost, dohURL, bytes.NewReader(wire))
	if err != nil {
		return nil, fmt.Errorf("create doh request: %w", err)
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute doh request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("doh returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read doh response: %w", err)
	}
	var dnsResponse mDNS.Msg
	if err := dnsResponse.Unpack(body); err != nil {
		return nil, fmt.Errorf("unpack doh response: %w", err)
	}
	if dnsResponse.Rcode != mDNS.RcodeSuccess {
		return nil, fmt.Errorf("doh rcode %s", mDNS.RcodeToString[dnsResponse.Rcode])
	}
	for _, rr := range dnsResponse.Answer {
		httpsRR, ok := rr.(*mDNS.HTTPS)
		if !ok {
			continue
		}
		for _, value := range httpsRR.Value {
			if value.Key().String() != "ech" {
				continue
			}
			if echValue, ok := value.(*mDNS.SVCBECHConfig); ok && len(echValue.ECH) > 0 {
				return echValue.ECH, nil
			}
			decoded, err := base64.StdEncoding.DecodeString(value.String())
			if err == nil && len(decoded) > 0 {
				return decoded, nil
			}
		}
	}
	return nil, fmt.Errorf("no ECH config found for %s via %s", queryServerName, dohURL)
}
