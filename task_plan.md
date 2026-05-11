# WindsurfAPI-Go Production Scheduling + HA Core

## Goal
- Prove the Go implementation can send one Claude message through Windsurf Language Server and receive non-empty assistant text.
- Prove `/v1/chat/completions` can complete the same send-and-receive flow through the Go server.
- Add the first production scheduling layer: account reservations, retry classification, LS proxy isolation, reuse pool, and debug visibility.

## Success Criteria
- `cmd/ls-smoke -mode=send` reaches `SendUserCascadeMessage`, polls Cascade, and prints non-empty assistant text. Done.
- `cmd/server` receives an OpenAI-compatible chat request, selects an account, sends via LS, and returns `choices[0].message.content`. Done.
- `cmd/server` reserves accounts through the scheduler instead of always using the first row. Done.
- LS pool can select entries by account proxy key and exposes generation/debug state. Done.
- `/debug/accounts`, `/debug/ls`, and `/debug/scheduler` return authenticated read-only state without leaking raw tokens. Done.
- `cmd/direct-smoke` can run safe cloud-direct account/status probes by default and only attempts quota-consuming direct chat when explicitly passed `-probe-api-chat` or `-probe-cascade`.
- `go test ./...` remains green.

## Scope
- Claude-only model catalog.
- Fix protocol/builder/client issues needed for real send.
- Build first-pass account HA scheduling and debug APIs.
- Do not build Dashboard, Redis distributed scheduling, multi-provider catalog, or complex tool emulation in this pass.

## Plan
1. Complete LS direct send smoke. Done.
2. Configure server path to use the newer `~/.windsurf/language_server_macos_arm64` LS. Done.
3. Seed Go SQLite with one active Windsurf token for local E2E. Done.
4. Run `/v1/chat/completions` real send-and-return test. Done.
5. Run `/v1/chat/completions` real send-and-return sweep for every configured Claude model. Done.
6. Implement reservation scheduler, error classification, LS proxy selection, reuse pool, and debug endpoints. Done.
7. Keep `go test ./...` green. Done.
8. Implement Plan B cloud-direct `ApiServerService/GetChatMessage` diagnostic probe without changing production LS-backed chat path. Done for raw gRPC smoke.
9. Convert `/v1/chat/completions` to direct-only `ApiServerService/GetChatMessage` while keeping OpenAI-compatible API. Done for text/non-tool chat.
10. Add native direct tool_calls support for Claude Code. Done for first-leg tool call generation, streaming `delta.tool_calls`, and tool-result continuation via a conservative no-repeat strategy.
11. Add Node-compatible `/v1/messages` and `/v1/responses` minimum direct routes. Done for Claude text, tool use/function call, tool-result continuation, and streaming event shapes.
12. Production P0/P1/P2-foundation hardening pass. Done: explicit transcript builder, health refresh, structured logs, route-aware load smoke, readiness/graceful shutdown, import validation, deployment docs, optional Redis scheduler coordinator, protocol compatibility edge cases, and regression scripts.
13. Complete non-quota Node parity hardening pass. In progress: direct reuse tool-chain affinity, caller scope isolation for shared API keys, stream retry boundary tests, structured stream error events, stream tool-call contract tests, OpenAI reasoning_content, JSON response_format/text.format hints, Anthropic output_config/cache_control/server-side tool pruning, Responses native/custom/namespace/web_search tool flattening, Responses multi-tool history grouping, usage cache field surfacing, model-access main-route enforcement with base/-thinking inheritance, CORS/preflight, dynamic request body limit config, strict auth token parsing, proxy private-host/SSRF guardrails, cross-protocol tool continuation fake tests, regression matrix scripts, stream load-smoke support, controlled real-account load-smoke with account subsets/model fallback, checklist tracking, routed React Dashboard sections, shadcn-style Dashboard table/form/switch/confirm-dialog controls, Dashboard cache clear parity, Node Dashboard API compatibility aliases including proxy/status/import/tier-access/account-management/experimental/availability/model-access endpoints, explicit Direct-only unavailable responses for legacy side-effect routes, account/proxy response secret masking, and broad log/error/export redaction are done; native direct multi-message/history field replacement and real 50/100 upstream capacity audit remain pending because current free/trial accounts hit official model limits.
14. Port Node dynamic account proxy binding lifecycle. Done: durable account proxy bindings, account-level bind/rotate/verify/clear/suspend/resume/failure handling, auto-renew/auto-bind worker plan, Direct request path integration, Dashboard/API controls, binding-change availability purge, and tests are implemented. Existing static `accounts.proxy_url` remains as manual override/backward compatibility behind active account dynamic bindings.

## Errors Encountered
| Error | Attempt | Resolution |
| --- | --- | --- |
| `SendUserCascadeMessage: neither PlanModel nor RequestedModel specified` | Prior smoke | In progress: compare Go vs Node request encoding. |
| `UpdatePanelStateWithUserStatus: http status 404` | Real smoke on macOS LS | Treat user-status panel update as best-effort; Node warmup does not require it. |
| New LS manager exits after `Exceeded maximum number of connection failures 1` | Smoke with `~/.windsurf/language_server_macos_arm64` | Remove extra pre-warm user-status RPCs from chat warmup; keep Node-compatible init sequence. |
| `listen tcp :3456: bind: address already in use` | First server E2E attempt | Existing Node service owns port 3456. Used temporary config on port 3466. |
| Cold-start `unexpected EOF` during `SendUserCascadeMessage` | First HA handler E2E after refactor | Added a 2.5s post-ready grace before panel init so LS manager finishes connecting to its child before warmup/send RPCs. |

## 2026-05-10 晚间接手
- 用户要求后续验证不要用 Haiku，统一用 claude-sonnet-4.6。
- 当前 account.txt 已导入 11 个新账号，加历史 1 个，共 12 个 enabled 账号；均无 proxy_url。
- 上一轮低并发 smoke 失败主因：无代理多账号共用 default LS，导致 panel state not found；另有 Invalid Devin token 需要按账号失效处理。
- 本阶段新增目标：无代理账号按 account_id 隔离 LS；默认 max_instances 覆盖 12 个账号；invalid Devin token 标记 banned；增加逐账号真实验证工具。

## Errors Encountered - 2026-05-11
| Error | Attempt | Resolution |
| --- | --- | --- |
| LS manager first cold start exceeded connection failures and exited | HTTP smoke first request | Added manager stable wait plus pre-send retry/refund path; restarted service and HTTP smoke passed. |
| account.txt tokens all returned Invalid token | account-check full run | `-apply` marked accounts 2-12 banned/disabled; only account 1 remains enabled. |
| Cloud direct `LanguageServerService/StartCascade` returned HTTP 404 | Plan B direct Cascade probe | Keep this as a reproducible negative result; add separate `ApiServerService/GetChatMessage` probe because local LS Cascade RPC names are not cloud-exposed. |
| `ApiServerService/GetChatMessage` returned internal error through Connect proto | Plan B API chat probe | Raw `application/grpc` succeeds, so force API chat probe onto raw gRPC. |
| Direct-only tool_calls support unknown | Production direct implementation | Must inspect/verify native GetChatMessage tool fields; do not silently text-degrade if native tool_calls cannot be proven. |
| Direct prompt with `User:` prefix made Claude continue a fake dialogue | First HTTP direct E2E returned assistant text plus invented `User: ...` continuation | Single user message now sends raw content; multi-turn prompt uses explicit context/latest-message boundaries and tells model not to invent future dialogue. |
| API chat response field 7 looked like usage in synthetic tests but is upstream metadata in real frames | HTTP direct E2E usage/parser review | Parser now extracts usage/model from nested metadata when present and no longer treats every field 7 as token usage blindly. |
| Direct tool_call arguments arrive fragmented across response frames | Real `-probe-tools` frame dump | Added a tool-call accumulator so OpenAI responses get one logical `tool_calls[]` item with complete `arguments`. |
| Tool result continuation repeated the same tool call | HTTP two-step tool E2E | When the last client message is a tool result and no specific function is forced, suppress tools for that continuation so Claude answers from the tool result instead of looping. Native multi-message GetChatMessage fields still need deeper protocol work. |
| Node parity routes missing | `/v1/messages` and `/v1/responses` absent in Go server | Added direct-only handlers for both routes, with Anthropic/OpenAI Responses shape adapters over the existing scheduler/direct core. |
| Small production smoke hit per-account Sonnet rate limits | 2026-05-11 P0/P1 smoke | Error classification marked model cooldown and retried another account successfully. Full 100-concurrency smoke should be an explicit quota-consuming run. |
| Tool-result continuation reuse missed account affinity | Fake parity continuation tests | Added reuse before/after fingerprints, trailing tool-result group stripping, assistant tool-call narration normalization, JSON argument canonicalization, and no-tools aliases. |
| Browser/client preflight would require auth and fail before POST | Local parity review | Added global CORS middleware that returns 204 for OPTIONS and allows OpenAI/Anthropic/Dashboard headers. |
| Unbounded JSON request bodies could consume memory on bad long-context clients | Local parity review against Node 25MB guard | Added global POST/PUT/PATCH max-body middleware with a 25MB default. |
| Dashboard showed body-limit runtime changes that would not affect middleware until restart | Runtime config review | Added dynamic max-body middleware backed by runtime config so new requests use the patched limit immediately. |
| Dashboard/global/account proxy setters could accept private proxy hosts | Node `dashboard-proxy-validate` parity review | Added default private-host rejection in proxy manager, Direct client, Dashboard setters, account import/patch, and proxy test target URLs; added `proxy.allow_private` escape hatch. |
| Post-first-delta stream errors were emitted as assistant text | Node `stream-error` parity review | Added protocol-native structured stream errors for OpenAI Chat, Anthropic Messages, and Responses while preserving no-retry-after-first-delta behavior. |
| Upstream errors could contain tokens, cookies, emails, or proxy credentials and flow into logs/export/SSE/debug surfaces | Secret redaction parity review | Added shared redaction helper and wired it into request stats persistence/export, stream errors, Direct stats, scheduler events, proxy last_error, and health logs. |
| Real tests were expanding too much and cooling down all accounts on one model | User-directed controlled testing pass | Added localhost-only account subset routing and `load-smoke -mode controlled` with small account groups, model fallback, and per-model request caps. |
| Fallback Claude models also returned rate limits on free/trial account groups | Controlled real tests on accounts `1,2,3` and `4,5,6` | Stopped testing after bounded samples; cooldowns persisted per account/model and inflight returned to zero. |
