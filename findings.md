# Findings

## 2026-05-10
- Current real acceptance must be send-and-receive, not just LS startup or `GetUserStatus`.
- Prior smoke reached `SendUserCascadeMessage`, which means LS startup and account metadata are mostly working.
- Current failure says model fields are not recognized by LS: `neither PlanModel nor RequestedModel specified`.
- Local macOS LS does not expose `/UpdatePanelStateWithUserStatus`; it returns HTTP 404. That RPC cannot be required for send warmup.
- Node catalog shows `claude-4.5-sonnet` has both uid `MODEL_PRIVATE_2` and enum `353`; useful to distinguish uid-only model failures from cascade_config layout failures.
- App-bundled macOS LS is old and lacks planner uid fields. It can see enum fields but cannot accept uid-only models like Claude 4.5 Haiku / 4.6 / 4.7.
- `~/.windsurf/language_server_macos_arm64` has the uid fields. With that binary, model validation progresses, but cold-start warmup must stay minimal to avoid the LS manager's 2s child health check failure.
- Service-level success must be judged by `/v1/chat/completions` returning OpenAI-compatible assistant text, not only LS process startup or `cmd/ls-smoke`.
- `data/windsurf.db` was empty before E2E. The server path requires at least one enabled, unbanned account with `firebase_token` populated.
- `config.Load` now supports runtime LS discovery: explicit YAML value first, then `LS_BINARY_PATH`, then `$HOME/.windsurf/language_server_macos_arm64` if present.
- Local port 3456 is currently occupied by the Node service. Go server E2E used port 3466.
- Redis connection failure is non-fatal for the current single-process E2E path.
- `/v1/chat/completions` E2E passed on 2026-05-10 22:26 CST with `claude-4.5-haiku`, prompt `Reply with exactly: hi`, HTTP `200 OK`, assistant text `hi`, usage `prompt_tokens=1949`, `completion_tokens=4`.
- Model E2E sweep on 2026-05-10 22:49 CST passed for all 10 configured Claude models. Each request used `/v1/chat/completions`, prompt `Reply with exactly: hi`, and returned HTTP `200` with assistant text `hi`.
- After adding the HA handler path, immediate post-ready warmup could still hit the newer LS manager's child health-check window and surface as `SendUserCascadeMessage ... unexpected EOF`. Waiting 2.5s from LS process start before panel init avoids that cold-start race in local E2E.
- Debug account state must never embed `firebase_token`; expose `token_set` instead. `/debug/accounts` was corrected before final verification.
- First-pass Go HA is intentionally single-process in-memory scheduling. SQLite persists account metadata and cooldowns; Redis distributed reservations are still deferred.
- 2026-05-10 23:40 CST HA E2E sweep passed for all 10 configured Claude models after scheduler/retry/LS/reuse/debug changes.

## 2026-05-10 晚间
- 接手用户的新要求：停止使用 Haiku 验证，改用 claude-sonnet-4.6。
- 检查 data/windsurf.db：当前有 12 个 enabled 且未 banned 账号，全部 proxy_url 为空。
- 本地 3469 端口仍有旧 server 进程监听，后续测试前需要停止，避免读到旧代码路径。

## 2026-05-11
- account.txt 中 11 个 token 都被 LS 上游判定为 Invalid token，并已禁用；历史账号 id=1 是当前唯一 Sonnet 4.6 可用账号。
- 多账号 LS 隔离已验证没有再出现 panel state not found；当前主要瓶颈不是调度，而是账号池有效容量。

## 2026-05-11 Node Docker 对比结论
- Node Docker 日志显示账号 s.angh... 导入后 `GetUserStatus` 成功：tier=pro plan=Trial allowed=152，并且 Chat claude-sonnet-4.6 可走 cascade。
- Go 失败点不是 token 原始无效，而是 chat 前缺少 Node probe 路径的 `GetUserStatus -> UpdatePanelStateWithUserStatus` 预热，导致 SendUserCascadeMessage 报 failed to get primary API key。
- 已把 GetUserStatus priming 加进 Go 的 workspace init 链路。

## 2026-05-11 importer 251-token bug
- The earlier invalid-token conclusion was caused by Go import parsing, not the account. The bad DB value included `----auth1_...` after the actual Devin token.
- Correct account.txt format has 4 fields: email, password, token, auth1. The persisted credential must be exactly field 3.
- After fixing import and reimporting, the same account passed Sonnet 4.6 send-and-receive in Go.

## 2026-05-11 Plan B cloud-direct chat
- Direct non-chat cloud RPCs already work without LS: `GetUserStatus`, `CheckUserMessageRateLimit`, and `GetCascadeModelConfigs`.
- Cloud-direct `LanguageServerService/StartCascade` fails with HTTP 404 on Windsurf cloud hosts, so local LS Cascade service paths are not directly exposed as production cloud endpoints.
- The LS binary descriptor exposes `ApiServerService/GetChatMessage`; request fields include metadata=1, prompt=2, repeated `ChatMessagePrompt`=3, request_type=7, chat_model_name=14, prompt_id=17, chat_model_uid=21. Response fields include delta_text=3, delta_thinking=9, usage=7, actual_model_uid=23.
- Added a gated `ProbeAPIChat` path for `ApiServerService/GetChatMessage`. It parses individual Connect/gRPC frames instead of joining server-streaming messages, so direct-chat protocol results can be diagnosed cleanly.
- `ApiServerService/GetChatMessage` succeeds with raw `application/grpc` but returns cloud internal error through Connect proto. Keep direct chat on raw gRPC regardless of the broader direct client's Connect setting.
- Production `/v1/chat/completions` remains LS-backed until this direct path returns real assistant text reliably.

## 2026-05-11 production hardening
- 当前 SQLite 有 12 个 enabled/unbanned/token_set 账号。`account-check -smoke=false -limit 3` 抽查 3 个均通过 direct GetUserStatus/rate-limit/model-config。
- 后台 health worker 跑完整 12 账号 direct 非聊天检查耗时约 15s，全部 ok；这不会消耗聊天 quota。
- 小规模 text/tools/tool-result smoke 覆盖 chat/messages/responses 均 100% 成功，且账号分布不是固定第一个账号。
- 同一轮小 smoke 中账号 11、12 触发 Sonnet 4.6 model rate limit，调度器按 rate_limit 分类设置 cooldown 并自动换号，后续 attempt 成功。
- 因为已有账号触发 10 分钟限流，100 并发压测应作为显式 quota-consuming 验收单独运行，不能在普通开发验证中默认执行。
- 进程检查没有 `language_server_macos_arm64`，Direct-only 主链路没有启动 LS。

## 2026-05-11 secret redaction
- Request/event errors are long-lived because they are stored in memory, persisted to SQLite, streamed to Dashboard logs, and exported as CSV/JSON/NDJSON. Redacting only at UI render time is insufficient.
- Stream errors are already committed HTTP responses, so their protocol-native error event must be redacted before write; OpenAI, Anthropic, and Responses each have separate writers.
- Direct client stats, scheduler events, proxy `last_error`, and health summaries are also operator-visible debug surfaces. They now redact independently so a future caller cannot bypass request stats redaction.
- The redaction helper intentionally replaces full `Authorization:`/`Cookie:` lines rather than trying to preserve subcomponents; this may remove adjacent identifiers such as email addresses in the same header line, which is acceptable and safer.

## 2026-05-11 real smoke and account capacity
- 当前 SQLite 有 12 个 active 账号；direct 非聊天健康检查全部通过，说明账号 token、本地导入格式、Direct host 和模型配置链路是通的。
- `claude-sonnet-4.6` 真实 full smoke 已覆盖 chat/messages/responses 的 text、stream、tool call 和 tool-result continuation，并且过程中没有启动 `language_server_macos_arm64`。
- 小并发真实请求可以正常跑通并分散到多个账号；Go 调度没有集中打第一个账号。
- 20 并发/50 请求触发的是 Windsurf/Claude 上游官方模型限流：错误文本包含 `Reached message rate limit for this model ... Resets in: 30m0s`。这不是 Go 进程崩溃，也不是 LS 依赖问题。
- 调度器能把这类错误分类为 `rate_limit`，自动切账号，释放 inflight，并把账号-模型 cooldown 持久化到 SQLite。当前这批账号的 Sonnet 4.6 cooldown 最晚到 2026-05-11 10:56:38 CST。
- 因为当前账号看起来偏 Trial/free-like，不能用这 12 个账号证明 50/100 真实并发稳定通过；要么增加更高额度账号/代理隔离，要么降低每账号并发和 RPM，再做正式容量验收。

## 2026-05-11 controlled model fallback testing
- 用户要求真实测试不要全账号一起打：应按小批账号测试，模型限流时切模型，小批不可用再换下一批，并且测试要收敛。
- 新增的 `X-Windsurf-Test-Account-IDs` 只在 localhost 生效，适合本地真实验收，不暴露为公网强制指定账号能力。
- 账号组 `1,2,3` 在 `claude-4.5-sonnet` 和 `claude-opus-4.6` 上均触发 Windsurf 上游模型限流；错误包含 reset 时间，Go 将其写为账号-模型 cooldown。
- 账号组 `4,5,6` 在 `claude-opus-4-7-medium` 上也触发模型限流。受控工具只发了 2 个真实请求后停止，没有继续消耗其它账号。
- `cmd/load-smoke -mode controlled` 现在会为每个模拟用户维护 per-route 历史，把上一轮 assistant 期望输出放进下一轮请求，能更接近 Claude Code/Cline 这种多人反复对话，而不是只发一次性 prompt。
- 账号组 `7,8,9` 使用 `claude-4.5-sonnet` 跑了受控成功样本：1 个用户、2 轮历史、最后 tool-result continuation，共 3 个真实请求，全部 200。由于串行低并发和调度排序，3 个请求集中在账号 8；这不代表并发分布失败，之前 6/12 和 10/10 小并发已验证分散。
- 这些结果说明当前账号池不只是 Sonnet 4.6 被限，多个 Claude 模型在这些 free/trial 账号上也存在共享或模型级限制。Go 的改进点在于能识别、持久化和停止扩散；容量不足本身需要账号额度或测试节奏解决。
- 所有受控测试后 `inflight=0`，未启动 LS；这是 Go 调度和 Direct-only 主链路稳定性的正向证据，但不是 50/100 并发容量通过的证据。

## 2026-05-11 Redis / nginx
- 本地专用 Redis 容器 `windsurfapi-go-redis` 已用于真实 coordinator smoke，端口为 `127.0.0.1:6380`。
- Redis coordinator 的生产关键点不能只依赖 `CanReserve -> INCR` 两步逻辑；多实例高并发下会有读写竞态。实际占位必须是 Redis 原子操作。
- 已把 `RedisCoordinator.Reserve` 改为 Lua 原子脚本：同一脚本内完成 cooldown 检查、max inflight 检查、inflight 递增、RPM zset 写入和 TTL 设置。
- Node 版 nginx 不是业务逻辑组件，主要是多副本反向代理：按 Authorization token sticky 到同一 Node 进程以保住内存 cascade/reuse，上游 SSE 关闭 buffering，并提供 25MB body cap 和基础 IP rate limit。
- Go 单实例不需要 nginx；Go 多实例如果启用 Redis coordinator，sticky 不再是核心正确性依赖，但仍需要反向代理处理 TLS、SSE 不缓冲、外层限流和健康检查。

## 2026-05-11 Claude Code real client
- Claude Code 2.1.138 sends `POST /v1/messages?beta=true` with `x-anthropic-billing-header`, Claude Agent SDK identity text, CWD/Date system blocks, and a leading `<system-reminder>` in user content. Forwarding these verbatim to Windsurf Direct can trigger upstream `policy_blocked`.
- Normalizing the Claude Code payload is enough for benign requests to pass: strip billing headers, neutralize Claude identity wording, preserve useful CWD/Date facts, and strip `system-reminder` wrappers from user text.
- Anthropic streaming tool calls must use separate content blocks. If text is streamed first, a later tool call must close the text block and start a `tool_use` block before emitting `input_json_delta`; otherwise Claude Code reports `Content block is not a input_json block` and falls back to non-streaming.
- A blanket “suppress tools after tool_result” policy is too aggressive for real Claude Code. It prevents second-stage tools such as Bash after Read results and causes the model to output textual `<tool_calls>` instead of native tool_use. The better rule is to keep tools available and rely on transcript no-repeat instructions plus tool_choice pruning.
- Real Claude Code long-chain smoke through Go Direct now works: Read task_plan.md, Read PRODUCTION.md, Bash `go test ./internal/account -count=1`, then final answer. This proves the `/v1/messages` adapter, native tool_use streaming, tool_result continuation, scheduler, Redis coordinator-enabled server path, and no-LS Direct path are compatible with Claude Code for this workflow.
- Cline was not available as a local CLI (`cline not found`), so Cline-specific real-client validation remains a manual/client-side follow-up rather than evidence gathered in this environment.

## 2026-05-11 dynamic proxy binding gap
- Node `src/dynamic-proxy.js` has a separate account-level dynamic proxy binding lifecycle, not just a shared proxy pool. It stores one binding per account with status, generated session id, expiry, verification metadata, egress IP/location/ISP, fail count, and timestamps.
- Node active proxy lookup only returns `status=active` and marks expired bindings as `expired`; returned dynamic bindings are strict account-specific proxies, so direct fallback does not hide an IP/proxy failure.
- Node supports bind, rotate, verify, clear, suspend, resume, runtime failure marking with optional auto rebind, auto-bind-new-account, and a maintenance plan for failed/expired, expiring-soon, and unbound accounts.
- Go currently has `proxy_pool` plus manual/bulk writes to `accounts.proxy_url`. That is useful but lacks durable per-account binding state, expiry/renewal, verification metadata, and automatic rebind on proxy failure.
- Implementation should preserve `accounts.proxy_url` as a static manual override, then prefer active account dynamic binding before static/account/global pool in the Direct request path.

## 2026-05-11 dynamic proxy binding completion
- Node's dynamic proxy verification target is `https://ipinfo.io/json`, not the Windsurf API host. Go now uses the same default so account bindings can store egress IP, country, region, city, and ISP metadata.
- Active account dynamic bindings should sit above static `accounts.proxy_url` in the effective proxy chain. Static account proxy remains useful as manual compatibility state, but a valid active binding represents the current account-specific IP identity.
- Proxy identity changes must purge account availability state. Without this, a new session/IP can inherit old cooldowns, model breakers, and recent transport failures from the previous IP, reducing availability and confusing operators.
- The maintenance worker should be bounded by `worker_concurrency`; sequential renewal is too slow for many expiring bindings, while unbounded renewal can stampede the proxy provider.
- Local `httptest` proxy verification is blocked by SSRF protection unless `allow_private` is explicitly true. Tests use that escape hatch; production defaults remain private-host safe.
