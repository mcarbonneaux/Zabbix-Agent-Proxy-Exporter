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

// Client queries Zabbix passive agents over TCP, one item per connection.
type Client struct {
	timeout time.Duration
}

// New returns a Client that uses timeout for both dialing and reading.
func New(timeout time.Duration) *Client {
	return &Client{timeout: timeout}
}

func (c *Client) roundTrip(ctx context.Context, addr, key string) ([]byte, error) {
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

	if err := zabbixproto.WritePlainRequest(conn, key); err != nil {
		return nil, fmt.Errorf("zabbixclient: send request %q to %s: %w", key, addr, err)
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

// GetItem requests key from the agent at addr and returns its raw string value.
func (c *Client) GetItem(ctx context.Context, addr, key string) (string, error) {
	payload, err := c.roundTrip(ctx, addr, key)
	if err != nil {
		return "", err
	}
	if err := checkNotSupported(key, addr, payload); err != nil {
		return "", err
	}
	return string(payload), nil
}

// Discover requests a discovery key (e.g. vfs.fs.discovery) and returns
// the list of discovered entities as maps of macro name to value.
func (c *Client) Discover(ctx context.Context, addr, discoveryKey string) ([]map[string]string, error) {
	payload, err := c.roundTrip(ctx, addr, discoveryKey)
	if err != nil {
		return nil, err
	}
	if err := checkNotSupported(discoveryKey, addr, payload); err != nil {
		return nil, err
	}

	var entities []map[string]string
	if err := json.Unmarshal(payload, &entities); err != nil {
		return nil, fmt.Errorf("zabbixclient: parse discovery JSON for %q from %s: %w", discoveryKey, addr, err)
	}
	return entities, nil
}
