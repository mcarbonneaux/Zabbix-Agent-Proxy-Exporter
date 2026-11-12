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
	case ProtocolPlain:
		writeErr = zabbixproto.WritePlainRequest(conn, key)
	case ProtocolLegacy:
		writeErr = zabbixproto.WriteLegacyRequest(conn, key)
	case ProtocolJSON:
		writeErr = zabbixproto.WriteJSONRequest(conn, key, int(c.timeout.Seconds()))
	default:
		return nil, fmt.Errorf("zabbixclient: unknown protocol %q", protocol)
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
			if strings.Contains(err.Error(), "not supported") {
				return "", fmt.Errorf("zabbixclient: item %q not supported by %s: %w", key, addr, err)
			}
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
			if strings.Contains(err.Error(), "not supported") {
				return nil, fmt.Errorf("zabbixclient: item %q not supported by %s: %w", discoveryKey, addr, err)
			}
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
