package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type runnerCall struct{ args []string }

type fakeRunner struct {
	outputs [][]byte
	errors  []error
	calls   []runnerCall
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, runnerCall{args: append([]string{}, args...)})
	index := len(f.calls) - 1
	var output []byte
	if index < len(f.outputs) {
		output = f.outputs[index]
	}
	if index < len(f.errors) {
		return output, f.errors[index]
	}
	return output, nil
}

func TestProviderListsExactD1ByName(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte(`[{"name":"other","uuid":"db-0"},{"name":"demo-d1","uuid":"db-1"}]`)}}
	provider := Provider{Runner: runner}
	resources, err := provider.FindExact(context.Background(), "d1", "demo-d1")
	if err != nil || len(resources) != 1 || resources[0].ID != "db-1" {
		t.Fatalf("FindExact() = %#v, %v", resources, err)
	}
	if !reflect.DeepEqual(runner.calls[0].args, []string{"d1", "list", "--json"}) {
		t.Fatalf("args = %#v", runner.calls[0].args)
	}
}

func TestProviderListsExactPagesByNameViaAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/accounts/account-1/pages/projects" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("page") != "1" || request.URL.Query().Get("per_page") != "10" {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		if request.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatal("missing bearer token")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"result":[{"name":"other"},{"name":"demo-pages","subdomain":"demo.pages.dev"}],"result_info":{"total_pages":1}}`))
	}))
	defer server.Close()

	provider := Provider{
		APIBaseURL: server.URL,
		HTTPClient: server.Client(),
		AccountID:  "account-1",
		APIToken:   "token-1",
	}
	resources, err := provider.FindExact(context.Background(), "pages", "demo-pages")
	if err != nil || len(resources) != 1 || resources[0].ID != "demo-pages" {
		t.Fatalf("FindExact() = %#v, %v", resources, err)
	}
	if resources[0].URL != "demo.pages.dev" {
		t.Fatalf("URL = %q", resources[0].URL)
	}
}

func TestProviderCreatesThenRediscovers(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte(`{"uuid":"ignored"}`),
		[]byte(`{"result":[{"name":"demo-d1","uuid":"db-1"}]}`),
	}}
	resource, err := (Provider{Runner: runner}).Create(context.Background(), "d1", "demo-d1")
	if err != nil || resource.ID != "db-1" || len(runner.calls) != 2 {
		t.Fatalf("Create() = %#v, %v, calls=%d", resource, err, len(runner.calls))
	}
}

func TestProviderDoesNotMaskCreateFailure(t *testing.T) {
	runner := &fakeRunner{errors: []error{errors.New("forbidden")}}
	_, err := (Provider{Runner: runner}).Create(context.Background(), "pages", "demo-pages")
	if err == nil || len(runner.calls) != 1 {
		t.Fatalf("Create() error = %v, calls=%d", err, len(runner.calls))
	}
}

func TestProviderRejectsMalformedList(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte(`{"unexpected":[]}`)}}
	_, err := (Provider{Runner: runner}).FindExact(context.Background(), "d1", "demo-d1")
	if err == nil {
		t.Fatal("FindExact() succeeded for malformed list")
	}
}
