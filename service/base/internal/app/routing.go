package app

import (
	"log"
	"net/netip"
	"strings"

	"easy_proxies/internal/geoip"
)

// geoipCountryLookup adapts geoip.Lookup to routerule.CountryLookup so GEOIP
// rules can classify literal-IP destinations by country.
type geoipCountryLookup struct{ l *geoip.Lookup }

func (g geoipCountryLookup) CountryISO(ip netip.Addr) string {
	if g.l == nil {
		return ""
	}
	info := g.l.LookupIP(ip.String())
	return strings.ToUpper(strings.TrimSpace(info.ISOCode))
}

// dispatchLogger adapts the standard logger to dispatch.Logger.
type dispatchLogger struct{}

func (dispatchLogger) Infof(format string, args ...any) { log.Printf(format, args...) }
func (dispatchLogger) Warnf(format string, args ...any) { log.Printf(format, args...) }
