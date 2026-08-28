package misub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 64 << 20

type Client struct {
	base *url.URL
	http *http.Client
}

type Export struct {
	Raw                   json.RawMessage
	ApplicationVersion    string
	DatabaseSchemaVersion int
	Counts                map[string]int
	LogicalDataSHA256     string
	DeploymentName        string
	PagesProject          string
	D1DatabaseID          string
	D1Binding             string
}

func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, errors.New("MiSub base URL must be an http(s) URL without credentials")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{base: base, http: &http.Client{Jar: jar, Timeout: timeout}}, nil
}

func (c *Client) Login(ctx context.Context, password string) error {
	if password == "" {
		return errors.New("MiSub admin password is required")
	}
	response, err := c.postJSON(ctx, "/api/login", []byte(`{"password":`+quoted(password)+`}`))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, 4096)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("MiSub login returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &result); err != nil || !result.Success {
		return errors.New("MiSub login response did not confirm success")
	}
	return nil
}

func (c *Client) Export(ctx context.Context) (Export, error) {
	response, err := c.postJSON(ctx, "/api/system/export", []byte(`{}`))
	if err != nil {
		return Export{}, err
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return Export{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Export{}, fmt.Errorf("MiSub export returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Success    bool            `json:"success"`
		ExportData json.RawMessage `json:"exportData"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || !envelope.Success || len(envelope.ExportData) == 0 {
		return Export{}, errors.New("MiSub export response is malformed")
	}
	return ValidateExport(envelope.ExportData)
}

func (c *Client) postJSON(ctx context.Context, path string, body []byte) (*http.Response, error) {
	target := c.base.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return c.http.Do(request)
}

func ValidateExport(raw json.RawMessage) (Export, error) {
	var value struct {
		Format             string         `json:"format"`
		FormatVersion      int            `json:"formatVersion"`
		ApplicationVersion string         `json:"applicationVersion"`
		SchemaVersion      int            `json:"schemaVersion"`
		Counts             map[string]int `json:"counts"`
		Integrity          struct {
			SHA256 string `json:"canonicalDataSha256"`
		} `json:"integrity"`
		ResourceIdentity struct {
			DeploymentName string `json:"deploymentName"`
			PagesProject   string `json:"pagesProject"`
			D1DatabaseID   string `json:"d1DatabaseId"`
			D1Binding      string `json:"d1Binding"`
		} `json:"resourceIdentity"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return Export{}, fmt.Errorf("decode MiSub logical backup: %w", err)
	}
	if value.Format != "misub-logical-backup" || value.FormatVersion != 1 || len(value.Data) == 0 || value.Counts == nil {
		return Export{}, errors.New("unsupported or incomplete MiSub logical backup")
	}
	var data interface{}
	decoder := json.NewDecoder(bytes.NewReader(value.Data))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		return Export{}, fmt.Errorf("decode MiSub logical data: %w", err)
	}
	canonical, err := json.Marshal(data)
	if err != nil {
		return Export{}, err
	}
	digest := sha256.Sum256(canonical)
	actual := hex.EncodeToString(digest[:])
	if value.Integrity.SHA256 == "" || actual != value.Integrity.SHA256 {
		return Export{}, errors.New("MiSub logical backup checksum mismatch")
	}
	return Export{
		Raw: raw, ApplicationVersion: value.ApplicationVersion, DatabaseSchemaVersion: value.SchemaVersion,
		Counts: value.Counts, LogicalDataSHA256: actual, DeploymentName: value.ResourceIdentity.DeploymentName,
		PagesProject: value.ResourceIdentity.PagesProject, D1DatabaseID: value.ResourceIdentity.D1DatabaseID,
		D1Binding: value.ResourceIdentity.D1Binding,
	}, nil
}

func quoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("MiSub response exceeds %d bytes", limit)
	}
	return data, nil
}
