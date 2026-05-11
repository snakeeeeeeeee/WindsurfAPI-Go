package windsurf

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	grpcpkg "github.com/zhangyu/windsurfapi-go/internal/grpc"
	"github.com/zhangyu/windsurfapi-go/internal/ls"
)

// 状态枚举值对齐 windsurf.js:786 注释 —— 3 = DONE, 8 = GENERATING, 其它见 LS proto。
const (
	statusUnspecified uint64 = 0
	statusGenerating  uint64 = 8
	statusDone        uint64 = 3
	statusError       uint64 = 4 // CORTEX_STEP_STATUS_ERROR（Node 版也按这个判错）

	cascadePollTimeout = 180 * time.Second
	cascadeColdStall   = 75 * time.Second
	lsPostReadyGrace   = 500 * time.Millisecond
)

// Client 是针对单个 LS 实例的高阶会话入口。
// 它不负责 LS 生命周期 —— Pool 负责拉起进程，Client 只管打 RPC。
type Client struct {
	pool *ls.Pool
	grpc *grpcpkg.Client

	// workspace init 在同一个 LS 上只能跑一次
	initMu   sync.Mutex
	initDone map[string]bool // key = entry.Key + generation
}

// NewClient 创建高阶会话客户端。复用外部传入的 pool / grpc，不自拥有。
func NewClient(pool *ls.Pool, g *grpcpkg.Client) *Client {
	return &Client{pool: pool, grpc: g, initDone: map[string]bool{}}
}

func (c *Client) Pool() *ls.Pool { return c.pool }

// ChatRequest 描述一次 OpenAI-style chat 调用（OpenAI tools / images 未覆盖，TODO）。
type ChatRequest struct {
	APIKey       string
	ModelEnum    uint64
	ModelUID     string
	ReportedName string // 回传到客户端的 model 字段（OpenAI 协议要求）
	Entry        *ls.Entry
	CascadeID    string

	Messages []ChatMessage

	// 流式回调。仅当调用方是 streaming 时设置。
	OnDelta      func(text string) error
	OnFirstDelta func()
}

// ToolCall 是 OpenAI-compatible function tool call 的内部中性表示。
type ToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// ChatMessage 是 OpenAI 风格消息。Content 必须是文本 —— 图像支持在 TODO。
type ChatMessage struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

// ChatResult 是非流式调用返回值。
type ChatResult struct {
	Text         string
	FinishReason string
	Usage        *Usage
	CascadeID    string
	Entry        *ls.Entry
	ToolCalls    []ToolCall
	Thinking     string
}

// Chat 走一次完整 Cascade 流程：
//
//	ensureLS → ensureWorkspaceInit → StartCascade → SendUserCascadeMessage →
//	poll GetCascadeTrajectorySteps → optional GetGeneratorMetadata → 汇总
//
// 并没有做 Node 版 client.js 里 MAX_PANEL_RETRIES 的多层重试 —— 首轮调通后再补。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResult, error) {
	if req.ModelUID == "" && req.ModelEnum == 0 {
		return nil, errors.New("windsurf.Chat: model_uid or model_enum required")
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("windsurf.Chat: empty messages")
	}

	entry := req.Entry
	if entry == nil {
		var err error
		entry, err = c.pool.EnsureDefault(ctx)
		if err != nil {
			return nil, fmt.Errorf("ensure LS: %w", err)
		}
	}
	if err := c.ensureWorkspaceInit(ctx, entry, req.APIKey); err != nil {
		return nil, fmt.Errorf("panel init: %w", err)
	}

	flatText := flattenMessages(req.Messages)

	cascadeID := req.CascadeID
	if cascadeID == "" {
		// StartCascade
		startResp, err := c.grpc.Unary(ctx, entry.Port, PathStartCascade,
			BuildStartCascadeRequest(req.APIKey, entry.SessionID),
			unaryOpts(entry.CSRFToken, 15*time.Second))
		if err != nil {
			return nil, fmt.Errorf("StartCascade: %w", err)
		}
		cascadeID, err = ParseStartCascadeResponse(startResp)
		if err != nil || cascadeID == "" {
			return nil, fmt.Errorf("StartCascade empty cascade_id (parse err=%v)", err)
		}
	}

	// SendUserCascadeMessage（不关心返回体，LS 只是 ack）
	sendBody := BuildSendCascadeMessageRequest(req.APIKey, cascadeID, flatText, entry.SessionID,
		SendOptions{ModelEnum: req.ModelEnum, ModelUID: req.ModelUID})
	if _, err := c.grpc.Unary(ctx, entry.Port, PathSendUserCascadeMessage, sendBody,
		unaryOpts(entry.CSRFToken, 60*time.Second)); err != nil {
		return nil, fmt.Errorf("SendUserCascadeMessage: %w", err)
	}

	result, err := c.pollTrajectory(ctx, entry, cascadeID, req)
	if err != nil {
		return nil, err
	}
	result.CascadeID = cascadeID
	result.Entry = entry

	// Generator metadata — token usage
	if metaResp, err := c.grpc.Unary(ctx, entry.Port, PathGetCascadeTrajectoryGeneratorMD,
		BuildGetGeneratorMetadataRequest(cascadeID, 0),
		unaryOpts(entry.CSRFToken, 10*time.Second)); err == nil {
		if u, perr := ParseGeneratorMetadata(metaResp); perr == nil && u != nil {
			result.Usage = u
		}
	}
	return result, nil
}

func (c *Client) pollTrajectory(ctx context.Context, entry *ls.Entry, cascadeID string, req ChatRequest) (*ChatResult, error) {
	var (
		result    ChatResult
		emitted   int // 已推给 OnDelta 的字节数
		idleSince = time.Now()
	)

	pollCtx, cancel := context.WithTimeout(ctx, cascadePollTimeout)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			if result.Text != "" {
				result.FinishReason = "length"
				return &result, nil
			}
			return nil, fmt.Errorf("trajectory poll timeout")
		case <-ticker.C:
		}

		resp, err := c.grpc.Unary(pollCtx, entry.Port, PathGetCascadeTrajectorySteps,
			BuildGetTrajectoryStepsRequest(cascadeID, 0),
			unaryOpts(entry.CSRFToken, 10*time.Second))
		if err != nil {
			return nil, fmt.Errorf("GetCascadeTrajectorySteps: %w", err)
		}
		steps, err := ParseTrajectorySteps(resp)
		if err != nil {
			return nil, fmt.Errorf("parse steps: %w", err)
		}

		// 挑最后一个有 text 的 step（planner response），简单但对纯文本 chat 已够。
		var latestText, latestErr string
		var latestStatus uint64
		for _, s := range steps {
			if s.ErrorMsg != "" {
				latestErr = s.ErrorMsg
			}
			if s.Text != "" {
				latestText = s.Text
				latestStatus = s.Status
			}
		}

		if latestText != "" {
			idleSince = time.Now()
			// 流式模式：推出增量；缓存已发字节数避免重发。
			if req.OnDelta != nil && len(latestText) > emitted {
				delta := latestText[emitted:]
				if emitted == 0 && req.OnFirstDelta != nil {
					req.OnFirstDelta()
				}
				if err := req.OnDelta(delta); err != nil {
					return nil, err
				}
				emitted = len(latestText)
			}
			result.Text = latestText
		}

		if latestErr != "" {
			return nil, fmt.Errorf("cascade error: %s", latestErr)
		}
		if latestStatus == statusDone {
			result.FinishReason = "stop"
			return &result, nil
		}
		if latestStatus == statusError {
			return nil, fmt.Errorf("cascade step reported ERROR status")
		}

		// Cold Cascade can sit silent for a long while before the first text
		// delta. Node production observed ~75s stalls, so do not kill it at 30s.
		if time.Since(idleSince) > cascadeColdStall {
			return nil, fmt.Errorf("cascade stalled for %s with no progress", cascadeColdStall)
		}
	}
}

func (c *Client) ensureWorkspaceInit(ctx context.Context, entry *ls.Entry, apiKey string) error {
	initKey := entry.Key + ":" + entry.Generation
	c.initMu.Lock()
	done := c.initDone[initKey]
	c.initMu.Unlock()
	if done {
		return nil
	}

	return entry.RunInit(func() error {
		if since := time.Since(entry.StartedAt()); since < lsPostReadyGrace {
			timer := time.NewTimer(lsPostReadyGrace - since)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		log.Printf("Windsurf: running panel init for LS %s (port=%d)", entry.Key, entry.Port)
		// 1) GetUserStatus primes Devin session tokens into LS panel state.
		// Node's account probe does this before chat; without it some
		// devin-session-token credentials fail SendUserCascadeMessage with
		// "failed to get primary API key" even though GetUserStatus accepts them.
		if statusResp, err := c.grpc.Unary(ctx, entry.Port, PathGetUserStatus,
			BuildGetUserStatusRequest(apiKey),
			unaryOpts(entry.CSRFToken, 10*time.Second)); err != nil {
			log.Printf("GetUserStatus returned err (non-fatal): %v", err)
		} else if userStatusBytes, xerr := ExtractUserStatusBytes(statusResp); xerr == nil && len(userStatusBytes) > 0 {
			if _, uerr := c.grpc.Unary(ctx, entry.Port, PathUpdatePanelStateWithUserStatus,
				BuildUpdatePanelStateWithUserStatusRequest(apiKey, entry.SessionID, userStatusBytes),
				unaryOpts(entry.CSRFToken, 5*time.Second)); uerr != nil {
				log.Printf("UpdatePanelStateWithUserStatus returned err (non-fatal): %v", uerr)
			}
		}
		// 2) InitializeCascadePanelState
		if _, err := c.grpc.Unary(ctx, entry.Port, PathInitializeCascadePanelState,
			BuildInitializePanelStateRequest(apiKey, entry.SessionID, true),
			unaryOpts(entry.CSRFToken, 10*time.Second)); err != nil {
			log.Printf("InitializeCascadePanelState returned err (non-fatal): %v", err)
		}
		// 3) AddTrackedWorkspace（路径参考 Node 版默认 <workspace>）
		if _, err := c.grpc.Unary(ctx, entry.Port, PathAddTrackedWorkspace,
			BuildAddTrackedWorkspaceRequest(entry.DataDir),
			unaryOpts(entry.CSRFToken, 5*time.Second)); err != nil {
			log.Printf("AddTrackedWorkspace returned err (non-fatal): %v", err)
		}
		// 4) UpdateCascadeWorkspaceTrust —— 不做 SendUserCascadeMessage 会 "untrusted workspace"
		if _, err := c.grpc.Unary(ctx, entry.Port, PathUpdateCascadeWorkspaceTrust,
			BuildUpdateWorkspaceTrustRequest(apiKey, entry.SessionID, true),
			unaryOpts(entry.CSRFToken, 5*time.Second)); err != nil {
			log.Printf("UpdateCascadeWorkspaceTrust returned err (non-fatal): %v", err)
		}
		// 5) Heartbeat — mirrors Node warmup and gives newer LS builds a cheap
		// post-init RPC after workspace state is in place.
		if _, err := c.grpc.Unary(ctx, entry.Port, PathHeartbeat,
			BuildHeartbeatRequest(apiKey, entry.SessionID),
			unaryOpts(entry.CSRFToken, 5*time.Second)); err != nil {
			log.Printf("Heartbeat returned err (non-fatal): %v", err)
		}
		c.initMu.Lock()
		c.initDone[initKey] = true
		c.initMu.Unlock()
		return nil
	})
}

// GetUserStatus 把 LS 的同名 RPC 暴露给外部 —— 冒烟 / dashboard 用。
func (c *Client) GetUserStatus(ctx context.Context, apiKey string) (*UserStatus, error) {
	entry, err := c.pool.EnsureDefault(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.grpc.Unary(ctx, entry.Port, PathGetUserStatus,
		BuildGetUserStatusRequest(apiKey),
		unaryOpts(entry.CSRFToken, 30*time.Second))
	if err != nil {
		return nil, err
	}
	if userStatusBytes, xerr := ExtractUserStatusBytes(resp); xerr == nil && len(userStatusBytes) > 0 {
		if _, uerr := c.grpc.Unary(ctx, entry.Port, PathUpdatePanelStateWithUserStatus,
			BuildUpdatePanelStateWithUserStatusRequest(apiKey, entry.SessionID, userStatusBytes),
			unaryOpts(entry.CSRFToken, 5*time.Second)); uerr != nil {
			log.Printf("UpdatePanelStateWithUserStatus returned err (non-fatal): %v", uerr)
		}
	}
	return ParseGetUserStatusResponse(resp)
}

func unaryOpts(csrf string, timeout time.Duration) grpcpkg.UnaryOpts {
	return grpcpkg.UnaryOpts{
		Protocol:  grpcpkg.DefaultProtocol(),
		CSRFToken: csrf,
		Timeout:   timeout,
	}
}

// flattenMessages 把 OpenAI messages 拍平成一段单独的文本。对齐 Node client.js 的
// "multi-turn conversation" 格式（系统前缀 + <human>/<assistant> 标签）。
func flattenMessages(msgs []ChatMessage) string {
	var sys []string
	var body []string
	for i, m := range msgs {
		switch m.Role {
		case "system":
			if strings.TrimSpace(m.Content) != "" {
				sys = append(sys, m.Content)
			}
		case "assistant":
			body = append(body, "<assistant>\n"+escapeTag(m.Content, "assistant")+"\n</assistant>")
		case "user", "":
			if i == len(msgs)-1 {
				body = append(body, "<human>\n"+escapeTag(m.Content, "human")+"\n</human>")
			} else {
				body = append(body, "<human>\n"+escapeTag(m.Content, "human")+"\n</human>")
			}
		default:
			body = append(body, "<"+m.Role+">\n"+escapeTag(m.Content, m.Role)+"\n</"+m.Role+">")
		}
	}
	var out strings.Builder
	if len(sys) > 0 {
		out.WriteString(strings.Join(sys, "\n\n"))
		out.WriteString("\n\n")
	}
	if len(body) > 1 {
		out.WriteString("The following is a multi-turn conversation. You MUST remember and use all information from prior turns.\n\n")
	}
	out.WriteString(strings.Join(body, "\n\n"))
	return out.String()
}

// escapeTag 防止用户文本里出现 `</human>` 骗过 LS 的 tag 解析 —— 只要出现就整体转义。
func escapeTag(s, tag string) string {
	close := "</" + tag + ">"
	return strings.ReplaceAll(s, close, "</"+tag+" \\/>")
}
