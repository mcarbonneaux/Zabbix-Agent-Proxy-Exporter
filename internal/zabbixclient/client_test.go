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
