package gatewayroute

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"easy_proxies/internal/geoip"
	"easy_proxies/internal/routerule"
)

type countryLookup struct{ lookup *geoip.Lookup }

func (g countryLookup) CountryISO(address netip.Addr) string {
	return g.lookup.LookupIP(address.String()).ISOCode
}

// The gateway owns its rules even when the explicit smart proxy is disabled.
// Client directives and device-profile selection are deliberately not changed.
func (r *routeOutbound) applyRules(providerRules []string) {
	rules := append([]string(nil), r.options.Rules...)
	rules = append(rules, providerRules...)
	if r.options.UseDefaultRules {
		rules = append(rules, routerule.DefaultRules()...)
	}
	// The gateway's configured fallback wins over FINAL lines in default lists.
	r.engine.SetRulesAndFinal(rules, routerule.NormalizePolicy(r.options.FinalPolicy))
}

func (r *routeOutbound) startRules() error {
	// Device profiles own their separate routing/provider lifecycle.
	if r.profiles != nil {
		return nil
	}
	if r.options.GeoIP.Enabled {
		path := strings.TrimSpace(r.options.GeoIP.DatabasePath)
		if path == "" {
			return fmt.Errorf("gateway GEOIP database path is empty")
		}
		interval := time.Duration(0)
		if r.options.GeoIP.AutoUpdateEnabled {
			interval = r.options.GeoIP.AutoUpdateInterval
			if interval <= 0 {
				interval = 24 * time.Hour
			}
		}
		lookup, err := geoip.NewWithAutoUpdate(path, interval)
		if err != nil {
			return fmt.Errorf("open gateway GEOIP database: %w", err)
		}
		r.geo = lookup
		r.engine.SetGeoLookup(countryLookup{lookup})
	}
	if len(r.options.RuleProviders) > 0 {
		specs := make([]routerule.ProviderSpec, 0, len(r.options.RuleProviders))
		for _, provider := range r.options.RuleProviders {
			specs = append(specs, routerule.ProviderSpec{
				URL: provider.URL, Policy: routerule.NormalizePolicy(provider.Policy),
				Interval: provider.Interval, Behavior: provider.Behavior,
			})
		}
		r.provider = routerule.NewProviderManager(specs, r.applyRules)
		r.provider.Start(r.ctx)
	}
	return nil
}
