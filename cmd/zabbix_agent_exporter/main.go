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

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("starting server", zap.String("listen_addr", cfg.Server.ListenAddr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			logger.Error("server failed to start or serve", zap.Error(err))
			os.Exit(1)
		}
		// err == nil means ListenAndServe returned http.ErrServerClosed,
		// which only happens after Shutdown was already called elsewhere;
		// this path is not expected to be reached in normal operation
		// since nothing calls Shutdown before ctx is done, but is handled
		// defensively rather than left to fall through.
		return
	case <-ctx.Done():
		logger.Info("shutting down")
	}

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
