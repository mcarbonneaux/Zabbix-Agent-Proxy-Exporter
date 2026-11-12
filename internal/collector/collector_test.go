// internal/collector/collector_test.go
package collector

import (
	"context"
	"encoding/json"
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
		"agent.ping":           []byte("1"),
		"vfs.fs.discovery":     []byte(`[{"{#FSNAME}":"/"}]`),
		"vfs.fs.size[/,pfree]": []byte("42.5"),
	})

	client := zabbixclient.New(2 * time.Second)
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

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
	client := zabbixclient.New(200 * time.Millisecond)
	col := New(client, testModuleConfig(), []string{"linux"}, "127.0.0.1:1", zabbixclient.ProtocolPlain, zap.NewNop())

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

	client := zabbixclient.New(2 * time.Second)
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

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

func TestNewDefaultsNilLoggerToNoop(t *testing.T) {
	addr := startFakeAgent(t, map[string][]byte{
		"agent.ping": []byte("1"),
	})

	client := zabbixclient.New(2 * time.Second)
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zabbixclient.ProtocolPlain, nil)

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	if _, err := reg.Gather(); err != nil {
		t.Fatalf("Gather returned error with nil logger: %v", err)
	}
}

func TestCollectEmitsOneMetricPerDiscoveredEntity(t *testing.T) {
	addr := startFakeAgent(t, map[string][]byte{
		"agent.ping":              []byte("1"),
		"vfs.fs.discovery":        []byte(`[{"{#FSNAME}":"/"},{"{#FSNAME}":"/var"}]`),
		"vfs.fs.size[/,pfree]":    []byte("42.5"),
		"vfs.fs.size[/var,pfree]": []byte("17.25"),
	})

	client := zabbixclient.New(2 * time.Second)
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_fs_free_percent free pct
# TYPE zabbix_fs_free_percent gauge
zabbix_fs_free_percent{mountpoint="/"} 42.5
zabbix_fs_free_percent{mountpoint="/var"} 17.25
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "zabbix_fs_free_percent"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectEmitsOneMetricPerLineListEntry(t *testing.T) {
	addr := startFakeAgent(t, map[string][]byte{
		"listfs_solaris": []byte("/\n/var\n/home\n"),
	})

	cfg := &config.Config{
		Modules: map[string]config.Module{
			"solaris": {
				LineListItems: []config.LineListItem{
					{
						Key:       "listfs_solaris",
						Metric:    "zabbix_fs_present",
						Help:      "Filesystem is present",
						LabelName: "mountpoint",
					},
				},
			},
		},
	}

	client := zabbixclient.New(2 * time.Second)
	col := New(client, cfg, []string{"solaris"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_fs_present Filesystem is present
# TYPE zabbix_fs_present gauge
zabbix_fs_present{mountpoint="/"} 1
zabbix_fs_present{mountpoint="/var"} 1
zabbix_fs_present{mountpoint="/home"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "zabbix_fs_present"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectSkipsEmptyLinesInLineListItem(t *testing.T) {
	addr := startFakeAgent(t, map[string][]byte{
		"listfs_solaris": []byte("/\n\n/var\n\n"),
	})

	cfg := &config.Config{
		Modules: map[string]config.Module{
			"solaris": {
				LineListItems: []config.LineListItem{
					{
						Key:       "listfs_solaris",
						Metric:    "zabbix_fs_present",
						Help:      "Filesystem is present",
						LabelName: "mountpoint",
					},
				},
			},
		},
	}

	client := zabbixclient.New(2 * time.Second)
	col := New(client, cfg, []string{"solaris"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_fs_present Filesystem is present
# TYPE zabbix_fs_present gauge
zabbix_fs_present{mountpoint="/"} 1
zabbix_fs_present{mountpoint="/var"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "zabbix_fs_present"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectLineListItemFailureCountsAsItemError(t *testing.T) {
	addr := startFakeAgent(t, map[string][]byte{
		"agent.ping": []byte("1"),
		// listfs_solaris intentionally not stubbed -> returns ZBX_NOTSUPPORTED
	})

	cfg := &config.Config{
		Modules: map[string]config.Module{
			"solaris": {
				Items: []config.Item{
					{Key: "agent.ping", Metric: "zabbix_agent_ping", Type: config.MetricTypeGauge, Help: "ping"},
				},
				LineListItems: []config.LineListItem{
					{
						Key:       "listfs_solaris",
						Metric:    "zabbix_fs_present",
						Help:      "Filesystem is present",
						LabelName: "mountpoint",
					},
				},
			},
		},
	}

	client := zabbixclient.New(2 * time.Second)
	col := New(client, cfg, []string{"solaris"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

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
		"zabbix_agent_ping", "zabbix_scrape_success", "zabbix_fs_present"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectHandlesSameMetricNameAcrossTwoModules(t *testing.T) {
	// Two different modules legitimately defining an item under the same
	// Prometheus metric name (e.g. both "linux" and "solaris" expose
	// agent.ping as zabbix_agent_ping) must not silently drop one
	// module's metrics when both are active for the same scrape.
	addr := startFakeAgent(t, map[string][]byte{
		"agent.ping": []byte("1"),
	})

	cfg := &config.Config{
		Modules: map[string]config.Module{
			"linux": {
				Items: []config.Item{
					{Key: "agent.ping", Metric: "zabbix_agent_ping", Type: config.MetricTypeGauge, Help: "ping"},
				},
			},
			"solaris": {
				Items: []config.Item{
					{Key: "agent.ping", Metric: "zabbix_agent_ping", Type: config.MetricTypeGauge, Help: "ping"},
				},
			},
		},
	}

	client := zabbixclient.New(2 * time.Second)
	col := New(client, cfg, []string{"linux", "solaris"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_agent_ping ping
# TYPE zabbix_agent_ping gauge
zabbix_agent_ping 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "zabbix_agent_ping"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectTrimsWhitespaceFromNumericItemValue(t *testing.T) {
	// Some UserParameter scripts (e.g. `wc -l` on Solaris) pad their
	// numeric output with leading/trailing whitespace or a trailing
	// newline; this must not cause the value to be skipped as
	// non-numeric.
	addr := startFakeAgent(t, map[string][]byte{
		"num.port.used": []byte("      28\n"),
	})

	cfg := &config.Config{
		Modules: map[string]config.Module{
			"solaris": {
				Items: []config.Item{
					{Key: "num.port.used", Metric: "zabbix_tcp_ports_used", Type: config.MetricTypeGauge, Help: "ports"},
				},
			},
		},
	}

	client := zabbixclient.New(2 * time.Second)
	col := New(client, cfg, []string{"solaris"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_tcp_ports_used ports
# TYPE zabbix_tcp_ports_used gauge
zabbix_tcp_ports_used 28
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "zabbix_tcp_ports_used"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

func TestCollectUsesConfiguredProtocolForQueries(t *testing.T) {
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
				payload, err := zabbixproto.ReadFrame(conn)
				if err != nil {
					return
				}
				var req struct {
					Data []struct {
						Key string `json:"key"`
					} `json:"data"`
				}
				if err := json.Unmarshal(payload, &req); err != nil || len(req.Data) == 0 {
					return
				}
				if req.Data[0].Key != "agent.ping" {
					return
				}
				_ = zabbixproto.WriteFrame(conn, []byte(`{"version":"7.0.3","variant":2,"data":[{"value":"1"}]}`))
			}()
		}
	}()
	addr := ln.Addr().String()

	cfg := &config.Config{
		Modules: map[string]config.Module{
			"solaris_modern": {
				Items: []config.Item{
					{Key: "agent.ping", Metric: "zabbix_agent_ping", Type: config.MetricTypeGauge, Help: "ping"},
				},
			},
		},
	}

	client := zabbixclient.New(2 * time.Second)
	col := New(client, cfg, []string{"solaris_modern"}, addr, zabbixclient.ProtocolJSON, zap.NewNop())

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	expected := `
# HELP zabbix_agent_ping ping
# TYPE zabbix_agent_ping gauge
zabbix_agent_ping 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "zabbix_agent_ping"); err != nil {
		t.Fatalf("unexpected metrics: %v", err)
	}
}

var _ = context.Background // keep context import if unused by later edits
