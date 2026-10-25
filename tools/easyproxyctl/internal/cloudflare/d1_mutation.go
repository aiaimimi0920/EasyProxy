package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var ErrMigrationUnknown = errors.New("EASYPROXY_MIGRATION_WRITE_UNKNOWN")

// D1SingleWriter never delegates mutations to Wrangler or an automatically
// retrying SDK. A lost response remains UNKNOWN even when SQL is idempotent.
type D1SingleWriter struct {
	account, token, endpoint string
	client                   *http.Client
}

func NewD1SingleWriter(account, token string) (*D1SingleWriter, error) {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(account) || token == "" || strings.ContainsAny(token, "\r\n") || token == "verification-not-applicable" {
		return nil, errors.New("EASYPROXY_MIGRATION_CREDENTIALS_REJECTED")
	}
	return &D1SingleWriter{account: account, token: token, endpoint: "https://api.cloudflare.com/client/v4",
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

func (writer *D1SingleWriter) Apply(ctx context.Context, databaseID string, plan MigrationPlan) error {
	if !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(databaseID) || len(plan.Statements) == 0 {
		return errors.New("EASYPROXY_MIGRATION_IDENTITY_REJECTED")
	}
	for _, sql := range plan.Statements {
		body, _ := json.Marshal(map[string]string{"sql": sql})
		// A non-replayable body also prevents net/http transport from replaying
		// a request on a stale pooled connection. Do not set Idempotency-Key.
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			writer.endpoint+"/accounts/"+writer.account+"/d1/database/"+databaseID+"/query",
			io.NopCloser(strings.NewReader(string(body))))
		if err != nil {
			return ErrMigrationUnknown
		}
		request.Header.Set("Authorization", "Bearer "+writer.token)
		request.Header.Set("Content-Type", "application/json")
		response, err := writer.client.Do(request)
		if err != nil {
			return ErrMigrationUnknown
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		closeErr := response.Body.Close()
		var result struct {
			Success bool `json:"success"`
			Result  []struct {
				Success bool `json:"success"`
			} `json:"result"`
		}
		if readErr != nil || closeErr != nil || len(payload) > 1<<20 || response.StatusCode != http.StatusOK ||
			json.Unmarshal(payload, &result) != nil || !result.Success || len(result.Result) != 1 || !result.Result[0].Success {
			return ErrMigrationUnknown
		}
	}
	return nil
}
