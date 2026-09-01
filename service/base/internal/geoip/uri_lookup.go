package geoip

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	uriLookupTimeout     = 3 * time.Second
	uriLookupConcurrency = 16
)

type ipAddressResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type hostResolution struct {
	host string
	ip   string
}

// LookupURIs resolves node hostnames within one shared deadline so a broken
// resolver cannot turn startup into a per-node sequence of DNS timeouts.
func (l *Lookup) LookupURIs(uris []string) []RegionInfo {
	regions := make([]RegionInfo, len(uris))
	for index := range regions {
		regions[index] = unknownRegion()
	}
	if l == nil || len(uris) == 0 || !l.IsEnabled() {
		return regions
	}

	hosts := resolveURIHosts(uris, net.DefaultResolver, uriLookupTimeout, uriLookupConcurrency)
	for index, host := range hosts {
		if host != "" {
			regions[index] = l.LookupIP(host)
		}
	}
	return regions
}

func (l *Lookup) LookupURI(uri string) RegionInfo {
	return l.LookupURIs([]string{uri})[0]
}

func resolveURIHosts(
	uris []string,
	resolver ipAddressResolver,
	timeout time.Duration,
	maxConcurrency int,
) []string {
	resolved := make([]string, len(uris))
	pending := make(map[string][]int)
	for index, uri := range uris {
		host := strings.ToLower(strings.TrimSpace(extractHostFromURI(uri)))
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			resolved[index] = ip.String()
			continue
		}
		pending[host] = append(pending[host], index)
	}
	if len(pending) == 0 {
		return resolved
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if timeout <= 0 {
		timeout = uriLookupTimeout
	}
	if maxConcurrency <= 0 || maxConcurrency > len(pending) {
		maxConcurrency = len(pending)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	jobs := make(chan string, len(pending))
	results := make(chan hostResolution, len(pending))
	for host := range pending {
		jobs <- host
	}
	close(jobs)

	var workers sync.WaitGroup
	workers.Add(maxConcurrency)
	for range maxConcurrency {
		go func() {
			defer workers.Done()
			for host := range jobs {
				if ctx.Err() != nil {
					return
				}
				addresses, err := resolver.LookupIPAddr(ctx, host)
				value := ""
				if err == nil && len(addresses) > 0 && addresses[0].IP != nil {
					value = addresses[0].IP.String()
				}
				results <- hostResolution{host: host, ip: value}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(results)
	}()

	for result := range results {
		for _, index := range pending[result.host] {
			resolved[index] = result.ip
		}
	}
	return resolved
}

func unknownRegion() RegionInfo {
	return RegionInfo{Code: RegionOther, Country: "Unknown", ISOCode: ""}
}
