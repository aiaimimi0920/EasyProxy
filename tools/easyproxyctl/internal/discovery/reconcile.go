package discovery

import (
	"context"
	"errors"
	"fmt"
)

type Mode string

const (
	ModeBootstrap Mode = "bootstrap"
	ModeUpdate    Mode = "update"
)

type Resource struct {
	Kind string
	Name string
	ID   string
	URL  string
}

type Provider interface {
	FindExact(context.Context, string, string) ([]Resource, error)
	Create(context.Context, string, string) (Resource, error)
}

var ErrMissingResource = errors.New("expected resource does not exist")

func Reconcile(ctx context.Context, provider Provider, mode Mode, kind, name string) (Resource, bool, error) {
	if mode != ModeBootstrap && mode != ModeUpdate {
		return Resource{}, false, fmt.Errorf("unsupported reconciliation mode %q", mode)
	}
	if kind == "" || name == "" {
		return Resource{}, false, errors.New("resource kind and name are required")
	}
	matches, err := provider.FindExact(ctx, kind, name)
	if err != nil {
		return Resource{}, false, fmt.Errorf("discover %s %q: %w", kind, name, err)
	}
	if len(matches) > 1 {
		return Resource{}, false, fmt.Errorf("discover %s %q: multiple exact matches", kind, name)
	}
	if len(matches) == 1 {
		if err := validateIdentity(matches[0], kind, name); err != nil {
			return Resource{}, false, err
		}
		return matches[0], false, nil
	}
	if mode == ModeUpdate {
		return Resource{}, false, fmt.Errorf("%w: %s %q", ErrMissingResource, kind, name)
	}
	created, err := provider.Create(ctx, kind, name)
	if err != nil {
		return Resource{}, false, fmt.Errorf("create %s %q: %w", kind, name, err)
	}
	if err := validateIdentity(created, kind, name); err != nil {
		return Resource{}, false, fmt.Errorf("created resource identity: %w", err)
	}
	return created, true, nil
}

func validateIdentity(resource Resource, kind, name string) error {
	if resource.Kind != kind || resource.Name != name || resource.ID == "" {
		return fmt.Errorf("resource identity mismatch: got kind=%q name=%q id_present=%t", resource.Kind, resource.Name, resource.ID != "")
	}
	return nil
}
