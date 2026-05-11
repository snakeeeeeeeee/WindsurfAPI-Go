package grpc

import (
	"bytes"
	"testing"
)

func TestConnectWrapRoundTripCompressed(t *testing.T) {
	payload := bytes.Repeat([]byte("connect test payload "), 20) // gzip 才有意义
	framed, err := ConnectWrap(payload, true)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if framed[0]&connectFlagCompressed == 0 {
		t.Fatalf("expect compressed flag set")
	}
	parser := &ConnectStreamParser{}
	parser.Push(framed)
	frames, err := parser.Drain()
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(frames) != 1 || frames[0].IsEndStream {
		t.Fatalf("unexpected frames: %+v", frames)
	}
	if !bytes.Equal(frames[0].Payload, payload) {
		t.Fatalf("payload mismatch")
	}
}

func TestConnectWrapUncompressed(t *testing.T) {
	payload := []byte("short")
	framed, err := ConnectWrap(payload, false)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if framed[0]&connectFlagCompressed != 0 {
		t.Fatalf("expected uncompressed flag")
	}
	parser := &ConnectStreamParser{}
	parser.Push(framed)
	frames, err := parser.Drain()
	if err != nil || len(frames) != 1 || !bytes.Equal(frames[0].Payload, payload) {
		t.Fatalf("round-trip failed: %+v err=%v", frames, err)
	}
}

func TestConnectEndOfStreamFrame(t *testing.T) {
	eos := ConnectEndOfStream()
	parser := &ConnectStreamParser{}
	parser.Push(eos)
	frames, err := parser.Drain()
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(frames) != 1 || !frames[0].IsEndStream {
		t.Fatalf("unexpected: %+v", frames)
	}
}

func TestConnectStreamingByteByByte(t *testing.T) {
	payload := []byte("hello-chunked-parse")
	frame1, _ := ConnectWrap(payload, false)
	frame2, _ := ConnectWrap([]byte("second"), false)
	all := append(append([]byte{}, frame1...), frame2...)

	parser := &ConnectStreamParser{}
	var collected []ConnectFrame
	for _, b := range all {
		parser.Push([]byte{b})
		frames, err := parser.Drain()
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		collected = append(collected, frames...)
	}
	if len(collected) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(collected))
	}
	if !bytes.Equal(collected[0].Payload, payload) || !bytes.Equal(collected[1].Payload, []byte("second")) {
		t.Fatalf("payload mismatch: %+v", collected)
	}
}

func TestParseConnectTrailerError(t *testing.T) {
	msg, err := ParseConnectTrailerError([]byte(`{"error":{"code":"internal","message":"bad stuff"}}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if msg != "bad stuff" {
		t.Fatalf("expected 'bad stuff', got %q", msg)
	}
	if m, _ := ParseConnectTrailerError([]byte("{}")); m != "" {
		t.Fatalf("expected empty msg for no error, got %q", m)
	}
	if m, _ := ParseConnectTrailerError([]byte("garbage")); m != "" {
		t.Fatalf("garbage should swallow: got %q", m)
	}
}
