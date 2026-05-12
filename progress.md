# Progress

## 2026-05-10
- Started focused pass on Cascade send smoke.
- Read current Go builders/client/model/smoke files and Node reference locations.
- First real smoke failed before send: macOS LS returns 404 for `UpdatePanelStateWithUserStatus`.
- Changed panel init to treat `GetUserStatus`/`UpdatePanelStateWithUserStatus` as best-effort, matching Node warmup behavior.
- Added Claude 4.5 Sonnet (`MODEL_PRIVATE_2`, enum `353`) as an enum-backed diagnostic model.
- Newer `~/.windsurf/language_server_macos_arm64` contains `plan_model_uid/requested_model_uid`; App-bundled `/Applications/.../language_server_macos_arm` is too old for uid-only Claude models.
- Real smoke against newer LS passed model validation but LS manager killed the child during cold start. Removed pre-warm `GetUserStatus/UpdatePanelStateWithUserStatus` from chat warmup and added Node-style `Heartbeat`.
- Added runtime LS binary discovery in `internal/config`: YAML value wins, then `LS_BINARY_PATH`, then `$HOME/.windsurf/language_server_macos_arm64`.
- Added tests for runtime LS discovery and raw `firebase_token` account selection.
- Imported one active Node SQLite account into Go `data/windsurf.db` for local E2E verification.
- Ran Go server on temporary port 3466 because Node service already owns 3456.
- Verified `/v1/chat/completions` end-to-end: HTTP `200 OK`, model `claude-4.5-haiku`, assistant content `hi`, usage `prompt_tokens=1949`, `completion_tokens=4`.
- `go test ./...` passes after the config/account test additions.
- Ran a full configured-model E2E sweep on temporary port 3467. All 10 models returned HTTP `200` and assistant text `hi`: `claude-4.5-haiku`, `claude-4.5-sonnet`, `claude-sonnet-4.6`, `claude-sonnet-4.6-thinking`, `claude-opus-4.6`, `claude-opus-4.6-thinking`, `claude-opus-4-7-medium`, `claude-opus-4-7-high`, `claude-opus-4-7-xhigh`, `claude-opus-4-7-max`.
- Implemented first-pass production scheduler in `internal/account`: Reserve/Release/Refund, RPM windows, inflight balancing, quota score ordering, model cooldowns, and debug snapshots.
- Extended LS pool to select by account/proxy key, inject proxy env, expose generation/debug state, and enforce `max_instances`.
- Reworked chat handler around reserve -> ensure LS -> send -> classify -> retry -> release, with delayed SSE header emission before the first delta.
- Added error classification, read-only debug endpoints, and a basic conversation reuse pool keyed by normalized message fingerprints and bound to account + LS generation.
- Fixed debug account output to avoid leaking raw `firebase_token`.
- Added a 2.5s LS post-ready grace to avoid the newer LS manager cold-start `unexpected EOF`/health-check race.
- Verification passed: `go test ./...`, `/debug/accounts`, `/debug/ls`, `/debug/scheduler`, and a 10-model `/v1/chat/completions` E2E sweep on temporary port 3468.

## 2026-05-10 晚间接手
- 已读取调度器、LS pool、chat handler、错误分类、配置、load-smoke 与现有计划记录。
- 下一步修改：account-level LS isolation for no-proxy accounts、invalid token 分类、max_instances 默认值、逐账号验证工具。

## 2026-05-11 真实账号验证
- `go test ./...` 通过。
- `cmd/account-check -model claude-sonnet-4.6 -limit 2`：账号 1 成功返回 hi；账号 2 invalid token。
- `cmd/account-check -model claude-sonnet-4.6 -apply`：12 个候选账号里 1 个可用、11 个 invalid token；invalid 账号已自动 banned/disabled。
- 当前不能做高并发压测，因为有效账号池实际只有 1 个。

## 2026-05-11 HTTP smoke
- 临时服务 3470 上第二次 HTTP `/v1/chat/completions` 使用 `claude-sonnet-4.6` 返回 200，content=hi，usage prompt_tokens=1957/completion_tokens=4。
- 发现第一次 HTTP 请求受 LS manager 冷启动自杀影响返回错误；已增加 pre-send transport/upstream retry，Refund reservation 后允许同账号重试，避免唯一可用账号被排除。

## 2026-05-11 最终验证
- 重启临时服务 3470 后，`curl /v1/chat/completions` 使用 `claude-sonnet-4.6` 返回 HTTP 200，content=hi，usage prompt_tokens=1957/completion_tokens=4。
- `/debug/ls` 显示请求使用 `acct_1`，不是 `default`，证明无代理账号也走 account-level LS isolation。
- `/debug/accounts` 显示 enabled 可用账号仅 id=1，disabled_count=11。

## 2026-05-11 Node Docker vs Go token debug
- 用户反馈同一个在 Go 中 Invalid token 的账号，导入已启动 Node Docker 后可用。开始对比 Node Docker 实际请求链路与 Go 请求链路。

## 2026-05-11 importer bugfix
- Root cause: Go `cmd/import-accounts` used regex over the whole line. Because token regex allowed `-`, it swallowed the `----auth1_xxx` fourth field into the token. Parsed token_len was 251 instead of the correct third-field token_len 189.
- Node dashboard text import splits by `----` first and persists only the third field as api_key; Docker accounts.json confirms `s.angh...` persisted key_len=189 tail=QTsYb6j9iuZg.
- Fixed Go importer to split `----` lines and extract token only from field 3. Dry-run now reports token_len=189.
- Reimported `s.angh...`, temporarily disabled id=1, and `cmd/account-check -model claude-sonnet-4.6` returned ok text=hi for account id=2. Restored id=1 enabled afterward.

## 2026-05-11 Plan B direct probe
- Implemented experimental cloud-direct `ApiServerService/GetChatMessage` client path in `internal/windsurf/direct`.
- Added `cmd/direct-smoke -probe-api-chat` and `-api-chat-request-type`; default smoke still only runs safe account/status/model-config checks and skips quota-consuming chat probes.
- Added protobuf wire tests for `GetChatMessageRequest`, `ChatMessagePrompt`, and response parser fields `delta_text`, `delta_thinking`, and `actual_model_uid`.
- User and local reproduction showed Connect proto returned internal error, while `-raw-grpc` returned frames=5 and text="hi" for `claude-sonnet-4.6`. Updated `ProbeAPIChat` to always use raw gRPC.

## 2026-05-11 direct-only implementation
- Started converting server chat path from LS-backed Cascade to direct cloud `ApiServerService/GetChatMessage`.
- Implementation constraint: OpenAI-compatible API remains; LS code remains legacy but must not be used by default chat path.
- Tool-call constraint: native tool_call support must be verified from descriptors/real responses; no silent text downgrade as final behavior.
- Added `chat.backend: direct` default config and rewired `cmd/server` to create `direct.Client` for `/v1/chat/completions`; LS pool remains only for legacy `/debug/ls` and smoke/debug tools.
- Added `/debug/direct` exposing host/protocol/recent success/failure stats.
- Production chat path now uses scheduler reservation + direct raw gRPC `ApiServerService/GetChatMessage`; text non-stream and stream both return OpenAI-compatible responses.
- Direct requests with `tools`, `tool_choice`, assistant `tool_calls`, or tool-result messages currently return HTTP 501 with an explicit native-tool-field verification message. This avoids silently degrading Claude Code tool use into plain text.
- Real direct frame dump showed top-level `field 3` is `delta_text`, `field 7` is upstream metadata with nested usage/model fields, and later `field 28` is token usage display metadata. Parser was tightened accordingly.
- Fixed prompt flattening: single user message is sent as raw content; multi-turn requests use explicit context/latest-message boundaries so Claude does not continue a fake `User:` dialogue.
- Verification passed:
  - `go test ./...`
  - `go run ./cmd/direct-smoke -account 1 -model claude-sonnet-4.6 -timeout 45s -probe-api-chat` returned `text="hi"` through `server.codeium.com` raw gRPC.
  - Temporary server on port 3474 returned HTTP 200 non-stream `content="hi"` for `claude-sonnet-4.6`.
  - Temporary server on port 3474 returned SSE role chunk, content delta, finish chunk with usage, and `[DONE]`.
  - Tools request returned HTTP 501 as expected.
  - `/debug/ls` stayed `[]` and process list showed no `language_server_macos_arm64`, proving the main chat path did not start LS.

## 2026-05-11 native direct tools
- Verified `ApiServerService/GetChatMessage` native tool fields with real `claude-sonnet-4.6`: request uses `tools` field 10, `disable_parallel_tool_calls` field 11 when needed, and `tool_choice` field 12; response streams `delta_tool_calls` field 6.
- Fixed tool-call parsing: upstream streams one tool call as id/name first, then fragmented `arguments_json`; Go now aggregates fragments into one logical `windsurf.ToolCall`.
- `/v1/chat/completions` no longer returns 501 for OpenAI function tools. It maps OpenAI `tools` and `tool_choice` to direct fields, maps upstream tool calls back to OpenAI `message.tool_calls`, and returns `finish_reason="tool_calls"`.
- Streaming now emits OpenAI-compatible `delta.tool_calls` chunks and finishes with `finish_reason="tool_calls"`.
- Tool-result continuation now works for the common Claude Code two-step path: if the latest client message is a `tool` result and no specific function is forced, the server suppresses tools on that continuation so Claude answers from the result instead of repeating the same call.
- Current limitation: continuation still relies on prompt-flattened transcript, not a fully decoded native `GetChatMessageRequest` conversation-history schema. Multi-step tool planning should be revisited once the native history fields are mapped.
- Verification passed:
  - `go test ./...`
  - `go run ./cmd/direct-smoke -account 1 -model claude-sonnet-4.6 -timeout 60s -probe-api-chat` returned `text="hi"`.
  - `go run ./cmd/direct-smoke -account 1 -model claude-sonnet-4.6 -timeout 60s -probe-tools` returned one `echo_text` tool call with complete JSON args.
  - Temporary server on port 3476 returned HTTP 200 non-stream `message.tool_calls` for a tools request.
  - Temporary server on port 3476 returned streaming `delta.tool_calls` chunks.
  - Temporary server on port 3476 accepted a tool-result continuation and returned final text `SECOND_LEG_OK`.

## 2026-05-11 Node parity core routes
- Refactored chat handler around a shared `executeDirectChat` core so OpenAI, Anthropic Messages, and Responses routes all use the same scheduler, retry, cooldown, and direct cloud client.
- Added `POST /v1/messages` with Claude Code-oriented Anthropic compatibility:
  - text request/response
  - `tool_use` and `tool_result` conversion
  - Anthropic SSE events: `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`
- Added `POST /v1/responses` minimum compatibility:
  - string input and message-array input
  - function tools and function_call output
  - simple Responses SSE events
- Added Node compatibility details:
  - `Cache-Control: no-store` on API responses
  - `X-Request-ID` / `Request-Id` headers from auth middleware
  - `tool_choice` aliases `any` / `required` / `none` / `tool`
  - reuse fingerprint helper now supports route/tools/tool_choice digests
- Updated `cmd/account-check` to use direct GetUserStatus/rate-limit/model-config checks and optional direct sonnet smoke; it no longer needs LS for health checks.
- Extended `cmd/load-smoke` with `text`, `tools`, and `tool-result` scenarios.
- Verification passed:
  - `go test ./...`
  - `/v1/chat/completions` text returned `hi`
  - `/v1/messages` text returned Anthropic content block `hi`
  - `/v1/messages` tool request returned `stop_reason=tool_use`
  - `/v1/messages` tool-result continuation returned final text `MSG_RESULT_OK`
  - `/v1/messages` stream returned standard Anthropic event sequence
  - `/v1/responses` text returned `object=response`
  - `/v1/responses` function tool returned `function_call`
  - `/v1/responses` stream returned `response.created`, text delta, and `response.completed`
  - `cmd/load-smoke -scenario tool-result -concurrency 2 -requests 4` succeeded 4/4
  - `cmd/account-check -limit 1 -smoke=false` succeeded through direct account checks
  - Process check showed no `language_server_macos_arm64` in the main path.

## 2026-05-11 production P0/P1 hardening
- Added config knobs for direct hosts/timeouts and background account health refresh.
- Added account health writeback fields in scheduler snapshots.
- Added a direct health worker that checks user status, rate limit, and model config without quota-consuming chat.
- Added `/healthz` and `/readyz`, plus graceful HTTP shutdown with request draining.
- Switched structured request logs to include req_id, route, account_id, attempt, latency, error class, usage, and tool_call_count.
- Added debug-safe response headers `X-Windsurf-Account-ID` and `X-Windsurf-Attempt` for load-smoke account distribution.
- Reworked direct transcript flattening into an explicit transcript builder and added tests for three tool rounds and repeated tool_call id prevention.
- Extended `cmd/load-smoke` to cover `/v1/chat/completions`, `/v1/messages`, and `/v1/responses` with text/tools/tool-result scenarios.
- Extended account import dry-run reports with duplicates, invalid lines, and proxy column parsing.
- Added Dockerfile, docker-compose, and PRODUCTION.md with capacity baseline and deployment caveats.
- Verification passed: `go test ./...`; account-check direct health dry-run for 3 accounts; temporary server `/healthz` and `/readyz`; debug accounts health summary; route-aware load-smoke for chat/messages/responses text and selected tools/tool-result scenarios.
- Temporary smoke observed two per-account Sonnet rate-limit errors; retry switched accounts and all smoke requests still succeeded. Full 100-concurrency run was intentionally not executed to avoid quota burn without explicit approval.
- Added optional Redis scheduler coordinator for P2 multi-instance foundations. It is disabled by default and can share per-account inflight, cooldown, and short RPM reservation state when `scheduler.redis_enabled=true`.

## 2026-05-11 non-load production parity cleanup
- Added broader `tool_choice` compatibility for Anthropic Messages and OpenAI Responses shapes, including `{"type":"tool","name":"..."}` and direct function-name forms.
- Added Responses input support for top-level `input_text` and `output_text` items.
- Expanded Responses streaming event shape with output item/content part added/done and function-call argument done events.
- Extended `/debug/scheduler` to expose health summary and coordinator status.
- Added `scripts/client-regression.sh` for small end-to-end client compatibility checks after server startup.
- Added `deploy/windsurfapi-go.service` as a systemd deployment template.
- Verification passed: `go test ./...`. The new regression script was not run automatically because it sends real chat requests.

## 2026-05-11 Codex continuation
- Resumed from existing Node parity checklist and prior partial blocked-model patch.
- Verified baseline `go test ./...` was green before new edits.
- Added scheduler test coverage for account-level blocked models: blocked model is skipped while unrelated models remain available.
- Added `/auth/accounts/:id/models` handler test coverage and confirmed snapshot exposes blocked_models.
- Added Direct client account proxy plumbing: chat requests pass account `proxy_url`; direct client creates per-proxy HTTP clients with HTTP/2 enabled and masks proxy credentials in debug stats.
- Verification passed: `go test ./...`.
- Extended proxy support so health worker and CLI account checks use each account's `proxy_url`, matching the direct chat path.
- Rebuilt Dashboard as a usable React operations console: account import/edit/delete, enable/disable, tier/proxy/notes editing, blocked model management, model catalog table, direct proxy/client stats, recent requests, and request stats.
- Fixed account PATCH semantics so empty `notes` and `proxy_url` can be intentionally cleared.
- Added in-memory request stats/log window for the shared direct chat path and exposed it via `/dashboard/api/stats`, `/dashboard/api/logs`, and `/debug/scheduler`.
- Updated `docs/node-parity-go-superiority-plan.md` checklist for blocked models, account proxy, Dashboard account management, model basics, requests/logs, and stats.
- Verification passed: `go test ./...`; `npm ci && npm run build` in `web/dashboard`; final `go test ./...` after embedded Dashboard build.
- Added runtime env overrides for deployment: port, API keys, DB, Redis, direct hosts/timeout, health settings, scheduler Redis/max-inflight/TTL, dashboard password, default proxy, and log level.
- Added global default proxy support in `direct.Client`; account `proxy_url` still takes priority.
- Added caller key hash to request stats/log records without exposing raw caller keys.
- Updated Docker Compose and systemd examples with key env variables.
- Verification passed again: `go test ./...`.

## 2026-05-11 model access parity
- Added persistent `model_access` SQLite table and `internal/modelaccess` manager.
- `/v1/models` now respects global model visibility; chat/messages/responses reject globally disabled models before scheduler reservation.
- Added `GET|PUT|PATCH|DELETE /auth/models/:id/access` and `/dashboard/api/model-access`.
- Dashboard Models table can toggle visible/enabled/deprecated, edit unsupported reason, and reset overrides.
- Updated Node parity checklist for global model visibility/model-access.
- Verification passed: `go test ./...`; `npm ci && npm run build` in `web/dashboard`.

## 2026-05-11 runtime config basics
- Added `internal/runtimeconfig` manager with masked config snapshots and validated runtime patches.
- Added `GET|PATCH /dashboard/api/config` for non-sensitive runtime config: direct hosts/timeout, health settings, scheduler settings, default proxy, and log level.
- Added Dashboard Settings panel for the same runtime fields and deployment status for sensitive config presence.
- Updated Node parity checklist for Settings/runtime config basics.
- Verification passed: `go test ./...`; `npm ci && npm run build` in `web/dashboard`.

## 2026-05-11 dashboard auth basics
- Added `DashboardAuthMiddleware`: Dashboard/Admin APIs accept API key or `X-Dashboard-Password`.
- Added non-local fail-closed behavior when dashboard password is empty or the default `admin`.
- Dashboard UI now supports separate Dashboard password input and prefers it for `/dashboard/api/*` and `/auth/*` calls.
- Docker Compose and systemd examples now include `WINDSURFAPI_DASHBOARD_PASSWORD`.
- Updated checklist for Dashboard independent auth and fail-closed basics.
- Verification passed: `go test ./...`; `npm run build` in `web/dashboard`.

## 2026-05-11 scheduler availability hardening
- Added in-memory recent failure windows per account/model in the scheduler.
- Added model breaker behavior: repeated transient/transport/model-unavailable failures on the same account/model open a short breaker and Reserve skips that account/model until it expires.
- Added drought mode and low-quota penalty: very low quota accounts are visible in snapshots and are deprioritized while healthier accounts can still absorb traffic.
- Scheduler snapshots now expose `drought`, `drought_penalty`, `model_breakers`, and `recent_errors` for Dashboard/debug visibility.
- Added scheduler Redis fail-closed config: `scheduler.redis_fail_closed` and `WINDSURFAPI_SCHEDULER_REDIS_FAIL_CLOSED`; when enabled, startup refuses to fall back to single-process scheduling if Redis is unavailable.
- Updated Dashboard Settings to expose Redis fail-closed and rebuilt embedded dashboard assets.
- Updated deployment examples and `PRODUCTION.md` with the Redis fail-closed production recommendation.
- Verification passed: `go test ./...`; `npm ci && npm run build` in `web/dashboard`.

## 2026-05-11 Node catalog parity
- Migrated the Node model catalog snapshot into Go as `internal/models/node_catalog.go`, including GPT, Gemini, Grok, Kimi, GLM, MiniMax, SWE, deprecated models, aliases, provider metadata, model UID/enum, and credit multipliers.
- Kept production routing conservative: only models marked `DirectSupported` are exposed through `/v1/models` and allowed by chat/messages/responses.
- Added explicit unsupported behavior for cataloged-but-unverified models such as `gpt-5`; they are visible in Dashboard but fail fast with a clear direct-backend unsupported error instead of being misrouted.
- Dashboard Models table now shows credit and Direct support status.
- `modelaccess` defaults now inherit catalog unsupported/deprecated state so reset overrides cannot accidentally make non-Direct models callable.
- Verification passed: `go test ./internal/models ./internal/modelaccess ./internal/handler ./internal/windsurf/direct`; `npm ci && npm run build` in `web/dashboard`.

## 2026-05-11 dashboard auth hardening
- Added Dashboard/Admin brute-force lockout: 5 failed auth attempts from the same remote address trigger a 30-minute lockout with `429` and `Retry-After`.
- Successful Dashboard password auth clears the failure counter.
- Tightened Dashboard/Admin remote auth: API key fallback is only accepted for local requests; remote access must use `X-Dashboard-Password` or dashboard password as bearer token.
- Kept the existing non-local fail-closed behavior when dashboard password is empty/default.
- Verification passed: `go test ./internal/handler`.

## 2026-05-11 persistent request stats
- Added SQLite `request_events` table and indexes for persisted request stats/logs.
- `cmd/server` now wires the shared request stats store to SQLite at startup.
- Request events still keep a 500-entry in-memory window for fast Dashboard/debug reads, but are also inserted into SQLite asynchronously from the request path.
- On startup/configure, the stats store reloads recent persisted events into memory so Dashboard stats survive restarts.
- `/dashboard/api/stats` and `/debug/scheduler` now expose `persistent` and `db_error` fields.
- Verification passed: `go test ./internal/handler ./internal/store`.

## 2026-05-11 logs stream/export/filter
- Confirmed full `go test ./...` was green before continuing.
- Added shared request log filtering for Dashboard/API logs by `q`, `route`, `model`, `status`, `error_class`, `account_id`, `http_status`, `stream`, and `retry`.
- `/dashboard/api/logs`, `/dashboard/api/logs/export`, and `/dashboard/api/logs/stream` now all use the same filter semantics.
- Dashboard recent requests panel now has compact log filters, CSV/NDJSON export links that preserve filters, and filtered SSE log stream subscription.
- Rebuilt embedded Dashboard assets into `internal/dashboard/dist` and removed temporary `web/dashboard/node_modules`.
- Updated `docs/node-parity-go-superiority-plan.md` to mark logs stream/export/filter complete.
- Verification passed: `go test ./internal/handler`; `npm ci && npm run build` in `web/dashboard`.

## 2026-05-11 fake concurrency validation
- Strengthened the existing 100 goroutine scheduler test so it now verifies reservations spread across accounts and do not concentrate on one account, in addition to checking final inflight returns to zero.
- Added a two-manager shared-coordinator fake concurrency test to simulate multi-instance scheduling without real Redis or upstream quota usage.
- Found and fixed a scheduler edge case: when a coordinator reports account-level inflight full, `Reserve` now briefly queues/retries instead of immediately returning `no available accounts`.
- Updated the production checklist to mark fake direct 100 concurrency and multi-instance coordinator fake concurrency complete.
- Verification passed: `go test ./internal/account -count=1`.

## 2026-05-11 Anthropic thinking strategy
- Added `Thinking` to the shared `windsurf.ChatResult` and direct `ChatRequest`.
- Direct chat now accumulates upstream field-9 thinking deltas and can stream them through a dedicated callback.
- `/v1/messages` now parses Anthropic `thinking` as disabled/enabled/auto with optional `budget_tokens`, injects a direct prompt instruction when enabled, returns non-stream `thinking` content blocks, and emits stream `thinking_delta` events.
- At this point OpenAI-compatible chat still kept thinking private; a later OpenAI reasoning pass added opt-in `reasoning_content` for clients that send `reasoning_effort` / `reasoning.effort`.
- Updated `docs/node-parity-go-superiority-plan.md` to mark Anthropic thinking strategy complete.
- Verification passed: `go test ./internal/windsurf/direct ./internal/handler -count=1`.

## 2026-05-11 reuse cache dashboard snapshot
- Replaced the placeholder `/dashboard/api/cache` response with a real reuse pool snapshot: enabled flag, entry count, stats, and entries.
- Added handler coverage for the cache snapshot so Dashboard no longer reports hard-coded fake cache state.
- Updated the Node parity matrix to mark stats/logs/cache basics complete.
- Verification passed: `go test ./internal/handler -count=1`.

## 2026-05-11 dynamic proxy persistence
- Added SQLite `proxy_pool` table and indexes so Dashboard-managed dynamic proxies survive server restarts.
- Wired `cmd/server` to pass the existing SQLite handle into `proxy.Manager`.
- `proxy.Manager` now loads dynamic proxies from DB at startup, persists add/patch/delete/test/success/failure state, and keeps API snapshots masked.
- Dynamic proxy snapshot now exposes `persistent=true` when backed by SQLite.
- Updated `docs/node-parity-go-superiority-plan.md` to mark dynamic proxy basics complete.
- Verification passed: `go test ./internal/proxy ./internal/handler ./cmd/server`; final `go test ./...`; `web/dashboard/node_modules` absent.

## 2026-05-11 Codex parity continuation
- Re-read the Node parity/superiority checklist and resumed only non-quota work.
- Strengthened direct conversation reuse for tool chains:
  - Added before/after fingerprints so successful assistant tool calls are stored under the state the next tool-result continuation will look up.
  - Before fingerprints drop the newest user turn or the full contiguous group of trailing tool results.
  - Assistant turns with tool calls ignore unstable narration, and tool-call JSON arguments are canonicalized before hashing.
  - The chat path now stores exact, after, and no-tools aliases so repeat requests and tool-result continuations both preserve account affinity.
- Added fake handler coverage proving tool-result continuation reuses the first-leg account even when normal scheduling would prefer another account.
- Added stream boundary coverage proving failures before the first delta retry/switch account, while failures after the first delta do not retry and instead finish the current stream.
- Added Responses API conversion for `custom_tool_call` and `custom_tool_call_output`.
- Added a global CORS/OPTIONS middleware for OpenAI/Anthropic/Dashboard headers and wired it into `cmd/server`.
- Added a global max request body middleware with a 25MB default limit for POST/PUT/PATCH routes, matching the Node server's memory guard for long-context payloads.
- Ran a non-quota temporary server check on port 3499:
  - `/healthz` returned HTTP 200.
  - `/dashboard` returned the embedded React SPA.
  - OPTIONS `/v1/chat/completions` returned HTTP 204 with CORS headers.
  - `pgrep -fl language_server_macos_arm64` returned empty, confirming this non-chat validation path did not start LS.
- Verification passed:
  - `go test ./...`
  - `npm ci && npm run build && rm -rf node_modules tsconfig.tsbuildinfo` in `web/dashboard`

## 2026-05-11 direct reuse account affinity
- Wired the existing reuse pool into the Direct-only chat/messages/responses shared path instead of leaving it as a dashboard-only placeholder.
- Successful requests now store a route/model/caller/tools/history fingerprint bound to the selected account and API key hash.
- Matching follow-up requests prefer the same account; `strict_reuse=true` or `X-Windsurf-Strict-Reuse: true` returns `429` if that sticky account is unavailable instead of silently switching accounts.
- Request stats/logs now include `reuse_hit` and `reuse_miss_reason`; Dashboard recent requests show reuse hit/miss and cache-read fields.
- Added fake tests for sticky account reuse and strict reuse unavailable behavior without consuming quota.
- Verification passed: `go test ./internal/account ./internal/handler ./internal/store -count=1`; final `go test ./...`.

## 2026-05-11 dashboard theme switch
- Added a Dashboard light/dark theme toggle with lucide icons, accessible labels, and localStorage persistence.
- Moved theme colors into CSS variables and added a light operations-console palette without changing the page structure.
- Rebuilt embedded Dashboard assets after the UI change and removed temporary `node_modules`.
- Updated the Node parity checklist to mark light/dark switching complete.
- Verification passed: `npm ci && npm run build && rm -rf node_modules tsconfig.tsbuildinfo`; final `go test ./...`.

## 2026-05-11 sensitive config safety view
- Added runtime config security snapshot for API keys, dashboard password, and Redis password without returning raw secret values.
- Dashboard now shows safe/action status, explanatory messages, and the env var names required to configure each sensitive value.
- Later pass added online runtime secret mutation: Dashboard can submit new API keys, Dashboard password, and Redis password through `secrets`; API keys and Dashboard password affect auth immediately, while Redis password updates the runtime config snapshot without reconnecting Redis.
- Updated the Node parity checklist to distinguish read-only sensitive config safety status from online secret mutation.
- Verification passed: `go test ./internal/runtimeconfig ./internal/handler -count=1`; `npm ci && npm run build && rm -rf node_modules tsconfig.tsbuildinfo`; final `go test ./...`.

## 2026-05-11 usage/cache observability
- Extended request stats snapshots with usage totals, cache-read totals/ratio, reuse hits/rate, stream count, tool-call count, account distribution, and latency buckets.
- Dashboard request stats now renders lightweight CSS bar charts for route/account/latency/error distribution without adding a heavy chart dependency.
- Kept usage/cache audit conservative: unknown upstream fields remain zero rather than being fabricated.
- Updated the Node parity checklist to mark usage/cache display basics and lightweight charts complete, while leaving real upstream usage audit as a validation gap.
- Verification passed: `go test ./internal/handler -count=1`; `npm ci && npm run build && rm -rf node_modules tsconfig.tsbuildinfo`; final `go test ./...`.

## 2026-05-11 non-quota parity hardening continuation
- Made `/v1/messages` and `/v1/responses` use the same fake-testable `directChatClient` interface as `/v1/chat/completions`.
- Added fake contract tests proving Anthropic Messages and Responses tool-result continuations reuse the first-leg account, matching the existing OpenAI Chat coverage.
- Added reuse fingerprint coverage proving tool-chain account affinity is route-isolated and cannot cross between OpenAI Chat, Anthropic Messages, and Responses.
- Promoted the request body guard to `server.max_request_body_bytes`, with `WINDSURFAPI_MAX_REQUEST_BODY_BYTES`, Docker/systemd examples, Dashboard Settings visibility, and runtime Dashboard patch support.
- Fixed the runtime body-limit path so Dashboard config changes are read by middleware for new requests immediately, rather than only being reflected in the config snapshot.
- Hardened `/dashboard/api/cache` so reuse entries expose `caller_key_hash` instead of raw `caller_key`, avoiding accidental API key/caller leakage in Dashboard/debug responses.
- Tightened Responses streaming contract: text streams now emit completed `response.output_item.done` events in addition to text/content done and response.completed, matching the function-call branch more closely.
- Rebuilt embedded Dashboard assets and removed temporary `web/dashboard/node_modules` and `tsconfig.tsbuildinfo`.
- Verification passed: `go test ./...`; `npm ci && npm run build`; `node_modules_status=1` and `tsbuildinfo_status=1` after cleanup.

## 2026-05-11 runtime secret mutation
- Added runtime secret patch support to `runtimeconfig.Patch`: `secrets.api_keys`, `secrets.dashboard_password`, and `secrets.redis_password`.
- Replaced static server auth wiring with dynamic `AuthMiddlewareFunc` and `DashboardAuthMiddlewareFunc`, so changed API keys and Dashboard password apply to new requests immediately.
- Dashboard Settings now includes non-echoing inputs for API keys, Dashboard password, and Redis password.
- Redis password mutation is intentionally limited to runtime config state; it does not rebuild the existing Redis client or scheduler coordinator.
- Rebuilt embedded Dashboard assets and removed temporary frontend dependency/build info files.
- Verification passed: `go test ./internal/runtimeconfig ./internal/handler ./cmd/server`; `npm ci && npm run build`.

## 2026-05-11 Codex continuation: regression matrix and stream contracts
- Expanded `scripts/client-regression.sh` into an explicit opt-in matrix runner: `MATRIX=quick` keeps the previous lightweight text/debug checks, while `streams`, `tools`, and `full` cover chat/messages/responses text, stream, first-leg tools, and tool-result continuation using `claude-sonnet-4.6` by default.
- Kept real upstream validation opt-in; the script is ready for production smoke but was not executed in this pass to avoid quota consumption.
- Added non-quota protocol contract tests for OpenAI SSE tool-call chunks, Anthropic Messages streaming `tool_use`/`input_json_delta`, and Responses streaming function-call item/delta/done events.
- Extended `cmd/load-smoke` with `-stream` so 10/50/100 concurrency smoke can validate stream termination for OpenAI `[DONE]`, Anthropic `message_stop`, and Responses `response.completed`.
- Updated `PRODUCTION.md` with quick/full regression commands and a stream load-smoke example.

## 2026-05-11 native chat prompts experiment
- Investigated the bundled Windsurf LS binary strings and confirmed `api_server_pb.GetChatMessageRequest` is constructed from repeated `chat_pb.ChatMessagePrompt` values, but no safe standalone conversation-history field was identifiable from strings alone.
- Added a default-off Direct experiment: `direct.native_chat_prompts` / `WINDSURFAPI_DIRECT_NATIVE_CHAT_PROMPTS=true` / `cmd/direct-smoke -native-chat-prompts` emits one `chat_message_prompts` field per internal message while preserving the top-level flattened prompt as fallback.
- Added field-level tests proving the experimental request carries repeated field 3 prompts with source values aligned to the Node RawGetChatMessage enum: user=1, system=2, assistant=3, tool=4.
- Kept production default on the proven transcript flatten path until real upstream smoke validates native prompt semantics for multi-turn and tool history.

## 2026-05-11 Dashboard Tailwind/shadcn foundation
- Used the UI/UX dashboard guidance to keep the existing dense operations-console layout and avoid a broad visual rewrite.
- Added Tailwind CSS infrastructure to `web/dashboard`: `tailwind.config.ts`, `postcss.config.cjs`, Tailwind directives, and package dependencies.
- Added a shadcn-style `Button` component backed by `class-variance-authority`, `clsx`, and `tailwind-merge`, plus `src/lib/utils.ts`.
- Migrated the topbar refresh/theme and toast close controls onto the new Button component to verify the integration path without destabilizing the current Dashboard.
- Rebuilt embedded Dashboard assets successfully.

## 2026-05-11 local dashboard serve verification
- Started a temporary server on port 3496 with health worker disabled and a non-default dashboard password.
- Verified `/healthz` returned ok, `/dashboard` served the embedded React SPA with the new Tailwind/shadcn-built asset names, and `/dashboard/api/config` exposed `direct.native_chat_prompts=false` without leaking secrets.
- Confirmed no `language_server_macos_arm64` process was started by the server/dashboard verification path.
- Stopped the temporary server with graceful shutdown.

## 2026-05-11 Dashboard routed operations console
- Converted the Dashboard from a single long operations page into routed sections backed by TanStack Router: Overview, Accounts, Scheduler, Availability, Models, Proxy, Requests, Settings, and Legacy LS.
- Kept Go as the production host: `/dashboard/*` still falls back to the embedded SPA, and route refreshes such as `/dashboard/accounts` return the React app.
- Migrated the remaining high-use action controls in Accounts, Models, Proxy, Requests, and Settings onto the shadcn-style Button component while leaving behavior unchanged.
- Migrated the Accounts, Models, Proxy, and Requests tables to TanStack Table (`useReactTable`) so the Dashboard now has a real table abstraction to build sorting/filtering/pagination on instead of only hand-written table markup.
- Added shadcn-style `Input`, `Select`, and `Textarea` components and migrated the topbar credentials, log filters, account editor, model access reason input, proxy input, and runtime settings form onto them.
- Replaced browser `window.confirm` for account/proxy delete with in-panel two-click confirmation states, so dangerous operations stay inside the Dashboard UI.
- Added Go handler regression coverage that proves `/dashboard`, `/dashboard/accounts`, and `/dashboard/scheduler` all return the embedded React SPA index.
- Rebuilt embedded Dashboard assets into `internal/dashboard/dist` and removed temporary `web/dashboard/node_modules` plus `tsconfig.tsbuildinfo`.
- Non-quota local verification passed on temporary port 3511: `/healthz`, `/dashboard`, and `/dashboard/accounts` returned expected responses, and no `language_server_macos_arm64` process was started.
- Verification passed: `go test ./internal/handler -count=1`; `go test ./...`; `npm ci && npm run build`; `bash -n scripts/client-regression.sh`.

## 2026-05-11 Dashboard switch controls
- Added a lightweight shadcn-style `Switch` component with `role="switch"`, `aria-checked`, disabled state, and focus styling.
- Replaced the remaining raw checkbox controls in the Dashboard:
  - account enabled/banned toggles
  - model visible/deprecated toggles
  - runtime Settings toggles for health, Redis scheduler, Redis fail-closed, and proxy rotation
- Confirmed no raw `type="checkbox"` or `window.confirm` usage remains under `web/dashboard/src`.
- Rebuilt embedded Dashboard assets into `internal/dashboard/dist` and removed temporary `web/dashboard/node_modules` plus `tsconfig.tsbuildinfo`.
- Verification passed: `npm ci`; `npm run build`; `bash -n scripts/client-regression.sh`; `go test ./...`.

## 2026-05-11 Dashboard confirm dialog
- Added a reusable Dashboard `ConfirmDialog` component using `role="alertdialog"`, overlay dismissal, Escape cancellation, and explicit destructive confirm/cancel actions.
- Replaced the account delete and dynamic proxy delete two-click confirmation flows with the new modal confirmation.
- Removed the old inline confirmation hint styling after migrating the remaining destructive actions.
- Rebuilt embedded Dashboard assets into `internal/dashboard/dist` and removed temporary `web/dashboard/node_modules` plus `tsconfig.tsbuildinfo`.
- Verification passed: `npm ci`; `npm run build`; `bash -n scripts/client-regression.sh`; `go test ./...`.

## 2026-05-11 local dashboard verification
- Started a temporary server on port 3521 with health worker disabled and a non-default Dashboard password.
- Verified `GET /healthz` returned HTTP 200.
- Verified `GET /dashboard` and `GET /dashboard/accounts` both returned the embedded React SPA, including route fallback for Dashboard subroutes.
- Confirmed no `language_server_macos_arm64` process was started during the non-chat local verification.
- Stopped the temporary server gracefully and removed the temporary SQLite verification database.

## 2026-05-11 Node dashboard API parity: cache clear
- Compared the Node Dashboard API route surface and found a low-risk parity gap: Node supports `DELETE /dashboard/api/cache`, while Go only exposed the reuse cache snapshot.
- Added `reuse.Pool.Clear()` and wired `DELETE /dashboard/api/cache` to clear in-memory conversation reuse/cache entries without touching active requests or upstream accounts.
- Added handler and reuse pool tests proving cache entries are cleared and caller keys are not leaked in the response.
- Added a Scheduler panel action in the React Dashboard to show cache entries/stores and clear reuse cache through a confirmation dialog.
- Rebuilt embedded Dashboard assets and removed temporary frontend dependency/build info files.
- Verification passed: `go test ./internal/reuse ./internal/handler -count=1`; `npm ci`; `npm run build`; `bash -n scripts/client-regression.sh`; `go test ./...`.

## 2026-05-11 Node dashboard API compatibility aliases
- Added low-risk Dashboard API compatibility endpoints modeled after the Node version:
  - `GET|POST /dashboard/api/auth`
  - `GET|PUT|PATCH /dashboard/api/settings/credentials`
  - `GET|PUT|PATCH /dashboard/api/settings/env`
  - `DELETE /dashboard/api/stats`
  - `DELETE /dashboard/api/experimental/conversation-pool`
- Implemented runtime config credential snapshots with masked API keys only; plaintext API keys and Dashboard passwords are never returned.
- Implemented request-stats reset for the Dashboard API without touching persisted account state or upstream quota.
- Added compatibility tests covering auth probe, credentials snapshot/update, env snapshot/update, stats reset, and conversation-pool clear.
- Verification passed: `go test ./internal/runtimeconfig ./internal/reuse ./internal/handler -count=1`; `bash -n scripts/client-regression.sh`; `go test ./...`.

## 2026-05-11 Node dashboard proxy/status compatibility aliases
- Added more low-risk Node Dashboard API compatibility endpoints:
  - `PUT|PATCH|DELETE /dashboard/api/proxy/global`
  - `PUT|PATCH|DELETE /dashboard/api/proxy/accounts/:id`
  - `GET /dashboard/api/drought`
  - `GET /dashboard/api/upstream-endpoints`
- `proxy/global` updates the Direct default proxy in both the proxy manager and runtime config snapshot without leaking proxy passwords in responses.
- `proxy/accounts/:id` maps to the Go account `proxy_url` field and does not start or depend on LS.
- Account-level proxy compatibility writes now reuse the same proxy URL validation as dynamic/global proxy management, so malformed proxy strings cannot be persisted through the alias.
- `drought` summarizes enabled accounts, drought account count, lowest quota, and active drought status from the scheduler snapshot.
- `upstream-endpoints` reports the Direct-only Windsurf cloud RPC endpoints, including raw gRPC `GetChatMessage`.
- Added tests covering masked global proxy output, account proxy set/clear, drought summary, and upstream endpoint reporting.
- Verification passed: `go test ./internal/proxy ./internal/runtimeconfig ./internal/handler -count=1`; `bash -n scripts/client-regression.sh`; `go test ./...`.

## 2026-05-11 Node dashboard import compatibility
- Added `POST /dashboard/api/import-accounts` as a Node Dashboard import compatibility endpoint over the Go account manager.
- The endpoint supports structured `accounts[]` with `api_key`, `token`, `firebase_token`, `label`, `email`, `proxy_url`, plus text import lines in the Node-style `email----password----token----auth1` shape.
- The text parser persists only the third `----` field as the Devin token, preserving the earlier importer bugfix that prevents `auth1` from being appended to the token.
- Added duplicate handling and warnings in import responses without returning raw tokens.
- Hardened account response secrecy: `safeAccount` and scheduler/debug snapshots now mask proxy credentials, while runtime chat/health paths still use the stored raw proxy URL.
- Updated the Dashboard account save path so a displayed masked proxy URL is not written back over the raw proxy unless the operator actually changes it.
- Verification passed so far: `go test ./internal/account ./internal/handler`; `go test ./internal/proxy ./internal/runtimeconfig`; `go test ./internal/models ./internal/modelaccess ./internal/windsurf/direct`.

## 2026-05-11 Node dashboard tier-access compatibility
- Added `models.TierAccessSnapshot()` with the Node-compatible `free`, `pro`, `unknown`, `expired`, and `allModels` fields.
- Kept Node's optimistic behavior where `unknown` gets the full catalog, while also exposing Go-specific `direct_supported` and `unsupported` lists so Dashboard/migration tooling can distinguish catalog visibility from Direct runtime support.
- Added `GET /dashboard/api/tier-access`.
- Verification passed: `go test ./internal/models ./internal/handler`.

## 2026-05-11 Node dashboard account management aliases
- Added `PATCH|DELETE /dashboard/api/accounts/:id` compatibility for Node-style Dashboard callers.
- `PATCH` maps Node fields to Go state:
  - `status=active/enabled` -> enabled and not banned.
  - `status=disabled/inactive` -> disabled.
  - `status=error/banned/invalid` -> disabled and banned.
  - `label` -> notes, `tier` -> tier, `blockedModels` -> account blocked models, `resetErrors` -> clears runtime recent failures and model breakers.
- Added `account.Manager.ResetAccountErrors()` for safe runtime error/breaker clearing without touching account tokens or quota fields.
- `DELETE` maps to the existing Go account deletion path.
- Verification passed: `go test ./internal/account ./internal/handler`.

## 2026-05-11 Node dashboard experimental/legacy compatibility
- Added `GET|PUT /dashboard/api/experimental` compatibility:
  - exposes Direct-only flags and reuse pool stats.
  - maps `directNativeChatPrompts` into runtime direct config.
  - clears reuse cache if a Node-style caller disables `cascadeConversationReuse`.
- Added `GET|PUT /dashboard/api/availability/config` compatibility over the Go health worker config snapshot/patch path.
- Added explicit Direct-only unavailable responses for legacy side-effect routes:
  - `/dashboard/api/accounts/import-local-availability`
  - `/dashboard/api/accounts/import-local`
  - `/dashboard/api/self-update/check`
  - `/dashboard/api/self-update`
  - `/dashboard/api/langserver/*`
  - `/dashboard/api/windsurf-login`, `/dashboard/api/windsurf-login/batch`, `/dashboard/api/oauth-login`
- These routes intentionally do not start LS, run update scripts, read local Windsurf desktop state, or perform external OAuth/login flows.
- Verification passed: `go test ./internal/handler ./internal/runtimeconfig`.

## 2026-05-11 Node dashboard legacy side-effect route boundaries
- Added explicit non-executing responses for more old Node Dashboard side-effect routes:
  - service restart
  - quiet-window auto-update
  - system prompt runtime edits
  - manual availability/model/account probes
  - batch login/import
  - reveal-key
  - account refresh-token/rate-limit/credit refresh actions
  - legacy dynamic-proxy routes
- These are intentionally not implemented in the Go Direct-only runtime because they either belong to the old LS/Node deployment model, can trigger external login/refresh flows, or could consume quota unexpectedly.
- Tests confirm the reveal-key path never returns raw account tokens.
- Verification passed: `go test ./internal/handler`.

## 2026-05-11 Node dashboard model-access compatibility
- Added Node-compatible write endpoints for Dashboard model access:
  - `PUT /dashboard/api/model-access`
  - `POST /dashboard/api/model-access/add`
  - `POST /dashboard/api/model-access/remove`
- Mapped Node allowlist/blocklist-style visibility into Go `model_access.visible` without changing Direct runtime `enabled` for unsupported models.
- Added `config` to `GET /dashboard/api/model-access`, while preserving the existing models/overrides payload.
- Tests verify that a Node-style allowlist can hide/show visible models but still cannot make unsupported catalog entries such as `gpt-5` callable through Direct.
- Verification passed: `go test ./internal/handler ./internal/modelaccess`.

## 2026-05-11 non-quota protocol parity hardening
- Strengthened caller scope isolation for chat/messages/responses:
  - Bearer API keys are hashed in the caller key and never stored raw.
  - shared API key callers are separated by `user`, `conversation`, `previous_response_id`, `metadata.conversation_id`, `metadata.session_id`, Anthropic/Claude Code `metadata.user_id`, or IP/UA fallback.
  - This reduces cross-user conversation reuse/account-affinity bleed under a shared proxy key.
- Added JSON output compatibility without contaminating long conversation history:
  - OpenAI Chat `response_format=json_object/json_schema`
  - Anthropic Messages `output_config.format`
  - OpenAI Responses `text.format`, including both flat and nested `json_schema` shapes
  - all three become an ephemeral system instruction prepended only to the current direct request.
- Improved Responses API tool parity:
  - consecutive `function_call`/`custom_tool_call` items are grouped into one assistant tool-call turn before tool outputs.
  - `custom`, `namespace`, `web_search/web_search_preview`, and `tool_search` tools are safely flattened to function tools.
  - Responses output restores custom/web_search/namespace metadata in non-stream and stream item events; server-side-only `file_search`, `computer_use_preview`, and `mcp` are not faked.
  - streaming handles arguments-before-name tool deltas without starting a malformed output item.
- Added tool_choice pruning across OpenAI Chat, Anthropic Messages, and Responses so named choices pointing at dropped/unavailable tools are removed before upstream dispatch.
- Extended usage mapping so real upstream cache fields, when parsed, are surfaced as OpenAI/Responses `cache_read_input_tokens`/`cache_creation_input_tokens` and Anthropic cache sibling fields. Unknown cache values remain zero.
- Verification passed: `go test ./internal/handler -count=1`; final `go test ./...`.

## 2026-05-11 Anthropic cache/output_config parity
- Added Anthropic Messages `cache_control` request policy parsing for top-level, tools, system blocks, and message content blocks.
- Cache markers are kept out of Direct transcript text while still influencing conversation reuse TTL: default/5m uses the normal reuse TTL, `ttl:"1h"` extends reuse entries to one hour.
- Added usage split fields so upstream `cache_write` can be surfaced as Anthropic `cache_creation.ephemeral_5m_input_tokens` or `ephemeral_1h_input_tokens` according to the request's cache policy, without fabricating unknown cache values.
- Added safe pruning for Anthropic server-side tools (`web_search_20250305`, `code_execution_20250522`, `advisor_20260301`) so Go does not pretend Direct can execute server-managed Anthropic tools; named `tool_choice` pointing at a dropped tool is removed.
- Added `output_config.effort` compatibility for Anthropic Messages: `low`, `medium`, and `high` map to Direct thinking prompts unless an explicit `thinking` request is already present.
- Verification passed: `go test ./internal/handler -count=1`; final `go test ./...`.

## 2026-05-11 Claude Code Read tool-result guard
- Ported the Node Anthropic Messages safeguard for risky Claude Code `Read` tool results.
- When a `Read` tool_result is an oversized-file error, cached/unchanged stub, or truncation stub, Go now appends the same kind of safety note before the Direct transcript is built so the model does not treat the stub as complete file contents.
- The guard only applies to the `Read` tool, preserves normal line-numbered file bodies, and does not annotate other tools such as `Bash`.
- Verification passed: `go test ./internal/handler -count=1`; final `go test ./...`.

## 2026-05-11 Responses reasoning output parity
- Added Responses API output mapping for Direct `ChatResult.Thinking`.
- Non-stream Responses now emits a `reasoning` output item with a `summary_text` block before the assistant message, matching the Node Responses adapter behavior for reasoning content.
- Responses `reasoning.effort` now maps to a Direct thinking prompt instead of being ignored.
- Streaming Responses now emits `response.reasoning_summary_text.delta` / `done` and keeps ordered output indexes for reasoning, text, and tool items.
- Hardened the Responses stream writer for arguments-before-name tool deltas after reasoning/text items: pending argument deltas now reserve an output index and reuse it when the tool item later starts, preventing output_index collisions.
- Added handler coverage for reasoning request prompt, non-stream reasoning + message output ordering, and stream reasoning events.
- Verification passed: `go test ./internal/handler -count=1`; final `go test ./...`.

## 2026-05-11 OpenAI reasoning + model-access hardening
- Finished the partially implemented OpenAI Chat reasoning path:
  - `reasoning_effort` and `reasoning.effort` now map to the Direct thinking prompt.
  - non-stream OpenAI Chat responses include `message.reasoning_content` when Direct returns thinking.
  - OpenAI SSE includes `delta.reasoning_content` chunks for thinking deltas.
- Replaced the temporary variadic `executeOpenAIChat` parameter plumbing with explicit `proxyPool` and `thinking` parameters.
- Added unit coverage for OpenAI reasoning prompt parsing, Direct `Thinking` propagation, non-stream `reasoning_content`, and SSE `reasoning_content` deltas.
- Tightened model-access semantics so hidden/allowlist-excluded models are rejected by chat/messages/responses instead of only being hidden from `/v1/models`.
- Added Node-compatible base/`-thinking` sibling inheritance for Dashboard model-access allowlist/blocklist operations, while keeping unsupported catalog entries such as `gpt-5` non-callable.
- Aligned local model-access rejection shape with Node's core behavior: OpenAI Chat and Responses return `403` with `error.type="model_blocked"`; Anthropic Messages returns an Anthropic error envelope with `error.type="model_blocked"`.
- Added SQLite-backed Node Dashboard model-access config persistence for `mode + list`.
  - `GET /dashboard/api/model-access` now returns the actual configured mode/list instead of inferring allowlist from hidden models.
  - `PUT /dashboard/api/model-access` keeps the persisted mode/list while still expanding base/`-thinking` visibility for runtime enforcement.
  - `POST /dashboard/api/model-access/add|remove` mutates the same persisted list, matching Node's control-plane shape.
- Verification passed: `go test ./internal/handler ./internal/sse -count=1`; `go test ./internal/modelaccess ./internal/handler -count=1`; `go test ./internal/handler -count=1`; `go test ./internal/modelaccess ./internal/handler -count=1`.

## 2026-05-11 auth/proxy safety parity hardening
- Ported the Node `server-auth` extraction semantics into Go:
  - `Bearer` auth scheme is now case-insensitive.
  - malformed or duplicate `Authorization` headers no longer fall back to `X-API-Key`.
  - `X-API-Key` still works when `Authorization` is absent.
- Added proxy private-host safety gate across Go control/runtime paths:
  - Dashboard global proxy, account proxy, account import/patch, dynamic proxy manager, and Direct client default/account proxy all reject private proxy hosts by default.
  - Blocks loopback, `localhost`, RFC1918, link-local, CGNAT, unspecified, and IPv4-mapped private addresses.
  - Added explicit escape hatch `proxy.allow_private=true` / `WINDSURFAPI_PROXY_ALLOW_PRIVATE=1` for operator-managed private proxy deployments.
  - Proxy test target URLs now reject private hosts to avoid SSRF-style test routes.
- Dashboard Settings now exposes the `Allow private proxy hosts` switch and includes it in runtime config patches.
- Rebuilt embedded Dashboard assets, removed `web/dashboard/node_modules` and `web/dashboard/tsconfig.tsbuildinfo`.
- Verification passed: `go test ./internal/handler ./internal/proxy ./internal/config ./internal/runtimeconfig ./internal/windsurf/direct ./cmd/server -count=1`; final `go test ./...`.
- Note: `npm ci && npm run build` reported one moderate npm audit finding; build succeeded and no audit fix was applied because that may alter dependency versions beyond the current scope.

## 2026-05-11 stream error contract parity
- Replaced post-first-delta stream failures being emitted as plain assistant text with structured protocol errors:
  - OpenAI Chat SSE emits `data: {"error": {"message", "type", "code"}}`.
  - Anthropic Messages SSE emits `event: error` with Anthropic error envelope and closes any active content block first.
  - Responses SSE emits `event: response.error`.
- Kept the existing retry boundary: failures before the first emitted delta can still switch accounts; failures after the first delta do not retry.
- Tightened error classification to avoid matching arbitrary prose containing `after` as `retry after`; rate-limit detection now matches `retry_after` or `retry-after`.
- Verification passed: `go test ./internal/handler ./internal/sse -count=1`; final `go test ./...`.

## 2026-05-11 secret redaction hardening
- Added shared `internal/redact` helper for log/debug/export-safe text redaction.
- Redaction now covers common leak classes before data reaches persistent or operator-visible surfaces:
  - `Authorization` / `Cookie` / `Set-Cookie` values.
  - `sk-*` API keys, Devin session tokens, JWT-shaped strings, AWS access key IDs.
  - JSON/key-value credential fields such as `api_key`, `firebase_token`, `session_token`, `caller_key`, `password`, and `secret`.
  - email addresses and proxy passwords in URLs.
- Wired redaction into request event recording, SQLite `request_events`, CSV/JSON/NDJSON exports, Dashboard log stream, OpenAI/Anthropic/Responses stream error events, Direct client debug stats, scheduler events/health summary, proxy `last_error`, and health worker logs.
- Added regression coverage for redaction in `internal/redact`, request stats persistence/export, OpenAI SSE, Anthropic SSE, Responses SSE, Direct stats, scheduler snapshot, and proxy last errors.
- Verification passed: `go test ./internal/redact ./internal/handler ./internal/sse ./internal/account ./internal/proxy ./internal/windsurf/direct ./internal/health -count=1`; final `go test ./...`.

## 2026-05-11 real account smoke and cooldown hardening
- Imported `account.txt` into `data/windsurf.db`: 12 total active accounts are present after import/merge.
- `cmd/account-check -model claude-sonnet-4.6 -smoke=false` checked all 12 candidates successfully for direct user status, model config, and rate-limit capacity without LS.
- Temporary server on `WINDSURFAPI_PORT=3490` passed `/healthz` and `/readyz`; process checks confirmed the Direct-only main path did not start `language_server_macos_arm64`.
- Real regression smoke using `claude-sonnet-4.6` passed `MATRIX=quick`, `MATRIX=streams`, `MATRIX=tools`, and `MATRIX=full`, covering chat/messages/responses text, stream, first-leg tool calls, and tool-result continuation.
- Real small load passed: `concurrency=6 requests=12` returned 12/12 success, and `concurrency=10 requests=10` returned 10/10 success with accounts spread across the pool.
- Real medium load exposed the current account-pool capacity limit: `concurrency=20 requests=50` returned 30 success and 20 upstream HTTP 429/rate-limit responses from Sonnet 4.6.
- Hardened cooldown handling so upstream reset text such as `Resets in: 30m0s` is parsed into a persisted account-model cooldown instead of using a fixed short fallback.
- Added local single-process `maxInflightPerAccount` enforcement and persisted cooldown loading in scheduler snapshots after restart.
- Verification passed after these changes: `go test ./internal/handler -count=1`, `go test ./internal/account -count=1`, `go test ./cmd/server ./internal/handler -count=1`, and final `go test ./...`.

## 2026-05-11 controlled real-account testing
- Added scheduler support for an explicit account allow-list through `Manager.ReserveFrom`.
- Added a localhost-only test header, `X-Windsurf-Test-Account-IDs`, wired through chat/messages/responses direct execution. Remote requests ignore this header, so production clients cannot force account selection.
- Extended `cmd/load-smoke`:
  - `-account-ids` limits a run to a small account group.
  - `-mode controlled` runs bounded real conversation scenarios.
  - `-models` provides model fallback order.
  - `-users`, `-rounds`, `-group-size`, `-max-groups`, and `-max-requests-per-model` keep tests small and stop expansion.
- Upgraded controlled scenarios to build real per-user multi-turn history for chat/messages/responses, instead of sending unrelated one-shot prompts for each round.
- Verified with unit tests:
  - `ReserveFrom` only selects from the allowed account set.
  - localhost account-subset header constrains routing.
  - remote account-subset header is ignored.
- Restarted the server on port 3490 with `WINDSURFAPI_HEALTH_ENABLED=0` to avoid background health probes during controlled real tests.
- Controlled real test 1: accounts `1,2,3`, models `claude-4.5-sonnet,claude-opus-4.6`, max 4 requests/model. Both models returned upstream model rate limits and the tool stopped the group instead of expanding to all accounts.
- Controlled real test 2: accounts `4,5,6`, model `claude-opus-4-7-medium`, max 2 requests/model. This also returned upstream model rate limit and stopped.
- Post-test audit: no `language_server_macos_arm64` process, all account inflight counts are 0, cooldowns were persisted per account/model.
- Verification passed: targeted `go test ./internal/account ./internal/handler ./cmd/load-smoke -count=1` and final `go test ./...`.
- Controlled real test 3: accounts `7,8,9`, model `claude-4.5-sonnet`, `users=1`, `rounds=2`, max 3 requests/model. This ran a real multi-turn history plus tool-result continuation and returned 3/3 success. Post-test inflight sum stayed 0 and no LS process was started.

## 2026-05-11 Redis coordinator and Node nginx audit
- Confirmed the dedicated local Redis container is available as `windsurfapi-go-redis` with host port `6380`.
- `docker exec windsurfapi-go-redis redis-cli ping` returned `PONG`.
- First `cmd/redis-coord-smoke` run exposed a real cleanup bug: when no keys matched the prefix, the command sent `DEL` with zero keys and Redis returned `ERR wrong number of arguments for 'del' command`.
- Fixed the smoke cleanup to skip empty key lists.
- Hardened `RedisCoordinator.Reserve` to use one Lua script for cooldown checks, max-inflight check, inflight increment, RPM reservation, and TTL writes. `CanReserve` remains a pre-filter, but the actual distributed reservation is now atomic.
- Verification passed: `go test ./cmd/redis-coord-smoke ./internal/account -count=1`.
- Real Redis coordinator smoke passed without hitting Windsurf/Claude upstream: `go run ./cmd/redis-coord-smoke -redis 127.0.0.1:6380 -db data/redis-coord-smoke.db -accounts 4 -workers 40 -max-inflight 2`.
- Node `../WindsurfAPI/nginx.conf` is a reverse proxy/load balancer for multi-replica Node deployment. It uses API-key sticky sessions because Node keeps cascade/reuse state in process memory, disables buffering for SSE, caps request bodies at 25MB, and applies simple IP rate limiting.

## 2026-05-11 Claude Code real client gate
- Fixed the Claude Code `/v1/messages` policy-block regression by normalizing Anthropic system/user payloads before Direct:
  - Strip `x-anthropic-billing-header`.
  - Neutralize Claude Code / Claude Agent SDK identity text into a neutral proxy instruction.
  - Strip `<system-reminder>...</system-reminder>` blocks from user text while preserving the real user request.
- Added regression coverage: `TestAnthropicClaudeCodePayloadNormalization`.
- Fixed Anthropic SSE tool-call shape after text deltas. The stream writer now starts a new `tool_use` content block before `input_json_delta`, which avoids Claude Code's `Content block is not a input_json block` fallback.
- Relaxed Anthropic tool-result continuation suppression. Tool results no longer disable all future tools; the transcript prompt still tells the model not to repeat existing tool_call ids.
- Added regression coverage: `TestAnthropicStreamWriterTextThenToolStartsToolBlock`.
- Verification passed: `go test ./internal/handler ./internal/windsurf/direct ./internal/account -count=1` and final `go test ./...`.
- Restarted temporary Go service on port `3491` with Redis coordinator enabled:
  `WINDSURFAPI_PORT=3491 WINDSURFAPI_API_KEYS=sk-windsurf-default WINDSURFAPI_DB_PATH=data/windsurf.db WINDSURFAPI_HEALTH_ENABLED=false WINDSURFAPI_REDIS_ADDR=127.0.0.1:6380 WINDSURFAPI_SCHEDULER_REDIS_ENABLED=true WINDSURFAPI_SCHEDULER_REDIS_FAIL_CLOSED=true go run ./cmd/server`.
- Real Claude Code no-tools smoke succeeded through Go `/v1/messages`:
  `printf '%s' 'Reply with exactly: CLAUDE_CODE_OK' | ANTHROPIC_BASE_URL=http://127.0.0.1:3491 ANTHROPIC_API_KEY=sk-windsurf-default DISABLE_AUTO_UPDATE=true claude --bare --setting-sources project,local --print --output-format json --model claude-sonnet-4-6 --permission-mode bypassPermissions --tools ''`.
- Real Claude Code long tool-chain smoke succeeded through Go `/v1/messages`:
  `claude --bare --setting-sources project,local --print --verbose --output-format stream-json --include-partial-messages --model claude-sonnet-4-6 --permission-mode bypassPermissions --tools Read,Bash --allowedTools 'Read,Bash(go test ./internal/account -count=1)'`.
- The long smoke ran 4 turns, emitted two real `Read` tool uses, one real `Bash` tool use, received Bash stdout `ok github.com/zhangyu/windsurfapi-go/internal/account 1.090s`, and produced a final answer.
- Post-test checks: `/dashboard/api/logs` showed successful `messages` requests with `tool_call_count=2` and `tool_call_count=1`; `/debug/accounts` showed all account `inflight=0`; `pgrep -fl language_server_macos_arm64` returned no process.

## 2026-05-11 Dashboard parity operations pass
- User confirmed the simplified Go Dashboard still needs Node-equivalent core operations: manual Availability actions, detailed credits, dynamic proxy batch workflows, and table sorting/pagination/bulk account operations.
- Implementation direction: keep Direct-only and avoid quota-consuming chat probes by default. Manual refresh uses GetUserStatus / CheckMessageRateLimit / model configs only; chat smoke remains explicit CLI.
- Current code findings: Dashboard routes for manual probe/refresh/availability mutations still return legacy unavailable; accounts store only tier + daily/weekly quota; direct.UserStatus already parses prompt/flex/daily/weekly reset and can be extended for overage/plan start/end; account.Manager has cooldown/breaker state but no targeted clear/prune APIs.

## 2026-05-11 Dashboard parity operations completion
- Confirmed the requested Node-equivalent operations are now present in the Go Dashboard without reintroducing LS:
  - Availability: manual Direct chat probe for model/account-model, prune expired availability state, clear account-model cooldown, clear account-model breaker, clear all breakers for a model.
  - Credits: account detail panel shows plan name, plan start/end, daily/weekly quota and reset times, prompt credits, flex credits, overage balance, and last health check.
  - Dynamic proxy: provider-specific generation is exposed, novproxy-style generated proxies can be added to the pool, and selected accounts can receive one generated proxy each.
  - Tables/bulk operations: Accounts/Models/Availability/Proxy/Requests use TanStack Table sorting/pagination; Accounts supports bulk refresh, enable, disable, ban, delete, bind proxy, and clear proxy.
- Updated `docs/node-parity-go-superiority-plan.md` so these items are marked as core-complete instead of "basic only" or legacy unavailable.
- Verification passed: `go test ./...`; `npm run build` in `web/dashboard`.

## 2026-05-11 dynamic proxy binding lifecycle port
- Started porting Node dynamic IP proxy account-binding logic into Go.
- Scope chosen: durable `account_proxy_bindings`, account-specific active proxy lookup, bind/rotate/verify/clear/suspend/resume/failure marking, maintenance worker plan, request-path integration, Dashboard API/UI controls, and tests.
- Existing static account proxy field remains as manual override/backward compatibility; dynamic binding is a higher-level lifecycle around generated provider proxies.

## 2026-05-11 Node dynamic proxy binding lifecycle completion
- Ported the remaining Node dynamic IP proxy account-binding lifecycle into the Go implementation.
- Changed the default dynamic proxy verification target to Node parity `https://ipinfo.io/json` so verification captures egress IP/location instead of probing `server.codeium.com`.
- Added account availability purge after proxy identity changes: bind, rotate, verify, clear, suspend, resume, static account proxy changes, and batch dynamic proxy actions now clear old cooldowns, model breakers, recent errors, RPM history, and local inflight for that account.
- Strengthened proxy maintenance: worker plan keeps Node priority order failed/expired -> expiring soon -> unbound, and `RunMaintenance` now honors configured worker concurrency.
- Expanded Dashboard Proxy page in Chinese with account-level binding table, binding status/egress/TTL/error details, row actions for verify/rotate/suspend/resume/clear, bulk dynamic bind/rotate/clear for selected accounts, and a manual maintenance trigger.
- Extended Settings so dynamic binding, auto-bind-new-accounts, renew-before, bind retries, worker interval/batch/concurrency are configurable from the Dashboard.
- Added backend tests for dynamic binding priority over static account proxy, expired binding fallback, failure marking, worker-plan priority, worker concurrency, Dashboard binding APIs, and Node-compatible dynamic-proxy batch routes.
- Verification passed: `go test ./internal/proxy ./internal/account ./internal/handler -count=1`, `npm run build` in `web/dashboard`, and final `go test ./...`.

## 2026-05-12 cctest model/usage normalization
- Investigated cctest's `claude-opus-4-7 -> claude-opus-4-7-medium` and abnormal cache usage report.
- Compared the sibling `kiro.rs` cache implementation and confirmed its `VirtualCacheUsageManager` is a local virtual usage ledger, not a reliable upstream billing/cache source for external verification.
- Updated public response behavior so `/v1/chat/completions`, `/v1/messages`, and `/v1/responses` keep internal Windsurf effort routing private while responding with the client-requested public model ID.
- Changed OpenAI usage output to only include conservative base fields: `prompt_tokens`, `completion_tokens`, and `total_tokens`. It no longer exposes unverified cache read/creation fields by default.
- Changed Anthropic usage output to use upstream input tokens when present, otherwise a conservative local estimate, and to zero out cache read/creation fields by default rather than mapping Windsurf/internal cache stats to official Anthropic billing fields.
- Added a fallback input-token estimate to Responses streaming `response.completed` usage.
- Added/updated tests for public model echo and conservative cache usage across OpenAI, Anthropic Messages, and Responses.

## 2026-05-12 configurable virtual cache billing
- Added `internal/usage.VirtualCacheUsageManager`, modeled after the sibling `kiro.rs` virtual ledger but scoped to Go's account/model/caller/route isolation.
- Added `usage.virtual_cache` YAML/env/runtime config. It is off by default and supports conservative/dynamic modes, 5m/1h TTL, uncached input sizing, creation min/max, jitter, and burst knobs.
- Wired virtual usage into `/v1/chat/completions`, `/v1/messages`, and `/v1/responses` so one successful upstream request computes one response usage view; account scheduling and upstream health still use raw upstream usage.
- Runtime Dashboard config can now toggle and tune virtual cache billing without restarting; applying config updates the in-memory virtual ledger manager.
- Dashboard Settings now exposes the main virtual cache controls in Chinese.

## 2026-05-12 Direct native trajectory root-cause exploration
- Investigated the remaining Opus 4.7 native tool-history internal errors instead of adding more fallback.
- Compared Go Direct request construction with Node's `cascade-native-bridge.js` and `windsurf.js`.
- Found the important mismatch: Go Direct puts tool history into flattened text/chat_message_prompts, while Node native bridge represents prior tool executions as `CortexTrajectoryStep` additional steps.
- Corrected the earlier field read: Direct `GetChatMessageRequest` field 1 is `metadata`, so `trajectory_steps` is not a confirmed Direct chat field.
- Removed the Opus 4.7 native tool-history automatic emulated fallback direction and started testing stable Direct conversation identifiers instead.

## 2026-05-12 Direct native tool-history root fix
- Continued the Opus 4.7 Direct native tools root-cause pass after the stable session/cascade-id change.
- Implemented native `ChatMessagePrompt` tool-history encoding in `internal/windsurf/direct/client.go`:
  - assistant `windsurf.ToolCall` values are now encoded as `ChatMessagePrompt.tool_calls` field 6 with nested `ChatToolCall{id=1,name=2,arguments_json=3}`.
  - tool result messages now encode `tool_call_id` field 7.
  - requests with tool history force native `chat_message_prompts` even when `direct.native_chat_prompts` is disabled.
  - the top-level Direct prompt no longer carries the XML flattened transcript for tool-history turns, avoiding duplicate/conflicting history.
- Updated Direct protocol tests to assert field 6/7 are present and `<tool_calls>` / `<tool_result>` no longer leak into native prompt text or top-level prompt.
- Verification passed: `go test ./internal/windsurf/direct -count=1`, `go test ./internal/handler ./internal/reuse ./internal/windsurf/direct -count=1`, and full `go test ./...`.
- Ran one minimal real upstream smoke against a temporary server on port 3492: `go run ./cmd/load-smoke -url http://127.0.0.1:3492 -api-key sk-windsurf-default -model claude-opus-4-7-high -route messages -scenario tool-result -concurrency 1 -requests 1 -timeout 90s -account-ids 8,9,10,11`.
- The request reached Windsurf Direct with `tool_mode=native shape_turns=3 tool_history=2`; it did not reproduce the previous immediate upstream `internal error`, but account 8 hit `Reached overall message rate limit`, so this is not a full success proof. A later smoke with cooled-down accounts should retry the same exact scenario.
