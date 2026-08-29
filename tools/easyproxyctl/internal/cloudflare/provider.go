package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aiaimimi0920/EasyProxy/tools/easyproxyctl/internal/discovery"
)

type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type CommandRunner struct {
	Executable string
	Prefix     []string
	Dir        string
}

func (r CommandRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	name := r.Executable
	if name == "" {
		name = "npx"
	}
	commandArgs := append(append([]string{}, r.Prefix...), args...)
	command := exec.CommandContext(ctx, name, commandArgs...)
	command.Dir = r.Dir
	command.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 4096 {
			message = message[len(message)-4096:]
		}
		return nil, fmt.Errorf("wrangler command failed: %w: %s", err, message)
	}
	return stdout.Bytes(), nil
}

type Provider struct {
	Runner           Runner
	ProductionBranch string
	APIBaseURL       string
	HTTPClient       *http.Client
	AccountID        string
	APIToken         string
}

func (p Provider) FindExact(ctx context.Context, kind, name string) ([]discovery.Resource, error) {
	if kind == "pages" {
		return p.findExactPages(ctx, name)
	}
	args, err := listArgs(kind)
	if err != nil {
		return nil, err
	}
	output, err := p.Runner.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	items, err := decodeList(output)
	if err != nil {
		return nil, fmt.Errorf("decode %s list: %w", kind, err)
	}
	resources := make([]discovery.Resource, 0, 1)
	for _, item := range items {
		itemName := firstString(item, "name", "project_name", "database_name")
		if itemName != name {
			continue
		}
		id := firstString(item, "uuid", "id")
		resources = append(resources, discovery.Resource{
			Kind: kind,
			Name: itemName,
			ID:   id,
			URL:  firstString(item, "url", "subdomain"),
		})
	}
	return resources, nil
}

func (p Provider) findExactPages(ctx context.Context, name string) ([]discovery.Resource, error) {
	accountID := strings.TrimSpace(p.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
	}
	token := strings.TrimSpace(p.APIToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN"))
	}
	if accountID == "" || token == "" {
		return nil, errors.New("Cloudflare account ID and API token are required for Pages discovery")
	}
	baseURL := strings.TrimRight(p.APIBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.cloudflare.com/client/v4"
	}
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resources := make([]discovery.Resource, 0, 1)
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf(
			"%s/accounts/%s/pages/projects?page=%d&per_page=100",
			baseURL,
			url.PathEscape(accountID),
			page,
		)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("list Cloudflare Pages projects: %w", err)
		}
		var payload struct {
			Success bool `json:"success"`
			Result  []struct {
				Name      string `json:"name"`
				Subdomain string `json:"subdomain"`
			} `json:"result"`
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload)
		closeErr := response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("list Cloudflare Pages projects returned status %d", response.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode Cloudflare Pages projects: %w", decodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close Cloudflare Pages response: %w", closeErr)
		}
		if !payload.Success {
			return nil, errors.New("Cloudflare Pages project listing was unsuccessful")
		}
		for _, item := range payload.Result {
			itemName := strings.TrimSpace(item.Name)
			if itemName == name {
				resources = append(resources, discovery.Resource{
					Kind: "pages",
					Name: itemName,
					ID:   itemName,
					URL:  strings.TrimSpace(item.Subdomain),
				})
			}
		}
		if payload.ResultInfo.TotalPages == 0 || page >= payload.ResultInfo.TotalPages {
			break
		}
	}
	return resources, nil
}

func (p Provider) Create(ctx context.Context, kind, name string) (discovery.Resource, error) {
	var args []string
	switch kind {
	case "pages":
		branch := p.ProductionBranch
		if branch == "" {
			branch = "main"
		}
		args = []string{"pages", "project", "create", name, "--production-branch", branch}
	case "d1":
		args = []string{"d1", "create", name, "--json"}
	default:
		return discovery.Resource{}, fmt.Errorf("unsupported Cloudflare resource kind %q", kind)
	}
	if _, err := p.Runner.Run(ctx, args...); err != nil {
		return discovery.Resource{}, err
	}
	matches, err := p.FindExact(ctx, kind, name)
	if err != nil {
		return discovery.Resource{}, err
	}
	if len(matches) != 1 {
		return discovery.Resource{}, fmt.Errorf("created %s %q but exact rediscovery returned %d matches", kind, name, len(matches))
	}
	return matches[0], nil
}

func listArgs(kind string) ([]string, error) {
	switch kind {
	case "d1":
		return []string{"d1", "list", "--json"}, nil
	default:
		return nil, fmt.Errorf("unsupported Cloudflare resource kind %q", kind)
	}
}

func decodeList(data []byte) ([]map[string]interface{}, error) {
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if object, ok := value.(map[string]interface{}); ok {
		value = object["result"]
	}
	items, ok := value.([]interface{})
	if !ok {
		return nil, errors.New("expected a JSON array or an object containing result array")
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]interface{})
		if !ok {
			return nil, errors.New("resource list contains a non-object item")
		}
		result = append(result, object)
	}
	return result, nil
}

func firstString(item map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
