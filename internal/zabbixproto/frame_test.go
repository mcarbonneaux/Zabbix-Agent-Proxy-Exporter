package zabbixproto

import (
	"bytes"
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

func TestWritePlainRequestHasNoHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePlainRequest(&buf, "agent.ping"); err != nil {
		t.Fatalf("WritePlainRequest returned error: %v", err)
	}
	if got := buf.String(); got != "agent.ping\n" {
		t.Fatalf("got %q, want %q", got, "agent.ping\n")
	}
}
