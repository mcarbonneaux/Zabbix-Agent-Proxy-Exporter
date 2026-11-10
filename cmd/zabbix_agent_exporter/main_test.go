package main

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestBuildLoggerFallsBackToInfoOnInvalidLevel(t *testing.T) {
	logger, err := buildLogger("not-a-real-level", "console")
	if err != nil {
		t.Fatalf("buildLogger returned error: %v", err)
	}
	if !logger.Core().Enabled(zapcore.InfoLevel) {
		t.Fatal("expected logger to fall back to info level and have it enabled")
	}
	if logger.Core().Enabled(zapcore.DebugLevel) {
		t.Fatal("expected debug level to be disabled under info-level fallback")
	}
}

func TestBuildLoggerUsesExplicitValidLevel(t *testing.T) {
	logger, err := buildLogger("debug", "console")
	if err != nil {
		t.Fatalf("buildLogger returned error: %v", err)
	}
	if !logger.Core().Enabled(zapcore.DebugLevel) {
		t.Fatal("expected debug level to be enabled when explicitly configured")
	}
}

func TestBuildLoggerAcceptsJSONEncoding(t *testing.T) {
	logger, err := buildLogger("info", "json")
	if err != nil {
		t.Fatalf("buildLogger returned error: %v", err)
	}
	if logger == nil {
		t.Fatal("expected non-nil logger for json encoding")
	}
}
