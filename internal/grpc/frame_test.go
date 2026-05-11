package grpc

import (
	"bytes"
	"testing"
)

func TestGRPCFrameRoundTrip(t *testing.T) {
	payload := []byte("hello world")
	framed := GRPCFrame(payload)
	if len(framed) != 5+len(payload) {
		t.Fatalf("expected frame length %d, got %d", 5+len(payload), len(framed))
	}
	if framed[0] != 0 {
		t.Fatalf("compressed flag must be 0")
	}
	frames, consumed, err := ExtractGRPCFrames(framed)
	if err != nil {
		t.Fatalf("extract err: %v", err)
	}
	if consumed != len(framed) {
		t.Fatalf("consumed %d, want %d", consumed, len(framed))
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], payload) {
		t.Fatalf("frame mismatch: %+v", frames)
	}
}

func TestGRPCMultipleFrames(t *testing.T) {
	a := GRPCFrame([]byte("aaa"))
	b := GRPCFrame([]byte("bbbb"))
	cat := append(a, b...)
	frames, consumed, err := ExtractGRPCFrames(cat)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if consumed != len(cat) || len(frames) != 2 {
		t.Fatalf("unexpected consumed=%d frames=%d", consumed, len(frames))
	}
	if !bytes.Equal(frames[0], []byte("aaa")) || !bytes.Equal(frames[1], []byte("bbbb")) {
		t.Fatalf("frame content mismatch: %q %q", frames[0], frames[1])
	}
}

func TestGRPCPartialFrame(t *testing.T) {
	framed := GRPCFrame([]byte("hello"))
	// 少一字节
	frames, consumed, err := ExtractGRPCFrames(framed[:len(framed)-1])
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if consumed != 0 || len(frames) != 0 {
		t.Fatalf("expect no frames, got consumed=%d frames=%d", consumed, len(frames))
	}
}

func TestExtractGRPCFrameTooLargeWithoutCompleteFrame(t *testing.T) {
	buf := []byte{0, 255, 255, 255, 255, 'o', 'k'}
	frames, consumed, err := ExtractGRPCFrames(buf)
	if err == nil {
		t.Fatal("expected oversized frame error")
	}
	if consumed != 0 || len(frames) != 0 {
		t.Fatalf("unexpected consumed=%d frames=%d", consumed, len(frames))
	}
	if !bytes.Equal(StripGRPCFrame(buf), buf) {
		t.Fatal("StripGRPCFrame should pass through malformed/incomplete body")
	}
}

func TestGRPCStripFrame(t *testing.T) {
	p := GRPCFrame([]byte("xyz"))
	if !bytes.Equal(StripGRPCFrame(p), []byte("xyz")) {
		t.Fatalf("strip mismatch")
	}
	// 不完整帧：原样返回
	short := []byte{0, 0, 0, 0, 5, 'a', 'b'}
	if !bytes.Equal(StripGRPCFrame(short), short) {
		t.Fatalf("expected passthrough on incomplete frame")
	}
}
