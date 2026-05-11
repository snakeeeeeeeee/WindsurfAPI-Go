package grpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/net/http2"
)

// Protocol 选择线缆格式。
type Protocol int

const (
	ProtoGRPC Protocol = iota
	ProtoConnect
)

// UseConnect 通过环境变量切协议（对齐 Node 的 GRPC_PROTOCOL=connect）。
func UseConnect() bool {
	return os.Getenv("GRPC_PROTOCOL") == "connect"
}

// DefaultProtocol 返回当前运行期协议（启动时由环境变量决定）。
func DefaultProtocol() Protocol {
	if UseConnect() {
		return ProtoConnect
	}
	return ProtoGRPC
}

// Client 是一组 HTTP/2 h2c 调用的入口。http2.Transport 内部已按 host:port
// 池化 ClientConn，这里只需一个实例就能服务所有 LS 端口。
type Client struct {
	transport *http2.Transport
	http      *http.Client
}

// NewClient 造一个 h2c 客户端。适用于所有本地 LS（明文 HTTP/2）。
func NewClient() *Client {
	t := &http2.Transport{
		AllowHTTP: true,
		// AllowHTTP=true 时 http2.Transport 对 http:// URL 也会调用此 dial，
		// 但不会做 TLS 握手。直接返回明文 TCP conn 即可。
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	return &Client{
		transport: t,
		http:      &http.Client{Transport: t},
	}
}

// CloseIdleConnections 关掉 transport 持有的所有空闲 HTTP/2 连接。
// 对齐 Node grpc.js 的 closeSessionForPort —— LS 重启后下一次调用会重连。
func (c *Client) CloseIdleConnections() {
	c.transport.CloseIdleConnections()
}

// UnaryOpts 控制一次 unary 调用。
type UnaryOpts struct {
	Protocol  Protocol
	CSRFToken string
	Timeout   time.Duration
}

// Unary 发一次单请求单响应的 RPC，返回合并后的 protobuf bytes（已去帧）。
func (c *Client) Unary(ctx context.Context, port int, path string, body []byte, opts UnaryOpts) ([]byte, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	req, err := c.buildRequest(ctx, port, path, body, opts.Protocol, opts.CSRFToken)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d: %s", resp.StatusCode, string(raw))
	}

	switch opts.Protocol {
	case ProtoConnect:
		parser := &ConnectStreamParser{}
		parser.Push(raw)
		frames, err := parser.Drain()
		if err != nil {
			return nil, err
		}
		var data [][]byte
		for _, f := range frames {
			if f.IsEndStream {
				msg, _ := ParseConnectTrailerError(f.Payload)
				if msg != "" {
					return nil, errors.New(msg)
				}
			} else {
				data = append(data, f.Payload)
			}
		}
		if len(data) == 0 {
			return raw, nil
		}
		return bytes.Join(data, nil), nil
	default:
		if err := grpcStatusErr(resp.Header, resp.Trailer); err != nil {
			return nil, err
		}
		frames, _, err := ExtractGRPCFrames(raw)
		if len(frames) == 0 {
			return StripGRPCFrame(raw), nil
		}
		if err != nil {
			return nil, err
		}
		return bytes.Join(frames, nil), nil
	}
}

// StreamOpts 控制服务端流。
type StreamOpts struct {
	Protocol  Protocol
	CSRFToken string
	Timeout   time.Duration
	OnData    func(frame []byte)
	OnEnd     func()
	OnError   func(err error)
}

// Stream 发一次 server-streaming 调用。阻塞直到流结束或出错，按顺序回调。
// 调用者通常把 Stream 放在 goroutine 里，靠 OnData 推进业务。
func (c *Client) Stream(ctx context.Context, port int, path string, body []byte, opts StreamOpts) {
	if opts.Timeout == 0 {
		opts.Timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	req, err := c.buildRequest(ctx, port, path, body, opts.Protocol, opts.CSRFToken)
	if err != nil {
		fireErr(opts, err)
		return
	}

	resp, err := c.http.Do(req)
	if err != nil {
		fireErr(opts, err)
		return
	}
	defer resp.Body.Close()

	pending := make([]byte, 0, 4096)
	connectParser := &ConnectStreamParser{}
	rdbuf := make([]byte, 32*1024)

	for {
		n, rerr := resp.Body.Read(rdbuf)
		if n > 0 {
			chunk := rdbuf[:n]
			if opts.Protocol == ProtoConnect {
				connectParser.Push(chunk)
				frames, ferr := connectParser.Drain()
				if ferr != nil {
					fireErr(opts, ferr)
					return
				}
				for _, f := range frames {
					if f.IsEndStream {
						msg, _ := ParseConnectTrailerError(f.Payload)
						if msg != "" {
							fireErr(opts, errors.New(msg))
							return
						}
					} else if opts.OnData != nil {
						opts.OnData(f.Payload)
					}
				}
			} else {
				pending = append(pending, chunk...)
				if len(pending) > MaxFrameSize {
					fireErr(opts, fmt.Errorf("gRPC pending buffer overflow (>%d)", MaxFrameSize))
					return
				}
				frames, consumed, ferr := ExtractGRPCFrames(pending)
				if ferr != nil {
					fireErr(opts, ferr)
					return
				}
				for _, f := range frames {
					// f 指向 pending 的底层数组；下一轮 append 可能把底层 buffer 搬走，
					// 所以回调前拷贝一次，避免 caller 异步持有时读到脏数据。
					cp := make([]byte, len(f))
					copy(cp, f)
					if opts.OnData != nil {
						opts.OnData(cp)
					}
				}
				pending = pending[consumed:]
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			fireErr(opts, rerr)
			return
		}
	}

	if opts.Protocol != ProtoConnect {
		if err := grpcStatusErr(resp.Header, resp.Trailer); err != nil {
			fireErr(opts, err)
			return
		}
	}
	if opts.OnEnd != nil {
		opts.OnEnd()
	}
}

func (c *Client) buildRequest(ctx context.Context, port int, path string, body []byte, proto Protocol, csrf string) (*http.Request, error) {
	var payload []byte
	switch proto {
	case ProtoConnect:
		w, err := ConnectWrap(body, true)
		if err != nil {
			return nil, fmt.Errorf("connect wrap: %w", err)
		}
		payload = w
	default:
		payload = GRPCFrame(body)
	}

	u := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: path}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	setHeaders(req, proto, csrf)
	return req, nil
}

func setHeaders(req *http.Request, proto Protocol, csrf string) {
	switch proto {
	case ProtoConnect:
		req.Header.Set("Content-Type", "application/connect+proto")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Connect-Accept-Encoding", "gzip")
		req.Header.Set("User-Agent", "connect-es/2.0.0")
	default:
		req.Header.Set("Content-Type", "application/grpc")
		req.Header.Set("TE", "trailers")
		req.Header.Set("Grpc-Accept-Encoding", "identity,gzip,deflate")
		req.Header.Set("User-Agent", "grpc-node/1.108.2")
	}
	if csrf != "" {
		req.Header.Set("X-Codeium-Csrf-Token", csrf)
	}
}

func grpcStatusErr(headers ...http.Header) error {
	var code, msg string
	for _, h := range headers {
		if h == nil {
			continue
		}
		if code == "" {
			code = h.Get("Grpc-Status")
		}
		if msg == "" {
			msg = h.Get("Grpc-Message")
		}
	}
	if code == "" || code == "0" {
		return nil
	}
	if msg == "" {
		return fmt.Errorf("gRPC status %s", code)
	}
	if unescaped, err := url.QueryUnescape(msg); err == nil {
		return errors.New(unescaped)
	}
	return errors.New(msg)
}

func fireErr(opts StreamOpts, err error) {
	if opts.OnError != nil {
		opts.OnError(err)
	}
}
