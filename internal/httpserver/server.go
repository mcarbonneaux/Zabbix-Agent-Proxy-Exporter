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
