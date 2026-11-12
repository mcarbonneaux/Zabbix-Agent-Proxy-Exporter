// internal/zabbixclient/client_test.go
package zabbixclient

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"zabbix_agent_exporter/internal/zabbixproto"
)

// startFakeAgent starts a TCP listener that, for each accepted connection,
// reads one request in the given protocol's format and writes back the
// ZBXD-framed response produced by respond(requestKey). For ProtocolPlain,
// the request is a bare newline-terminated key with no header; for
// ProtocolLegacy and ProtocolJSON, the request is ZBXD-framed (with a bare
// key, or a `{"request":"passive checks","data":[{"key":...}]}` JSON body,
// respectively) and requestKey is extracted accordingly before being
// passed to respond.
func startFakeAgent(t *testing.T, protocol Protocol, respond func(requestKey string) []byte) string {
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

				var requestKey string
				switch protocol {
				case ProtocolPlain:
					buf := make([]byte, 4096)
					n, err := conn.Read(buf)
					if err != nil {
						return
					}
					requestKey = strings.TrimRight(string(buf[:n]), "\n")
				case ProtocolLegacy:
					payload, err := zabbixproto.ReadFrame(conn)
					if err != nil {
						return
					}
					requestKey = string(payload)
				case ProtocolJSON:
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
					requestKey = req.Data[0].Key
				}

				_ = zabbixproto.WriteFrame(conn, respond(requestKey))
			}()
		}
	}()
	return ln.Addr().String()
}

func TestGetItemReturnsValue(t *testing.T) {
	addr := startFakeAgent(t, ProtocolPlain, func(key string) []byte {
		if key != "agent.ping" {
			t.Errorf("unexpected request: %q", key)
		}
		return []byte("1")
	})

	client := New(2 * time.Second)
	got, err := client.GetItem(context.Background(), addr, "agent.ping", ProtocolPlain)
	if err != nil {
		t.Fatalf("GetItem returned error: %v", err)
	}
	if got != "1" {
		t.Fatalf("got %q, want %q", got, "1")
	}
}

func TestGetItemReturnsNotSupportedError(t *testing.T) {
	addr := startFakeAgent(t, ProtocolPlain, func(key string) []byte {
		return []byte("ZBX_NOTSUPPORTED\x00Unsupported item key.")
	})

	client := New(2 * time.Second)
	_, err := client.GetItem(context.Background(), addr, "bogus.key", ProtocolPlain)
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
	_, err := client.GetItem(context.Background(), "127.0.0.1:1", "agent.ping", ProtocolPlain)
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}
}

func TestDiscoverParsesJSONEntities(t *testing.T) {
	addr := startFakeAgent(t, ProtocolPlain, func(key string) []byte {
		if key != "vfs.fs.discovery" {
			t.Errorf("unexpected request: %q", key)
		}
		return []byte(`[{"{#FSNAME}":"/","{#FSTYPE}":"ext4"},{"{#FSNAME}":"/var","{#FSTYPE}":"ext4"}]`)
	})

	client := New(2 * time.Second)
	entities, err := client.Discover(context.Background(), addr, "vfs.fs.discovery", ProtocolPlain)
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

func TestDiscoverReturnsErrorOnNotSupported(t *testing.T) {
	addr := startFakeAgent(t, ProtocolPlain, func(key string) []byte {
		return []byte("ZBX_NOTSUPPORTED\x00Unsupported discovery key.")
	})

	client := New(2 * time.Second)
	_, err := client.Discover(context.Background(), addr, "bogus.discovery", ProtocolPlain)
	if err == nil {
		t.Fatal("expected error for unsupported discovery key, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected 'not supported' in error, got: %v", err)
	}
}

func TestDiscoverReturnsErrorOnMalformedJSON(t *testing.T) {
	addr := startFakeAgent(t, ProtocolPlain, func(key string) []byte {
		return []byte("not valid json")
	})

	client := New(2 * time.Second)
	_, err := client.Discover(context.Background(), addr, "vfs.fs.discovery", ProtocolPlain)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestGetItemUsesLegacyProtocolSendsZBXDFramedBareKey(t *testing.T) {
	addr := startFakeAgent(t, ProtocolLegacy, func(key string) []byte {
		if key != "agent.ping" {
			t.Errorf("unexpected request: %q", key)
		}
		return []byte("1")
	})

	client := New(2 * time.Second)
	got, err := client.GetItem(context.Background(), addr, "agent.ping", ProtocolLegacy)
	if err != nil {
		t.Fatalf("GetItem returned error: %v", err)
	}
	if got != "1" {
		t.Fatalf("got %q, want %q", got, "1")
	}
}

func TestGetItemUsesJSONProtocolSendsStructuredRequestAndParsesValue(t *testing.T) {
	addr := startFakeAgent(t, ProtocolJSON, func(key string) []byte {
		if key != "agent.ping" {
			t.Errorf("unexpected request: %q", key)
		}
		return []byte(`{"version":"7.0.3","variant":2,"data":[{"value":"1"}]}`)
	})

	client := New(2 * time.Second)
	got, err := client.GetItem(context.Background(), addr, "agent.ping", ProtocolJSON)
	if err != nil {
		t.Fatalf("GetItem returned error: %v", err)
	}
	if got != "1" {
		t.Fatalf("got %q, want %q", got, "1")
	}
}

func TestGetItemJSONProtocolReturnsNotSupportedOnDataError(t *testing.T) {
	addr := startFakeAgent(t, ProtocolJSON, func(key string) []byte {
		return []byte(`{"version":"7.0.3","variant":2,"data":[{"error":"Unsupported item key."}]}`)
	})

	client := New(2 * time.Second)
	_, err := client.GetItem(context.Background(), addr, "bogus.key", ProtocolJSON)
	if err == nil {
		t.Fatal("expected error for unsupported item, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected 'not supported' in error, got: %v", err)
	}
}

func TestDiscoverUsesJSONProtocol(t *testing.T) {
	addr := startFakeAgent(t, ProtocolJSON, func(key string) []byte {
		if key != "vfs.fs.discovery" {
			t.Errorf("unexpected request: %q", key)
		}
		return []byte(`{"version":"7.0.3","variant":2,"data":[{"value":"[{\"{#FSNAME}\":\"/\"}]"}]}`)
	})

	client := New(2 * time.Second)
	entities, err := client.Discover(context.Background(), addr, "vfs.fs.discovery", ProtocolJSON)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(entities) != 1 || entities[0]["{#FSNAME}"] != "/" {
		t.Fatalf("got entities %v, want one entity with {#FSNAME}=/", entities)
	}
}
