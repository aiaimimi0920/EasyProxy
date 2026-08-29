package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

func (m *connectorRuntimeManager) fetchZenProxyRuntimeSources(cfg *config.Config, sources []RuntimeSource) ([]RuntimeSource, error) {
	if cfg == nil || len(sources) == 0 {
		return nil, nil
	}

	var runtimeSources []RuntimeSource
	var errs []string
	timeout := cfg.SourceSync.RequestTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	for _, source := range sources {
		if source.Kind != SourceKindConnector {
			continue
		}
		if extractStringOption(source.Options, "connector_type") != connectorTypeZenProxyClient {
			continue
		}

		connectorCfg, err := parseZenProxyConnectorConfig(extractMapOption(source.Options, "connector_config"))
		if err != nil {
			errs = append(errs, fmt.Sprintf("zenproxy connector %s config: %v", source.Name, err))
			continue
		}

		fetched, err := m.fetchZenProxyConnectorSource(cfg, source, connectorCfg, timeout)
		if err != nil {
			errs = append(errs, fmt.Sprintf("zenproxy connector %s fetch: %v", source.Name, err))
			continue
		}
		runtimeSources = append(runtimeSources, fetched...)
	}

	if len(errs) > 0 {
		return runtimeSources, errors.New(strings.Join(errs, "; "))
	}
	return runtimeSources, nil
}

func (m *connectorRuntimeManager) fetchZenProxyConnectorSource(cfg *config.Config, source RuntimeSource, connectorCfg zenProxyConnectorConfig, timeout time.Duration) ([]RuntimeSource, error) {
	endpoint, err := buildZenProxyFetchURL(source.Input, connectorCfg.FetchPath)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(m.ctx, timeout)
	defer cancel()

	requestFactory := func(requestCtx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		if connectorCfg.APIKey != "" && !connectorCfg.AuthInQuery {
			req.Header.Set("Authorization", "Bearer "+connectorCfg.APIKey)
		}

		values := req.URL.Query()
		if connectorCfg.AuthInQuery && connectorCfg.APIKey != "" {
			values.Set("api_key", connectorCfg.APIKey)
		}
		if connectorCfg.Count > 0 {
			values.Set("count", strconv.Itoa(connectorCfg.Count))
		}
		if connectorCfg.Country != "" {
			values.Set("country", connectorCfg.Country)
		}
		if connectorCfg.ProxyType != "" {
			values.Set("type", connectorCfg.ProxyType)
		}
		if connectorCfg.ProxyID != "" {
			values.Set("proxy_id", connectorCfg.ProxyID)
		}
		if connectorCfg.ChatGPT {
			values.Set("chatgpt", "true")
		}
		if connectorCfg.Google {
			values.Set("google", "true")
		}
		if connectorCfg.Residential {
			values.Set("residential", "true")
		}
		if connectorCfg.RiskMax != nil {
			values.Set("risk_max", strconv.FormatFloat(*connectorCfg.RiskMax, 'f', -1, 64))
		}
		req.URL.RawQuery = values.Encode()
		return req, nil
	}

	statusCode, responseBody, err := m.doZenProxyGET(ctx, requestFactory)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if statusCode != http.StatusOK {
		body := responseBody
		if len(body) > 16*1024 {
			body = body[:16*1024]
		}
		return nil, fmt.Errorf("unexpected status %d: %s", statusCode, strings.TrimSpace(string(body)))
	}

	var payload zenProxyFetchResponse
	if err := json.NewDecoder(bytes.NewReader(responseBody)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(payload.Proxies) == 0 {
		if strings.TrimSpace(payload.Message) != "" {
			return nil, errors.New(strings.TrimSpace(payload.Message))
		}
		return nil, fmt.Errorf("no proxies returned")
	}

	providerRef := strings.TrimSpace(source.ID)
	if providerRef == "" {
		providerRef = sourceKey(source)
	}

	runtimeSources := make([]RuntimeSource, 0, len(payload.Proxies))
	var conversionErrs []string
	for idx, item := range payload.Proxies {
		uri, err := config.BuildURIFromSingboxOutbound(item.Name, item.Outbound)
		if err != nil {
			conversionErrs = append(conversionErrs, fmt.Sprintf("%s: %v", firstNonEmpty(strings.TrimSpace(item.Name), strings.TrimSpace(item.ID), fmt.Sprintf("proxy-%d", idx+1)), err))
			continue
		}

		displayName := buildZenProxyRuntimeDisplayName(source.Name, item.Name, item.Server, item.Port, idx)
		runtimeSources = append(runtimeSources, RuntimeSource{
			ID:     providerRef,
			Kind:   SourceKindProxyURI,
			Name:   displayName,
			Input:  uri,
			Origin: firstNonEmpty(strings.TrimSpace(source.Origin), "manifest"),
			Options: map[string]any{
				"connector_type":       connectorTypeZenProxyClient,
				"connector_proxy_id":   strings.TrimSpace(item.ID),
				"connector_proxy_type": firstNonEmpty(strings.TrimSpace(item.Type), strings.TrimSpace(extractStringOption(item.Outbound, "type"))),
				"connector_provider":   firstNonEmpty(strings.TrimSpace(source.Name), "ZenProxy"),
			},
		})
	}

	if len(runtimeSources) == 0 && len(conversionErrs) > 0 {
		return nil, fmt.Errorf("all returned proxies failed conversion: %s", strings.Join(conversionErrs, "; "))
	}
	if len(conversionErrs) > 0 {
		return runtimeSources, fmt.Errorf("partial conversion failures: %s", strings.Join(conversionErrs, "; "))
	}
	return runtimeSources, nil
}

func (m *connectorRuntimeManager) doZenProxyGET(ctx context.Context, requestFactory func(context.Context) (*http.Request, error)) (int, []byte, error) {
	for attempt := 0; attempt < zenProxyFetchMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}

		attemptCtx := ctx
		cancelAttempt := func() {}
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			attemptsLeft := zenProxyFetchMaxAttempts - attempt
			if remaining <= 0 {
				return 0, nil, context.DeadlineExceeded
			}
			attemptBudget := remaining / time.Duration(attemptsLeft)
			if attemptBudget > 0 {
				attemptCtx, cancelAttempt = context.WithTimeout(ctx, attemptBudget)
			}
		}

		req, err := requestFactory(attemptCtx)
		if err != nil {
			cancelAttempt()
			return 0, nil, err
		}
		resp, err := m.httpClient.Do(req)
		if err != nil {
			cancelAttempt()
			if attempt+1 < zenProxyFetchMaxAttempts && ctx.Err() == nil && isRetryableZenProxyTransportError(err) {
				if waitErr := waitZenProxyRetry(ctx, attempt); waitErr != nil {
					return 0, nil, waitErr
				}
				continue
			}
			return 0, nil, err
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, zenProxyFetchBodyLimit+1))
		_ = resp.Body.Close()
		cancelAttempt()
		if readErr != nil {
			return 0, nil, fmt.Errorf("read response: %w", readErr)
		}
		if len(body) > zenProxyFetchBodyLimit {
			return 0, nil, fmt.Errorf("response exceeds %d bytes", zenProxyFetchBodyLimit)
		}
		if attempt+1 < zenProxyFetchMaxAttempts && isRetryableZenProxyStatus(resp.StatusCode) {
			if waitErr := waitZenProxyRetry(ctx, attempt); waitErr != nil {
				return 0, nil, waitErr
			}
			continue
		}
		return resp.StatusCode, body, nil
	}
	return 0, nil, errors.New("zenproxy request exhausted retry attempts")
}

func isRetryableZenProxyTransportError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func isRetryableZenProxyStatus(statusCode int) bool {
	return statusCode == http.StatusBadGateway || statusCode == http.StatusServiceUnavailable || statusCode == http.StatusGatewayTimeout
}

func waitZenProxyRetry(ctx context.Context, attempt int) error {
	delay := zenProxyFetchRetryBaseDelay * time.Duration(1<<attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
