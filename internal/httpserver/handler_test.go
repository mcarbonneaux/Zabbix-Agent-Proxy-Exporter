// internal/httpserver/handler_test.go
package httpserver

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestServerHandlesConcurrentSetConfigAndRequests(t *testing.T) {
	addr := startFakeAgent(t)
	host, _, _ := net.SplitHostPort(addr)
	cfg := testConfigForAddr(addr)

	srv := New(cfg, zap.NewNop())
	handler := srv.Handler()

	var wg sync.WaitGroup

	// One goroutine repeatedly swaps the config (same content, just to
	// exercise the write path concurrently with readers).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			srv.SetConfig(testConfigForAddr(addr))
		}
	}()

	// Several goroutines repeatedly hit /metrics concurrently.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				req := httptest.NewRequest(http.MethodGet, "/metrics?target="+host, nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("got status %d, want 200", rec.Code)
				}
			}
		}()
	}

	wg.Wait()
}
