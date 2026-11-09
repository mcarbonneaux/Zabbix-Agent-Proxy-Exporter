package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and validates the exporter configuration from path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", path, err)
	}
	return &cfg, nil
}

func validate(cfg *Config) error {
	for name, module := range cfg.Modules {
		for _, item := range module.Items {
			if item.Type != MetricTypeGauge && item.Type != MetricTypeCounter {
				return fmt.Errorf("module %q item %q: invalid type %q (want gauge or counter)", name, item.Key, item.Type)
			}
		}
		for _, di := range module.DiscoveredItems {
			if di.Type != MetricTypeGauge && di.Type != MetricTypeCounter {
				return fmt.Errorf("module %q discovered item %q: invalid type %q (want gauge or counter)", name, di.DiscoveryKey, di.Type)
			}
		}
	}
	for _, group := range cfg.Groups {
		for _, moduleName := range group.Modules {
			if _, ok := cfg.Modules[moduleName]; !ok {
				return fmt.Errorf("group %q references unknown module %q", group.Name, moduleName)
			}
		}
	}
	return nil
}
