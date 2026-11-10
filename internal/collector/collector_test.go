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
		"agent.ping":           []byte("1"),
		"vfs.fs.discovery":     []byte(`[{"{#FSNAME}":"/"}]`),
		"vfs.fs.size[/,pfree]": []byte("42.5"),
	})

	client := zabbixclient.New(2 * time.Second)
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zap.NewNop())

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
	col := New(client, testModuleConfig(), []string{"linux"}, "127.0.0.1:1", zap.NewNop())

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
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zap.NewNop())

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
