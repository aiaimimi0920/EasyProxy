package cloudflare

import (
	"context"
	"errors"
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
