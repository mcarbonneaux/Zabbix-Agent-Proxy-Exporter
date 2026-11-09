package zabbixproto

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	magic        = "ZBXD"
	headerLen    = 13 // 4 (magic) + 1 (flags) + 4 (datalen) + 4 (reserved)
	flagResponse = 0x01
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

	payload := make([]byte, dataLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("zabbixproto: read payload: %w", err)
	}
	return payload, nil
}
