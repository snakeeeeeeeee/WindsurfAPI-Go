package windsurf

import (
	p "github.com/zhangyu/windsurfapi-go/internal/proto"
)

// ─── 简单 unary 请求 ───────────────────────────────────────

// BuildGetUserStatusRequest 对齐 windsurf.js:1083。
func BuildGetUserStatusRequest(apiKey string) []byte {
	return p.WriteMessageField(1, BuildMetadata(apiKey, ""))
}

// BuildInitializePanelStateRequest 对齐 windsurf.js:265。
func BuildInitializePanelStateRequest(apiKey, sessionID string, trusted bool) []byte {
	return p.Concat(
		p.WriteMessageField(1, BuildMetadata(apiKey, sessionID)),
		p.WriteBoolField(3, trusted),
	)
}

// BuildAddTrackedWorkspaceRequest 对齐 windsurf.js:278。
func BuildAddTrackedWorkspaceRequest(workspacePath string) []byte {
	return p.WriteStringField(1, workspacePath)
}

// BuildUpdateWorkspaceTrustRequest 对齐 windsurf.js:283。
func BuildUpdateWorkspaceTrustRequest(apiKey, sessionID string, trusted bool) []byte {
	return p.Concat(
		p.WriteMessageField(1, BuildMetadata(apiKey, sessionID)),
		p.WriteBoolField(2, trusted),
	)
}

// BuildUpdatePanelStateWithUserStatusRequest 对齐 windsurf.js:290。
func BuildUpdatePanelStateWithUserStatusRequest(apiKey, sessionID string, userStatusBytes []byte) []byte {
	return p.Concat(
		p.WriteMessageField(1, BuildMetadata(apiKey, sessionID)),
		p.WriteMessageField(2, userStatusBytes),
	)
}

// BuildHeartbeatRequest 对齐 windsurf.js:273。
func BuildHeartbeatRequest(apiKey, sessionID string) []byte {
	return p.WriteMessageField(1, BuildMetadata(apiKey, sessionID))
}

// ─── Cascade 请求 ──────────────────────────────────────────

// BuildStartCascadeRequest 对齐 windsurf.js:306。
func BuildStartCascadeRequest(apiKey, sessionID string) []byte {
	return p.Concat(
		p.WriteMessageField(1, BuildMetadata(apiKey, sessionID)),
		p.WriteVarintField(4, 1), // CORTEX_TRAJECTORY_SOURCE_CASCADE_CLIENT
		p.WriteVarintField(5, 1), // CORTEX_TRAJECTORY_TYPE_USER_MAINLINE
	)
}

// SendOptions 是 SendCascadeMessage 的可选参数。本轮只暴露模型信息；
// Node 版的 images / nativeMode / additionalSteps 都留空（NO_TOOL 路径）。
type SendOptions struct {
	ModelEnum uint64
	ModelUID  string
}

// BuildSendCascadeMessageRequest 对齐 windsurf.js:327。
// 只实现 NO_TOOL 路径 —— 无工具、无图片、无 additional_steps，
// 这是最稳妥的 "纯文本 chat API" 行为。扩展见 Node 版 toolPreamble/forceDefault 分支。
func BuildSendCascadeMessageRequest(apiKey, cascadeID, text, sessionID string, opt SendOptions) []byte {
	return p.Concat(
		p.WriteStringField(1, cascadeID),
		// field 2: TextOrScopeItem { text = 1 }
		p.WriteMessageField(2, p.WriteStringField(1, text)),
		p.WriteMessageField(3, BuildMetadata(apiKey, sessionID)),
		p.WriteMessageField(5, buildCascadeConfigNoTool(opt.ModelEnum, opt.ModelUID)),
	)
}

// buildCascadeConfigNoTool 构造 NO_TOOL 路径的 CascadeConfig。
// 对齐 windsurf.js:385 buildCascadeConfig（非 toolPreamble / 非 nativeMode 分支）。
func buildCascadeConfigNoTool(modelEnum uint64, modelUID string) []byte {
	// 1) ConversationalPlannerConfig
	convParts := [][]byte{
		p.WriteVarintField(4, 3), // planner_mode = NO_TOOL
	}

	// field 10: tool_calling_section override
	// SectionOverrideConfig { override_mode=1 (OVERRIDE), text=2 }
	toolCallingOverride := p.Concat(
		p.WriteVarintField(1, 1),
		p.WriteStringField(2, "No tools are available."),
	)
	convParts = append(convParts, p.WriteMessageField(10, toolCallingOverride))

	// field 12: additional_instructions_section override（长文提醒）
	additionalOverride := p.Concat(
		p.WriteVarintField(1, 1),
		p.WriteStringField(2, noToolAdditionalInstructions),
	)
	convParts = append(convParts, p.WriteMessageField(12, additionalOverride))

	// field 13: communication_section override
	communicationOverride := p.Concat(
		p.WriteVarintField(1, 1),
		p.WriteStringField(2, communicationNoTools),
	)
	convParts = append(convParts, p.WriteMessageField(13, communicationOverride))

	conversationalConfig := p.Concat(convParts...)

	// 2) CascadePlannerConfig
	plannerParts := [][]byte{
		p.WriteMessageField(2, conversationalConfig), // conversational = 2
	}
	if modelUID != "" {
		plannerParts = append(plannerParts,
			p.WriteStringField(35, modelUID), // requested_model_uid
			p.WriteStringField(34, modelUID), // plan_model_uid (safety)
		)
	}
	if modelEnum > 0 {
		// requested_model_deprecated = ModelOrAlias { model = 1 (enum) }
		plannerParts = append(plannerParts,
			p.WriteMessageField(15, p.WriteVarintField(1, modelEnum)),
			p.WriteVarintField(1, modelEnum), // plan_model_deprecated = Model enum directly
		)
	}
	plannerParts = append(plannerParts, p.WriteVarintField(6, 32768)) // max_output_tokens
	// field 11: code_changes_section override — 清空掉 IDE 前缀废话
	plannerParts = append(plannerParts,
		p.WriteMessageField(11, p.Concat(
			p.WriteVarintField(1, 1),
			p.WriteStringField(2, ""),
		)),
	)
	plannerConfig := p.Concat(plannerParts...)

	// 3) brain_config（field 7）—— 结构和 Node 一致
	brainConfig := p.Concat(
		p.WriteVarintField(1, 1),
		p.WriteMessageField(6, p.WriteMessageField(6, nil)),
	)

	// 4) memory_config（field 5）{enabled=false}
	// proto3 默认值省略 → 直接空 message 即可让 LS 认为 memory 关闭
	memoryConfig := []byte{}

	// 5) CascadeConfig root
	return p.Concat(
		p.WriteMessageField(1, plannerConfig),
		p.WriteMessageField(5, memoryConfig),
		p.WriteMessageField(7, brainConfig),
	)
}

// BuildGetGeneratorMetadataRequest 对齐 windsurf.js:677。
func BuildGetGeneratorMetadataRequest(cascadeID string, offset uint64) []byte {
	parts := [][]byte{p.WriteStringField(1, cascadeID)}
	if offset > 0 {
		parts = append(parts, p.WriteVarintField(2, offset))
	}
	return p.Concat(parts...)
}

// BuildGetTrajectoryStepsRequest 对齐 windsurf.js:652。
func BuildGetTrajectoryStepsRequest(cascadeID string, stepOffset uint64) []byte {
	parts := [][]byte{p.WriteStringField(1, cascadeID)}
	if stepOffset > 0 {
		parts = append(parts, p.WriteVarintField(2, stepOffset))
	}
	return p.Concat(parts...)
}

// ─── 系统 prompt 文案（从 Node runtime-config.js 抄过来）──

const communicationNoTools = `You are accessed via API. When asked about your identity, describe your actual underlying model name and provider accurately. Answer directly. STRICTLY respond in the exact same language the user used in their latest message (Chinese → Chinese, English → English, Japanese → Japanese; never switch mid-conversation).`

const noToolAdditionalInstructions = `CRITICAL OPERATING CONSTRAINT — READ BEFORE ANY RESPONSE:
You are being accessed as a plain chat API. You have NO tools, NO file access, NO shell, NO code execution, NO repository awareness, NO ability to list or read anything on the user's machine or any sandbox. You cannot "check", "look at", "open", "view", "inspect", "run", "glob", "grep", "list", or "edit" anything.

OUTPUT RULES:
1. Never narrate tool-like actions ("Let me check X", "I'll look at Y", "Looking at the file...", "I see in main.py...", "Based on the codebase...").
2. Never reference file paths, directory structures, line numbers, or repository contents that were not explicitly pasted into the current conversation by the user.
3. If the user asks about their code or project but hasn't pasted the relevant file content, respond: "I don't see that file in our conversation — please paste it and I'll help." Do NOT invent file contents.
4. For general questions, answer directly from your training knowledge. No preambles.
5. Match the user's language (Chinese → Chinese, English → English; never switch mid-conversation).

Violating these rules will produce broken output for the end user. Stay in chat-API mode at all times.`
