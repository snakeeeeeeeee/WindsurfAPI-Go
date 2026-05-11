// Package grpc 实现 gRPC + Connect-RPC 两种线缆格式和对应 HTTP/2 传输。
// 对齐 WindsurfAPI/src/grpc.js（350 行）和 connect.js（157 行）。
package grpc

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxFrameSize 限制单个 gRPC 帧大小，防止错误长度导致 OOM。Node 版一致。
const MaxFrameSize = 100 * 1024 * 1024

// GRPCFrame 包装 payload 为 gRPC 线格式：
//
//	[1 byte compressed flag (=0)][4 bytes big-endian length][payload]
func GRPCFrame(payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = 0
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

// ExtractGRPCFrames 从可能拼接的多帧 buffer 中提取全部已完整接收的 frame。
// 返回：
//   - frames: 各 frame 的 payload（compressed=0 时共享底层 buffer；compressed=1
//     时为 gzip 解压后的新切片）
//   - consumed: 已消费字节数（caller 用来截 pending buffer）
//   - err: 遇到超大 length / 解压失败时返回
//
// 注意 LS 在响应较大时会启用 gzip（compressed flag=1），实测 GetUserStatus 真账
// 号回包 8KB+ 时即触发，必须在这里就地解压；早期把它当 "LS 从不压缩" 跳过会让
// 上层 fallback 到含 gRPC header 的原始字节，然后报 protobuf wire type 7。
func ExtractGRPCFrames(buf []byte) (frames [][]byte, consumed int, err error) {
	offset := 0
	for offset+5 <= len(buf) {
		compressed := buf[offset]
		msgLen := binary.BigEndian.Uint32(buf[offset+1 : offset+5])
		if msgLen > MaxFrameSize {
			return frames, consumed, errors.New("gRPC frame too large")
		}
		if offset+5+int(msgLen) > len(buf) {
			break
		}
		payload := buf[offset+5 : offset+5+int(msgLen)]
		switch compressed {
		case 0:
			frames = append(frames, payload)
		case 1:
			decoded, derr := gunzip(payload)
			if derr != nil {
				return frames, consumed, fmt.Errorf("gRPC frame gunzip: %w", derr)
			}
			frames = append(frames, decoded)
		default:
			return frames, consumed, fmt.Errorf("gRPC frame: unsupported compressed flag %d", compressed)
		}
		offset += 5 + int(msgLen)
		consumed = offset
	}
	return frames, consumed, nil
}

// StripGRPCFrame 去掉 5 字节头返回 payload；如果是 compressed=1 帧则就地 gunzip。
// 帧不完整 / 解压失败时返回原始 buf（caller 会再用 ExtractGRPCFrames 重试）。
func StripGRPCFrame(buf []byte) []byte {
	if len(buf) < 5 {
		return buf
	}
	compressed := buf[0]
	msgLen := binary.BigEndian.Uint32(buf[1:5])
	if len(buf) < 5+int(msgLen) {
		return buf
	}
	payload := buf[5 : 5+int(msgLen)]
	switch compressed {
	case 0:
		return payload
	case 1:
		if decoded, err := gunzip(payload); err == nil {
			return decoded
		}
	}
	return buf
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
