// Package sse 把 Windsurf Cascade 的文本增量序列化为 OpenAI 兼容的
// /v1/chat/completions streaming chunk。只覆盖客户端最常用的字段 —
// id / created / model / choices[0].delta.{role,content} / finish_reason / usage。
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/redact"
	"github.com/zhangyu/windsurfapi-go/internal/sanitize"
)

// Writer 包装一个带 Flusher 的 ResponseWriter，并带上 id/model/created 元信息。
type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
	id      string
	model   string
	created int64
	done    bool
}

// NewWriter 初始化 SSE 写入器并发送响应头。
func NewWriter(w http.ResponseWriter, model string) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &Writer{
		w:       w,
		flusher: flusher,
		id:      "chatcmpl-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		model:   model,
		created: time.Now().Unix(),
	}, nil
}

func (s *Writer) writeEvent(obj any) error {
	if s.done {
		return nil
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Role 发送首块（带 role=assistant 的空 delta），大多数 OpenAI 客户端依赖它建连。
func (s *Writer) Role() error {
	return s.writeEvent(map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{"role": "assistant"},
				"finish_reason": nil,
			},
		},
	})
}

// Delta 推送一段纯文本增量。content 为空时跳过 —— OpenAI 客户端遇到空块会乱。
func (s *Writer) Delta(content string) error {
	content = sanitize.Text(content)
	if content == "" {
		return nil
	}
	return s.writeEvent(map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{"content": content},
				"finish_reason": nil,
			},
		},
	})
}

// ReasoningDelta pushes an OpenAI-compatible reasoning_content delta used by
// Claude Code and other clients that request reasoning.
func (s *Writer) ReasoningDelta(content string) error {
	content = sanitize.Text(content)
	if content == "" {
		return nil
	}
	return s.writeEvent(map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{"reasoning_content": content},
				"finish_reason": nil,
			},
		},
	})
}

// ToolCallDelta 推送 OpenAI-compatible streaming tool_calls 增量。
func (s *Writer) ToolCallDelta(index int, id, name, arguments string) error {
	id = sanitize.Text(id)
	name = sanitize.Text(name)
	arguments = sanitize.Text(arguments)
	if id == "" && name == "" && arguments == "" {
		return nil
	}
	fn := map[string]any{}
	if name != "" {
		fn["name"] = name
	}
	if arguments != "" {
		fn["arguments"] = arguments
	}
	call := map[string]any{"index": index, "function": fn}
	if id != "" {
		call["id"] = id
		call["type"] = "function"
	}
	return s.writeEvent(map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{"tool_calls": []any{call}},
				"finish_reason": nil,
			},
		},
	})
}

// Error sends a structured OpenAI-compatible stream error object. It is used
// only after the HTTP/SSE stream is already committed, where returning a normal
// JSON error response is no longer possible.
func (s *Writer) Error(class account.ErrorClass, err error) error {
	message := "upstream stream error"
	if err != nil && err.Error() != "" {
		message = redact.Text(err.Error())
	}
	typ := string(class)
	if typ == "" {
		typ = "upstream_error"
	}
	return s.writeEvent(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    typ,
			"code":    typ,
		},
	})
}

// Usage 是可选 usage 面板。OpenAI 的 streaming 协议把 usage 塞在最后一个真 chunk 上，
// 这里单独作为一个带空 choices 的 chunk 发出，更容易兼容多种客户端。
type Usage struct {
	PromptTokens      uint64 `json:"prompt_tokens"`
	CompletionTokens  uint64 `json:"completion_tokens"`
	TotalTokens       uint64 `json:"total_tokens"`
	CachedInputTokens uint64 `json:"cached_input_tokens,omitempty"`
}

// Finish 发送结束块（finish_reason=stop），可附带 usage。
func (s *Writer) Finish(reason string, usage *Usage) error {
	if reason == "" {
		reason = "stop"
	}
	chunk := map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": reason,
			},
		},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	if err := s.writeEvent(chunk); err != nil {
		return err
	}
	// `data: [DONE]` 是 OpenAI 协议强制的收尾标记
	_, err := fmt.Fprint(s.w, "data: [DONE]\n\n")
	s.flusher.Flush()
	s.done = true
	return err
}

// ID 导出内部 chatcmpl id，非流模式下组装 JSON 响应时会用到。
func (s *Writer) ID() string { return s.id }
