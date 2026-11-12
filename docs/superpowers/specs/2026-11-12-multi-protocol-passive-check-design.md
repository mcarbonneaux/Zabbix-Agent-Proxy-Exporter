# Multi-protocol Zabbix passive check support — Design

Date: 2026-11-12
Status: Approved for planning

## Context and problem

The exporter currently speaks exactly one dialect of the Zabbix passive-check
wire protocol: a bare key followed by `\n`, with no `ZBXD` header on the
request (`zabbixproto.WritePlainRequest`). This matches very old Zabbix
agents (pre-2.0, e.g. the 1.8.5 agent used by the `solaris` example module).

Real-world testing against a Zabbix agent 7.0.3 (prebuilt for Solaris
10/SPARC) failed on every single item with the agent logging:

```
Message from <exporter-ip> is missing header. Message ignored.
```

Investigation (confirmed via Zabbix's own protocol documentation) found that
the passive-check *request* format has evolved across three distinct
generations that are all still found in the wild on a legacy fleet:

1. **Pre-2.0 ("plain")**: no header at all, just `<key>\n` as the raw TCP
   payload. This is what the exporter currently implements.
2. **2.0–6.x ("legacy")**: `ZBXD` header (same framing as today) wrapping a
   payload that is just the bare item key (no trailing newline, no JSON).
3. **7.0+ ("json")**: `ZBXD` header wrapping a JSON payload:
   `{"request":"passive checks","data":[{"key":"<key>","timeout":<N>}]}`,
   with a JSON response `{"version":"...","data":[{"value":"..."}]}` or
   `{"data":[{"error":"..."}]}` on failure.

The `ZBXD` header itself did not need to change: the flags byte's `0x04`
("large packet") bit controls whether `datalen` is 4 or 8 bytes, but that
bit is never set for payloads under 4 GB (true for every item this exporter
queries), so the existing 13-byte header format
(`"ZBXD"` + 1 flags byte + 4-byte `datalen` + 4-byte reserved) is correctly
read/written for all three generations. Only the *payload* format differs.

## Goal

Let the operator declare, per host (with a global default), which of the
three request/response dialects to use when talking to that host's Zabbix
agent — since the exporter has no reliable way to auto-detect this from a
single failed round trip, and a config fleet realistically spans all three
generations simultaneously (1.8.x, 2.0–6.x, and 7.0+ agents side by side).

## Configuration

A new `protocol` setting, following the exact same cascade pattern already
used for `port`/`timeout`:

```yaml
defaults:
  port: 10050
  timeout: 5s
  protocol: "legacy"   # new; one of "plain", "legacy", "json"

hosts:
  old-solaris-185: {}                 # no override -> uses defaults.protocol ("legacy")
  modern-solaris-703:
    port: 10051
    protocol: "json"                   # explicit override for this host
  very-old-aix:
    protocol: "plain"                  # explicit override for pre-2.0 agents
```

- `Defaults.Protocol Protocol` (non-pointer, always has a value — defaults
  to `"legacy"` if omitted from the YAML entirely, since `legacy` covers the
  broadest practical range of real Zabbix versions, 2.0 through 6.x).
- `HostConfig.Protocol *Protocol` (pointer, like `Port`/`Timeout`, so "not
  set" is distinguishable from an explicit value) overrides `Defaults` for
  that host only.
- No URL query-parameter override for protocol (not needed — this is a
  property of the agent/host, not something a scrape would reasonably want
  to override per-request, unlike port/timeout which can be forced ad hoc
  for a one-off diagnostic scrape).
- `validate()` rejects any `protocol` value other than `"plain"`, `"legacy"`,
  or `"json"`, consistent with how `item.Type` is already validated.
- `ResolvedTarget` (the output of `config.Resolve`) gains a `Protocol
  Protocol` field, resolved via the same two-level cascade (`Defaults` →
  `HostConfig` override) as port/timeout, minus the URL-override step.

This changes the exporter's *default* behavior from today's implicit
"plain" to "legacy" — a deliberate breaking change, justified because
"legacy" (2.0–6.x) covers a much wider range of real deployments than
"plain" (pre-2.0 only), and any fleet still running pre-2.0 agents needs an
explicit `protocol: "plain"` override already since those are the rare case
going forward.

## Wire format changes (`internal/zabbixproto`)

The existing `WriteFrame`/`ReadFrame` (the `ZBXD` header codec) are
unchanged and reused by both new write paths. Two new functions are added
alongside the existing `WritePlainRequest`:

```go
// WriteLegacyRequest writes key as a ZBXD-framed payload containing just
// the bare key (no trailing newline, no JSON) — the Zabbix 2.0–6.x
// passive-check request format.
func WriteLegacyRequest(w io.Writer, key string) error {
	return WriteFrame(w, []byte(key))
}

// WriteJSONRequest writes a ZBXD-framed JSON "passive checks" request for
// key, with the given timeout (in seconds, minimum 1) — the Zabbix 7.0+
// passive-check request format.
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

(`jsonRequest`, `jsonRequestItem`, `jsonResponse`, `jsonResponseItem` are
unexported structs with the obvious `json:"..."` tags matching the shapes
shown in the Context section above.)

## Client changes (`internal/zabbixclient`)

To avoid an import cycle (`config` must not depend on `zabbixclient`, and
`zabbixclient` already doesn't depend on `config`), a small `Protocol`
string type is defined locally in `zabbixclient` (mirroring
`config.Protocol`'s three values) and `collector` converts
`config.Protocol` → `zabbixclient.Protocol` at the one call site where it
builds the client call. `GetItem`/`Discover` both gain a `protocol
Protocol` parameter; `roundTrip` branches on it to choose the write
function (`WritePlainRequest` / `WriteLegacyRequest` / `WriteJSONRequest`)
and, for the `json` case only, additionally runs the response payload
through `ParseJSONResponse` before applying the existing
`checkNotSupported` logic (which stays unchanged and still applies to the
`plain`/`legacy` paths' raw-text `ZBX_NOTSUPPORTED` responses).

## Collector changes (`internal/collector`)

Purely a plumbing change: `Collect` already receives a `*config.Config` and
builds `addr` from `ResolvedTarget`; it additionally reads
`resolved.Protocol`, converts it to the local `zabbixclient.Protocol`, and
passes it to every `GetItem`/`Discover` call it makes (both for simple
items, discovered items, and line-list items — all three call paths use
the same protocol for a given host, since it's a property of the agent,
not the item). No change to error handling, success/failure semantics, or
meta-metrics — a protocol mismatch simply surfaces as a per-item failure
through the exact same `itemErrors`/`zabbix_scrape_success` machinery
already in place (e.g. if `json` is configured but the agent only speaks
`legacy`, every item fails with a JSON-parse error, which is diagnosable
from the exporter's own logs exactly like today's "missing header" case
was diagnosed from the *agent's* logs).

## Testing

- `internal/zabbixproto`: round-trip tests for `WriteLegacyRequest` (verify
  header + bare key, no trailing newline), `WriteJSONRequest` (verify exact
  JSON payload for a given key/timeout, including the minimum-1-second
  floor), and `ParseJSONResponse` (success value extraction, data-array
  error extraction with "not supported" in the message, empty-data-array
  error, malformed-JSON error).
- `internal/zabbixclient`: existing tests updated to pass an explicit
  `Protocol` (using `ProtocolPlain` where they test today's behavior, to
  avoid silently changing what they cover); new tests for `legacy` (fake
  agent asserts it received a ZBXD-framed bare key) and `json` (fake agent
  asserts it received the exact JSON request shape, responds with a JSON
  value and separately with a JSON error, `GetItem` extracts correctly in
  both cases).
- `internal/config`: new tests for parsing `protocol` in `defaults`/`hosts`,
  rejecting an invalid `protocol` value at load time, and the two-level
  resolution cascade (`Defaults.Protocol` → `HostConfig.Protocol` override,
  no URL step).
- `internal/collector`: existing fixtures updated to set an explicit
  protocol (`ProtocolLegacy`, matching the new default) so test behavior
  doesn't silently drift when the default changes.

## Out of scope

- No automatic protocol detection/fallback (the user explicitly chose
  static per-host configuration over this).
- No URL query-parameter override for protocol.
- No change to the `ZBXD` header codec itself (confirmed compatible as-is
  across all three generations for payloads under 4 GB, which covers every
  realistic item response this exporter will ever see).
- `docs/zabbix_agentd.solaris.conf.example` / `config.example.yaml`'s
  existing `solaris` module (1.8.5) and `solaris_modern` module (7.0.3)
  will get an explicit `protocol: "plain"` / `protocol: "json"` override
  respectively on their example hosts as part of implementation, so the
  example config keeps demonstrating a working setup for both — this is a
  docs follow-up, not a new design concern.
