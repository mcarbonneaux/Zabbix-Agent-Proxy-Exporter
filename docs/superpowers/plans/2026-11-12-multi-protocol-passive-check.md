# Multi-protocol Passive Check Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the exporter talk to Zabbix passive agents spanning three real-world protocol generations (pre-2.0 "plain" text, 2.0–6.x "legacy" ZBXD+text, 7.0+ "json" ZBXD+JSON), selectable per host via a new `protocol` config field, so a single exporter instance can scrape a fleet with mixed agent versions.

**Architecture:** A new `Protocol` enum threads through the existing cascade (`config.Defaults`/`config.HostConfig` → `config.Resolve` → `httpserver` → `collector` → `zabbixclient`). The `ZBXD` header codec (`zabbixproto.WriteFrame`/`ReadFrame`) is unchanged and reused by all three variants; only the request payload format and response parsing differ per variant.

**Tech Stack:** Go 1.27, existing `internal/zabbixproto`, `internal/zabbixclient`, `internal/config`, `internal/collector`, `internal/httpserver` packages — no new dependencies.

Full design reference: `docs/superpowers/specs/2026-11-12-multi-protocol-passive-check-design.md`

---

## File Structure

```
internal/zabbixproto/frame.go        # + WriteLegacyRequest, WriteJSONRequest, ParseJSONResponse
internal/zabbixproto/frame_test.go   # + tests for the three new functions

internal/zabbixclient/client.go       # + Protocol type, GetItem/Discover gain a protocol param
internal/zabbixclient/client_test.go  # existing tests pass explicit ProtocolPlain; + legacy/json tests

internal/config/types.go              # + Protocol type, Defaults.Protocol, HostConfig.Protocol
internal/config/load.go               # validate() rejects unknown protocol values
internal/config/load_test.go          # + fixtures/tests for protocol parsing and validation
internal/config/resolve.go            # ResolvedTarget gains Protocol; Resolve cascades it
internal/config/resolve_test.go       # + protocol cascade tests; existing tests unaffected (no new required arg)

internal/collector/collector.go       # ZabbixCollector gains a protocol field; zabbixQuerier methods gain a protocol param; all 3 call sites pass it
internal/collector/collector_test.go  # existing tests updated to pass explicit protocol; + protocol-specific tests

internal/httpserver/handler.go        # passes resolved.Protocol, converted, into collector.New

configs/config.example.yaml           # solaris host gets protocol: "plain"; solaris_modern host gets protocol: "json"
```

Dependency direction is unchanged: `zabbixproto` ← `zabbixclient` ← `collector` ← `httpserver`; `config` is independent and consumed by `collector`/`httpserver`/`resolve`.

---

## Task 1: Protocol wire formats (`internal/zabbixproto`)

**Files:**
- Modify: `internal/zabbixproto/frame.go`
- Test: `internal/zabbixproto/frame_test.go`

This task adds the two new request-writing functions and the JSON response parser, on top of the existing (unchanged) `WriteFrame`/`ReadFrame`/`WritePlainRequest`.

- [ ] **Step 1: Write failing tests for the three new functions**

Append to `internal/zabbixproto/frame_test.go` (add `"encoding/json"` to the existing import block, which already has `bytes`, `encoding/binary`, `errors`, `io`, `strings`, `testing`):

```go
func TestWriteLegacyRequestWrapsKeyInZBXDHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLegacyRequest(&buf, "agent.ping"); err != nil {
		t.Fatalf("WriteLegacyRequest returned error: %v", err)
	}

	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}
	if string(got) != "agent.ping" {
		t.Fatalf("got payload %q, want %q (no trailing newline)", got, "agent.ping")
	}
}

func TestWriteJSONRequestBuildsPassiveChecksPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSONRequest(&buf, "agent.ping", 5); err != nil {
		t.Fatalf("WriteJSONRequest returned error: %v", err)
	}

	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("failed to unmarshal written payload: %v", err)
	}
	if decoded["request"] != "passive checks" {
		t.Fatalf("got request %q, want %q", decoded["request"], "passive checks")
	}
	data, ok := decoded["data"].([]interface{})
	if !ok || len(data) != 1 {
		t.Fatalf("got data %v, want a single-element array", decoded["data"])
	}
	item, ok := data[0].(map[string]interface{})
	if !ok {
		t.Fatalf("got data[0] %v, want an object", data[0])
	}
	if item["key"] != "agent.ping" {
		t.Fatalf("got key %v, want %q", item["key"], "agent.ping")
	}
	if item["timeout"] != float64(5) {
		t.Fatalf("got timeout %v, want 5", item["timeout"])
	}
}

func TestWriteJSONRequestFloorsTimeoutToOneSecond(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSONRequest(&buf, "agent.ping", 0); err != nil {
		t.Fatalf("WriteJSONRequest returned error: %v", err)
	}

	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("failed to unmarshal written payload: %v", err)
	}
	data := decoded["data"].([]interface{})
	item := data[0].(map[string]interface{})
	if item["timeout"] != float64(1) {
		t.Fatalf("got timeout %v, want floor of 1", item["timeout"])
	}
}

func TestParseJSONResponseExtractsValue(t *testing.T) {
	payload := []byte(`{"version":"7.0.3","variant":2,"data":[{"value":"1"}]}`)

	got, err := ParseJSONResponse(payload)
	if err != nil {
		t.Fatalf("ParseJSONResponse returned error: %v", err)
	}
	if got != "1" {
		t.Fatalf("got %q, want %q", got, "1")
	}
}

func TestParseJSONResponseReturnsErrorOnDataError(t *testing.T) {
	payload := []byte(`{"version":"7.0.3","variant":2,"data":[{"error":"Unsupported item key."}]}`)

	_, err := ParseJSONResponse(payload)
	if err == nil {
		t.Fatal("expected error for data[].error, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected 'not supported' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Unsupported item key.") {
		t.Fatalf("expected original Zabbix error text in error, got: %v", err)
	}
}

func TestParseJSONResponseReturnsErrorOnEmptyDataArray(t *testing.T) {
	payload := []byte(`{"version":"7.0.3","variant":2,"data":[]}`)

	_, err := ParseJSONResponse(payload)
	if err == nil {
		t.Fatal("expected error for empty data array, got nil")
	}
}

func TestParseJSONResponseReturnsErrorOnMalformedJSON(t *testing.T) {
	_, err := ParseJSONResponse([]byte("not valid json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/zabbixproto/... -run 'WriteLegacyRequest|WriteJSONRequest|ParseJSONResponse' -v`
Expected: FAIL — `undefined: WriteLegacyRequest`, `undefined: WriteJSONRequest`, `undefined: ParseJSONResponse`.

- [ ] **Step 3: Implement the three functions**

Add `"encoding/json"` to the import block of `internal/zabbixproto/frame.go` (currently `encoding/binary`, `fmt`, `io`), and append:

```go
// WriteLegacyRequest writes key as a ZBXD-framed payload containing just
// the bare key — no trailing newline, no JSON. This is the Zabbix
// 2.0–6.x passive-check request format.
func WriteLegacyRequest(w io.Writer, key string) error {
	return WriteFrame(w, []byte(key))
}

type jsonRequest struct {
	Request string            `json:"request"`
	Data    []jsonRequestItem `json:"data"`
}

type jsonRequestItem struct {
	Key     string `json:"key"`
	Timeout int    `json:"timeout"`
}

// WriteJSONRequest writes a ZBXD-framed JSON "passive checks" request for
// key, with the given timeout in seconds (floored to 1 if lower). This is
// the Zabbix 7.0+ passive-check request format.
func WriteJSONRequest(w io.Writer, key string, timeoutSeconds int) error {
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}
	payload, err := json.Marshal(jsonRequest{
		Request: "passive checks",
		Data:    []jsonRequestItem{{Key: key, Timeout: timeoutSeconds}},
	})
	if err != nil {
		return fmt.Errorf("zabbixproto: marshal json request for %q: %w", key, err)
	}
	return WriteFrame(w, payload)
}

type jsonResponse struct {
	Data []jsonResponseItem `json:"data"`
}

type jsonResponseItem struct {
	Value string `json:"value"`
	Error string `json:"error"`
}

// ParseJSONResponse extracts the single value (or error) from a Zabbix
// 7.0+ passive-check JSON response payload, as read via ReadFrame.
func ParseJSONResponse(payload []byte) (string, error) {
	var resp jsonResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return "", fmt.Errorf("zabbixproto: parse json response: %w", err)
	}
	if len(resp.Data) == 0 {
		return "", fmt.Errorf("zabbixproto: json response data array is empty")
	}
	item := resp.Data[0]
	if item.Error != "" {
		return "", fmt.Errorf("zabbixproto: item not supported: %s", item.Error)
	}
	return item.Value, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/zabbixproto/... -v`
Expected: PASS — all tests in the package green (the 6 pre-existing tests plus the 6 new ones).

- [ ] **Step 5: Commit**

```bash
git add internal/zabbixproto
git commit -m "feat: add legacy and JSON passive-check request/response wire formats"
```

---

## Task 2: Protocol type and client support (`internal/zabbixclient`)

**Files:**
- Modify: `internal/zabbixclient/client.go`
- Test: `internal/zabbixclient/client_test.go`

Adds a local `Protocol` type (to avoid an import cycle with `config`), and threads it through `GetItem`/`Discover`/`roundTrip`.

- [ ] **Step 1: Write failing tests for all three protocols**

This step rewrites `internal/zabbixclient/client_test.go`'s `startFakeAgent` helper to support reading any of the three request formats (the existing helper only reads a bare newline-terminated line, which breaks for `legacy`/`json` requests that are `ZBXD`-framed), and updates every existing test to pass an explicit protocol, plus adds new protocol-specific tests.

Replace the entire file `internal/zabbixclient/client_test.go` with:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/zabbixclient/... -v`
Expected: FAIL — `undefined: Protocol`, `undefined: ProtocolPlain`, and `GetItem`/`Discover` called with too many arguments (current signatures don't take a protocol parameter).

- [ ] **Step 3: Implement the Protocol type and thread it through the client**

Replace `internal/zabbixclient/client.go` in full:

```go
// internal/zabbixclient/client.go
package zabbixclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"zabbix_agent_exporter/internal/zabbixproto"
)

// Protocol selects which Zabbix passive-check wire dialect to speak to an
// agent. Different Zabbix agent generations expect different request
// framing and response shapes on the same TCP port; see
// docs/superpowers/specs/2026-11-12-multi-protocol-passive-check-design.md
// for the full rationale.
type Protocol string

const (
	// ProtocolPlain sends a bare newline-terminated key with no ZBXD
	// header on the request. Matches Zabbix agents older than 2.0.
	ProtocolPlain Protocol = "plain"
	// ProtocolLegacy sends a ZBXD-framed bare key (no JSON). Matches
	// Zabbix agents 2.0 through 6.x.
	ProtocolLegacy Protocol = "legacy"
	// ProtocolJSON sends a ZBXD-framed JSON "passive checks" request and
	// expects a JSON response. Matches Zabbix agents 7.0 and newer.
	ProtocolJSON Protocol = "json"
)

// Client queries Zabbix passive agents over TCP, one item per connection.
type Client struct {
	timeout time.Duration
}

// New returns a Client that uses timeout for both dialing and reading.
func New(timeout time.Duration) *Client {
	return &Client{timeout: timeout}
}

func (c *Client) roundTrip(ctx context.Context, addr, key string, protocol Protocol) ([]byte, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("zabbixclient: dial %s: %w", addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(c.timeout)
	// SetDeadline enforces c.timeout independently of ctx; cancelling ctx
	// mid-read will not abort the read early.
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("zabbixclient: set deadline: %w", err)
	}

	var writeErr error
	switch protocol {
	case ProtocolLegacy:
		writeErr = zabbixproto.WriteLegacyRequest(conn, key)
	case ProtocolJSON:
		writeErr = zabbixproto.WriteJSONRequest(conn, key, int(c.timeout.Seconds()))
	default:
		writeErr = zabbixproto.WritePlainRequest(conn, key)
	}
	if writeErr != nil {
		return nil, fmt.Errorf("zabbixclient: send request %q to %s: %w", key, addr, writeErr)
	}

	payload, err := zabbixproto.ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("zabbixclient: read response for %q from %s: %w", key, addr, err)
	}
	return payload, nil
}

func checkNotSupported(key, addr string, payload []byte) error {
	value := string(payload)
	if strings.HasPrefix(value, "ZBX_NOTSUPPORTED") {
		reason := strings.TrimPrefix(value, "ZBX_NOTSUPPORTED\x00")
		return fmt.Errorf("zabbixclient: item %q not supported by %s: %s", key, addr, reason)
	}
	return nil
}

// GetItem requests key from the agent at addr using the given protocol and
// returns its string value.
func (c *Client) GetItem(ctx context.Context, addr, key string, protocol Protocol) (string, error) {
	payload, err := c.roundTrip(ctx, addr, key, protocol)
	if err != nil {
		return "", err
	}

	if protocol == ProtocolJSON {
		value, err := zabbixproto.ParseJSONResponse(payload)
		if err != nil {
			return "", fmt.Errorf("zabbixclient: item %q from %s: %w", key, addr, err)
		}
		return value, nil
	}

	if err := checkNotSupported(key, addr, payload); err != nil {
		return "", err
	}
	return string(payload), nil
}

// Discover requests a discovery key (e.g. vfs.fs.discovery) using the
// given protocol and returns the list of discovered entities as maps of
// macro name to value.
func (c *Client) Discover(ctx context.Context, addr, discoveryKey string, protocol Protocol) ([]map[string]string, error) {
	payload, err := c.roundTrip(ctx, addr, discoveryKey, protocol)
	if err != nil {
		return nil, err
	}

	var lldJSON []byte
	if protocol == ProtocolJSON {
		value, err := zabbixproto.ParseJSONResponse(payload)
		if err != nil {
			return nil, fmt.Errorf("zabbixclient: discovery %q from %s: %w", discoveryKey, addr, err)
		}
		lldJSON = []byte(value)
	} else {
		if err := checkNotSupported(discoveryKey, addr, payload); err != nil {
			return nil, err
		}
		lldJSON = payload
	}

	var entities []map[string]string
	if err := json.Unmarshal(lldJSON, &entities); err != nil {
		return nil, fmt.Errorf("zabbixclient: parse discovery JSON for %q from %s: %w", discoveryKey, addr, err)
	}
	return entities, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/zabbixclient/... -v`
Expected: PASS — all tests in the package green (9 pre-existing behaviors re-verified under explicit `ProtocolPlain`, plus 4 new protocol-specific tests).

- [ ] **Step 5: Commit**

```bash
git add internal/zabbixclient
git commit -m "feat: add Protocol type to zabbixclient, support legacy and json dialects"
```

---

## Task 3: Protocol config field (`internal/config`)

**Files:**
- Modify: `internal/config/types.go`
- Modify: `internal/config/load.go`
- Test: `internal/config/load_test.go`
- Test fixtures: `internal/config/testdata/valid.yaml` (modify), `internal/config/testdata/invalid_protocol.yaml` (create)

- [ ] **Step 1: Write failing tests for protocol parsing and validation**

Modify `internal/config/testdata/valid.yaml` — add a `protocol` field to `defaults` and to the `srv-old-02` host override (read the current file first; it currently has `defaults: {port: 10050, timeout: 5s}` and `hosts: {srv-old-01: {}, srv-old-02: {port: 10150, timeout: 10s}}`):

```yaml
defaults:
  port: 10050
  timeout: 5s
  protocol: "legacy"

hosts:
  srv-old-01: {}
  srv-old-02:
    port: 10150
    timeout: 10s
    protocol: "json"
```

(Leave every other section of `valid.yaml` — `server`, `modules`, `groups` — exactly as-is.)

Create `internal/config/testdata/invalid_protocol.yaml`:

```yaml
server:
  listen_addr: ":9290"
defaults:
  port: 10050
  timeout: 5s
  protocol: "not-a-real-protocol"
modules:
  linux:
    items:
      - key: "agent.ping"
        metric: "zabbix_agent_ping"
        type: gauge
        help: "ping"
groups: []
```

Add to `internal/config/load_test.go` (append; the file already imports `os`, `testing`, `time`):

```go
func TestLoadParsesProtocolField(t *testing.T) {
	cfg, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Defaults.Protocol != ProtocolLegacy {
		t.Fatalf("got Defaults.Protocol %q, want %q", cfg.Defaults.Protocol, ProtocolLegacy)
	}

	host, ok := cfg.Hosts["srv-old-02"]
	if !ok {
		t.Fatal("expected host srv-old-02 to be present")
	}
	if host.Protocol == nil || *host.Protocol != ProtocolJSON {
		t.Fatalf("got srv-old-02 protocol override %v, want %q", host.Protocol, ProtocolJSON)
	}

	defaultHost, ok := cfg.Hosts["srv-old-01"]
	if !ok {
		t.Fatal("expected host srv-old-01 to be present")
	}
	if defaultHost.Protocol != nil {
		t.Fatalf("expected srv-old-01 to have no protocol override, got %v", defaultHost.Protocol)
	}
}

func TestLoadRejectsInvalidProtocol(t *testing.T) {
	_, err := Load("testdata/invalid_protocol.yaml")
	if err == nil {
		t.Fatal("expected error for invalid protocol value, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -run 'TestLoadParsesProtocolField|TestLoadRejectsInvalidProtocol' -v`
Expected: FAIL — `undefined: ProtocolLegacy` (and `ProtocolJSON`), since the `Protocol` type doesn't exist in `config` yet.

- [ ] **Step 3: Add the Protocol type and fields to types.go**

In `internal/config/types.go`, add near the top (after the existing `MetricType` block):

```go
// Protocol selects which Zabbix passive-check wire dialect to use for a
// host. Mirrors zabbixclient.Protocol's three values; kept as a separate
// type here (rather than importing zabbixclient) to avoid a dependency
// from config on zabbixclient — collector converts between the two at
// the one call site that needs both.
type Protocol string

const (
	ProtocolPlain  Protocol = "plain"
	ProtocolLegacy Protocol = "legacy"
	ProtocolJSON   Protocol = "json"
)
```

Modify `Defaults` (currently `Port int` / `Timeout time.Duration`) to add a third field:

```go
// Defaults holds the fallback port/timeout/protocol used when a host has
// no override.
type Defaults struct {
	Port     int           `yaml:"port"`
	Timeout  time.Duration `yaml:"timeout"`
	Protocol Protocol      `yaml:"protocol"`
}
```

Modify `HostConfig` (currently `Port *int` / `Timeout *time.Duration`) to add a third pointer field:

```go
// HostConfig holds optional per-host overrides of port/timeout/protocol.
type HostConfig struct {
	Port     *int           `yaml:"port"`
	Timeout  *time.Duration `yaml:"timeout"`
	Protocol *Protocol      `yaml:"protocol"`
}
```

- [ ] **Step 4: Add validation and the "legacy" default in load.go**

In `internal/config/load.go`, after the `decoder.Decode(&cfg)` call and before calling `validate(&cfg)`, default an empty `Protocol` to `ProtocolLegacy` (YAML omits the field entirely in configs written before this feature existed, which unmarshals to the Go zero value `""`, not a valid `Protocol`):

```go
	if cfg.Defaults.Protocol == "" {
		cfg.Defaults.Protocol = ProtocolLegacy
	}
```

(Insert this line right after the `decoder.Decode(&cfg)` block, before `if err := validate(&cfg); err != nil {`.)

Add protocol validation inside `validate()`. The current function starts with a loop over `cfg.Modules`; add this new, independent validation block right after the existing `seenGroupNames`/`cfg.Groups` loop, before the final `return nil`:

```go
	if !isValidProtocol(cfg.Defaults.Protocol) {
		return fmt.Errorf("defaults: invalid protocol %q (want plain, legacy, or json)", cfg.Defaults.Protocol)
	}
	for name, host := range cfg.Hosts {
		if host.Protocol != nil && !isValidProtocol(*host.Protocol) {
			return fmt.Errorf("host %q: invalid protocol %q (want plain, legacy, or json)", name, *host.Protocol)
		}
	}
```

Add the helper function at the bottom of `load.go`:

```go
func isValidProtocol(p Protocol) bool {
	return p == ProtocolPlain || p == ProtocolLegacy || p == ProtocolJSON
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — all tests in the package green, including the two new ones and all pre-existing ones (pre-existing tests don't reference `protocol` at all, so they get the `ProtocolLegacy` default transparently and are unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/config/types.go internal/config/load.go internal/config/load_test.go internal/config/testdata/valid.yaml internal/config/testdata/invalid_protocol.yaml
git commit -m "feat: add protocol field to config with legacy default and validation"
```

---

## Task 4: Protocol resolution cascade (`internal/config`)

**Files:**
- Modify: `internal/config/resolve.go`
- Test: `internal/config/resolve_test.go`

Adds `Protocol` to `ResolvedTarget` and cascades it `Defaults.Protocol` → `HostConfig.Protocol` override (no URL-level override, per the design).

- [ ] **Step 1: Write failing tests for the protocol cascade**

Modify `internal/config/resolve_test.go`'s `testConfig()` helper (currently builds a `*Config` with `Defaults{Port: 10050, Timeout: 5*time.Second}` and two hosts) to also set a default protocol and a per-host override:

```go
func testConfig() *Config {
	ten := 10150
	tenSec := 10 * time.Second
	jsonProto := ProtocolJSON
	return &Config{
		Defaults: Defaults{Port: 10050, Timeout: 5 * time.Second, Protocol: ProtocolLegacy},
		Hosts: map[string]HostConfig{
			"srv-old-01": {},
			"srv-old-02": {Port: &ten, Timeout: &tenSec, Protocol: &jsonProto},
		},
		Modules: map[string]Module{
			"linux": {},
			"mysql": {},
			"other": {},
		},
		Groups: []Group{
			{Name: "tous-linux", Hosts: []string{"srv-old-01", "srv-old-02"}, Modules: []string{"linux"}},
			{Name: "base-de-donnees", Hosts: []string{"srv-old-02"}, Modules: []string{"mysql"}},
		},
	}
}
```

Append this new test to `internal/config/resolve_test.go`:

```go
func TestResolveProtocolCascade(t *testing.T) {
	cfg := testConfig()

	// srv-old-01 has no host override -> falls back to Defaults.Protocol.
	resolved, err := Resolve(cfg, "srv-old-01", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Protocol != ProtocolLegacy {
		t.Fatalf("got protocol %q, want default %q", resolved.Protocol, ProtocolLegacy)
	}

	// srv-old-02 has a host override -> takes precedence over Defaults.
	resolved, err = Resolve(cfg, "srv-old-02", nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Protocol != ProtocolJSON {
		t.Fatalf("got protocol %q, want host override %q", resolved.Protocol, ProtocolJSON)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/... -run TestResolveProtocolCascade -v`
Expected: FAIL — `resolved.Protocol` doesn't compile because `ResolvedTarget` has no `Protocol` field yet.

- [ ] **Step 3: Add Protocol to ResolvedTarget and cascade it in Resolve**

In `internal/config/resolve.go`, modify `ResolvedTarget` (currently `Host string`, `Port int`, `Timeout time.Duration`, `Modules []string`):

```go
// ResolvedTarget holds the fully resolved connection settings and module
// list for a single scrape of one target.
type ResolvedTarget struct {
	Host     string
	Port     int
	Timeout  time.Duration
	Protocol Protocol
	Modules  []string
}
```

In the `Resolve` function body, the existing port/timeout cascade block is:

```go
	port := cfg.Defaults.Port
	timeout := cfg.Defaults.Timeout
	if host, ok := cfg.Hosts[target]; ok {
		if host.Port != nil {
			port = *host.Port
		}
		if host.Timeout != nil {
			timeout = *host.Timeout
		}
	}
	if urlPort != nil {
		port = *urlPort
	}
	if urlTimeout != nil {
		timeout = *urlTimeout
	}
```

Change it to also resolve `protocol` (no URL-level override step for protocol, per the design):

```go
	port := cfg.Defaults.Port
	timeout := cfg.Defaults.Timeout
	protocol := cfg.Defaults.Protocol
	if host, ok := cfg.Hosts[target]; ok {
		if host.Port != nil {
			port = *host.Port
		}
		if host.Timeout != nil {
			timeout = *host.Timeout
		}
		if host.Protocol != nil {
			protocol = *host.Protocol
		}
	}
	if urlPort != nil {
		port = *urlPort
	}
	if urlTimeout != nil {
		timeout = *urlTimeout
	}
```

And update the final `return` statement from:

```go
	return ResolvedTarget{
		Host:    target,
		Port:    port,
		Timeout: timeout,
		Modules: modules,
	}, nil
```

to:

```go
	return ResolvedTarget{
		Host:     target,
		Port:     port,
		Timeout:  timeout,
		Protocol: protocol,
		Modules:  modules,
	}, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS — all tests in the package green, including the new cascade test and every pre-existing `resolve_test.go` test (they don't check `resolved.Protocol`, so adding the field doesn't break their assertions).

- [ ] **Step 5: Commit**

```bash
git add internal/config/resolve.go internal/config/resolve_test.go
git commit -m "feat: resolve protocol via defaults-to-host-override cascade"
```

---

## Task 5: Thread protocol through the collector (`internal/collector`)

**Files:**
- Modify: `internal/collector/collector.go`
- Test: `internal/collector/collector_test.go`

The `zabbixQuerier` interface's two methods gain a `protocol` parameter; `ZabbixCollector` gains a `protocol` field set at construction; all three call sites inside `Collect` (items, discovered items, line-list items) pass it through.

- [ ] **Step 1: Update existing tests to match the new New() signature, and add protocol-specific tests**

The collector's own test file defines a local `startFakeAgent` (different from `zabbixclient`'s — it responds based on a map of request-string → response-bytes, always reading a bare newline-terminated request). Since `New()` is changing to require an explicit protocol, and the existing collector tests all exercise `ProtocolPlain` semantics (bare key, no header) which matches the existing `startFakeAgent` exactly, no changes are needed to `startFakeAgent` itself — only to every `New(...)` call site, which must add a protocol argument.

In `internal/collector/collector_test.go`, update every call of the form `New(client, cfg, []string{...}, addr, zap.NewNop())` or `New(client, cfg, []string{...}, addr, nil)` to insert `zabbixclient.ProtocolPlain` as the argument right after `addr`. There are 9 such call sites in the file (one per test function): `TestCollectEmitsSimpleAndDiscoveredMetrics`, `TestCollectMarksScrapeFailedWhenAgentUnreachable`, `TestCollectContinuesAfterSingleItemError`, `TestNewDefaultsNilLoggerToNoop`, `TestCollectEmitsOneMetricPerDiscoveredEntity`, `TestCollectEmitsOneMetricPerLineListEntry`, `TestCollectSkipsEmptyLinesInLineListItem`, `TestCollectLineListItemFailureCountsAsItemError`, `TestCollectHandlesSameMetricNameAcrossTwoModules`, `TestCollectTrimsWhitespaceFromNumericItemValue` (10 total). For example, change:

```go
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zap.NewNop())
```

to:

```go
	col := New(client, testModuleConfig(), []string{"linux"}, addr, zabbixclient.ProtocolPlain, zap.NewNop())
```

Apply the same `, zabbixclient.ProtocolPlain` insertion (right before the logger argument) to all 10 call sites in the file. The file already imports `"zabbix_agent_exporter/internal/zabbixclient"`, so no new import is needed.

Then append this new test, which proves the collector actually passes a non-`ProtocolPlain` value through correctly by using a fake agent that only understands the JSON wire format:

```go
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
```

Add `"encoding/json"` to the file's import block (it currently has `context`, `net`, `strings`, `testing`, `time`, plus the three external/internal packages).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/collector/... -v`
Expected: FAIL — too many/too few arguments in calls to `New` (current signature doesn't accept a protocol parameter yet).

- [ ] **Step 3: Thread protocol through ZabbixCollector**

In `internal/collector/collector.go`, update the `zabbixQuerier` interface (currently):

```go
type zabbixQuerier interface {
	GetItem(ctx context.Context, addr, key string) (string, error)
	Discover(ctx context.Context, addr, discoveryKey string) ([]map[string]string, error)
}
```

to:

```go
type zabbixQuerier interface {
	GetItem(ctx context.Context, addr, key string, protocol zabbixclient.Protocol) (string, error)
	Discover(ctx context.Context, addr, discoveryKey string, protocol zabbixclient.Protocol) ([]map[string]string, error)
}
```

Add `"zabbix_agent_exporter/internal/zabbixclient"` to the import block (currently `context`, `strconv`, `strings`, `time`, plus `prometheus` and `zap`).

Update `ZabbixCollector` (currently `client`, `cfg`, `modules`, `addr`, `logger` fields) to add a `protocol` field:

```go
type ZabbixCollector struct {
	client   zabbixQuerier
	cfg      *config.Config
	modules  []string
	addr     string
	protocol zabbixclient.Protocol
	logger   *zap.Logger
}
```

Update `New` (currently `func New(client zabbixQuerier, cfg *config.Config, modules []string, addr string, logger *zap.Logger) *ZabbixCollector`) to accept and store the protocol, inserting the new parameter right before `logger`:

```go
// New builds a collector for one scrape of addr using the given modules,
// looked up by name in cfg.Modules, speaking the given protocol. If
// logger is nil, a no-op logger is used instead of panicking on the
// first Warn call.
func New(client zabbixQuerier, cfg *config.Config, modules []string, addr string, protocol zabbixclient.Protocol, logger *zap.Logger) *ZabbixCollector {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ZabbixCollector{client: client, cfg: cfg, modules: modules, addr: addr, protocol: protocol, logger: logger}
}
```

Update the three call sites inside `Collect` that call `c.client.GetItem(...)` or `c.client.Discover(...)`. There are four such calls:

1. Simple items — change `value, err := c.client.GetItem(ctx, c.addr, item.Key)` to `value, err := c.client.GetItem(ctx, c.addr, item.Key, c.protocol)`.
2. Discovery call — change `entities, err := c.client.Discover(ctx, c.addr, di.DiscoveryKey)` to `entities, err := c.client.Discover(ctx, c.addr, di.DiscoveryKey, c.protocol)`.
3. Per-entity value query inside the discovery loop — change `value, err := c.client.GetItem(ctx, c.addr, valueKey)` to `value, err := c.client.GetItem(ctx, c.addr, valueKey, c.protocol)`.
4. Line-list items — change `value, err := c.client.GetItem(ctx, c.addr, lli.Key)` to `value, err := c.client.GetItem(ctx, c.addr, lli.Key, c.protocol)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/collector/... -v`
Expected: PASS — all tests in the package green (10 pre-existing tests with `ProtocolPlain` added, plus the 1 new JSON-protocol test).

- [ ] **Step 5: Commit**

```bash
git add internal/collector
git commit -m "feat: thread configured protocol from collector into zabbixclient calls"
```

---

## Task 6: Wire protocol through the HTTP handler and example config

**Files:**
- Modify: `internal/httpserver/handler.go`
- Modify: `configs/config.example.yaml`

- [ ] **Step 1: Pass resolved.Protocol into collector.New**

In `internal/httpserver/handler.go`, the current call is:

```go
	addr := fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)
	client := zabbixclient.New(resolved.Timeout)
	col := collector.New(client, cfg, resolved.Modules, addr, s.logger)
```

`resolved.Protocol` is a `config.Protocol`, but `collector.New` expects a `zabbixclient.Protocol` — both are the string type `"plain"`/`"legacy"`/`"json"`, so a direct conversion works. Change to:

```go
	addr := fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)
	client := zabbixclient.New(resolved.Timeout)
	col := collector.New(client, cfg, resolved.Modules, addr, zabbixclient.Protocol(resolved.Protocol), s.logger)
```

- [ ] **Step 2: Run the httpserver test suite to confirm nothing broke**

Run: `go test ./internal/httpserver/... -v`
Expected: PASS — all tests in the package green. (These tests build a `*config.Config` by hand without setting `Defaults.Protocol`, so it defaults to the Go zero value `""`; `zabbixclient.Protocol("")` doesn't match `ProtocolLegacy`/`ProtocolJSON` in `roundTrip`'s switch, so it falls through to the `default` case, which sends a `ProtocolPlain`-style plain request — exactly matching what the httpserver tests' fake agent already expects, since it was written for the old no-protocol behavior. No test changes needed here.)

- [ ] **Step 3: Add explicit protocol overrides to the example config's Solaris hosts**

Read `configs/config.example.yaml` first to find the current `hosts:` section (it currently has a single entry, `srv-old-01: {}`, and does not yet have entries for the Solaris example hosts used in the `groups:` section — if the `groups:` section already references host names like `iu0620cli.phys.pack` from prior manual edits, leave those as-is and only add the two illustrative entries below if they don't already exist under different names).

Add two new entries to the `hosts:` map (do not remove `srv-old-01: {}` or any existing entries):

```yaml
hosts:
  srv-old-01: {}
  legacy-solaris-185:
    protocol: "plain"      # Zabbix agent 1.8.5 — no ZBXD header on requests
  modern-solaris-703:
    port: 10051
    protocol: "json"        # Zabbix agent 7.0.3 — JSON passive-check protocol
```

Add a short comment above `defaults:` noting the new field (find the existing `defaults:` block, currently `port: 10050` / `timeout: 5s`):

```yaml
defaults:
  port: 10050
  timeout: 5s
  # Covers Zabbix 2.0–6.x agents (ZBXD header + bare key, no JSON) — the
  # most common case. Override per host with "plain" (pre-2.0, no header
  # at all) or "json" (7.0+, ZBXD header + JSON request/response).
  protocol: "legacy"
```

- [ ] **Step 4: Validate the example config still loads**

Run this from the project root (creates a throwaway test file, runs it, deletes it — this is a one-off manual check, not a permanent test):

```bash
cat > internal/config/zz_manual_validate_test.go << 'EOF'
package config

import "testing"

func TestManualValidateExampleConfig(t *testing.T) {
	cfg, err := Load("../../configs/config.example.yaml")
	if err != nil {
		t.Fatalf("LOAD ERROR: %v", err)
	}
	t.Logf("Defaults.Protocol = %q", cfg.Defaults.Protocol)
	for name, h := range cfg.Hosts {
		if h.Protocol != nil {
			t.Logf("host %q protocol override = %q", name, *h.Protocol)
		}
	}
}
EOF
go test ./internal/config/... -run TestManualValidateExampleConfig -v
rm internal/config/zz_manual_validate_test.go
```

Expected: PASS, with log output showing `Defaults.Protocol = "legacy"` and the two host overrides (`"plain"`, `"json"`).

- [ ] **Step 5: Run the full repo test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages pass, build and vet clean.

- [ ] **Step 6: Commit**

```bash
git add internal/httpserver/handler.go configs/config.example.yaml
git commit -m "feat: wire resolved protocol into HTTP handler, document it in example config"
```

---

## Self-Review Notes

- **Spec coverage:** wire formats for all three protocol generations (Task 1), client-level `Protocol` type and dispatch (Task 2), config field with cascade and validation (Tasks 3–4), collector plumbing (Task 5), HTTP handler wiring and example config (Task 6). Every section of the design doc has a corresponding task.
- **No URL-level protocol override** was implemented anywhere (Task 4's `Resolve` explicitly omits a `urlProtocol` parameter) — matches the design's explicit "Out of scope" callout.
- **Default change from `plain` to `legacy`** is implemented at the `config.Load` level (Task 3, Step 4) rather than as a Go zero-value default on the `Protocol` type itself, specifically so that hand-built `*config.Config` values in tests (which don't go through `Load`) keep their current zero-value (`""`) behavior — which Task 6 confirms resolves through `zabbixclient.roundTrip`'s `default` case to plain-protocol behavior, matching every pre-existing `httpserver` test's fake agent without needing to touch that test file at all.
- **Type consistency check:** `zabbixclient.Protocol` (Task 2) and `config.Protocol` (Task 3) are deliberately two separate Go types with identical string values and constant names, converted at exactly one call site (`internal/httpserver/handler.go`, Task 6) via `zabbixclient.Protocol(resolved.Protocol)` — confirmed this conversion is valid Go since both are defined as `type X string`. `ResolvedTarget.Protocol` (Task 4) is a `config.Protocol`, matching what `Resolve` cascades from `cfg.Defaults.Protocol`/`host.Protocol` (both `config.Protocol`/`*config.Protocol`), confirmed consistent.
- **Collector's `zabbixQuerier` interface** (Task 5) signature change (`GetItem`/`Discover` gaining a `protocol zabbixclient.Protocol` parameter) is checked against the actual `*zabbixclient.Client` methods defined in Task 2 — both gain the identical new trailing parameter in the same position, so `*zabbixclient.Client` continues to satisfy `zabbixQuerier` structurally with no adapter needed.
