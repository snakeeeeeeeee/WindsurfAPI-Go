package windsurf

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"

	p "github.com/zhangyu/windsurfapi-go/internal/proto"
)

// DefaultClientVersion 对齐 Node 版 DEFAULT_CLIENT_VERSION（可被 env 覆盖）。
func DefaultClientVersion() string {
	if v := os.Getenv("WINDSURF_CLIENT_VERSION"); v != "" {
		return v
	}
	return "2.0.67"
}

func osLabel() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

func archLabel() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x86_64"
}

// BuildMetadata 镜像 windsurf.js 行 94 buildMetadata。
func BuildMetadata(apiKey, sessionID string) []byte {
	version := DefaultClientVersion()
	requestID := rand.Uint64() & ((1 << 48) - 1)
	if sessionID == "" {
		sessionID = NewUUID()
	}
	return p.Concat(
		p.WriteStringField(1, "windsurf"),
		p.WriteStringField(2, version),
		p.WriteStringField(3, apiKey),
		p.WriteStringField(4, "en"),
		p.WriteStringField(5, osLabel()),
		p.WriteStringField(7, version),
		p.WriteStringField(8, archLabel()),
		p.WriteVarintField(9, requestID),
		p.WriteStringField(10, sessionID),
		p.WriteStringField(12, "windsurf"),
	)
}

// NewUUID 返回一个 RFC 4122 v4 格式的 UUID。不依赖 google/uuid。
func NewUUID() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		// 极端情况兜底：用 math/rand 拼一个，仍是 36 位字符串，LS 不会校验
		binary.BigEndian.PutUint64(b[:8], rand.Uint64())
		binary.BigEndian.PutUint64(b[8:], rand.Uint64())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}
