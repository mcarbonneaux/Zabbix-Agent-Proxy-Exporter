package zabbixproto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	magic         = "ZBXD"
	headerLen     = 13 // 4 (magic) + 1 (flags) + 4 (datalen) + 4 (reserved)
	flagResponse  = 0x01
	maxPayloadLen = 128 * 1024 * 1024 // matches Zabbix's own default frame size cap
)

// WriteFrame writes payload wrapped in the Zabbix ZBXD header to w.
func WriteFrame(w io.Writer, payload []byte) error {
	header := make([]byte, headerLen)
	copy(header[0:4], magic)
	header[4] = flagResponse
	binary.LittleEndian.PutUint32(header[5:9], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[9:13], 0)

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("zabbixproto: write header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("zabbixproto: write payload: %w", err)
	}
	return nil
}

// WritePlainRequest writes key as a bare newline-terminated request,
// for legacy agents that do not expect a ZBXD header on requests.
func WritePlainRequest(w io.Writer, key string) error {
	_, err := io.WriteString(w, key+"\n")
	if err != nil {
		return fmt.Errorf("zabbixproto: write plain request: %w", err)
	}
	return nil
}

// ReadFrame reads a ZBXD-framed payload from r.
func ReadFrame(r io.Reader) ([]byte, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("zabbixproto: read header: %w", err)
	}
	if string(header[0:4]) != magic {
		return nil, fmt.Errorf("zabbixproto: bad magic %q, want %q", header[0:4], magic)
	}
	dataLen := binary.LittleEndian.Uint32(header[5:9])
	if dataLen > maxPayloadLen {
		return nil, fmt.Errorf("zabbixproto: declared payload length %d exceeds max %d", dataLen, maxPayloadLen)
	}

	payload := make([]byte, dataLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("zabbixproto: read payload: %w", err)
	}
	return payload, nil
}

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
// 7.0+ passive-check JSON response payload, as read via ReadFrame. This
// exporter always requests exactly one item per request, so only the
// first element of the response's data array is examined; any further
// elements are ignored.
func ParseJSONResponse(payload []byte) (string, error) {
	var resp jsonResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return "", fmt.Errorf("zabbixproto: parse json response: %w", err)
	}
	if len(resp.Data) == 0 {
		return "", fmt.Errorf("zabbixproto: json response data array is empty")
	}
	// Only data[0] is examined, per the single-item-per-request design
	// documented above.
	item := resp.Data[0]
	if item.Error != "" {
		return "", fmt.Errorf("zabbixproto: item not supported: %s", item.Error)
	}
	return item.Value, nil
}
