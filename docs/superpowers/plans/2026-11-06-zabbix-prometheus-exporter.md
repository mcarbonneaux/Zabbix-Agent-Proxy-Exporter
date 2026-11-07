# Zabbix Agent Exporter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Prometheus exporter that proxies scrape requests to legacy Zabbix passive agents, translating configured Zabbix item keys into Prometheus metrics, so the legacy Zabbix server can be decommissioned.

**Architecture:** On-demand multi-target exporter (same pattern as `blackbox_exporter`/`snmp_exporter`): a single Go binary exposes `GET /metrics?target=<host>&module=<name>`, resolves YAML-configured modules/overrides via Viper, opens short-lived TCP connections speaking the Zabbix passive-agent binary protocol, and builds a temporary `prometheus.Registry` per request. No internal scheduler or cache.

**Tech Stack:** Go 1.27, `go.uber.org/zap` (logging), `github.com/spf13/viper` (config + hot-reload), `github.com/prometheus/client_golang` (metrics + HTTP handler), `gopkg.in/yaml.v3` (custom unmarshalling for the `hosts` map), standard library `net`/`encoding/json` for the Zabbix protocol.

Full design reference: `docs/superpowers/specs/2026-11-06-zabbix-prometheus-exporter-design.md`

---

## File Structure

```
go.mod / go.sum
cmd/zabbix_agent_exporter/main.go

internal/zabbixproto/
  frame.go            # ZBXD header encode/decode, read/write framed payload
  frame_test.go

internal/zabbixclient/
  client.go           # Client.GetItem, Client.Discover (dial, send, parse)
  client_test.go       # fake TCP server simulating a passive Zabbix agent

internal/config/
  types.go            # Config, Defaults, HostConfig, Module, Item, DiscoveredItem, Group
  hosts_yaml.go       # custom UnmarshalYAML for the hosts map (string key -> override block)
  load.go             # Load(path) (*Config, error), Watch(path, onReload)
  resolve.go          # Resolve(cfg, target string, urlModules []string) (ResolvedTarget, error)
  load_test.go
  resolve_test.go

internal/collector/
  collector.go        # ZabbixCollector implementing prometheus.Collector
  collector_test.go    # uses fake Zabbix TCP server + testutil.CollectAndCompare

internal/httpserver/
  server.go           # NewServer(cfg *config.Config, logger *zap.Logger) *http.Server
  handler.go          # metricsHandler, healthzHandler
  handler_test.go

configs/config.example.yaml
```

Dependency direction: `zabbixproto` ← `zabbixclient` ← `collector` ← `httpserver` ← `main`. `config` is independent and used by `collector` and `httpserver`. Each package is testable without the ones above it.

---

## Task 1: Module scaffolding

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add required dependencies**

Run:
```bash
go get go.uber.org/zap@latest
go get github.com/spf13/viper@latest
go get github.com/prometheus/client_golang@latest
go get gopkg.in/yaml.v3@latest
```
Expected: `go.mod` gains `require` entries for all four modules (plus transitive deps), `go.sum` is created.

- [ ] **Step 2: Verify module builds**

Run: `go build ./...`
Expected: succeeds with no files yet beyond `go.mod`/`go.sum` (no-op build, exit code 0 — if it errors "no Go files", that's expected since no `.go` files exist yet; skip this check and proceed).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add zap, viper, client_golang, yaml.v3 dependencies"
```

---

## Task 2: Zabbix wire protocol (`internal/zabbixproto`)

**Files:**
- Create: `internal/zabbixproto/frame.go`
- Test: `internal/zabbixproto/frame_test.go`

This package encodes/decodes the Zabbix passive-agent framing:
`"ZBXD"` (4 bytes) + flags (1 byte, `0x01`) + data length (uint32 LE) +
reserved (uint32 LE, always 0) + payload bytes.

- [ ] **Step 1: Write failing tests for WriteFrame/ReadFrame round-trip and error cases**

```go
// internal/zabbixproto/frame_test.go
package zabbixproto

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriteFrameThenReadFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("3.0005\n")

	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame returned error: %v", err)
	}

	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got payload %q, want %q", got, payload)
	}
}

func TestReadFrameRejectsBadMagic(t *testing.T) {
	buf := bytes.NewBufferString("XXXX\x01\x05\x00\x00\x00\x00\x00\x00\x00hello")

	_, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
	if !strings.Contains(err.Error(), "magic") {
		t.Fatalf("expected error mentioning magic, got: %v", err)
	}
}

func TestReadFrameHandlesTruncatedHeader(t *testing.T) {
	buf := bytes.NewBufferString("ZBX")

	_, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error for truncated header, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF-related error, got: %v", err)
	}
}

func TestWritePlainRequestHasNoHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePlainRequest(&buf, "agent.ping"); err != nil {
		t.Fatalf("WritePlainRequest returned error: %v", err)
	}
	if got := buf.String(); got != "agent.ping\n" {
		t.Fatalf("got %q, want %q", got, "agent.ping\n")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/zabbixproto/... -v`
Expected: FAIL — `undefined: WriteFrame`, `undefined: ReadFrame`, `undefined: WritePlainRequest` (package doesn't exist yet).

- [ ] **Step 3: Implement the frame codec**

```go
// internal/zabbixproto/frame.go
package zabbixproto

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	magic        = "ZBXD"
	headerLen    = 13 // 4 (magic) + 1 (flags) + 4 (datalen) + 4 (reserved)
	flagResponse = 0x01
)

// WriteFrame writes payload wrapped in the Zabbix ZBXD header to w.
func WriteFrame(w io.Writer, payload []byte) error {
	header := make([]byte, headerLen)
	copy(header[0:4], magic)
	header[4] = flagResponse
	binary.LittleEndian.PutUint32(header[5:9], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[9:13], 0)

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("zabbixproto: write header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("zabbixproto: write payload: %w", err)
	}
	return nil
}

// WritePlainRequest writes key as a bare newline-terminated request,
// for legacy agents that do not expect a ZBXD header on requests.
func WritePlainRequest(w io.Writer, key string) error {
	_, err := io.WriteString(w, key+"\n")
	if err != nil {
		return fmt.Errorf("zabbixproto: write plain request: %w", err)
	}
	return nil
}

// ReadFrame reads a ZBXD-framed payload from r.
func ReadFrame(r io.Reader) ([]byte, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("zabbixproto: read header: %w", err)
	}
	if string(header[0:4]) != magic {
		return nil, fmt.Errorf("zabbixproto: bad magic %q, want %q", header[0:4], magic)
	}
	dataLen := binary.LittleEndian.Uint32(header[5:9])

	payload := make([]byte, dataLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("zabbixproto: read payload: %w", err)
	}
	return payload, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/zabbixproto/... -v`
Expected: PASS — all 4 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/zabbixproto
git commit -m "feat: implement Zabbix ZBXD frame codec"
```

---

## Task 3: Zabbix client (`internal/zabbixclient`)

**Files:**
- Create: `internal/zabbixclient/client.go`
- Test: `internal/zabbixclient/client_test.go`

The client dials a Zabbix passive agent, sends one item key per connection,
reads back the response, and classifies special cases
(`ZBX_NOTSUPPORTED`, JSON discovery payloads).

- [ ] **Step 1: Write failing tests using a fake TCP agent**

```go
// internal/zabbixclient/client_test.go
package zabbixclient

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"zabbix_agent_exporter/internal/zabbixproto"
)

// startFakeAgent starts a TCP listener that, for each accepted connection,
// reads one newline-terminated request line and writes back the ZBXD-framed
// response produced by respond(requestLine). It serves exactly one
// connection per call to accept, looping until the listener is closed.
func startFakeAgent(t *testing.T, respond func(request string) []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start fake agent: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				request := strings.TrimRight(string(buf[:n]), "\n")
				_ = zabbixproto.WriteFrame(conn, respond(request))
			}()
		}
	}()
	return ln.Addr().String()
}

func TestGetItemReturnsValue(t *testing.T) {
	addr := startFakeAgent(t, func(request string) []byte {
		if request != "agent.ping" {
			t.Errorf("unexpected request: %q", request)
		}
		return []byte("1")
	})

	client := New(2 * time.Second)
	got, err := client.GetItem(context.Background(), addr, "agent.ping")
	if err != nil {
		t.Fatalf("GetItem returned error: %v", err)
	}
	if got != "1" {
		t.Fatalf("got %q, want %q", got, "1")
	}
}

func TestGetItemReturnsNotSupportedError(t *testing.T) {
	addr := startFakeAgent(t, func(request string) []byte {
		return []byte("ZBX_NOTSUPPORTED\x00Unsupported item key.")
	})

	client := New(2 * time.Second)
	_, err := client.GetItem(context.Background(), addr, "bogus.key")
	if err == nil {
		t.Fatal("expected error for unsupported item, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected 'not supported' in error, got: %v", err)
	}
}

func TestGetItemReturnsErrorOnConnectionRefused(t *testing.T) {
	// Port 1 is reserved and nothing listens there in test environments.
	client := New(500 * time.Millisecond)
	_, err := client.GetItem(context.Background(), "127.0.0.1:1", "agent.ping")
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}
}

func TestDiscoverParsesJSONEntities(t *testing.T) {
	addr := startFakeAgent(t, func(request string) []byte {
		if request != "vfs.fs.discovery" {
			t.Errorf("unexpected request: %q", request)
		}
		return []byte(`[{"{#FSNAME}":"/","{#FSTYPE}":"ext4"},{"{#FSNAME}":"/var","{#FSTYPE}":"ext4"}]`)
	})

	client := New(2 * time.Second)
	entities, err := client.Discover(context.Background(), addr, "vfs.fs.discovery")
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("got %d entities, want 2", len(entities))
	}
	if entities[0]["{#FSNAME}"] != "/" {
		t.Fatalf("got %q, want %q", entities[0]["{#FSNAME}"], "/")
	}
	if entities[1]["{#FSNAME}"] != "/var" {
		t.Fatalf("got %q, want %q", entities[1]["{#FSNAME}"], "/var")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/zabbixclient/... -v`
Expected: FAIL — `undefined: New` (package doesn't exist yet).

- [ ] **Step 3: Implement the client**

```go
// internal/zabbixclient/client.go
package zabbixclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"zabbix_agent_exporter/internal/zabbixproto"
)

// Client queries Zabbix passive agents over TCP, one item per connection.
type Client struct {
	timeout time.Duration
}

// New returns a Client that uses timeout for both dialing and reading.
func New(timeout time.Duration) *Client {
	return &Client{timeout: timeout}
}

func (c *Client) roundTrip(ctx context.Context, addr, key string) ([]byte, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("zabbixclient: dial %s: %w", addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("zabbixclient: set deadline: %w", err)
	}

	if err := zabbixproto.WritePlainRequest(conn, key); err != nil {
		return nil, fmt.Errorf("zabbixclient: send request %q to %s: %w", key, addr, err)
	}

	payload, err := zabbixproto.ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("zabbixclient: read response for %q from %s: %w", key, addr, err)
	}
	return payload, nil
}

// GetItem requests key from the agent at addr and returns its raw string value.
func (c *Client) GetItem(ctx context.Context, addr, key string) (string, error) {
	payload, err := c.roundTrip(ctx, addr, key)
	if err != nil {
		return "", err
	}

	value := string(payload)
	if strings.HasPrefix(value, "ZBX_NOTSUPPORTED") {
		reason := strings.TrimPrefix(value, "ZBX_NOTSUPPORTED\x00")
		return "", fmt.Errorf("zabbixclient: item %q not supported by %s: %s", key, addr, reason)
	}
	return value, nil
}

// Discover requests a discovery key (e.g. vfs.fs.discovery) and returns
// the list of discovered entities as maps of macro name to value.
func (c *Client) Discover(ctx context.Context, addr, discoveryKey string) ([]map[string]string, error) {
	payload, err := c.roundTrip(ctx, addr, discoveryKey)
	if err != nil {
		return nil, err
	}

	var entities []map[string]string
	if err := json.Unmarshal(payload, &entities); err != nil {
		return nil, fmt.Errorf("zabbixclient: parse discovery JSON for %q from %s: %w", discoveryKey, addr, err)
	}
	return entities, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/zabbixclient/... -v`
Expected: PASS — all 4 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/zabbixclient
git commit -m "feat: implement Zabbix passive agent client (GetItem, Discover)"
```

---

## Task 4: Config types and YAML loading (`internal/config`)

**Files:**
- Create: `internal/config/types.go`
- Create: `internal/config/hosts_yaml.go`
- Create: `internal/config/load.go`
- Test: `internal/config/load_test.go`
- Test fixtures: `internal/config/testdata/valid.yaml`, `internal/config/testdata/invalid.yaml`

- [ ] **Step 1: Write failing test for loading a valid config fixture**

```yaml
# internal/config/testdata/valid.yaml
server:
  listen_addr: ":9290"
  log_level: "debug"
  log_encoding: "console"

defaults:
  port: 10050
  timeout: 5s

hosts:
  srv-old-01: {}
  srv-old-02:
    port: 10150
    timeout: 10s

modules:
  linux:
    items:
      - key: "agent.ping"
        metric: "zabbix_agent_ping"
        type: gauge
        help: "Agent reachability (1=up)"
    discovered_items:
      - discovery_key: "vfs.fs.discovery"
        value_key: "vfs.fs.size[{#FSNAME},pfree]"
        metric: "zabbix_fs_free_percent"
        type: gauge
        help: "Free space percent per filesystem"
        label_name: "mountpoint"
        label_field: "{#FSNAME}"
  mysql:
    items:
      - key: "mysql.ping"
        metric: "zabbix_mysql_ping"
        type: gauge
        help: "MySQL reachability"

groups:
  - name: "tous-linux"
    hosts: ["srv-old-01", "srv-old-02"]
    modules: ["linux"]
  - name: "base-de-donnees"
    hosts: ["srv-old-02"]
    modules: ["mysql"]
```

```yaml
# internal/config/testdata/invalid.yaml
modules:
  linux:
    items:
      - key: "agent.ping"
        metric: "zabbix_agent_ping"
        type: "not-a-real-type"
        help: "bad type"
```

```go
// internal/config/load_test.go
package config

import (
	"testing"
	"time"
)

func TestLoadParsesValidConfig(t *testing.T) {
	cfg, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Server.ListenAddr != ":9290" {
		t.Errorf("got ListenAddr %q, want %q", cfg.Server.ListenAddr, ":9290")
	}
	if cfg.Defaults.Port != 10050 {
		t.Errorf("got Defaults.Port %d, want 10050", cfg.Defaults.Port)
	}
	if cfg.Defaults.Timeout != 5*time.Second {
		t.Errorf("got Defaults.Timeout %v, want 5s", cfg.Defaults.Timeout)
	}

	host, ok := cfg.Hosts["srv-old-02"]
	if !ok {
		t.Fatal("expected host srv-old-02 to be present")
	}
	if host.Port == nil || *host.Port != 10150 {
		t.Fatalf("got srv-old-02 port override %v, want 10150", host.Port)
	}
	if host.Timeout == nil || *host.Timeout != 10*time.Second {
		t.Fatalf("got srv-old-02 timeout override %v, want 10s", host.Timeout)
	}

	defaultHost, ok := cfg.Hosts["srv-old-01"]
	if !ok {
		t.Fatal("expected host srv-old-01 to be present")
	}
	if defaultHost.Port != nil {
		t.Fatalf("expected srv-old-01 to have no port override, got %v", defaultHost.Port)
	}

	linux, ok := cfg.Modules["linux"]
	if !ok {
		t.Fatal("expected module 'linux' to be present")
	}
	if len(linux.Items) != 1 || linux.Items[0].Key != "agent.ping" {
		t.Fatalf("unexpected linux.Items: %+v", linux.Items)
	}
	if len(linux.DiscoveredItems) != 1 || linux.DiscoveredItems[0].DiscoveryKey != "vfs.fs.discovery" {
		t.Fatalf("unexpected linux.DiscoveredItems: %+v", linux.DiscoveredItems)
	}

	if len(cfg.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(cfg.Groups))
	}
}

func TestLoadRejectsInvalidItemType(t *testing.T) {
	_, err := Load("testdata/invalid.yaml")
	if err == nil {
		t.Fatal("expected error for invalid item type, got nil")
	}
}

func TestLoadReturnsErrorForMissingFile(t *testing.T) {
	_, err := Load("testdata/does-not-exist.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -v`
Expected: FAIL — `undefined: Load` (package doesn't exist yet).

- [ ] **Step 3: Implement config types**

```go
// internal/config/types.go
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
	Server   ServerConfig         `yaml:"server"`
	Defaults Defaults             `yaml:"defaults"`
	Hosts    map[string]HostConfig `yaml:"hosts"`
	Modules  map[string]Module    `yaml:"modules"`
	Groups   []Group              `yaml:"groups"`
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
```

- [ ] **Step 4: Implement config loading and validation**

```go
// internal/config/load.go
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
```

Note: `HostConfig` with `*int`/`*time.Duration` fields and a plain
`map[string]HostConfig` already unmarshal correctly for both `srv-old-01: {}`
and `srv-old-02: {port: 10150, timeout: 10s}` forms with `yaml.v3`'s default
struct unmarshalling — no custom `UnmarshalYAML` is needed. Skip creating
`hosts_yaml.go`; delete it from the file structure if already created empty.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — all 3 tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/config
git commit -m "feat: add config types and YAML loading with validation"
```

---

## Task 5: Config resolution (hosts/groups/modules/overrides)

**Files:**
- Create: `internal/config/resolve.go`
- Test: `internal/config/resolve_test.go`

Implements the cascade described in the design doc: module resolution
(union of groups, or URL override replaces), and port/timeout resolution
(`defaults → hosts.<target> → URL params`).

- [ ] **Step 1: Write failing tests for resolution rules**

```go
// internal/config/resolve_test.go
package config

import (
	"testing"
	"time"
)

func testConfig() *Config {
	ten := 10150
	tenSec := 10 * time.Second
	return &Config{
		Defaults: Defaults{Port: 10050, Timeout: 5 * time.Second},
		Hosts: map[string]HostConfig{
			"srv-old-01": {},
			"srv-old-02": {Port: &ten, Timeout: &tenSec},
		},
		Modules: map[string]Module{
			"linux": {},
			"mysql": {},
			"other": {},
		},
		Groups: []Group{
			{Name: "tous-linux", Hosts: []string{"srv-old-01", "srv-old-02"}, Modules: []string{"linux"}},
			{Name: "base-de-donnees", Hosts: []string{"srv-old-02"}, Modules: []string{"mysql"}},
		},
	}
}

func TestResolveUnionsModulesAcrossGroups(t *testing.T) {
	cfg := testConfig()

	resolved, err := Resolve(cfg, "srv-old-02", nil, 0, 0)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	modules := map[string]bool{}
	for _, m := range resolved.Modules {
		modules[m] = true
	}
	if !modules["linux"] || !modules["mysql"] || len(modules) != 2 {
		t.Fatalf("got modules %v, want exactly [linux mysql]", resolved.Modules)
	}
}

func TestResolveURLModulesReplaceGroupModules(t *testing.T) {
	cfg := testConfig()

	resolved, err := Resolve(cfg, "srv-old-02", []string{"other"}, 0, 0)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved.Modules) != 1 || resolved.Modules[0] != "other" {
		t.Fatalf("got modules %v, want exactly [other]", resolved.Modules)
	}
}

func TestResolveReturnsErrorWhenNoModuleResolved(t *testing.T) {
	cfg := testConfig()

	_, err := Resolve(cfg, "unknown-host", nil, 0, 0)
	if err == nil {
		t.Fatal("expected error when no module resolves for target, got nil")
	}
}

func TestResolvePortTimeoutCascade(t *testing.T) {
	cfg := testConfig()

	// srv-old-01 has no host override -> falls back to defaults.
	resolved, err := Resolve(cfg, "srv-old-01", nil, 0, 0)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Port != 10050 {
		t.Fatalf("got port %d, want default 10050", resolved.Port)
	}
	if resolved.Timeout != 5*time.Second {
		t.Fatalf("got timeout %v, want default 5s", resolved.Timeout)
	}

	// srv-old-02 has a host override -> takes precedence over defaults.
	resolved, err = Resolve(cfg, "srv-old-02", nil, 0, 0)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Port != 10150 {
		t.Fatalf("got port %d, want host override 10150", resolved.Port)
	}
	if resolved.Timeout != 10*time.Second {
		t.Fatalf("got timeout %v, want host override 10s", resolved.Timeout)
	}

	// Explicit URL params take precedence over everything.
	resolved, err = Resolve(cfg, "srv-old-02", nil, 20000, 20*time.Second)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Port != 20000 {
		t.Fatalf("got port %d, want URL override 20000", resolved.Port)
	}
	if resolved.Timeout != 20*time.Second {
		t.Fatalf("got timeout %v, want URL override 20s", resolved.Timeout)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -run TestResolve -v`
Expected: FAIL — `undefined: Resolve`.

- [ ] **Step 3: Implement resolution logic**

```go
// internal/config/resolve.go
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
// urlPort/urlTimeout (if non-zero).
func Resolve(cfg *Config, target string, urlModules []string, urlPort int, urlTimeout time.Duration) (ResolvedTarget, error) {
	modules := urlModules
	if len(modules) == 0 {
		modules = modulesFromGroups(cfg, target)
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
	if urlPort != 0 {
		port = urlPort
	}
	if urlTimeout != 0 {
		timeout = urlTimeout
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — all tests in the package green.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: resolve target modules and port/timeout override cascade"
```

---

## Task 6: Config file watching (hot-reload)

**Files:**
- Modify: `internal/config/load.go`
- Test: `internal/config/load_test.go`

- [ ] **Step 1: Write failing test for Watch**

Append to `internal/config/load_test.go`:

```go
func TestWatchReloadsOnFileChange(t *testing.T) {
	tmpFile := t.TempDir() + "/watch.yaml"
	initial := []byte(`
server:
  listen_addr: ":9290"
defaults:
  port: 10050
  timeout: 5s
modules:
  linux:
    items:
      - key: "agent.ping"
        metric: "zabbix_agent_ping"
        type: gauge
        help: "ping"
groups: []
`)
	if err := osWriteFile(tmpFile, initial); err != nil {
		t.Fatalf("failed to write initial config: %v", err)
	}

	reloaded := make(chan *Config, 1)
	stop, err := Watch(tmpFile, func(cfg *Config, err error) {
		if err != nil {
			t.Errorf("unexpected reload error: %v", err)
			return
		}
		reloaded <- cfg
	})
	if err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
	defer stop()

	updated := []byte(`
server:
  listen_addr: ":9999"
defaults:
  port: 10050
  timeout: 5s
modules:
  linux:
    items:
      - key: "agent.ping"
        metric: "zabbix_agent_ping"
        type: gauge
        help: "ping"
groups: []
`)
	if err := osWriteFile(tmpFile, updated); err != nil {
		t.Fatalf("failed to write updated config: %v", err)
	}

	select {
	case cfg := <-reloaded:
		if cfg.Server.ListenAddr != ":9999" {
			t.Fatalf("got reloaded ListenAddr %q, want %q", cfg.Server.ListenAddr, ":9999")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reload callback")
	}
}
```

Add the small helper used above (avoids importing `os` twice with naming
collisions in the test file):

```go
// internal/config/load_test.go (add near the top, after imports)
var osWriteFile = os.WriteFile
```

Add `"os"` to the imports of `internal/config/load_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestWatchReloadsOnFileChange -v`
Expected: FAIL — `undefined: Watch`.

- [ ] **Step 3: Implement Watch using fsnotify via Viper's watcher**

```bash
go get github.com/fsnotify/fsnotify@latest
```

Append to `internal/config/load.go`:

```go
// Watch monitors path for changes and invokes onChange with the newly
// loaded config whenever the file is modified. onChange receives a non-nil
// error (and a nil config) if the new file fails to load or validate; the
// caller is expected to keep using its last-known-good config in that case.
// Watch returns a stop function that must be called to release the watcher.
func Watch(path string, onChange func(cfg *Config, err error)) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: create watcher: %w", err)
	}
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("config: watch %s: %w", path, err)
	}

	done := make(chan struct{})
	go func() {
		absPath, _ := filepath.Abs(path)
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				eventAbsPath, _ := filepath.Abs(event.Name)
				if eventAbsPath != absPath {
					continue
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}
				cfg, loadErr := Load(path)
				onChange(cfg, loadErr)
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			case <-done:
				return
			}
		}
	}()

	stop = func() {
		close(done)
		watcher.Close()
	}
	return stop, nil
}
```

Add `"path/filepath"` and `"github.com/fsnotify/fsnotify"` to the imports
of `internal/config/load.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/... -v`
Expected: PASS — all tests in the package green, including the new watch test.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/config
git commit -m "feat: add config hot-reload via file watching"
```

---

## Task 7: Prometheus collector (`internal/collector`)

**Files:**
- Create: `internal/collector/collector.go`
- Test: `internal/collector/collector_test.go`

The collector takes a resolved target (host:port, timeout, modules) plus
the full `*config.Config` (for module definitions), queries the Zabbix
client for every item, and emits Prometheus metrics plus the meta metrics
(`zabbix_scrape_success`, `zabbix_scrape_duration_seconds`,
`zabbix_item_errors_total`).

- [ ] **Step 1: Write failing tests using the fake Zabbix agent + testutil**

```go
// internal/collector/collector_test.go
package collector

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"

	"zabbix_agent_exporter/internal/config"
	"zabbix_agent_exporter/internal/zabbixclient"
	"zabbix_agent_exporter/internal/zabbixproto"
)

func startFakeAgent(t *testing.T, responses map[string][]byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start fake agent: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				request := strings.TrimRight(string(buf[:n]), "\n")
				resp, ok := responses[request]
				if !ok {
					resp = []byte("ZBX_NOTSUPPORTED\x00Unknown key")
				}
				_ = zabbixproto.WriteFrame(conn, resp)
			}()
		}
	}()
	return ln.Addr().String()
}

func testModuleConfig() *config.Config {
	return &config.Config{
		Modules: map[string]config.Module{
			"linux": {
				Items: []config.Item{
					{Key: "agent.ping", Metric: "zabbix_agent_ping", Type: config.MetricTypeGauge, Help: "ping"},
				},
				DiscoveredItems: []config.DiscoveredItem{
					{
						DiscoveryKey: "vfs.fs.discovery",
						ValueKey:     "vfs.fs.size[{#FSNAME},pfree]",
						Metric:       "zabbix_fs_free_percent",
						Type:         config.MetricTypeGauge,
						Help:         "free pct",
						LabelName:    "mountpoint",
						LabelField:   "{#FSNAME}",
					},
				},
			},
		},
	}
}

func TestCollectEmitsSimpleAndDiscoveredMetrics(t *testing.T) {
	addr := startFakeAgent(t, map[string][]byte{
		"agent.ping":                     []byte("1"),
		"vfs.fs.discovery":               []byte(`[{"{#FSNAME}":"/"}]`),
		"vfs.fs.size[/,pfree]":           []byte("42.5"),
	})

	resolved := config.ResolvedTarget{Host: addr, Port: 0, Timeout: 2 * time.Second, Modules: []string{"linux"}}
	// Host already includes port via the fake listener address; use it directly as addr below.
	client := zabbixclient.New(resolved.Timeout)
	col := New(client, testModuleConfig(), resolved.Modules, addr, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_agent_ping ping
# TYPE zabbix_agent_ping gauge
zabbix_agent_ping 1
# HELP zabbix_fs_free_percent free pct
# TYPE zabbix_fs_free_percent gauge
zabbix_fs_free_percent{mountpoint="/"} 42.5
# HELP zabbix_scrape_success scrape_success
# TYPE zabbix_scrape_success gauge
zabbix_scrape_success 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"zabbix_agent_ping", "zabbix_fs_free_percent", "zabbix_scrape_success"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectMarksScrapeFailedWhenAgentUnreachable(t *testing.T) {
	resolved := config.ResolvedTarget{Timeout: 200 * time.Millisecond, Modules: []string{"linux"}}
	client := zabbixclient.New(resolved.Timeout)
	col := New(client, testModuleConfig(), resolved.Modules, "127.0.0.1:1", zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_scrape_success scrape_success
# TYPE zabbix_scrape_success gauge
zabbix_scrape_success 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "zabbix_scrape_success"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectContinuesAfterSingleItemError(t *testing.T) {
	addr := startFakeAgent(t, map[string][]byte{
		"agent.ping": []byte("1"),
		// vfs.fs.discovery intentionally not stubbed -> returns ZBX_NOTSUPPORTED
	})

	resolved := config.ResolvedTarget{Timeout: 2 * time.Second, Modules: []string{"linux"}}
	client := zabbixclient.New(resolved.Timeout)
	col := New(client, testModuleConfig(), resolved.Modules, addr, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_agent_ping ping
# TYPE zabbix_agent_ping gauge
zabbix_agent_ping 1
# HELP zabbix_scrape_success scrape_success
# TYPE zabbix_scrape_success gauge
zabbix_scrape_success 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected),
		"zabbix_agent_ping", "zabbix_scrape_success"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

var _ = context.Background // keep context import if unused by later edits
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/collector/... -v`
Expected: FAIL — `undefined: New` (package doesn't exist yet).

- [ ] **Step 3: Implement the collector**

```go
// internal/collector/collector.go
package collector

import (
	"context"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"zabbix_agent_exporter/internal/config"
)

// zabbixQuerier is the subset of zabbixclient.Client used by the collector,
// defined locally so tests can substitute fakes without importing net.
type zabbixQuerier interface {
	GetItem(ctx context.Context, addr, key string) (string, error)
	Discover(ctx context.Context, addr, discoveryKey string) ([]map[string]string, error)
}

// ZabbixCollector implements prometheus.Collector for a single resolved
// target: it queries every item in the given modules sequentially against
// one Zabbix agent address.
type ZabbixCollector struct {
	client  zabbixQuerier
	cfg     *config.Config
	modules []string
	addr    string
	logger  *zap.Logger
}

// New builds a collector for one scrape of addr using the given modules,
// looked up by name in cfg.Modules.
func New(client zabbixQuerier, cfg *config.Config, modules []string, addr string, logger *zap.Logger) *ZabbixCollector {
	return &ZabbixCollector{client: client, cfg: cfg, modules: modules, addr: addr, logger: logger}
}

var (
	scrapeSuccessDesc = prometheus.NewDesc("zabbix_scrape_success", "scrape_success", nil, nil)
	scrapeDurationDesc = prometheus.NewDesc("zabbix_scrape_duration_seconds", "scrape_duration_seconds", nil, nil)
	itemErrorsDesc     = prometheus.NewDesc("zabbix_item_errors_total", "item_errors_total", []string{"item"}, nil)
)

// Describe implements prometheus.Collector. Descriptors are dynamic per
// module configuration, so this intentionally sends nothing, marking the
// collector as unchecked (standard practice for dynamically-shaped exporters
// like blackbox_exporter and snmp_exporter).
func (c *ZabbixCollector) Describe(ch chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *ZabbixCollector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx := context.Background()

	success := 1.0
	firstContact := true
	itemErrors := map[string]int{}

	for _, moduleName := range c.modules {
		module, ok := c.cfg.Modules[moduleName]
		if !ok {
			c.logger.Warn("unknown module referenced", zap.String("module", moduleName))
			continue
		}

		for _, item := range module.Items {
			value, err := c.client.GetItem(ctx, c.addr, item.Key)
			if err != nil {
				c.logger.Warn("item query failed", zap.String("item", item.Key), zap.Error(err))
				itemErrors[item.Key]++
				if firstContact {
					success = 0
				}
				continue
			}
			firstContact = false
			c.emitValue(ch, item.Metric, item.Help, item.Type, value, nil, nil)
		}

		for _, di := range module.DiscoveredItems {
			entities, err := c.client.Discover(ctx, c.addr, di.DiscoveryKey)
			if err != nil {
				c.logger.Warn("discovery query failed", zap.String("item", di.DiscoveryKey), zap.Error(err))
				itemErrors[di.DiscoveryKey]++
				if firstContact {
					success = 0
				}
				continue
			}
			firstContact = false

			for _, entity := range entities {
				labelValue := entity[di.LabelField]
				valueKey := substituteMacro(di.ValueKey, di.LabelField, labelValue)
				value, err := c.client.GetItem(ctx, c.addr, valueKey)
				if err != nil {
					c.logger.Warn("discovered item query failed", zap.String("item", valueKey), zap.Error(err))
					itemErrors[valueKey]++
					continue
				}
				c.emitValue(ch, di.Metric, di.Help, di.Type, value, []string{di.LabelName}, []string{labelValue})
			}
		}
	}

	ch <- prometheus.MustNewConstMetric(scrapeSuccessDesc, prometheus.GaugeValue, success)
	ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, time.Since(start).Seconds())
	for item, count := range itemErrors {
		ch <- prometheus.MustNewConstMetric(itemErrorsDesc, prometheus.CounterValue, float64(count), item)
	}
}

func (c *ZabbixCollector) emitValue(ch chan<- prometheus.Metric, metricName, help string, metricType config.MetricType, rawValue string, labelNames, labelValues []string) {
	value, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		c.logger.Warn("non-numeric item value skipped", zap.String("metric", metricName), zap.String("raw_value", rawValue))
		return
	}

	valueType := prometheus.GaugeValue
	if metricType == config.MetricTypeCounter {
		valueType = prometheus.CounterValue
	}

	desc := prometheus.NewDesc(metricName, help, labelNames, nil)
	ch <- prometheus.MustNewConstMetric(desc, valueType, value, labelValues...)
}

func substituteMacro(template, macro, value string) string {
	result := ""
	for i := 0; i < len(template); {
		if i+len(macro) <= len(template) && template[i:i+len(macro)] == macro {
			result += value
			i += len(macro)
			continue
		}
		result += string(template[i])
		i++
	}
	return result
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/collector/... -v`
Expected: PASS — all 3 tests green. If `TestCollectEmitsSimpleAndDiscoveredMetrics`
fails because the fake listener address already contains a port (so
`resolved.Port` is unused), that's expected — the test passes `addr`
directly to `New`, ignoring `resolved.Port`; this is intentional for the
test and does not affect production wiring (Task 9 builds `addr` as
`fmt.Sprintf("%s:%d", host, resolved.Port)`).

- [ ] **Step 5: Commit**

```bash
git add internal/collector
git commit -m "feat: implement Prometheus collector for resolved Zabbix targets"
```

---

## Task 8: HTTP server (`internal/httpserver`)

**Files:**
- Create: `internal/httpserver/server.go`
- Create: `internal/httpserver/handler.go`
- Test: `internal/httpserver/handler_test.go`

- [ ] **Step 1: Write failing tests for the metrics and healthz handlers**

```go
// internal/httpserver/handler_test.go
package httpserver

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"zabbix_agent_exporter/internal/config"
	"zabbix_agent_exporter/internal/zabbixproto"
)

func startFakeAgent(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start fake agent: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				if _, err := conn.Read(buf); err != nil {
					return
				}
				_ = zabbixproto.WriteFrame(conn, []byte("1"))
			}()
		}
	}()
	return ln.Addr().String()
}

func testConfigForAddr(addr string) *config.Config {
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = net.LookupPort("tcp", portStr) // no-op, keep portStr parse simple below
	if p, err := net.LookupPort("tcp", portStr); err == nil {
		port = p
	}
	return &config.Config{
		Defaults: config.Defaults{Port: port, Timeout: 2 * time.Second},
		Hosts:    map[string]config.HostConfig{host: {}},
		Modules: map[string]config.Module{
			"linux": {Items: []config.Item{
				{Key: "agent.ping", Metric: "zabbix_agent_ping", Type: config.MetricTypeGauge, Help: "ping"},
			}},
		},
		Groups: []config.Group{
			{Name: "g", Hosts: []string{host}, Modules: []string{"linux"}},
		},
	}
}

func TestMetricsHandlerReturnsMetricsForKnownTarget(t *testing.T) {
	addr := startFakeAgent(t)
	host, _, _ := net.SplitHostPort(addr)
	cfg := testConfigForAddr(addr)

	srv := New(cfg, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/metrics?target="+host, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "zabbix_agent_ping") {
		t.Fatalf("expected body to contain zabbix_agent_ping, got: %s", rec.Body.String())
	}
}

func TestMetricsHandlerReturns400WhenTargetMissing(t *testing.T) {
	cfg := testConfigForAddr("127.0.0.1:12345")
	srv := New(cfg, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400", rec.Code)
	}
}

func TestMetricsHandlerReturns400WhenNoModuleResolved(t *testing.T) {
	cfg := testConfigForAddr("127.0.0.1:12345")
	srv := New(cfg, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/metrics?target=unknown-host", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400", rec.Code)
	}
}

func TestHealthzReturns200(t *testing.T) {
	cfg := testConfigForAddr("127.0.0.1:12345")
	srv := New(cfg, zap.NewNop())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpserver/... -v`
Expected: FAIL — `undefined: New` (package doesn't exist yet).

- [ ] **Step 3: Implement the server and handlers**

```go
// internal/httpserver/server.go
package httpserver

import (
	"net/http"
	"sync"

	"go.uber.org/zap"

	"zabbix_agent_exporter/internal/config"
)

// Server wires the current config into HTTP handlers. cfg can be swapped
// out at runtime (SetConfig) to support hot-reload.
type Server struct {
	mu     sync.RWMutex
	cfg    *config.Config
	logger *zap.Logger
}

// New builds a Server bound to the given initial config.
func New(cfg *config.Config, logger *zap.Logger) *Server {
	return &Server{cfg: cfg, logger: logger}
}

// SetConfig atomically replaces the config used by subsequent requests.
func (s *Server) SetConfig(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

func (s *Server) currentConfig() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Handler returns the root http.Handler for the exporter.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", s.metricsHandler)
	mux.HandleFunc("/healthz", s.healthzHandler)
	return mux
}
```

```go
// internal/httpserver/handler.go
package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"zabbix_agent_exporter/internal/collector"
	"zabbix_agent_exporter/internal/config"
	"zabbix_agent_exporter/internal/zabbixclient"
)

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "missing required 'target' query parameter", http.StatusBadRequest)
		return
	}

	cfg := s.currentConfig()
	urlModules := r.URL.Query()["module"]

	var urlPort int
	if p := r.URL.Query().Get("port"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			http.Error(w, "invalid 'port' query parameter", http.StatusBadRequest)
			return
		}
		urlPort = parsed
	}

	var urlTimeout time.Duration
	if t := r.URL.Query().Get("timeout"); t != "" {
		parsed, err := time.ParseDuration(t)
		if err != nil {
			http.Error(w, "invalid 'timeout' query parameter", http.StatusBadRequest)
			return
		}
		urlTimeout = parsed
	}

	resolved, err := config.Resolve(cfg, target, urlModules, urlPort, urlTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	addr := fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)
	client := zabbixclient.New(resolved.Timeout)
	col := collector.New(client, cfg, resolved.Modules, addr, s.logger)

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (s *Server) healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

var _ = zap.NewNop // keep zap import if unused by later edits
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpserver/... -v`
Expected: PASS — all 4 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/httpserver
git commit -m "feat: implement HTTP server with /metrics and /healthz handlers"
```

---

## Task 9: Entry point (`cmd/zabbix_agent_exporter`)

**Files:**
- Create: `cmd/zabbix_agent_exporter/main.go`
- Create: `configs/config.example.yaml`

- [ ] **Step 1: Write the example config**

```yaml
# configs/config.example.yaml
server:
  listen_addr: ":9290"
  log_level: "info"
  log_encoding: "console"

defaults:
  port: 10050
  timeout: 5s

hosts:
  srv-old-01: {}

modules:
  linux:
    items:
      - key: "agent.ping"
        metric: "zabbix_agent_ping"
        type: gauge
        help: "Agent reachability (1=up)"
      - key: "system.cpu.load[,avg1]"
        metric: "zabbix_cpu_load1"
        type: gauge
        help: "CPU load average 1m"
      - key: "system.uptime"
        metric: "zabbix_uptime_seconds"
        type: gauge
        help: "System uptime in seconds"
      - key: "vm.memory.size[available]"
        metric: "zabbix_memory_available_bytes"
        type: gauge
        help: "Available memory in bytes"
      - key: "vm.memory.size[total]"
        metric: "zabbix_memory_total_bytes"
        type: gauge
        help: "Total memory in bytes"
    discovered_items:
      - discovery_key: "vfs.fs.discovery"
        value_key: "vfs.fs.size[{#FSNAME},pfree]"
        metric: "zabbix_fs_free_percent"
        type: gauge
        help: "Free space percent per filesystem"
        label_name: "mountpoint"
        label_field: "{#FSNAME}"
      - discovery_key: "net.if.discovery"
        value_key: "net.if.in[{#IFNAME}]"
        metric: "zabbix_net_if_in_bytes"
        type: counter
        help: "Network bytes received per interface"
        label_name: "interface"
        label_field: "{#IFNAME}"

groups:
  - name: "tous-linux"
    hosts: ["srv-old-01"]
    modules: ["linux"]
```

- [ ] **Step 2: Implement main.go wiring config, logger, watcher, and HTTP server**

```go
// cmd/zabbix_agent_exporter/main.go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"zabbix_agent_exporter/internal/config"
	"zabbix_agent_exporter/internal/httpserver"
)

func main() {
	configPath := flag.String("config", "configs/config.example.yaml", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	logger, err := buildLogger(cfg.Server.LogLevel, cfg.Server.LogEncoding)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	srv := httpserver.New(cfg, logger)

	stopWatch, err := config.Watch(*configPath, func(newCfg *config.Config, reloadErr error) {
		if reloadErr != nil {
			logger.Error("config reload failed, keeping previous config", zap.Error(reloadErr))
			return
		}
		logger.Info("config reloaded")
		srv.SetConfig(newCfg)
	})
	if err != nil {
		logger.Error("failed to start config watcher, hot-reload disabled", zap.Error(err))
	} else {
		defer stopWatch()
	}

	httpSrv := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: srv.Handler(),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("starting server", zap.String("listen_addr", cfg.Server.ListenAddr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", zap.Error(err))
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", zap.Error(err))
	}
}

func buildLogger(level, encoding string) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		zapLevel = zapcore.InfoLevel
	}

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapLevel),
		Development:      false,
		Encoding:         encoding,
		EncoderConfig:    zap.NewProductionEncoderConfig(),
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}
	if encoding == "console" {
		cfg.EncoderConfig = zap.NewDevelopmentEncoderConfig()
	}
	return cfg.Build()
}
```

- [ ] **Step 3: Build the binary**

Run: `go build -o bin/zabbix_agent_exporter.exe ./cmd/zabbix_agent_exporter`
Expected: builds successfully with no errors, producing `bin/zabbix_agent_exporter.exe`.

- [ ] **Step 4: Run full test suite**

Run: `go test ./...`
Expected: PASS for all packages (`zabbixproto`, `zabbixclient`, `config`, `collector`, `httpserver`).

- [ ] **Step 5: Manual smoke test**

Run the binary and confirm it serves `/healthz` and rejects an unresolvable
target cleanly:

```bash
./bin/zabbix_agent_exporter.exe -config configs/config.example.yaml &
sleep 1
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:9290/healthz
curl -s -o /dev/null -w "%{http_code}\n" "http://localhost:9290/metrics?target=unknown-host"
kill %1
```
Expected: first `curl` prints `200`, second prints `400` (no agent actually
running on `srv-old-01:10050` in this environment, so a full successful
scrape isn't expected here — this only confirms routing and config
resolution work end-to-end).

- [ ] **Step 6: Commit**

```bash
git add cmd/zabbix_agent_exporter configs/config.example.yaml
git commit -m "feat: add main entrypoint wiring config, logger, watcher, and HTTP server"
```

---

## Task 10: README

**Files:**
- Create: `README.md`

- [ ] **Step 1: Write README covering build, config, and usage**

```markdown
# zabbix_agent_exporter

Prometheus exporter that proxies scrapes to legacy Zabbix passive agents.
It lets a fleet of old Zabbix agents (on hosts where `node_exporter` isn't
available) be monitored by Prometheus without running a Zabbix server.

## Build

    go build -o bin/zabbix_agent_exporter ./cmd/zabbix_agent_exporter

## Run

    ./bin/zabbix_agent_exporter -config configs/config.example.yaml

## Configuration

See `configs/config.example.yaml` for a full example. Key concepts:

- `defaults`: fallback Zabbix agent port/timeout.
- `hosts`: per-host port/timeout overrides.
- `modules`: named sets of Zabbix item -> Prometheus metric mappings,
  including LLD-discovered items (filesystems, network interfaces, ...).
- `groups`: lists of hosts associated with one or more modules; a host's
  resolved modules are the union of every group that contains it.

The config file is watched for changes and hot-reloaded without restarting
the process.

## Prometheus scrape config

    scrape_configs:
      - job_name: zabbix
        static_configs:
          - targets: ["srv-old-01", "srv-old-02"]
        relabel_configs:
          - source_labels: [__address__]
            target_label: __param_target
          - target_label: __address__
            replacement: localhost:9290

## Endpoints

- `GET /metrics?target=<host>[&module=<name>]*[&port=<p>][&timeout=<d>]`
- `GET /healthz`
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add README with build, config, and usage instructions"
```

---

## Self-Review Notes

- **Spec coverage:** protocol framing (Task 2), client + LLD (Task 3),
  config types/loading/validation (Task 4), resolution cascade incl.
  union-vs-replace modules and port/timeout cascade (Task 5), hot-reload
  (Task 6), collector with per-item error handling and the three meta
  metrics (Task 7), HTTP multi-target handler (Task 8), systemd-friendly
  single binary entrypoint (Task 9), docs (Task 10). All design sections
  are covered.
- **Dropped `hosts_yaml.go`:** the design doc anticipated needing a custom
  YAML unmarshaller for the `hosts` map; Task 4 verifies plain struct
  unmarshalling is sufficient and explicitly removes that file from scope
  to avoid dead code.
- **Type consistency check:** `config.ResolvedTarget`, `config.HostConfig`,
  `config.Module`/`Item`/`DiscoveredItem`, and `zabbixclient.Client` method
  signatures (`GetItem`, `Discover`) are defined once in Tasks 3-5 and
  reused with identical signatures in Tasks 7-9 (`zabbixQuerier` interface
  in Task 7 matches `*zabbixclient.Client`'s actual methods structurally).
- **systemd unit file:** not included as a task — out of scope per design
  ("binary + systemd" deployment means operational packaging, which the
  user can add once the binary exists; writing a `.service` file requires
  environment-specific paths/users not yet known).