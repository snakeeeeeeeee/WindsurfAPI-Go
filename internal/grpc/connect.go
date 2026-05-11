package grpc

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	connectFlagCompressed = 0x01
	connectFlagEndStream  = 0x02
	// MaxConnectFrame 镜像 connect.js MAX_FRAME_SIZE。
	MaxConnectFrame = 16 * 1024 * 1024
)

// ConnectWrap 包装 payload 为 Connect envelope：
//
//	[flags u8][len u32be][maybe-gzipped payload]
//
// compress=true 且 payload 非空时会 gzip + 置位 0x01。
func ConnectWrap(payload []byte, compress bool) ([]byte, error) {
	flags := byte(0)
	body := payload
	if compress && len(payload) > 0 {
		compressed, err := gzipBytes(payload)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		body = compressed
		flags |= connectFlagCompressed
	}
	out := make([]byte, 5+len(body))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(body)))
	copy(out[5:], body)
	return out, nil
}

// ConnectEndOfStream 造一个 JSON `{}` 的 end-of-stream trailer 帧。
func ConnectEndOfStream() []byte {
	trailer := []byte("{}")
	out := make([]byte, 5+len(trailer))
	out[0] = connectFlagEndStream
	binary.BigEndian.PutUint32(out[1:5], uint32(len(trailer)))
	copy(out[5:], trailer)
	return out
}

// ConnectFrame 表示一条已解出的 envelope 帧。
type ConnectFrame struct {
	Flags       byte
	IsEndStream bool
	Payload     []byte // 已解压
}

// ConnectStreamParser 累积字节、逐帧产出。跨 HTTP/2 DATA 边界安全。
type ConnectStreamParser struct {
	buf []byte
}

// Push 追加新到达的字节。
func (p *ConnectStreamParser) Push(b []byte) {
	p.buf = append(p.buf, b...)
}

// Drain 返回当前已完整接收的全部帧，并从内部 buffer 里弹走。
func (p *ConnectStreamParser) Drain() ([]ConnectFrame, error) {
	var out []ConnectFrame
	for len(p.buf) >= 5 {
		ln := binary.BigEndian.Uint32(p.buf[1:5])
		if ln > MaxConnectFrame {
			return nil, fmt.Errorf("connect frame size %d exceeds %d", ln, MaxConnectFrame)
		}
		need := 5 + int(ln)
		if len(p.buf) < need {
			break
		}
		flags := p.buf[0]
		payload := p.buf[5:need]
		if flags&connectFlagCompressed != 0 {
			decoded, err := gunzipBytes(payload)
			if err != nil {
				return nil, fmt.Errorf("connect frame decompression: %w", err)
			}
			payload = decoded
		} else {
			// 拷贝出来，因为后面会把 p.buf 丢掉
			cp := make([]byte, len(payload))
			copy(cp, payload)
			payload = cp
		}
		out = append(out, ConnectFrame{
			Flags:       flags,
			IsEndStream: flags&connectFlagEndStream != 0,
			Payload:     payload,
		})
		p.buf = p.buf[need:]
	}
	return out, nil
}

// ParseConnectTrailerError 解析结束帧 JSON 的 error.message。
// 返回 (msg, nil)：msg 为空即无错。JSON 解析失败时吞错返回空串（对齐 Node try/catch）。
func ParseConnectTrailerError(payload []byte) (string, error) {
	var t struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &t); err != nil {
		return "", nil
	}
	if t.Error != nil {
		return t.Error.Message, nil
	}
	return "", nil
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(b); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(b []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}
