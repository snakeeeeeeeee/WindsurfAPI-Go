// Package proto —— 零依赖、无 schema 的 protobuf wire format 编解码。
//
// 严格镜像 Node 版 WindsurfAPI/src/proto.js（173 行）；两端字节布局 100% 一致，
// 迁移/对拍抓包时可以直接互认。
//
// Wire types:
//
//	0 = Varint    (int32 / uint64 / bool / enum)
//	1 = Fixed64   (double / fixed64)
//	2 = LenDelim  (string / bytes / embedded message)
//	5 = Fixed32   (float / fixed32)
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	WireVarint   = 0
	WireFixed64  = 1
	WireLenDelim = 2
	WireFixed32  = 5
)

// ─── Varint ────────────────────────────────────────────────

// EncodeVarint 把 uint64 编成 LEB128 字节流。负数请先按 two's-complement
// 转 uint64（Go 里直接 `uint64(int64(v))`）再调本函数。
func EncodeVarint(v uint64) []byte {
	buf := make([]byte, 0, 10)
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v == 0 {
			buf = append(buf, b)
			return buf
		}
		buf = append(buf, b|0x80)
	}
}

// DecodeVarint 从 buf[offset:] 读一个 varint，返回值和消耗字节数。
// 遇到截断或 >64bit 溢出会返回 error。
func DecodeVarint(buf []byte, offset int) (value uint64, n int, err error) {
	var shift uint
	pos := offset
	for pos < len(buf) {
		b := buf[pos]
		pos++
		if shift >= 64 {
			return 0, 0, errors.New("varint overflow (>64 bits)")
		}
		value |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return value, pos - offset, nil
		}
		shift += 7
	}
	return 0, 0, errors.New("truncated varint")
}

// ─── Tag 构造 ──────────────────────────────────────────────

func makeTag(fieldNum, wireType int) []byte {
	return EncodeVarint(uint64(fieldNum)<<3 | uint64(wireType&0x07))
}

// ─── 字段级 writer ────────────────────────────────────────

// WriteVarintField 写 wire type 0 的字段。
func WriteVarintField(fieldNum int, value uint64) []byte {
	tag := makeTag(fieldNum, WireVarint)
	body := EncodeVarint(value)
	out := make([]byte, 0, len(tag)+len(body))
	out = append(out, tag...)
	out = append(out, body...)
	return out
}

// WriteBoolField 只在 value=true 时写，对齐 Node 行为（proto3 默认值省略）。
func WriteBoolField(fieldNum int, value bool) []byte {
	if !value {
		return nil
	}
	return WriteVarintField(fieldNum, 1)
}

// WriteStringField 写 wire type 2（UTF-8 字节）。空串对齐 Node：直接返回空切片。
func WriteStringField(fieldNum int, s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return writeLenDelim(fieldNum, []byte(s))
}

// WriteBytesField 写 wire type 2。空 bytes 同样省略。
func WriteBytesField(fieldNum int, data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	return writeLenDelim(fieldNum, data)
}

// WriteMessageField 写嵌套 message（wire type 2）。空 message 省略。
func WriteMessageField(fieldNum int, msg []byte) []byte {
	if len(msg) == 0 {
		return nil
	}
	return writeLenDelim(fieldNum, msg)
}

// WriteFixed64Field 写 wire type 1（必须严格 8 字节）。
func WriteFixed64Field(fieldNum int, buf8 []byte) []byte {
	if len(buf8) != 8 {
		panic(fmt.Sprintf("WriteFixed64Field: expect 8 bytes, got %d", len(buf8)))
	}
	tag := makeTag(fieldNum, WireFixed64)
	out := make([]byte, 0, len(tag)+8)
	out = append(out, tag...)
	out = append(out, buf8...)
	return out
}

// WriteFixed32Field 写 wire type 5（必须严格 4 字节）。
func WriteFixed32Field(fieldNum int, buf4 []byte) []byte {
	if len(buf4) != 4 {
		panic(fmt.Sprintf("WriteFixed32Field: expect 4 bytes, got %d", len(buf4)))
	}
	tag := makeTag(fieldNum, WireFixed32)
	out := make([]byte, 0, len(tag)+4)
	out = append(out, tag...)
	out = append(out, buf4...)
	return out
}

func writeLenDelim(fieldNum int, data []byte) []byte {
	tag := makeTag(fieldNum, WireLenDelim)
	lenBytes := EncodeVarint(uint64(len(data)))
	out := make([]byte, 0, len(tag)+len(lenBytes)+len(data))
	out = append(out, tag...)
	out = append(out, lenBytes...)
	out = append(out, data...)
	return out
}

// Concat 合并多个字段 / 子消息，过滤 nil，减少 caller 样板。
func Concat(parts ...[]byte) []byte {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]byte, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// ─── Parser ────────────────────────────────────────────────

// Field 是 parseFields 的返回值。按 wire type 不同：
//
//	0（varint）：UintValue 有效（Number）。
//	1（fixed64）：BytesValue 长度必为 8。
//	2（len-delim）：BytesValue 指向 buf 的子切片（caller 决定 string / bytes / 子消息）。
//	5（fixed32）：BytesValue 长度必为 4。
type Field struct {
	FieldNum   int
	WireType   int
	UintValue  uint64
	BytesValue []byte
}

// ParseFields 把 buf 按 wire format 拆成 Field 列表。
// 遇到未知 wire type、截断 varint / len-delim 会返回 error。
func ParseFields(buf []byte) ([]Field, error) {
	var out []Field
	pos := 0
	for pos < len(buf) {
		tag, tagLen, err := DecodeVarint(buf, pos)
		if err != nil {
			return nil, fmt.Errorf("tag at %d: %w", pos, err)
		}
		pos += tagLen
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x07)

		f := Field{FieldNum: fieldNum, WireType: wireType}
		switch wireType {
		case WireVarint:
			v, n, err := DecodeVarint(buf, pos)
			if err != nil {
				return nil, fmt.Errorf("varint field %d at %d: %w", fieldNum, pos, err)
			}
			f.UintValue = v
			pos += n
		case WireFixed64:
			if pos+8 > len(buf) {
				return nil, fmt.Errorf("truncated fixed64 at %d", pos)
			}
			f.BytesValue = buf[pos : pos+8]
			pos += 8
		case WireLenDelim:
			l, n, err := DecodeVarint(buf, pos)
			if err != nil {
				return nil, fmt.Errorf("len-delim size for field %d at %d: %w", fieldNum, pos, err)
			}
			pos += n
			sz := int(l)
			if sz < 0 || pos+sz > len(buf) {
				return nil, fmt.Errorf("truncated len-delim field %d at %d (need %d, have %d)",
					fieldNum, pos, sz, len(buf)-pos)
			}
			f.BytesValue = buf[pos : pos+sz]
			pos += sz
		case WireFixed32:
			if pos+4 > len(buf) {
				return nil, fmt.Errorf("truncated fixed32 at %d", pos)
			}
			f.BytesValue = buf[pos : pos+4]
			pos += 4
		default:
			return nil, fmt.Errorf("unknown wire type %d at offset %d", wireType, pos)
		}
		out = append(out, f)
	}
	return out, nil
}

// GetField 返回第一个匹配的字段；wireType 传 -1 表示不限制。
func GetField(fields []Field, fieldNum int, wireType int) *Field {
	for i := range fields {
		if fields[i].FieldNum != fieldNum {
			continue
		}
		if wireType >= 0 && fields[i].WireType != wireType {
			continue
		}
		return &fields[i]
	}
	return nil
}

// GetAllFields 返回全部匹配字段（repeated 字段用）。
func GetAllFields(fields []Field, fieldNum int) []Field {
	var out []Field
	for i := range fields {
		if fields[i].FieldNum == fieldNum {
			out = append(out, fields[i])
		}
	}
	return out
}

// ─── 便捷读取 helper ───────────────────────────────────────

// String 假定字段是 len-delim 且内容是 UTF-8。
func (f *Field) String() string {
	if f == nil {
		return ""
	}
	return string(f.BytesValue)
}

// Uint 返回 varint 值，非 varint 字段返回 0。
func (f *Field) Uint() uint64 {
	if f == nil || f.WireType != WireVarint {
		return 0
	}
	return f.UintValue
}

// Bool 返回 varint 是否非零；非 varint 字段返回 false。
func (f *Field) Bool() bool {
	return f.Uint() != 0
}

// Bytes 返回原始字节切片（len-delim / fixed 字段都行）。
func (f *Field) Bytes() []byte {
	if f == nil {
		return nil
	}
	return f.BytesValue
}

// Fixed64 用小端解释 8 字节（对齐 protobuf 规范）。
func (f *Field) Fixed64() uint64 {
	if f == nil || f.WireType != WireFixed64 || len(f.BytesValue) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(f.BytesValue)
}

// Fixed32 用小端解释 4 字节。
func (f *Field) Fixed32() uint32 {
	if f == nil || f.WireType != WireFixed32 || len(f.BytesValue) != 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(f.BytesValue)
}
