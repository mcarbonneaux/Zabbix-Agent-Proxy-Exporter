package zabbixproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriteFrameThenReadFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("3.0005\n")

	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame returned error: %v", err)
	}

	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got payload %q, want %q", got, payload)
	}
}

func TestReadFrameRejectsBadMagic(t *testing.T) {
	buf := bytes.NewBufferString("XXXX\x01\x05\x00\x00\x00\x00\x00\x00\x00hello")

	_, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
	if !strings.Contains(err.Error(), "magic") {
		t.Fatalf("expected error mentioning magic, got: %v", err)
	}
}

func TestReadFrameHandlesTruncatedHeader(t *testing.T) {
	buf := bytes.NewBufferString("ZBX")

	_, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error for truncated header, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF-related error, got: %v", err)
	}
}

func TestReadFrameRejectsOversizedDeclaredLength(t *testing.T) {
	header := make([]byte, headerLen)
	copy(header[0:4], "ZBXD")
	header[4] = 0x01
	binary.LittleEndian.PutUint32(header[5:9], maxPayloadLen+1)
	// No payload bytes follow — ReadFrame must reject based on the
	// declared length alone, before attempting to read any payload.
	buf := bytes.NewBuffer(header)

	_, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error for oversized declared length, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds max") {
		t.Fatalf("expected error mentioning 'exceeds max', got: %v", err)
	}
}

func TestWriteFrameThenReadFrameRoundTripEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, []byte{}); err != nil {
		t.Fatalf("WriteFrame returned error: %v", err)
	}

	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got payload %q, want empty", got)
	}
}

func TestWritePlainRequestHasNoHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePlainRequest(&buf, "agent.ping"); err != nil {
		t.Fatalf("WritePlainRequest returned error: %v", err)
	}
	if got := buf.String(); got != "agent.ping\n" {
		t.Fatalf("got %q, want %q", got, "agent.ping\n")
	}
}

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
