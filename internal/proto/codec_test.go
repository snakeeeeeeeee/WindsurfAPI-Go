package proto

import (
	"bytes"
	"testing"
)

func TestVarintRoundTrip(t *testing.T) {
	cases := []uint64{
		0, 1, 127, 128, 255, 16383, 16384,
		1<<21 - 1, 1 << 21,
		1<<28 - 1, 1 << 28,
		1<<35 - 1, 1 << 35,
		1<<42 - 1, 1 << 42,
		1<<49 - 1, 1 << 49,
		1<<56 - 1, 1 << 56,
		1<<63 - 1, 1 << 63, ^uint64(0),
	}
	for _, v := range cases {
		enc := EncodeVarint(v)
		dec, n, err := DecodeVarint(enc, 0)
		if err != nil {
			t.Fatalf("decode %d: %v", v, err)
		}
		if dec != v {
			t.Fatalf("round-trip mismatch: in=%d out=%d", v, dec)
		}
		if n != len(enc) {
			t.Fatalf("consumed %d but encoded %d", n, len(enc))
		}
	}
}

func TestDecodeVarintTruncated(t *testing.T) {
	// 0x80 = 继续位置位，但后面没字节
	if _, _, err := DecodeVarint([]byte{0x80}, 0); err == nil {
		t.Fatal("expect error on truncated varint")
	}
}

func TestWriteStringField(t *testing.T) {
	// field=1, wire=2, len=5, "hello"
	// tag = (1<<3)|2 = 0x0a
	expected := []byte{0x0a, 0x05, 'h', 'e', 'l', 'l', 'o'}
	got := WriteStringField(1, "hello")
	if !bytes.Equal(got, expected) {
		t.Fatalf("expected %x, got %x", expected, got)
	}
}

func TestWriteStringFieldEmpty(t *testing.T) {
	if got := WriteStringField(3, ""); len(got) != 0 {
		t.Fatalf("expect empty output for empty string, got %x", got)
	}
}

func TestWriteBoolField(t *testing.T) {
	// field=2, wire=0, value=1 → 0x10 0x01
	if got := WriteBoolField(2, true); !bytes.Equal(got, []byte{0x10, 0x01}) {
		t.Fatalf("unexpected: %x", got)
	}
	if got := WriteBoolField(2, false); len(got) != 0 {
		t.Fatalf("false should be omitted, got %x", got)
	}
}

func TestWriteVarintFieldLarge(t *testing.T) {
	// field=7, value=300 → tag=(7<<3)|0=0x38, body=0xac,0x02
	if got := WriteVarintField(7, 300); !bytes.Equal(got, []byte{0x38, 0xac, 0x02}) {
		t.Fatalf("unexpected: %x", got)
	}
}

func TestParseFieldsMixed(t *testing.T) {
	// 构造：field 1 string "hi" + field 2 varint 300 + field 3 bool true
	buf := Concat(
		WriteStringField(1, "hi"),
		WriteVarintField(2, 300),
		WriteBoolField(3, true),
	)
	fields, err := ParseFields(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("want 3 fields, got %d", len(fields))
	}
	if got := GetField(fields, 1, WireLenDelim); got == nil || got.String() != "hi" {
		t.Fatalf("field 1 mismatch: %+v", got)
	}
	if got := GetField(fields, 2, WireVarint); got == nil || got.Uint() != 300 {
		t.Fatalf("field 2 mismatch: %+v", got)
	}
	if got := GetField(fields, 3, WireVarint); got == nil || !got.Bool() {
		t.Fatalf("field 3 mismatch: %+v", got)
	}
}

func TestParseFieldsTruncated(t *testing.T) {
	// tag 存在，但 varint body 被截断
	if _, err := ParseFields([]byte{0x08}); err == nil {
		t.Fatal("expect error on truncated varint body")
	}
	// len-delim 声明 10 字节但只给 3
	if _, err := ParseFields([]byte{0x0a, 0x0a, 'x', 'y', 'z'}); err == nil {
		t.Fatal("expect error on truncated len-delim")
	}
}

func TestGetAllFields(t *testing.T) {
	// 两次同 field number（repeated）
	buf := Concat(
		WriteStringField(5, "a"),
		WriteStringField(5, "b"),
		WriteVarintField(6, 1),
	)
	fields, err := ParseFields(buf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	all := GetAllFields(fields, 5)
	if len(all) != 2 || all[0].String() != "a" || all[1].String() != "b" {
		t.Fatalf("unexpected repeated: %+v", all)
	}
}

func TestWriteFixed64(t *testing.T) {
	buf8 := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	out := WriteFixed64Field(9, buf8)
	// tag = (9<<3)|1 = 0x49
	expected := append([]byte{0x49}, buf8...)
	if !bytes.Equal(out, expected) {
		t.Fatalf("fixed64 mismatch: %x vs %x", out, expected)
	}
	fields, err := ParseFields(out)
	if err != nil || len(fields) != 1 || fields[0].Fixed64() != 0x0807060504030201 {
		t.Fatalf("parse fixed64: %+v err=%v", fields, err)
	}
}
