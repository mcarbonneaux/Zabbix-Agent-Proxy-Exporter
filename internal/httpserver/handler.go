// internal/httpserver/handler.go
package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

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

	var urlPort *int
	if p := r.URL.Query().Get("port"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			http.Error(w, "invalid 'port' query parameter", http.StatusBadRequest)
			return
		}
		urlPort = &parsed
	}

	var urlTimeout *time.Duration
	if t := r.URL.Query().Get("timeout"); t != "" {
		parsed, err := time.ParseDuration(t)
		if err != nil {
			http.Error(w, "invalid 'timeout' query parameter", http.StatusBadRequest)
			return
		}
		urlTimeout = &parsed
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
