package config

import "time"

// MetricType is the Prometheus metric type for an item mapping.
type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"
	MetricTypeCounter MetricType = "counter"
)

// Protocol selects which Zabbix passive-check wire dialect to use for a
// host. Mirrors zabbixclient.Protocol's three values; kept as a separate
// type here (rather than importing zabbixclient) to avoid a dependency
// from config on zabbixclient — collector converts between the two at
// the one call site that needs both.
type Protocol string

const (
	ProtocolPlain  Protocol = "plain"
	ProtocolLegacy Protocol = "legacy"
	ProtocolJSON   Protocol = "json"
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

// Defaults holds the fallback port/timeout/protocol used when a host has
// no override.
type Defaults struct {
	Port     int           `yaml:"port"`
	Timeout  time.Duration `yaml:"timeout"`
	Protocol Protocol      `yaml:"protocol"`
}

// HostConfig holds optional per-host overrides of port/timeout/protocol.
type HostConfig struct {
	Port     *int           `yaml:"port"`
	Timeout  *time.Duration `yaml:"timeout"`
	Protocol *Protocol      `yaml:"protocol"`
}

// Module is a named set of item mappings (simple, LLD-discovered, and
// plain-text-line-enumerated).
type Module struct {
	Items           []Item           `yaml:"items"`
	DiscoveredItems []DiscoveredItem `yaml:"discovered_items"`
	LineListItems   []LineListItem   `yaml:"line_list_items"`
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

// LineListItem maps a Zabbix item key whose response is a plain,
// newline-separated list of entries (not JSON LLD) to one Prometheus
// series per non-empty line, each with value 1 and the line's text as a
// label. This supports legacy agents (pre-2.0, before Zabbix LLD existed)
// whose UserParameter scripts enumerate entities as plain text — e.g. one
// filesystem path per line — with no second key to query per entity, so
// unlike DiscoveredItem there is no ValueKey: presence (1) is all that can
// be reported.
type LineListItem struct {
	Key       string `yaml:"key"`
	Metric    string `yaml:"metric"`
	Help      string `yaml:"help"`
	LabelName string `yaml:"label_name"`
}

// Group aggregates a list of hosts under a list of modules. Hosts listed
// here are not required to also appear in Config.Hosts: a host with no
// entry there simply has no port/timeout override and falls back to
// Defaults.
type Group struct {
	Name    string   `yaml:"name"`
	Hosts   []string `yaml:"hosts"`
	Modules []string `yaml:"modules"`
}
