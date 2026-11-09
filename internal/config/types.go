package config

import "time"

// MetricType is the Prometheus metric type for an item mapping.
type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"
	MetricTypeCounter MetricType = "counter"
)

// Config is the fully parsed exporter configuration.
type Config struct {
	Server   ServerConfig          `yaml:"server"`
	Defaults Defaults              `yaml:"defaults"`
	Hosts    map[string]HostConfig `yaml:"hosts"`
	Modules  map[string]Module     `yaml:"modules"`
	Groups   []Group               `yaml:"groups"`
}

// ServerConfig holds HTTP server and logging settings.
type ServerConfig struct {
	ListenAddr  string `yaml:"listen_addr"`
	LogLevel    string `yaml:"log_level"`
	LogEncoding string `yaml:"log_encoding"`
}

// Defaults holds the fallback port/timeout used when a host has no override.
type Defaults struct {
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
}

// HostConfig holds optional per-host overrides of port/timeout.
type HostConfig struct {
	Port    *int           `yaml:"port"`
	Timeout *time.Duration `yaml:"timeout"`
}

// Module is a named set of item mappings (simple and LLD-discovered).
type Module struct {
	Items           []Item           `yaml:"items"`
	DiscoveredItems []DiscoveredItem `yaml:"discovered_items"`
}

// Item maps one simple Zabbix item key to one Prometheus metric.
type Item struct {
	Key    string     `yaml:"key"`
	Metric string     `yaml:"metric"`
	Type   MetricType `yaml:"type"`
	Help   string     `yaml:"help"`
}

// DiscoveredItem maps an LLD discovery key plus a parameterized value key
// to a Prometheus metric with one label populated from the discovered macro.
type DiscoveredItem struct {
	DiscoveryKey string     `yaml:"discovery_key"`
	ValueKey     string     `yaml:"value_key"`
	Metric       string     `yaml:"metric"`
	Type         MetricType `yaml:"type"`
	Help         string     `yaml:"help"`
	LabelName    string     `yaml:"label_name"`
	LabelField   string     `yaml:"label_field"`
}

// Group aggregates a list of hosts under a list of modules.
type Group struct {
	Name    string   `yaml:"name"`
	Hosts   []string `yaml:"hosts"`
	Modules []string `yaml:"modules"`
}
