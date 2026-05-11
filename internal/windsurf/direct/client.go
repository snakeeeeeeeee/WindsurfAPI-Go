// Package direct contains experimental clients for Windsurf cloud RPCs that do
// not require the local language_server process.
package direct

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"

	grpcpkg "github.com/zhangyu/windsurfapi-go/internal/grpc"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	p "github.com/zhangyu/windsurfapi-go/internal/proto"
	"github.com/zhangyu/windsurfapi-go/internal/redact"
	"github.com/zhangyu/windsurfapi-go/internal/sanitize"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

const (
	defaultTimeout = 30 * time.Second

	userStatusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

	apiServerServicePrefix = "/exa.api_server_pb.ApiServerService/"
	modelConfigPath        = apiServerServicePrefix + "GetCascadeModelConfigs"
	rateLimitPath          = apiServerServicePrefix + "CheckUserMessageRateLimit"
	getChatMessagePath     = apiServerServicePrefix + "GetChatMessage"

	lsServicePrefix        = "/exa.language_server_pb.LanguageServerService/"
	startCascadePath       = lsServicePrefix + "StartCascade"
	sendCascadeMessagePath = lsServicePrefix + "SendUserCascadeMessage"
	getTrajectoryStepsPath = lsServicePrefix + "GetCascadeTrajectorySteps"
)

var defaultHosts = []string{
	"server.codeium.com",
	"server.self-serve.windsurf.com",
}

// Client talks directly to Windsurf cloud Connect/gRPC endpoints. This is an
// experimental Plan B path; production chat still uses the LS-backed client.
type Client struct {
	hosts           []string
	http            *http.Client
	transport       *http2.Transport
	userAgent       string
	timeout         time.Duration
	rawGRPC         bool
	compress        bool
	httpScheme      string
	defaultProxyURL string
	allowPrivate    bool
	nativePrompts   bool

	mu           sync.Mutex
	stats        Stats
	proxyClients map[string]*http.Client
}

// Option customizes the direct client.
type Option func(*Client)

func WithHosts(hosts []string) Option {
	return func(c *Client) {
		var out []string
		for _, h := range hosts {
			if h = strings.TrimSpace(h); h != "" {
				out = append(out, h)
			}
		}
		if len(out) > 0 {
			c.hosts = out
		}
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
			c.http.Timeout = timeout
			for _, client := range c.proxyClients {
				client.Timeout = timeout
			}
		}
	}
}

func WithRawGRPC(enabled bool) Option {
	return func(c *Client) { c.rawGRPC = enabled }
}

func WithConnectCompression(enabled bool) Option {
	return func(c *Client) { c.compress = enabled }
}

func WithDefaultProxyURL(proxyURL string) Option {
	return func(c *Client) { c.defaultProxyURL = strings.TrimSpace(proxyURL) }
}

func WithAllowPrivateProxy(enabled bool) Option {
	return func(c *Client) { c.allowPrivate = enabled }
}

func WithNativeChatPrompts(enabled bool) Option {
	return func(c *Client) { c.nativePrompts = enabled }
}

// NewClient returns a cloud direct client.
func NewClient(opts ...Option) *Client {
	t := &http2.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			d := &tls.Dialer{Config: cfg}
			return d.DialContext(ctx, network, addr)
		},
	}
	c := &Client{
		hosts:        append([]string(nil), defaultHosts...),
		http:         &http.Client{Transport: t, Timeout: defaultTimeout},
		transport:    t,
		userAgent:    "windsurf/1.9600.41",
		timeout:      defaultTimeout,
		compress:     true,
		httpScheme:   "https",
		proxyClients: map[string]*http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// UserStatus is a normalized subset of GetUserStatus.
type UserStatus struct {
	PlanName       string
	DailyPercent   *float64
	WeeklyPercent  *float64
	Percent        *float64
	DailyResetAt   *int64
	WeeklyResetAt  *int64
	Prompt         CreditBucket
	Flex           CreditBucket
	OverageBalance *float64
	PlanStart      string
	PlanEnd        string
	Raw            map[string]any
}

type CreditBucket struct {
	Limit     *float64
	Used      *float64
	Remaining *float64
}

type ModelConfigs struct {
	Configs         []map[string]any
	Sorts           []map[string]any
	DefaultOverride map[string]any
	Raw             map[string]any
}

type RateLimit struct {
	HasCapacity       bool
	MessagesRemaining int
	MaxMessages       int
	RetryAfterMS      *int64
	Raw               map[string]any
}

type ChatRequest struct {
	APIKey                   string
	ProxyURL                 string
	Model                    *models.Model
	Messages                 []windsurf.ChatMessage
	ReportedName             string
	Thinking                 string
	Tools                    []ToolDefinition
	ToolChoice               *ToolChoice
	DisableParallelToolCalls bool

	OnDelta         func(text string) error
	OnThinkingDelta func(text string) error
	OnToolCallDelta func(index int, call windsurf.ToolCall) error
	OnFirstDelta    func()
}

// ProbeChat is a quota-consuming availability probe for one account/model. It
// uses the same raw gRPC GetChatMessage path as production direct chat and
// honors the account proxy.
func (c *Client) ProbeChat(ctx context.Context, apiKey, proxyURL string, model *models.Model, prompt string) (*windsurf.ChatResult, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = "Availability probe. Reply exactly: OK"
	}
	return c.Chat(ctx, ChatRequest{
		APIKey:   apiKey,
		ProxyURL: proxyURL,
		Model:    model,
		Messages: []windsurf.ChatMessage{{Role: "user", Content: prompt}},
	})
}

type ToolDefinition struct {
	Name        string
	Description string
	SchemaJSON  string
	Strict      bool
}

type ToolChoice struct {
	OptionName string
	ToolName   string
}

type ToolMode string

const (
	ToolModeNative   ToolMode = "native"
	ToolModeEmulated ToolMode = "emulated"
	ToolModeNone     ToolMode = "none"
)

type Stats struct {
	Protocol      string    `json:"protocol"`
	Hosts         []string  `json:"hosts"`
	ProxyClients  int       `json:"proxy_clients"`
	Successes     uint64    `json:"successes"`
	Failures      uint64    `json:"failures"`
	LastHost      string    `json:"last_host,omitempty"`
	LastProxy     string    `json:"last_proxy,omitempty"`
	LastModel     string    `json:"last_model,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastTextBytes int       `json:"last_text_bytes,omitempty"`
	LastLatencyMS int64     `json:"last_latency_ms,omitempty"`
	LastAt        time.Time `json:"last_at,omitempty"`
}

// Snapshot returns debug-safe direct client state.
func (c *Client) Snapshot() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.stats
	out.Protocol = "grpc"
	out.Hosts = append([]string(nil), c.hosts...)
	out.ProxyClients = len(c.proxyClients)
	return out
}

func (c *Client) httpClient(proxyURL string) (*http.Client, error) {
	proxyURL = c.effectiveProxyURL(proxyURL)
	if proxyURL == "" {
		return c.http, nil
	}
	if err := validateProxyURL(proxyURL, c.allowPrivate); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return nil, fmt.Errorf("invalid proxy_url %q: %w", maskProxyURL(proxyURL), err)
	}
	key := parsed.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if client := c.proxyClients[key]; client != nil {
		return client, nil
	}
	transport := &http.Transport{
		Proxy:             http.ProxyURL(parsed),
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: transport, Timeout: c.timeout}
	c.proxyClients[key] = client
	return client, nil
}

func validateProxyURL(raw string, allowPrivate bool) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return fmt.Errorf("invalid proxy_url %q: %w", maskProxyURL(raw), err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
	default:
		return fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}
	if allowPrivate {
		return nil
	}
	if isPrivateHost(parsed.Hostname()) {
		return fmt.Errorf("ERR_PROXY_PRIVATE_HOST: proxy host %q is private; set proxy.allow_private=true or WINDSURFAPI_PROXY_ALLOW_PRIVATE=1 to allow it", parsed.Hostname())
	}
	return nil
}

func isPrivateHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 0 ||
			ip4[0] == 10 ||
			ip4[0] == 127 ||
			(ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127) ||
			(ip4[0] == 169 && ip4[1] == 254) ||
			(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
			(ip4[0] == 192 && ip4[1] == 168)
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
}

func (c *Client) effectiveProxyURL(proxyURL string) string {
	if v := strings.TrimSpace(proxyURL); v != "" {
		return v
	}
	return strings.TrimSpace(c.defaultProxyURL)
}

func maskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	if u.User != nil {
		username := u.User.Username()
		if username == "" {
			username = "***"
		}
		u.User = url.UserPassword(username, "***")
	}
	return u.String()
}

// ProbeCascadeResult reports the first point at which cloud-direct Cascade
// fails or succeeds. It is intentionally diagnostic rather than production API.
type ProbeCascadeResult struct {
	Host        string
	Protocol    string
	Stage       string
	CascadeID   string
	Assistant   string
	RawResponse string
	Elapsed     time.Duration
	Err         error
}

// ProbeAPIChatResult reports the experimental cloud-direct
// ApiServerService/GetChatMessage outcome.
type ProbeAPIChatResult struct {
	Host         string
	Protocol     string
	Stage        string
	Assistant    string
	Thinking     string
	ActualModel  string
	ToolCalls    []windsurf.ToolCall
	FrameCount   int
	FrameSummary []string
	RawResponse  string
	Elapsed      time.Duration
	Err          error
}

func (r ProbeCascadeResult) OK() bool {
	return r.Err == nil && strings.TrimSpace(r.Assistant) != ""
}

func (r ProbeCascadeResult) Error() string {
	if r.Err == nil {
		return ""
	}
	return r.Err.Error()
}

func (r ProbeAPIChatResult) OK() bool {
	return r.Err == nil && (strings.TrimSpace(r.Assistant) != "" || len(r.ToolCalls) > 0)
}

func (r ProbeAPIChatResult) Error() string {
	if r.Err == nil {
		return ""
	}
	return r.Err.Error()
}

func (c *Client) GetUserStatus(ctx context.Context, apiKey string) (*UserStatus, error) {
	return c.GetUserStatusWithProxy(ctx, apiKey, "")
}

func (c *Client) GetUserStatusWithProxy(ctx context.Context, apiKey, proxyURL string) (*UserStatus, error) {
	var out map[string]any
	if err := c.postJSONAnyHost(ctx, userStatusPath, map[string]any{"metadata": metadataJSON(apiKey)}, &out, proxyURL); err != nil {
		return nil, err
	}
	return normalizeUserStatus(out), nil
}

func (c *Client) GetCascadeModelConfigs(ctx context.Context, apiKey string) (*ModelConfigs, error) {
	return c.GetCascadeModelConfigsWithProxy(ctx, apiKey, "")
}

func (c *Client) GetCascadeModelConfigsWithProxy(ctx context.Context, apiKey, proxyURL string) (*ModelConfigs, error) {
	var out map[string]any
	if err := c.postJSONAnyHost(ctx, modelConfigPath, map[string]any{"metadata": metadataJSON(apiKey)}, &out, proxyURL); err != nil {
		return nil, err
	}
	return &ModelConfigs{
		Configs:         mapSlice(out["clientModelConfigs"]),
		Sorts:           mapSlice(out["clientModelSorts"]),
		DefaultOverride: mapValue(out["defaultOverrideModelConfig"]),
		Raw:             out,
	}, nil
}

func (c *Client) CheckMessageRateLimit(ctx context.Context, apiKey string) (*RateLimit, error) {
	return c.CheckMessageRateLimitWithProxy(ctx, apiKey, "")
}

func (c *Client) CheckMessageRateLimitWithProxy(ctx context.Context, apiKey, proxyURL string) (*RateLimit, error) {
	var out map[string]any
	if err := c.postJSONAnyHost(ctx, rateLimitPath, map[string]any{"metadata": metadataJSON(apiKey)}, &out, proxyURL); err != nil {
		return nil, err
	}
	return &RateLimit{
		HasCapacity:       boolValue(out["hasCapacity"], true),
		MessagesRemaining: intValue(out["messagesRemaining"], -1),
		MaxMessages:       intValue(out["maxMessages"], -1),
		RetryAfterMS:      int64Ptr(out["retryAfterMs"]),
		Raw:               out,
	}, nil
}

// Chat sends a Claude-only chat request directly to Windsurf cloud
// ApiServerService/GetChatMessage. This is the production direct path and does
// not start or depend on the local language server.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*windsurf.ChatResult, error) {
	start := time.Now()
	if req.Model == nil {
		return nil, errors.New("direct.Chat: model is nil")
	}
	if !req.Model.DirectSupported {
		reason := strings.TrimSpace(req.Model.UnsupportedReason)
		if reason == "" {
			reason = "model is cataloged but not enabled for the direct backend"
		}
		return nil, fmt.Errorf("direct.Chat: model %s unsupported: %s", req.Model.ID, reason)
	}
	if strings.TrimSpace(req.APIKey) == "" {
		return nil, errors.New("direct.Chat: empty api key")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("direct.Chat: empty messages")
	}
	req.Messages = sanitize.Messages(req.Messages)
	for i := range req.Tools {
		req.Tools[i].Name = sanitize.Text(req.Tools[i].Name)
		req.Tools[i].Description = sanitize.Text(req.Tools[i].Description)
		req.Tools[i].SchemaJSON = sanitize.Text(req.Tools[i].SchemaJSON)
	}
	if req.ToolChoice != nil {
		req.ToolChoice.OptionName = sanitize.Text(req.ToolChoice.OptionName)
		req.ToolChoice.ToolName = sanitize.Text(req.ToolChoice.ToolName)
	}
	prompt := flattenMessages(req.Messages)
	if thinking := strings.TrimSpace(req.Thinking); thinking != "" {
		prompt = strings.TrimSpace(sanitize.Text(thinking) + "\n\n" + prompt)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("direct.Chat: empty prompt")
	}

	var lastErr error
	for _, host := range c.hosts {
		result, err := c.chatHost(ctx, host, req, prompt)
		if err == nil {
			c.recordStats(host, req.ProxyURL, req.Model.ID, "", len(result.Text), time.Since(start))
			return result, nil
		}
		lastErr = err
		c.recordStats(host, req.ProxyURL, req.Model.ID, err.Error(), 0, time.Since(start))
	}
	if lastErr == nil {
		lastErr = errors.New("direct.Chat: no hosts configured")
	}
	return nil, lastErr
}

func (c *Client) chatHost(ctx context.Context, host string, req ChatRequest, prompt string) (*windsurf.ChatResult, error) {
	toolMode := ToolModeForRequest(req.Model, req.Tools, req.ToolChoice, req.Messages)
	upstreamTools := req.Tools
	upstreamChoice := req.ToolChoice
	upstreamDisableParallel := req.DisableParallelToolCalls
	if toolMode == ToolModeEmulated {
		prompt = BuildEmulatedToolPrompt(req.Messages, req.Tools, req.ToolChoice)
		upstreamTools = nil
		upstreamChoice = nil
		upstreamDisableParallel = false
	}
	body := buildAPIGetChatMessageRequestWithMessages(req.APIKey, req.Model, prompt, 5, upstreamTools, upstreamChoice, upstreamDisableParallel, c.nativePrompts, req.Messages)
	frames, err := c.postProtoFramesWithProtocol(ctx, host, getChatMessagePath, body, true, req.ProxyURL)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, errors.New("empty GetChatMessage response")
	}
	result := &windsurf.ChatResult{FinishReason: "stop"}
	var emitted bool
	toolAccum := newToolCallAccumulator()
	for _, frame := range frames {
		parsed, err := parseAPIGetChatMessageResponse(frame)
		if err != nil {
			return nil, fmt.Errorf("parse GetChatMessage response: %w", err)
		}
		if parsed.Assistant != "" {
			parsed.Assistant = sanitize.Text(parsed.Assistant)
			if toolMode == ToolModeEmulated {
				result.Text += parsed.Assistant
				continue
			}
			if !emitted && req.OnFirstDelta != nil {
				req.OnFirstDelta()
				emitted = true
			}
			if req.OnDelta != nil {
				if err := req.OnDelta(parsed.Assistant); err != nil {
					return nil, err
				}
			}
			result.Text += parsed.Assistant
		}
		if parsed.Thinking != "" {
			parsed.Thinking = sanitize.Text(parsed.Thinking)
			if !emitted && req.OnFirstDelta != nil {
				req.OnFirstDelta()
				emitted = true
			}
			if req.OnThinkingDelta != nil {
				if err := req.OnThinkingDelta(parsed.Thinking); err != nil {
					return nil, err
				}
			}
			result.Thinking += parsed.Thinking
		}
		if len(parsed.ToolCalls) > 0 {
			if !emitted && req.OnFirstDelta != nil {
				req.OnFirstDelta()
				emitted = true
			}
			for _, delta := range parsed.ToolCalls {
				delta = sanitize.ToolCall(delta)
				idx, _ := toolAccum.Add(delta)
				if req.OnToolCallDelta != nil {
					if err := req.OnToolCallDelta(idx, delta); err != nil {
						return nil, err
					}
				}
			}
		}
		if parsed.Usage != nil {
			result.Usage = parsed.Usage
		}
	}
	result.ToolCalls = toolAccum.All()
	if toolMode == ToolModeEmulated {
		result.Text, result.ToolCalls = parseEmulatedToolCalls(result.Text)
		if len(result.ToolCalls) > 0 {
			if !emitted && req.OnFirstDelta != nil {
				req.OnFirstDelta()
				emitted = true
			}
			for i, call := range result.ToolCalls {
				if req.OnToolCallDelta != nil {
					if err := req.OnToolCallDelta(i, call); err != nil {
						return nil, err
					}
				}
			}
		} else if result.Text != "" && req.OnDelta != nil {
			if !emitted && req.OnFirstDelta != nil {
				req.OnFirstDelta()
				emitted = true
			}
			if err := req.OnDelta(result.Text); err != nil {
				return nil, err
			}
		}
	}
	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	}
	if strings.TrimSpace(result.Text) == "" && len(result.ToolCalls) == 0 {
		return nil, errors.New("GetChatMessage returned no delta_text or delta_tool_calls")
	}
	return result, nil
}

func (c *Client) recordStats(host, proxyURL, model, err string, textBytes int, latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.LastHost = host
	c.stats.LastProxy = maskProxyURL(c.effectiveProxyURL(proxyURL))
	c.stats.LastModel = model
	c.stats.LastError = redact.Text(err)
	c.stats.LastTextBytes = textBytes
	c.stats.LastLatencyMS = latency.Milliseconds()
	c.stats.LastAt = time.Now()
	if err == "" {
		c.stats.Successes++
	} else {
		c.stats.Failures++
	}
}

// ProbeCascade tries the LS Cascade RPC names directly against the cloud host.
// Most accounts currently appear to need the LS routing layer for this path; the
// method exists to make that conclusion reproducible without reverse-engineering
// by guesswork.
func (c *Client) ProbeCascade(ctx context.Context, apiKey string, model *models.Model, prompt string) ProbeCascadeResult {
	start := time.Now()
	if model == nil {
		return ProbeCascadeResult{Stage: "validate", Err: errors.New("model is nil")}
	}
	sessionID := windsurf.NewUUID()
	var last ProbeCascadeResult
	for _, host := range c.hosts {
		last = c.probeCascadeHost(ctx, host, apiKey, model, prompt, sessionID)
		last.Elapsed = time.Since(start)
		if last.Err == nil {
			return last
		}
	}
	return last
}

// ProbeAPIChat calls the cloud ApiServerService/GetChatMessage endpoint without
// going through the local LS. It is intentionally a diagnostic probe and must
// stay opt-in because a successful call can consume model quota.
func (c *Client) ProbeAPIChat(ctx context.Context, apiKey string, model *models.Model, prompt string, requestType uint64) ProbeAPIChatResult {
	return c.ProbeAPIChatWithTools(ctx, apiKey, model, prompt, requestType, nil, nil)
}

func (c *Client) ProbeAPIChatWithTools(ctx context.Context, apiKey string, model *models.Model, prompt string, requestType uint64, tools []ToolDefinition, choice *ToolChoice) ProbeAPIChatResult {
	start := time.Now()
	if model == nil {
		return ProbeAPIChatResult{Stage: "validate", Err: errors.New("model is nil")}
	}
	if strings.TrimSpace(prompt) == "" {
		return ProbeAPIChatResult{Stage: "validate", Err: errors.New("prompt is empty")}
	}
	var last ProbeAPIChatResult
	for _, host := range c.hosts {
		last = c.probeAPIChatHost(ctx, host, apiKey, model, prompt, requestType, tools, choice)
		last.Elapsed = time.Since(start)
		if last.Err == nil {
			return last
		}
	}
	return last
}

func (c *Client) probeAPIChatHost(ctx context.Context, host, apiKey string, model *models.Model, prompt string, requestType uint64, tools []ToolDefinition, choice *ToolChoice) ProbeAPIChatResult {
	result := ProbeAPIChatResult{Host: host, Protocol: "grpc", Stage: "GetChatMessage"}
	body := buildAPIGetChatMessageRequest(apiKey, model, prompt, requestType, tools, choice, len(tools) > 0)
	frames, err := c.postProtoFramesWithProtocol(ctx, host, getChatMessagePath, body, true, "")
	if err != nil {
		result.Err = err
		return result
	}
	result.FrameCount = len(frames)
	if len(frames) == 0 {
		result.Err = errors.New("empty GetChatMessage response")
		return result
	}
	var raw []byte
	toolAccum := newToolCallAccumulator()
	for _, frame := range frames {
		raw = append(raw, frame...)
		result.FrameSummary = append(result.FrameSummary, summarizeProtoFrame(frame, 0))
		parsed, err := parseAPIGetChatMessageResponse(frame)
		if err != nil {
			result.Stage = "GetChatMessage.parse"
			result.RawResponse = fmt.Sprintf("%x", truncateBytes(frame, 96))
			result.Err = err
			return result
		}
		result.Assistant += parsed.Assistant
		result.Thinking += parsed.Thinking
		for _, delta := range parsed.ToolCalls {
			toolAccum.Add(delta)
		}
		if parsed.ActualModel != "" {
			result.ActualModel = parsed.ActualModel
		}
	}
	result.ToolCalls = toolAccum.All()
	if strings.TrimSpace(result.Assistant) == "" && len(result.ToolCalls) == 0 {
		result.RawResponse = fmt.Sprintf("%x", truncateBytes(raw, 160))
		result.Err = errors.New("GetChatMessage returned no delta_text or delta_tool_calls")
	}
	return result
}

func (c *Client) probeCascadeHost(ctx context.Context, host, apiKey string, model *models.Model, prompt, sessionID string) ProbeCascadeResult {
	protocol := "connect+proto"
	if c.rawGRPC {
		protocol = "grpc"
	}
	result := ProbeCascadeResult{Host: host, Protocol: protocol}

	startBody := windsurf.BuildStartCascadeRequest(apiKey, sessionID)
	startResp, err := c.postProto(ctx, host, startCascadePath, startBody)
	if err != nil {
		result.Stage = "StartCascade"
		result.Err = err
		return result
	}
	cascadeID, err := windsurf.ParseStartCascadeResponse(startResp)
	if err != nil || cascadeID == "" {
		result.Stage = "StartCascade.parse"
		result.RawResponse = fmt.Sprintf("%x", truncateBytes(startResp, 96))
		if err == nil {
			err = errors.New("empty cascade_id")
		}
		result.Err = err
		return result
	}
	result.CascadeID = cascadeID

	sendBody := windsurf.BuildSendCascadeMessageRequest(apiKey, cascadeID, prompt, sessionID, windsurf.SendOptions{
		ModelEnum: model.ModelEnum,
		ModelUID:  model.ModelUID,
	})
	if _, err := c.postProto(ctx, host, sendCascadeMessagePath, sendBody); err != nil {
		result.Stage = "SendUserCascadeMessage"
		result.Err = err
		return result
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		stepsResp, err := c.postProto(ctx, host, getTrajectoryStepsPath, windsurf.BuildGetTrajectoryStepsRequest(cascadeID, 0))
		if err != nil {
			result.Stage = "GetCascadeTrajectorySteps"
			result.Err = err
			return result
		}
		steps, err := windsurf.ParseTrajectorySteps(stepsResp)
		if err != nil {
			result.Stage = "GetCascadeTrajectorySteps.parse"
			result.Err = err
			return result
		}
		for _, step := range steps {
			if step.ErrorMsg != "" {
				result.Stage = "trajectory.error"
				result.Err = errors.New(step.ErrorMsg)
				return result
			}
			if step.Text != "" {
				result.Assistant = step.Text
				if step.Status == 3 {
					return result
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if strings.TrimSpace(result.Assistant) != "" {
		return result
	}
	result.Stage = "poll.timeout"
	result.Err = errors.New("no assistant text before timeout")
	return result
}

func (c *Client) postJSONAnyHost(ctx context.Context, path string, body any, dst any, proxyURL string) error {
	var lastErr error
	for _, host := range c.hosts {
		err := c.postJSON(ctx, host, path, body, dst, proxyURL)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no direct hosts configured")
	}
	return lastErr
}

func (c *Client) postJSON(ctx context.Context, host, path string, body any, dst any, proxyURL string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: c.httpScheme, Host: host, Path: path}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("User-Agent", c.userAgent)
	client, err := c.httpClient(proxyURL)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s%s: %w", host, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s%s http %d: %s", host, path, resp.StatusCode, truncateString(string(raw), 300))
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%s%s parse json: %w raw=%s", host, path, err, truncateString(string(raw), 200))
	}
	return nil
}

func (c *Client) postProto(ctx context.Context, host, path string, body []byte) ([]byte, error) {
	frames, err := c.postProtoFrames(ctx, host, path, body)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, nil
	}
	return bytes.Join(frames, nil), nil
}

func (c *Client) postProtoFrames(ctx context.Context, host, path string, body []byte) ([][]byte, error) {
	return c.postProtoFramesWithProtocol(ctx, host, path, body, c.rawGRPC, "")
}

func (c *Client) postProtoFramesWithProtocol(ctx context.Context, host, path string, body []byte, rawGRPC bool, proxyURL string) ([][]byte, error) {
	var payload []byte
	var err error
	if rawGRPC {
		payload = grpcpkg.GRPCFrame(body)
	} else {
		payload, err = grpcpkg.ConnectWrap(body, c.compress)
		if err != nil {
			return nil, err
		}
	}

	u := url.URL{Scheme: c.httpScheme, Host: host, Path: path}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if rawGRPC {
		req.Header.Set("Content-Type", "application/grpc")
		req.Header.Set("TE", "trailers")
		req.Header.Set("Grpc-Accept-Encoding", "identity,gzip,deflate")
		req.Header.Set("User-Agent", "grpc-node/1.108.2")
	} else {
		req.Header.Set("Content-Type", "application/connect+proto")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set("Connect-Accept-Encoding", "gzip")
		req.Header.Set("User-Agent", "connect-es/2.0.0")
	}
	client, err := c.httpClient(proxyURL)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s%s: %w", host, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s%s http %d: %s", host, path, resp.StatusCode, truncateString(string(raw), 300))
	}
	if rawGRPC {
		if err := grpcStatusErr(resp.Header, resp.Trailer); err != nil {
			return nil, err
		}
		frames, _, err := grpcpkg.ExtractGRPCFrames(raw)
		if err != nil {
			return nil, err
		}
		if len(frames) == 0 {
			return [][]byte{grpcpkg.StripGRPCFrame(raw)}, nil
		}
		return frames, nil
	}
	parser := &grpcpkg.ConnectStreamParser{}
	parser.Push(raw)
	frames, err := parser.Drain()
	if err != nil {
		return nil, err
	}
	var chunks [][]byte
	for _, frame := range frames {
		if frame.IsEndStream {
			if msg, _ := grpcpkg.ParseConnectTrailerError(frame.Payload); msg != "" {
				return nil, errors.New(msg)
			}
			continue
		}
		chunks = append(chunks, frame.Payload)
	}
	if len(chunks) == 0 {
		return [][]byte{raw}, nil
	}
	return chunks, nil
}

func buildAPIGetChatMessageRequest(apiKey string, model *models.Model, prompt string, requestType uint64, tools []ToolDefinition, choice *ToolChoice, disableParallelToolCalls bool) []byte {
	return buildAPIGetChatMessageRequestWithMessages(apiKey, model, prompt, requestType, tools, choice, disableParallelToolCalls, false, nil)
}

func buildAPIGetChatMessageRequestWithMessages(apiKey string, model *models.Model, prompt string, requestType uint64, tools []ToolDefinition, choice *ToolChoice, disableParallelToolCalls bool, nativePrompts bool, messages []windsurf.ChatMessage) []byte {
	if requestType == 0 {
		requestType = 5 // ChatMessageRequestType.CASCADE
	}
	promptID := windsurf.NewUUID()
	sessionID := windsurf.NewUUID()
	promptMessages := []windsurf.ChatMessage{{Role: "user", Content: prompt}}
	if nativePrompts {
		promptMessages = promptableMessages(messages)
		if len(promptMessages) == 0 {
			promptMessages = []windsurf.ChatMessage{{Role: "user", Content: prompt}}
		}
	}
	parts := [][]byte{
		p.WriteMessageField(1, windsurf.BuildMetadata(apiKey, sessionID)),
		p.WriteStringField(2, prompt),
		p.WriteVarintField(7, requestType),
		p.WriteStringField(14, model.CascadeName),
		p.WriteStringField(17, promptID),
		p.WriteStringField(21, model.ModelUID),
	}
	for _, msg := range promptMessages {
		parts = append(parts, p.WriteMessageField(3, buildChatMessagePromptFromMessage(msg)))
	}
	for _, tool := range tools {
		parts = append(parts, p.WriteMessageField(10, buildChatToolDefinition(tool)))
	}
	if len(tools) > 0 && disableParallelToolCalls {
		parts = append(parts, p.WriteBoolField(11, true))
	}
	if choice != nil {
		parts = append(parts, p.WriteMessageField(12, buildChatToolChoice(*choice)))
	}
	return p.Concat(parts...)
}

func buildChatMessagePrompt(prompt string) []byte {
	return buildChatMessagePromptFromMessage(windsurf.ChatMessage{Role: "user", Content: prompt})
}

func buildChatMessagePromptFromMessage(message windsurf.ChatMessage) []byte {
	source := chatMessageSource(message.Role)
	prompt := strings.TrimSpace(sanitize.Text(message.Content))
	return p.Concat(
		p.WriteStringField(1, windsurf.NewUUID()),
		p.WriteVarintField(2, source),
		p.WriteStringField(3, prompt),
		p.WriteBoolField(5, true),
	)
}

func promptableMessages(messages []windsurf.ChatMessage) []windsurf.ChatMessage {
	out := make([]windsurf.ChatMessage, 0, len(messages))
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.ToolCallID) == "" {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(sanitize.Text(msg.Content))
		if len(msg.ToolCalls) > 0 {
			content = strings.TrimSpace(content + "\n" + formatToolCallsForPrompt(sanitizeToolCalls(msg.ToolCalls)))
		}
		if role == "tool" {
			content = formatToolResultTurn(msg.ToolCallID, content)
		}
		out = append(out, windsurf.ChatMessage{Role: role, Content: content})
	}
	return out
}

func chatMessageSource(role string) uint64 {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return 2 // SOURCE.SYSTEM, matching Node RawGetChatMessage builder.
	case "assistant":
		return 3 // SOURCE.ASSISTANT, matching Node RawGetChatMessage builder.
	case "tool":
		return 4 // CHAT_MESSAGE_SOURCE_TOOL
	default:
		return 1 // CHAT_MESSAGE_SOURCE_USER
	}
}

type apiChatFrame struct {
	Assistant   string
	Thinking    string
	ActualModel string
	Usage       *windsurf.Usage
	ToolCalls   []windsurf.ToolCall
}

func parseAPIGetChatMessageResponse(buf []byte) (apiChatFrame, error) {
	fields, err := p.ParseFields(buf)
	if err != nil {
		return apiChatFrame{}, err
	}
	usage, actualModel := parseAPIStats(fields)
	return apiChatFrame{
		Assistant:   p.GetField(fields, 3, p.WireLenDelim).String(),
		Thinking:    p.GetField(fields, 9, p.WireLenDelim).String(),
		ActualModel: actualModel,
		Usage:       usage,
		ToolCalls:   parseToolCalls(fields, 6),
	}, nil
}

func buildChatToolDefinition(tool ToolDefinition) []byte {
	return p.Concat(
		p.WriteStringField(1, tool.Name),
		p.WriteStringField(2, tool.Description),
		p.WriteStringField(3, tool.SchemaJSON),
		p.WriteBoolField(4, tool.Strict),
	)
}

func buildChatToolChoice(choice ToolChoice) []byte {
	if choice.ToolName != "" {
		return p.WriteStringField(2, choice.ToolName)
	}
	return p.WriteStringField(1, choice.OptionName)
}

func parseToolCalls(fields []p.Field, fieldNum int) []windsurf.ToolCall {
	var out []windsurf.ToolCall
	for _, field := range p.GetAllFields(fields, fieldNum) {
		nested, err := p.ParseFields(field.Bytes())
		if err != nil {
			continue
		}
		call := windsurf.ToolCall{
			ID:            p.GetField(nested, 1, p.WireLenDelim).String(),
			Name:          p.GetField(nested, 2, p.WireLenDelim).String(),
			ArgumentsJSON: p.GetField(nested, 3, p.WireLenDelim).String(),
		}
		if call.ID != "" || call.Name != "" || call.ArgumentsJSON != "" {
			out = append(out, call)
		}
	}
	return out
}

type toolCallAccumulator struct {
	calls []windsurf.ToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{}
}

func (a *toolCallAccumulator) Add(delta windsurf.ToolCall) (int, windsurf.ToolCall) {
	idx := -1
	if delta.ID != "" || delta.Name != "" || len(a.calls) == 0 {
		a.calls = append(a.calls, windsurf.ToolCall{})
		idx = len(a.calls) - 1
	} else {
		idx = len(a.calls) - 1
	}
	if delta.ID != "" {
		a.calls[idx].ID = delta.ID
	}
	if delta.Name != "" {
		a.calls[idx].Name = delta.Name
	}
	if delta.ArgumentsJSON != "" {
		a.calls[idx].ArgumentsJSON += delta.ArgumentsJSON
	}
	return idx, a.calls[idx]
}

func (a *toolCallAccumulator) All() []windsurf.ToolCall {
	out := make([]windsurf.ToolCall, 0, len(a.calls))
	for _, call := range a.calls {
		if call.ID != "" || call.Name != "" || call.ArgumentsJSON != "" {
			out = append(out, call)
		}
	}
	return out
}

func parseAPIStats(fields []p.Field) (*windsurf.Usage, string) {
	var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens uint64
	var actualModel string
	for _, field := range p.GetAllFields(fields, 7) {
		nested, err := p.ParseFields(field.Bytes())
		if err != nil {
			continue
		}
		if v := p.GetField(nested, 2, p.WireVarint).Uint(); v > inputTokens {
			inputTokens = v
		}
		if v := p.GetField(nested, 3, p.WireVarint).Uint(); v > outputTokens {
			outputTokens = v
		}
		if v := p.GetField(nested, 4, p.WireVarint).Uint(); v > cacheWriteTokens {
			cacheWriteTokens = v
		}
		if v := p.GetField(nested, 5, p.WireVarint).Uint(); v > cacheReadTokens {
			cacheReadTokens = v
		}
		if v := p.GetField(nested, 9, p.WireLenDelim).String(); v != "" {
			actualModel = v
		}
	}
	u := &windsurf.Usage{
		InputTokens:        inputTokens,
		OutputTokens:       outputTokens,
		CacheWriteTokens:   cacheWriteTokens,
		CacheWrite5mTokens: cacheWriteTokens,
		CacheReadTokens:    cacheReadTokens,
	}
	if u.InputTokens|u.OutputTokens|u.CacheWriteTokens|u.CacheReadTokens == 0 {
		u = nil
	}
	return u, actualModel
}

func summarizeProtoFrame(buf []byte, depth int) string {
	fields, err := p.ParseFields(buf)
	if err != nil {
		return fmt.Sprintf("parse_error=%v hex=%x", err, truncateBytes(buf, 80))
	}
	var parts []string
	for _, f := range fields {
		part := fmt.Sprintf("%d:%d", f.FieldNum, f.WireType)
		if f.WireType == p.WireVarint {
			part += fmt.Sprintf("=%d", f.UintValue)
		}
		if f.WireType == p.WireLenDelim {
			s := strings.ToValidUTF8(string(f.BytesValue), "")
			if looksTexty(s) {
				part += fmt.Sprintf("=%q", truncateString(s, 80))
			} else {
				part += fmt.Sprintf("[len=%d]", len(f.BytesValue))
			}
			if depth < 1 {
				if nested, err := p.ParseFields(f.BytesValue); err == nil && len(nested) > 0 {
					var ns []string
					for _, nf := range nested {
						np := fmt.Sprintf("%d:%d", nf.FieldNum, nf.WireType)
						if nf.WireType == p.WireVarint {
							np += fmt.Sprintf("=%d", nf.UintValue)
						}
						if nf.WireType == p.WireLenDelim {
							s := strings.ToValidUTF8(string(nf.BytesValue), "")
							if looksTexty(s) {
								np += fmt.Sprintf("=%q", truncateString(s, 60))
							} else {
								np += fmt.Sprintf("[len=%d]", len(nf.BytesValue))
							}
						}
						ns = append(ns, np)
					}
					part += "{" + strings.Join(ns, ",") + "}"
				}
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

func looksTexty(s string) bool {
	if s == "" {
		return false
	}
	printable := 0
	for _, r := range s {
		if r == '\n' || r == '\t' || r == '\r' || r >= 0x20 {
			printable++
		}
	}
	return printable >= len([]rune(s))*4/5
}

func flattenMessages(messages []windsurf.ChatMessage) string {
	var nonEmpty []windsurf.ChatMessage
	for _, m := range messages {
		if strings.TrimSpace(m.Content) != "" || len(m.ToolCalls) > 0 || strings.TrimSpace(m.ToolCallID) != "" {
			nonEmpty = append(nonEmpty, m)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	if len(nonEmpty) == 1 && (nonEmpty[0].Role == "" || nonEmpty[0].Role == "user") {
		return strings.TrimSpace(nonEmpty[0].Content)
	}

	tb := newTranscriptBuilder(nonEmpty)
	if tb.isContinuation() {
		return tb.continuationPrompt()
	}
	return tb.latestUserPrompt()
}

func ToolModeForRequest(model *models.Model, tools []ToolDefinition, choice *ToolChoice, messages []windsurf.ChatMessage) ToolMode {
	if len(tools) == 0 {
		return ToolModeNone
	}
	if choice != nil && strings.EqualFold(strings.TrimSpace(choice.OptionName), "none") {
		return ToolModeNone
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("WINDSURFAPI_DIRECT_TOOL_MODE")), "emulated") {
		return ToolModeEmulated
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("WINDSURFAPI_DIRECT_TOOL_MODE")), "native") {
		return ToolModeNative
	}
	if model != nil && isOpus47ModelID(model.ID) {
		return ToolModeEmulated
	}
	return ToolModeNative
}

func isOpus47ModelID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.HasPrefix(id, "claude-opus-4-7")
}

func BuildEmulatedToolPrompt(messages []windsurf.ChatMessage, tools []ToolDefinition, choice *ToolChoice) string {
	prompt := flattenMessages(messages)
	if len(tools) == 0 {
		return prompt
	}
	var parts []string
	parts = append(parts, emulatedToolInstructions(tools, choice))
	if prompt != "" {
		parts = append(parts, prompt)
	}
	return strings.Join(parts, "\n\n")
}

func emulatedToolInstructions(tools []ToolDefinition, choice *ToolChoice) string {
	var b strings.Builder
	b.WriteString("You have access to external function tools through the API client.\n")
	b.WriteString("When you need a tool, do not answer in prose. Output exactly one tool call block and nothing else:\n")
	b.WriteString("<tool_call>{\"name\":\"tool_name\",\"arguments\":{...}}</tool_call>\n")
	b.WriteString("Use valid compact JSON. The arguments value must be a JSON object matching the chosen tool schema.\n")
	b.WriteString("After the client returns tool results in later messages, use those results to answer and do not repeat a completed tool_call id.\n")
	switch toolChoiceMode(choice) {
	case "required":
		b.WriteString("For this turn you must call one tool instead of answering directly.\n")
	case "none":
		b.WriteString("For this turn you must not call tools; answer directly.\n")
	}
	if choice != nil && strings.TrimSpace(choice.ToolName) != "" {
		b.WriteString("For this turn you must call the function ")
		b.WriteString(strconv.Quote(sanitize.Text(choice.ToolName)))
		b.WriteString(" and no other function.\n")
	}
	b.WriteString("\nAvailable tools:\n")
	for _, tool := range tools {
		name := sanitize.Text(tool.Name)
		if name == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(name)
		if desc := strings.TrimSpace(sanitize.Text(tool.Description)); desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
		schema := strings.TrimSpace(sanitize.Text(tool.SchemaJSON))
		if schema == "" {
			schema = `{"type":"object"}`
		}
		b.WriteString("\n  parameters: ")
		b.WriteString(schema)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func toolChoiceMode(choice *ToolChoice) string {
	if choice == nil {
		return "auto"
	}
	if strings.TrimSpace(choice.ToolName) != "" {
		return "required"
	}
	switch strings.ToLower(strings.TrimSpace(choice.OptionName)) {
	case "required", "any":
		return "required"
	case "none":
		return "none"
	default:
		return "auto"
	}
}

type transcriptBuilder struct {
	messages []windsurf.ChatMessage
	system   []string
	turns    []string
	latest   string
}

func newTranscriptBuilder(messages []windsurf.ChatMessage) *transcriptBuilder {
	tb := &transcriptBuilder{messages: messages}
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		text := strings.TrimSpace(m.Content)
		switch role {
		case "system":
			if text != "" {
				tb.system = append(tb.system, text)
			}
		case "assistant":
			tb.turns = append(tb.turns, formatAssistantTurn(text, m.ToolCalls))
		case "tool":
			tb.turns = append(tb.turns, formatToolResultTurn(m.ToolCallID, text))
		default:
			if tb.latest != "" {
				tb.turns = append(tb.turns, "<user>\n"+tb.latest+"\n</user>")
			}
			tb.latest = text
		}
	}
	return tb
}

func (tb *transcriptBuilder) isContinuation() bool {
	if len(tb.messages) == 0 {
		return false
	}
	lastRole := strings.TrimSpace(tb.messages[len(tb.messages)-1].Role)
	return lastRole == "assistant" || lastRole == "tool"
}

func (tb *transcriptBuilder) latestUserPrompt() string {
	latest := tb.latest
	if latest == "" && len(tb.messages) > 0 {
		latest = strings.TrimSpace(tb.messages[len(tb.messages)-1].Content)
	}
	var parts []string
	if len(tb.system) > 0 {
		parts = append(parts, strings.Join(tb.system, "\n"))
	}
	if len(tb.turns) > 0 {
		parts = append(parts, "Previous conversation, for context only:\n"+strings.Join(tb.turns, "\n\n"))
	}
	parts = append(parts, "Answer only the latest user message below. Do not continue or invent future dialogue.\n\nLatest user message:\n"+latest)
	return strings.Join(parts, "\n\n")
}

func (tb *transcriptBuilder) continuationPrompt() string {
	var parts []string
	if len(tb.system) > 0 {
		parts = append(parts, strings.Join(tb.system, "\n"))
	}
	parts = append(parts, "Conversation transcript:\n"+strings.Join(tb.allTurns(), "\n\n"))
	parts = append(parts, "Continue as the assistant from the current point. If the last message is a tool result, use that result to answer the user's request; do not repeat any tool_call id already present in the transcript. If more tool data is genuinely required, call only a new tool with a new id.")
	return strings.Join(parts, "\n\n")
}

func (tb *transcriptBuilder) allTurns() []string {
	turns := append([]string(nil), tb.turns...)
	if tb.latest != "" {
		turns = append(turns, "<user>\n"+tb.latest+"\n</user>")
	}
	return turns
}

func formatAssistantTurn(text string, calls []windsurf.ToolCall) string {
	block := strings.TrimSpace(sanitize.Text(text))
	if len(calls) > 0 {
		block = strings.TrimSpace(block + "\n" + formatToolCallsForPrompt(sanitizeToolCalls(calls)))
	}
	return "<assistant>\n" + block + "\n</assistant>"
}

func formatToolResultTurn(toolCallID, text string) string {
	id := strings.TrimSpace(sanitize.Text(toolCallID))
	text = sanitize.Text(text)
	if id == "" {
		return "<tool_result>\n" + strings.TrimSpace(text) + "\n</tool_result>"
	}
	return "<tool_result id=" + strconv.Quote(id) + ">\n" + strings.TrimSpace(text) + "\n</tool_result>"
}

func flattenMessagesLegacy(messages []windsurf.ChatMessage) string {
	var nonEmpty []windsurf.ChatMessage
	for _, m := range messages {
		if strings.TrimSpace(m.Content) != "" || len(m.ToolCalls) > 0 || strings.TrimSpace(m.ToolCallID) != "" {
			nonEmpty = append(nonEmpty, m)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	if len(nonEmpty) == 1 && (nonEmpty[0].Role == "" || nonEmpty[0].Role == "user") {
		return strings.TrimSpace(nonEmpty[0].Content)
	}

	var system []string
	var history []string
	var latest string
	for _, m := range nonEmpty {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		text := strings.TrimSpace(m.Content)
		switch role {
		case "system":
			system = append(system, text)
		case "assistant":
			block := sanitize.Text(text)
			if len(m.ToolCalls) > 0 {
				block = strings.TrimSpace(block + "\n" + formatToolCallsForPrompt(sanitizeToolCalls(m.ToolCalls)))
			}
			history = append(history, "<assistant>\n"+block+"\n</assistant>")
		case "tool":
			id := strings.TrimSpace(m.ToolCallID)
			if id == "" {
				history = append(history, "<tool_result>\n"+sanitize.Text(text)+"\n</tool_result>")
			} else {
				history = append(history, "<tool_result id="+strconv.Quote(sanitize.Text(id))+">\n"+sanitize.Text(text)+"\n</tool_result>")
			}
		default:
			if latest != "" {
				history = append(history, "<user>\n"+latest+"\n</user>")
			}
			latest = sanitize.Text(text)
		}
	}
	if latest == "" {
		latest = strings.TrimSpace(nonEmpty[len(nonEmpty)-1].Content)
	}

	var parts []string
	if len(system) > 0 {
		parts = append(parts, strings.Join(system, "\n"))
	}
	if len(history) > 0 {
		parts = append(parts, "Previous conversation, for context only:\n"+strings.Join(history, "\n\n"))
	}
	parts = append(parts, "Answer only the latest user message below. Do not continue or invent future dialogue.\n\nLatest user message:\n"+latest)
	return strings.Join(parts, "\n\n")
}

func flattenContinuationMessages(messages []windsurf.ChatMessage) string {
	var system []string
	var transcript []string
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		text := strings.TrimSpace(m.Content)
		switch role {
		case "system":
			system = append(system, text)
		case "assistant":
			block := sanitize.Text(text)
			if len(m.ToolCalls) > 0 {
				block = strings.TrimSpace(block + "\n" + formatToolCallsForPrompt(sanitizeToolCalls(m.ToolCalls)))
			}
			transcript = append(transcript, "<assistant>\n"+block+"\n</assistant>")
		case "tool":
			id := strings.TrimSpace(m.ToolCallID)
			if id == "" {
				transcript = append(transcript, "<tool_result>\n"+sanitize.Text(text)+"\n</tool_result>")
			} else {
				transcript = append(transcript, "<tool_result id="+strconv.Quote(sanitize.Text(id))+">\n"+sanitize.Text(text)+"\n</tool_result>")
			}
		default:
			transcript = append(transcript, "<user>\n"+sanitize.Text(text)+"\n</user>")
		}
	}

	var parts []string
	if len(system) > 0 {
		parts = append(parts, strings.Join(system, "\n"))
	}
	parts = append(parts, "Conversation transcript:\n"+strings.Join(transcript, "\n\n"))
	parts = append(parts, "Continue as the assistant from the current point. If the last message is a tool result, use that result to answer the user's request; do not repeat the same tool call unless additional tool data is required.")
	return strings.Join(parts, "\n\n")
}

func formatToolCallsForPrompt(calls []windsurf.ToolCall) string {
	items := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		call = sanitize.ToolCall(call)
		items = append(items, map[string]any{
			"id":        call.ID,
			"name":      call.Name,
			"arguments": call.ArgumentsJSON,
		})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	return "<tool_calls>\n" + string(raw) + "\n</tool_calls>"
}

func sanitizeToolCalls(calls []windsurf.ToolCall) []windsurf.ToolCall {
	out := make([]windsurf.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, sanitize.ToolCall(call))
	}
	return out
}

var (
	toolCallBlockRE    = regexp.MustCompile(`(?is)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
	toolCallsSectionRE = regexp.MustCompile(`(?is)<tool_calls>\s*(\[.*?\])\s*</tool_calls>`)
	toolCodeRE         = regexp.MustCompile(`(?is)<tool_code>\s*(\{.*?\})\s*</tool_code>`)
)

func parseEmulatedToolCalls(text string) (string, []windsurf.ToolCall) {
	var calls []windsurf.ToolCall
	cleaned := toolCallBlockRE.ReplaceAllStringFunc(text, func(match string) string {
		sub := toolCallBlockRE.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		if call, ok := parseToolCallObject(sub[1]); ok {
			calls = append(calls, call)
			return ""
		}
		return match
	})
	cleaned = toolCodeRE.ReplaceAllStringFunc(cleaned, func(match string) string {
		sub := toolCodeRE.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		if call, ok := parseToolCallObject(sub[1]); ok {
			calls = append(calls, call)
			return ""
		}
		return match
	})
	cleaned = toolCallsSectionRE.ReplaceAllStringFunc(cleaned, func(match string) string {
		sub := toolCallsSectionRE.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		var items []any
		if err := json.Unmarshal([]byte(sub[1]), &items); err != nil {
			return match
		}
		added := 0
		for _, item := range items {
			raw, _ := json.Marshal(item)
			if call, ok := parseToolCallObject(string(raw)); ok {
				calls = append(calls, call)
				added++
			}
		}
		if added > 0 {
			return ""
		}
		return match
	})
	if len(calls) == 0 {
		if call, ok := parseBareJSONToolCall(cleaned); ok {
			return "", []windsurf.ToolCall{call}
		}
	}
	for i := range calls {
		if strings.TrimSpace(calls[i].ID) == "" {
			calls[i].ID = fmt.Sprintf("call_%d_%s", i, strconv.FormatInt(time.Now().UnixNano(), 36))
		}
		calls[i] = sanitize.ToolCall(calls[i])
	}
	return strings.TrimSpace(cleaned), calls
}

func parseBareJSONToolCall(text string) (windsurf.ToolCall, bool) {
	text = strings.TrimSpace(strings.Trim(text, "`"))
	if !strings.HasPrefix(text, "{") || !strings.HasSuffix(text, "}") {
		return windsurf.ToolCall{}, false
	}
	return parseToolCallObject(text)
}

func parseToolCallObject(raw string) (windsurf.ToolCall, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &obj); err != nil {
		return windsurf.ToolCall{}, false
	}
	if nested, ok := obj["function_call"].(map[string]any); ok {
		obj = nested
	} else if nested, ok := obj["tool_call"].(map[string]any); ok {
		obj = nested
	}
	name := strings.TrimSpace(stringValue(obj["name"], ""))
	if name == "" {
		if fn, ok := obj["function"].(map[string]any); ok {
			name = strings.TrimSpace(stringValue(fn["name"], ""))
			if obj["arguments"] == nil {
				obj["arguments"] = fn["arguments"]
			}
		}
	}
	if name == "" {
		return windsurf.ToolCall{}, false
	}
	args := normalizeToolArgumentsJSON(obj["arguments"])
	if args == "" {
		args = "{}"
	}
	return windsurf.ToolCall{
		ID:            strings.TrimSpace(stringValue(firstNonNil(obj["id"], obj["call_id"]), "")),
		Name:          name,
		ArgumentsJSON: args,
	}, true
}

func normalizeToolArgumentsJSON(v any) string {
	switch x := v.(type) {
	case nil:
		return "{}"
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return "{}"
		}
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			return marshalCompactJSON(parsed)
		}
		return marshalCompactJSON(map[string]any{"input": s})
	default:
		return marshalCompactJSON(x)
	}
}

func marshalCompactJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func metadataJSON(apiKey string) map[string]any {
	version := windsurf.DefaultClientVersion()
	return map[string]any{
		"apiKey":           apiKey,
		"ideName":          "windsurf",
		"ideVersion":       version,
		"extensionName":    "windsurf",
		"extensionVersion": version,
		"locale":           "en",
	}
}

func normalizeUserStatus(data map[string]any) *UserStatus {
	ps := mapValue(pathValue(data, "userStatus", "planStatus"))
	plan := mapValue(ps["planInfo"])
	out := &UserStatus{
		PlanName:      stringValue(plan["planName"], "Unknown"),
		DailyPercent:  floatPtr(ps["dailyQuotaRemainingPercent"]),
		WeeklyPercent: floatPtr(ps["weeklyQuotaRemainingPercent"]),
		DailyResetAt:  int64Ptr(ps["dailyQuotaResetAtUnix"]),
		WeeklyResetAt: int64Ptr(ps["weeklyQuotaResetAtUnix"]),
		Prompt: CreditBucket{
			Limit:     creditPtr(plan["monthlyPromptCredits"]),
			Used:      creditPtr(ps["usedPromptCredits"]),
			Remaining: creditPtr(ps["availablePromptCredits"]),
		},
		Flex: CreditBucket{
			Limit:     creditPtr(plan["monthlyFlexCreditPurchaseAmount"]),
			Used:      creditPtr(ps["usedFlexCredits"]),
			Remaining: creditPtr(ps["availableFlexCredits"]),
		},
		OverageBalance: microsPtr(ps["overageBalanceMicros"]),
		PlanStart:      stringValue(ps["planStart"], ""),
		PlanEnd:        stringValue(ps["planEnd"], ""),
		Raw:            data,
	}
	if out.DailyPercent != nil {
		v := *out.DailyPercent
		out.Percent = &v
	} else if out.Prompt.Limit != nil && out.Prompt.Remaining != nil && *out.Prompt.Limit > 0 {
		v := (*out.Prompt.Remaining / *out.Prompt.Limit) * 100
		out.Percent = &v
	}
	return out
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

func pathValue(m map[string]any, path ...string) any {
	var cur any = m
	for _, key := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[key]
	}
	return cur
}

func mapValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func mapSlice(v any) []map[string]any {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringValue(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func boolValue(v any, fallback bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return fallback
}

func intValue(v any, fallback int) int {
	if f, ok := numberValue(v); ok {
		return int(f)
	}
	return fallback
}

func int64Ptr(v any) *int64 {
	if f, ok := numberValue(v); ok {
		n := int64(f)
		return &n
	}
	if s, ok := v.(string); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err == nil {
			return &n
		}
	}
	return nil
}

func floatPtr(v any) *float64 {
	if f, ok := numberValue(v); ok {
		return &f
	}
	return nil
}

func creditPtr(v any) *float64 {
	if f, ok := numberValue(v); ok {
		f = f / 100
		return &f
	}
	return nil
}

func microsPtr(v any) *float64 {
	if f, ok := numberValue(v); ok {
		f = f / 1_000_000
		return &f
	}
	return nil
}

func numberValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func truncateBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
