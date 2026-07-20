package profile

import (
	"context"
	"time"

	"easy_proxies/internal/routerule"
)

// ProviderRunner drives one profile's remote rule providers.
type ProviderRunner interface {
	Start(context.Context)
	Stop()
}

// ProviderStatus reports the health of a profile's provider set.
type ProviderStatus struct {
	Degraded  bool      `json:"degraded"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// ProviderFactory constructs a runner for a profile's provider specs.
type ProviderFactory func([]routerule.ProviderSpec, func([]string), func(ProviderStatus)) ProviderRunner

type providerRuntime struct {
	revision   int64
	generation uint64
	runner     ProviderRunner
	status     ProviderStatus
}

func defaultProviderFactory(specs []routerule.ProviderSpec, onRules func([]string), onStatus func(ProviderStatus)) ProviderRunner {
	statusSink := func(status routerule.ProviderStatus) {
		if onStatus == nil {
			return
		}
		onStatus(ProviderStatus{
			Degraded:  status.Degraded,
			LastError: status.LastError,
			UpdatedAt: status.UpdatedAt,
		})
	}
	return routerule.NewProviderManagerWithStatus(specs, onRules, statusSink)
}
