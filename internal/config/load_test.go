package config

import (
	"os"
	"testing"
	"time"
)

var osWriteFile = os.WriteFile

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

func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load("testdata/unknown_key.yaml")
	if err == nil {
		t.Fatal("expected error for unknown top-level key (typo'd 'moduels'), got nil")
	}
}

func TestLoadRejectsDuplicateGroupName(t *testing.T) {
	_, err := Load("testdata/duplicate_group.yaml")
	if err == nil {
		t.Fatal("expected error for duplicate group name, got nil")
	}
}

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
	if err := osWriteFile(tmpFile, initial, 0o644); err != nil {
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
	if err := osWriteFile(tmpFile, updated, 0o644); err != nil {
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
