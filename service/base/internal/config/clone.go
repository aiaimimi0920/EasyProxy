package config

import (
	"reflect"
)

func (c *Config) RLock() {
	if c != nil {
		c.mu.RLock()
	}
}

// RUnlock releases the read lock on the config.
func (c *Config) RUnlock() {
	if c != nil {
		c.mu.RUnlock()
	}
}

// Lock acquires a write lock on the config.
func (c *Config) Lock() {
	if c != nil {
		c.mu.Lock()
	}
}

// Unlock releases the write lock on the config.
func (c *Config) Unlock() {
	if c != nil {
		c.mu.Unlock()
	}
}

// Clone creates a deep copy of the Config (without the mutex).
// The caller must hold at least a read lock if the config may be modified concurrently.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	cloned := Config{
		Mode:                c.Mode,
		Listener:            c.Listener,
		MultiPort:           c.MultiPort,
		Pool:                c.Pool,
		Management:          c.Management,
		SubscriptionRefresh: c.SubscriptionRefresh,
		SourceSync:          c.SourceSync,
		GeoIP:               c.GeoIP,
		Nodes:               cloneConfigSlice(c.Nodes),
		Connectors:          cloneConfigSlice(c.Connectors),
		NodesFile:           c.NodesFile,
		Subscriptions:       cloneConfigSlice(c.Subscriptions),
		ExternalIP:          c.ExternalIP,
		LogLevel:            c.LogLevel,
		SkipCertVerify:      c.SkipCertVerify,
		DNS:                 c.DNS,
		DatabasePath:        c.DatabasePath,
		ExtraListeners:      cloneConfigSlice(c.ExtraListeners),
		LocalServer:         c.LocalServer,
		Routing:             c.Routing,
		Gateway:             c.Gateway,
		filePath:            c.filePath,
	}

	cloned.Management.Enabled = cloneConfigBool(c.Management.Enabled)
	cloned.Management.ProbeTargets = cloneConfigSlice(c.Management.ProbeTargets)
	cloned.DNS.Enabled = cloneConfigBool(c.DNS.Enabled)
	cloned.DNS.RemoteServers = cloneConfigSlice(c.DNS.RemoteServers)
	cloned.Routing.UseDefaultRules = cloneConfigBool(c.Routing.UseDefaultRules)
	cloned.Routing.Rules = cloneConfigSlice(c.Routing.Rules)
	cloned.Routing.RuleFiles = cloneConfigSlice(c.Routing.RuleFiles)
	cloned.Routing.RuleProviders = cloneConfigSlice(c.Routing.RuleProviders)
	cloned.Routing.NodeFilter.Countries = cloneConfigSlice(c.Routing.NodeFilter.Countries)
	cloned.Routing.NodeFilter.Regions = cloneConfigSlice(c.Routing.NodeFilter.Regions)
	cloned.Routing.NodeFilter.LongLived = cloneConfigBool(c.Routing.NodeFilter.LongLived)
	cloned.Gateway.Ingress.Interfaces = cloneConfigSlice(c.Gateway.Ingress.Interfaces)
	cloned.Gateway.Ingress.InterfacePatterns = cloneConfigSlice(c.Gateway.Ingress.InterfacePatterns)
	cloned.Gateway.Ingress.TrustedCIDRs = cloneConfigSlice(c.Gateway.Ingress.TrustedCIDRs)
	cloned.Gateway.Tun.Addresses = cloneConfigSlice(c.Gateway.Tun.Addresses)
	if c.Gateway.Devices != nil {
		cloned.Gateway.Devices = make(map[string]GatewayDeviceConfig, len(c.Gateway.Devices))
		for name, device := range c.Gateway.Devices {
			cloned.Gateway.Devices[name] = GatewayDeviceConfig{Addresses: cloneConfigSlice(device.Addresses)}
		}
	}
	cloned.SourceSync.FallbackSubscriptions = cloneConfigSlice(c.SourceSync.FallbackSubscriptions)
	cloned.SourceSync.ConnectorRuntime.Enabled = cloneConfigBool(c.SourceSync.ConnectorRuntime.Enabled)
	for idx := range cloned.Connectors {
		cloned.Connectors[idx].ConnectorConfig = cloneConfigStringMap(c.Connectors[idx].ConnectorConfig)
	}
	return &cloned
}

func cloneConfigSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
}

func cloneConfigBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneConfigStringMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	return cloneConfigValue(values).(map[string]any)
}

func cloneConfigValue(value any) any {
	if value == nil {
		return nil
	}
	cloned := cloneConfigReflectValue(reflect.ValueOf(value), make(map[configCloneVisit]reflect.Value))
	return cloned.Interface()
}

type configCloneVisit struct {
	typ      reflect.Type
	kind     reflect.Kind
	pointer  uintptr
	length   int
	capacity int
}

func cloneConfigReflectValue(value reflect.Value, visited map[configCloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneConfigReflectValue(value.Elem(), visited))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := configCloneVisit{
			typ:     value.Type(),
			kind:    value.Kind(),
			pointer: value.Pointer(),
		}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[visit] = cloned
		iter := value.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneConfigReflectValue(iter.Value(), visited))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := configCloneVisit{
			typ:      value.Type(),
			kind:     value.Kind(),
			pointer:  value.Pointer(),
			length:   value.Len(),
			capacity: value.Cap(),
		}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Cap())
		visited[visit] = cloned
		for idx := 0; idx < value.Len(); idx++ {
			cloned.Index(idx).Set(cloneConfigReflectValue(value.Index(idx), visited))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for idx := 0; idx < value.Len(); idx++ {
			cloned.Index(idx).Set(cloneConfigReflectValue(value.Index(idx), visited))
		}
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		if value.Elem().Kind() == reflect.Struct {
			return value
		}
		visit := configCloneVisit{
			typ:     value.Type(),
			kind:    value.Kind(),
			pointer: value.Pointer(),
		}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		cloned := reflect.New(value.Type().Elem())
		visited[visit] = cloned
		cloned.Elem().Set(cloneConfigReflectValue(value.Elem(), visited))
		return cloned
	default:
		return value
	}
}

// FilePath returns the config file path.
