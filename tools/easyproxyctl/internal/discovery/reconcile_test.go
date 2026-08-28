package discovery

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	found       []Resource
	created     Resource
	createCalls int
}

func (f *fakeProvider) FindExact(context.Context, string, string) ([]Resource, error) {
	return f.found, nil
}

func (f *fakeProvider) Create(context.Context, string, string) (Resource, error) {
	f.createCalls++
	return f.created, nil
}

func TestBootstrapReusesExactResource(t *testing.T) {
	provider := &fakeProvider{found: []Resource{{Kind: "d1", Name: "demo-misub-d1", ID: "db-1"}}}
	resource, created, err := Reconcile(context.Background(), provider, ModeBootstrap, "d1", "demo-misub-d1")
	if err != nil || created || resource.ID != "db-1" || provider.createCalls != 0 {
		t.Fatalf("Reconcile() = (%#v, %t, %v), create calls = %d", resource, created, err, provider.createCalls)
	}
}

func TestBootstrapCreatesOnlyMissingResource(t *testing.T) {
	provider := &fakeProvider{created: Resource{Kind: "r2", Name: "demo-artifacts", ID: "bucket-1"}}
	_, created, err := Reconcile(context.Background(), provider, ModeBootstrap, "r2", "demo-artifacts")
	if err != nil || !created || provider.createCalls != 1 {
		t.Fatalf("Reconcile() created = %t, error = %v, calls = %d", created, err, provider.createCalls)
	}
}

func TestUpdateNeverCreatesMissingResource(t *testing.T) {
	provider := &fakeProvider{created: Resource{Kind: "d1", Name: "demo-misub-d1", ID: "unexpected"}}
	_, _, err := Reconcile(context.Background(), provider, ModeUpdate, "d1", "demo-misub-d1")
	if !errors.Is(err, ErrMissingResource) || provider.createCalls != 0 {
		t.Fatalf("Reconcile() error = %v, create calls = %d", err, provider.createCalls)
	}
}

func TestAmbiguousIdentityStops(t *testing.T) {
	provider := &fakeProvider{found: []Resource{
		{Kind: "d1", Name: "demo-misub-d1", ID: "db-1"},
		{Kind: "d1", Name: "demo-misub-d1", ID: "db-2"},
	}}
	_, _, err := Reconcile(context.Background(), provider, ModeBootstrap, "d1", "demo-misub-d1")
	if err == nil || provider.createCalls != 0 {
		t.Fatalf("Reconcile() error = %v, create calls = %d", err, provider.createCalls)
	}
}
