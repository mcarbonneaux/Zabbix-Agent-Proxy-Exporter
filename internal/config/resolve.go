package config

import (
	"fmt"
	"time"
)

// ResolvedTarget holds the fully resolved connection settings and module
// list for a single scrape of one target.
type ResolvedTarget struct {
	Host    string
	Port    int
	Timeout time.Duration
	Modules []string
}

// Resolve computes the modules, port, and timeout to use for target.
//
// Module resolution: if urlModules is non-empty, it replaces the modules
// that would otherwise come from the groups containing target. Otherwise,
// the result is the union of modules from every group that lists target.
//
// Port/timeout resolution cascades: Defaults -> Hosts[target] override ->
// urlPort/urlTimeout. urlPort and urlTimeout are pointers so a caller can
// distinguish "not provided" (nil) from an explicit value (including the
// zero value) provided via the URL.
func Resolve(cfg *Config, target string, urlModules []string, urlPort *int, urlTimeout *time.Duration) (ResolvedTarget, error) {
	modules := urlModules
	if len(modules) == 0 {
		modules = modulesFromGroups(cfg, target)
	} else {
		for _, m := range modules {
			if _, ok := cfg.Modules[m]; !ok {
				return ResolvedTarget{}, fmt.Errorf("config: module %q (from ?module=) is not defined in config", m)
			}
		}
	}
	if len(modules) == 0 {
		return ResolvedTarget{}, fmt.Errorf("config: no module resolved for target %q; pass ?module=", target)
	}

	port := cfg.Defaults.Port
	timeout := cfg.Defaults.Timeout
	if host, ok := cfg.Hosts[target]; ok {
		if host.Port != nil {
			port = *host.Port
		}
		if host.Timeout != nil {
			timeout = *host.Timeout
		}
	}
	if urlPort != nil {
		port = *urlPort
	}
	if urlTimeout != nil {
		timeout = *urlTimeout
	}

	return ResolvedTarget{
		Host:    target,
		Port:    port,
		Timeout: timeout,
		Modules: modules,
	}, nil
}

func modulesFromGroups(cfg *Config, target string) []string {
	seen := map[string]bool{}
	var modules []string
	for _, group := range cfg.Groups {
		if !containsHost(group.Hosts, target) {
			continue
		}
		for _, m := range group.Modules {
			if !seen[m] {
				seen[m] = true
				modules = append(modules, m)
			}
		}
	}
	return modules
}

func containsHost(hosts []string, target string) bool {
	for _, h := range hosts {
		if h == target {
			return true
		}
	}
	return false
}
