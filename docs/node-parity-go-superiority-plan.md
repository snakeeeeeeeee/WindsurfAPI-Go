# WindsurfAPI-Go 完全替代 Node 版并增强生产能力计划

> 说明：`[x]` 表示当前 Go 项目已落地或已有可运行骨架；`[ ]` 表示还未完成或只停留在计划阶段。部分条目会拆成“基础已完成”和“增强待做”，避免把半成品误判为完成。

## Summary

- [x] 明确目标：Go 版最终完整替代 Node 版 WindsurfAPI。
- [x] 明确生产主链路：Direct-only，不依赖 LS。
- [x] 明确 Dashboard 方向：不照搬 Node 静态 HTML，改为轻量 React SPA。
- [x] 核心 Node parity 已达到：Chat、Messages、Responses、Models、Admin/Dashboard API、模型目录、stats/logs/cache 和 Direct-only 主链路均已覆盖；少量 Node legacy/side-effect 路由仍显式 unavailable。
- [x] 调度、并发、高可用、观测能力的核心骨架已超过 Node 版：Direct-only、账号级调度、错误分类、cooldown 持久化、结构化日志、React Dashboard、fake 100 并发和 Redis coordinator fake 多实例验收已完成。
- [ ] 真实 50/100 并发上线验收仍受当前 Trial/free 账号池官方限流约束，不能声明已通过。

## Frontend Choice

- [x] 选定 `Vite + React + TypeScript`，只构建静态文件，生产由 Go `embed` 服务。
- [x] 新增前端目录：`web/dashboard`。
- [x] 新增构建产物目录：`internal/dashboard/dist`。
- [x] 前端依赖引入 `React`、`TanStack Query`、`TanStack Router`、`TanStack Table`、`lucide-react`。
- [x] Vite dev proxy 已配置 `/dashboard/api`、`/debug/*`、`/auth/*` 到 Go。
- [x] Go 已通过 embed 服务 dashboard 静态产物。
- [x] TanStack Router 已接入 Dashboard：`/dashboard`、`/dashboard/accounts`、`/dashboard/scheduler` 等子路由可刷新并由 Go SPA fallback 服务。
- [x] Tailwind 基础设施已正式接入：`tailwind.config.ts`、PostCSS、CSS token 映射和生产构建。
- [x] shadcn/ui 风格组件体系已开始接入：`src/components/ui/button.tsx`、`cn()`、`class-variance-authority`，顶部、账号、模型、代理、日志、配置等主要操作按钮已迁移验证。
- [x] TanStack Table 已开始落地：Accounts、Models、Proxy、Requests 表格已迁移到 `useReactTable`，后续可复用列定义/排序/过滤能力。
- [x] shadcn/ui 风格表单基础组件已开始落地：`Input`、`Select`、`Textarea` 已接入顶部认证、日志过滤、账号管理、代理、模型访问和运行配置表单。
- [x] 危险操作二次确认基础已完成：账号删除、代理删除改为面板内确认状态，不再依赖浏览器原生 confirm。
- [x] shadcn/ui 风格高级控件基础已完成：账号、模型和运行配置的 checkbox 已迁移为可访问 Switch 组件。
- [x] shadcn/ui 风格专用确认弹窗基础已完成：账号删除、代理删除使用统一确认弹窗，支持取消和 Escape。
- [x] 轻量自绘指标图已实现：Dashboard 请求统计展示 route/account/latency/error 分布，不引入重图表库。

## Dashboard Redesign

- [x] Dashboard React SPA 骨架已完成。
- [x] `/dashboard` 和 `/dashboard/*` 已挂载到 Go server。
- [x] Dashboard 已拆成真实分区视图：Overview、Accounts、Scheduler、Availability、Models、Proxy、Requests、Settings、Legacy LS。
- [x] Overview 基础视图已完成：账号数、inflight、RPM、quota。
- [x] Accounts 只读表格已完成：账号、tier、状态、inflight、RPM、quota。
- [x] Scheduler 基础事件展示已完成。
- [x] Availability 基础健康摘要已展示。
- [x] Legacy LS 状态已作为 debug/legacy 面板展示。
- [x] token 不硬编码到前端；Dashboard 需要用户在本地输入 API key 后请求 debug API。
- [x] Accounts 管理基础已完成：导入、启停、删除、备注、tier、proxy、blocked models。
- [x] Models 页面基础已完成：provider、UID、supported、选中账号 blocked/allowed 状态。
- [x] Models 页面增强基础已完成：deprecated、全局可见性、enabled/disabled 配置。
- [x] Models 页面 credit 字段已迁移。
- [x] Account proxy 基础已完成：Dashboard 可设置账号 proxy，Direct 主链路按账号代理出站。
- [x] Global proxy 基础已完成：`proxy.default` / env 可作为 Direct 默认出站代理，账号 proxy 优先。
- [x] Proxy 页面增强已完成：dynamic proxy、测试、轮换、失败原因、SQLite 持久化、provider-specific 代理生成、账号级动态绑定表、代理页独立账号勾选、绑定/轮换/验证/暂停/恢复/清除、批量绑定/轮换/验证/暂停/恢复/解绑、维护任务入口。
- [x] Requests 基础视图已完成：最近请求、stream、重试、usage、latency、错误分类。
- [x] Logs 基础接口已完成：`/dashboard/api/logs` 返回最近请求和调度事件。
- [x] Logs 增强已完成：`/dashboard/api/logs/stream` SSE 流、CSV/JSON/NDJSON 导出、Dashboard 和 API 过滤。
- [x] Settings 基础页面已完成：Direct host/timeout、max request body、health interval/model、Redis scheduler、默认代理、日志级别。
- [x] Settings 敏感配置安全状态已完成：API key、dashboard password、Redis password 状态、风险提示、env 名称提示。
- [x] Settings 在线修改敏感配置已实现：API keys 和 Dashboard password 运行时立即影响鉴权，响应不回显原值；Redis password 仅更新运行时配置快照，不自动重连 Redis。
- [x] 运行时 env override 基础已完成：端口、API keys、max request body、DB、Redis、Direct hosts/timeout、健康检查、调度、Dashboard password、默认代理、私网代理逃生开关、日志级别。
- [x] Dashboard 独立鉴权基础已完成：支持 `X-Dashboard-Password`，同时保留 API key。
- [x] 账号删除和代理删除危险操作已加面板内二次确认。
- [x] 亮色/暗色切换已完成：主题按钮、localStorage 持久化、CSS token 双主题。
- [x] Dashboard 表格运维能力已补齐核心项：Accounts、Models、Availability、Proxy、Requests 使用 TanStack Table 排序/分页；Accounts 支持批量启用、停用、封禁、删除、刷新余额、绑定代理、清除代理。
- [x] 账号余额详情已对齐 Node 核心信息：plan name、plan start/end、daily/weekly quota/reset、prompt credits、flex credits、overage balance、最近健康检查均展示在账号详情。
- [x] Availability 手动操作已补齐核心项：手动 direct chat probe 单模型/单账号模型、prune 过期状态、清账号-模型 cooldown、清账号 breaker、清全模型 breaker。

## P0：主链路替代

- [x] `POST /v1/chat/completions` 已挂载并走 direct client。
- [x] `POST /v1/messages` 已挂载并走 direct client。
- [x] `POST /v1/responses` 已挂载并走 direct client。
- [x] `GET /v1/models` 已挂载。
- [x] server 启动不要求 LS binary。
- [x] chat/messages/responses 主链路不启动 LS。
- [x] LS pool 只保留 legacy smoke/debug。
- [x] OpenAI tools 请求映射、tool_choice、streaming tool_calls 基础已实现。
- [x] Anthropic `tool_use`、`tool_result` 基础转换已实现。
- [x] Responses `function_call`、`function_call_output` 基础转换已实现。
- [x] Responses `custom_tool_call`、`custom_tool_call_output` 基础转换已实现。
- [x] 连续 3 轮 tool history 的 prompt transcript 单测已覆盖。
- [x] compact 场景 suppress tools 的基础逻辑已存在。
- [x] OpenAI/Anthropic/Responses 路由都有基础错误输出。
- [x] model-access 拒绝错误形状已对齐 Node 核心语义：OpenAI/Responses 返回 `403` + `error.type="model_blocked"`，Anthropic 返回 `403` + Anthropic error envelope。
- [x] stream 首包前 transport/transient 失败允许换号；首包后失败不再重试，并按协议输出结构化错误事件后结束当前 stream。
- [x] policy block、rate limit、model unavailable、transport 的错误分类基础已实现。
- [x] 动态响应 `Cache-Control: no-store` 基础已实现。
- [x] CORS/OPTIONS preflight 基础已实现，兼容 OpenAI/Anthropic/Dashboard 常用请求头。
- [x] 请求体大小保护已实现：默认 25MB，支持 YAML/env/Dashboard runtime 配置，Dashboard 保存后新请求立即生效。
- [x] OpenAI/Auth token 提取已对齐 Node 安全语义：`Bearer` 大小写不敏感； malformed/重复 `Authorization` 不会回退到 `X-API-Key`。
- [x] `x-request-id` 基础已由 auth middleware 设置。
- [x] `/v1/response` alias 到 `/v1/responses` 已实现。
- [x] OpenAI header 基础对齐已实现：`openai-model`、`openai-processing-ms`、`openai-version`。
- [x] Anthropic header 基础对齐已实现：`request-id`、`anthropic-model`。
- [x] OpenAI `response_format=json_object/json_schema` 已兼容：转换为仅本次请求生效的 system JSON 输出提示，不污染历史消息。
- [x] OpenAI `reasoning_effort` / `reasoning.effort` 已兼容：请求侧转成 Direct thinking 提示，非流响应输出 `reasoning_content`，流式响应输出 `delta.reasoning_content`。
- [x] Anthropic `output_config.format` 已兼容：`json_object/json_schema` 转换为本次请求 system JSON 输出提示。
- [x] Anthropic `output_config.effort` 已兼容：`low/medium/high` 映射到 Direct thinking 提示预算，显式 `thinking` 请求优先。
- [x] Anthropic `cache_control` 基础兼容已完成：识别 tools/system/messages/top-level 的 ephemeral marker，不泄漏到 Direct transcript，并用 `ttl:"1h"` 调整 reuse TTL 和 Anthropic usage cache split。
- [x] Anthropic server-side tools 安全剪枝已完成：`web_search_20250305`、`code_execution_20250522`、`advisor_20260301` 不伪造成普通 function tool，相关 `tool_choice` 会被剪掉。
- [x] Anthropic Claude Code `Read` tool_result stub 保护已完成：超大文件/缓存未变更/截断 stub 会加安全注释，避免模型误判为完整文件内容；真实带行号文件体不误标。
- [x] Responses `text.format` 已兼容：`json_object/json_schema` 转换为本次请求 system JSON 输出提示，`strict` 默认按 Responses 习惯处理。
- [x] Direct native `chat_message_prompts` 实验开关已实现并有字段级单测：`direct.native_chat_prompts` / `WINDSURFAPI_DIRECT_NATIVE_CHAT_PROMPTS`。
- [x] 工具链生产默认仍使用 transcript builder 过渡方案，但已通过真实 Claude Code 多轮工具链验收；native 多消息/history 字段保留为后续增强，不能作为当前上线阻塞项。
- [x] 真实 API smoke 已覆盖 chat/messages/responses 的 text、stream、tools、tool-result 链路。
- [x] 真实 Claude Code 客户端完整工具链会话已跑通：`claude --bare --setting-sources project,local --print --verbose --output-format stream-json --include-partial-messages --model claude-sonnet-4-6 --tools Read,Bash` 通过 Go `/v1/messages` 执行 2 次 Read、1 次 Bash、最终回答成功；本机未安装可调用 Cline CLI，Cline 需另行用客户端手工验收。

## P1：调度与可用性超过 Node

- [x] 调度器已实现 Reserve/Release/Refund 基础接口。
- [x] 调度过滤已覆盖 enabled、banned、token 空、rate limit、model cooldown、RPM 满。
- [x] 调度排序已按 inflight、quota score、RPM headroom、last used。
- [x] 成功/失败路径会记录 scheduler event。
- [x] 账号健康后台 worker 已存在。
- [x] 健康 worker 已能 direct 检查 token、quota、rate limit、model config。
- [x] 健康结果可写回 tier、quota daily/weekly、rate limit、plan name、plan start/end、prompt/flex credits、overage balance。
- [x] Redis coordinator 基础已存在，支持共享 inflight、RPM reservation、cooldown。
- [x] Redis coordinator 默认可选启用，Redis 不可用时默认单机降级。
- [x] Redis fail-closed 策略已实现：`scheduler.redis_fail_closed` / `WINDSURFAPI_SCHEDULER_REDIS_FAIL_CLOSED`。
- [x] model breaker 基础已实现：账号-模型连续 transient/transport/model unavailable 失败会短期熔断。
- [x] 最近错误窗口统计基础已实现，并在 scheduler snapshot / Dashboard debug 中暴露。
- [x] drought mode 基础已实现：极低 quota 账号进入 drought 状态。
- [x] 低 quota 账号降权策略基础已实现：drought 账号不会在健康账号仍有余量时被优先打爆。
- [x] 多实例协调器 fake 并发验收已完成：两个 Manager 共享 coordinator 时不会超过账号级 inflight 上限，释放后 inflight 归零。
- [x] 真实小并发已执行：`concurrency=6 requests=12` 和 `concurrency=10 requests=10` 均 100% 成功，账号分布没有集中到第一个账号。
- [x] 真实中等并发已暴露官方容量边界：`concurrency=20 requests=50` 返回 30 成功、20 个上游 `rate_limit`，调度器正确换号、记录 cooldown，inflight 最终归零。
- [x] 受控真实测试工具已完成：`cmd/load-smoke -mode controlled` 支持 `-account-ids`、`-group-size`、`-models` fallback、多人/多轮历史小样本和每模型请求上限；服务端只允许 localhost 使用 `X-Windsurf-Test-Account-IDs`，避免影响生产请求。
- [x] 受控模型 fallback 验证已执行：账号组 `1,2,3` 测 `claude-4.5-sonnet -> claude-opus-4.6`，账号组 `4,5,6` 测 `claude-opus-4-7-medium`，均快速暴露官方模型限流并停止扩散，所有 inflight 归零。
- [x] 受控多人多轮成功样本已补跑：账号组 `7,8,9`，`claude-4.5-sonnet`，1 个用户 2 轮历史 + tool-result continuation，共 3 个真实请求，100% 成功，inflight 归零。
- [x] Availability 人工干预面已补齐：Dashboard/API 可对模型或账号-模型执行 Direct 手动探测，成功后清 cooldown/breaker/recent error，失败后按错误分类写入 cooldown、breaker 或 ban；也支持 prune、手动清 cooldown、手动清 breaker。
- [ ] 真实 50/100 并发正式验收未通过；当前 12 个 Trial/free-like 账号触发 Sonnet 4.6 账号-模型 30 分钟官方限流，需更多可用额度账号或降低并发/RPM 后再验收。

## P2：Node 协议与模型目录 parity

- [x] Claude-only 模型目录基础已存在。
- [x] Claude 常见别名基础已存在。
- [x] Responses API 已支持 string input。
- [x] Responses API 已支持 message item、input_text、output_text、function_call、function_call_output、custom_tool_call、custom_tool_call_output 基础转换。
- [x] Responses API 已支持 reasoning output item 基础转换：Direct `Thinking` 会作为 `output[].type="reasoning"` 的 summary 返回，流式输出包含 reasoning summary delta/done。
- [x] Responses `reasoning.effort` 已兼容：请求侧转成 Direct thinking 提示，不再静默忽略客户端 reasoning 配置。
- [x] Responses stream 已支持 text delta、text item done、tool call delta、function-call arguments done、completed 基础事件。
- [x] Anthropic Messages 已支持 `system` string 和部分 content blocks。
- [x] Anthropic Messages 已支持 `tool_use`、`tool_result` 基础转换。
- [x] Conversation reuse pool 基础已存在。
- [x] reuse fingerprint 已纳入 route、tools schema、tool_choice、caller/history 等基础维度。
- [x] caller scope 隔离已增强：共享 API key 下会纳入 `user`、`conversation`、`previous_response_id`、`metadata.session_id/conversation_id`、Anthropic `metadata.user_id` 或 IP/UA fallback，避免多用户串用 reuse。
- [x] direct reuse 已接入主链路：命中后优先回同账号，strict reuse 账号不可用返回 429。
- [x] direct reuse 已增强工具链续轮：成功 tool_calls 写入 after key，下一轮剥离整组 trailing tool_result 后优先回同账号；assistant tool_calls narration / JSON 参数格式漂移不破坏指纹。
- [x] direct reuse 工具续轮 fake 验收已覆盖 OpenAI Chat、Anthropic Messages、Responses 三条协议，且 route fingerprint 不会跨协议串用。
- [x] Node 同级多 provider 模型目录已迁移到 Go catalog：GPT、Gemini、Grok、Kimi、GLM、SWE 等可在 Dashboard 查看。
- [x] provider、deprecated、unsupported 字段模型化基础已完成。
- [x] credit 字段模型化已完成。
- [x] Direct 不支持/禁用模型的显式 unsupported/deprecated 策略基础已实现。
- [x] 非 Claude/未验证 Direct 模型默认不进入 `/v1/models` 可调用列表，并在 chat/messages/responses 上显式返回 unsupported。
- [x] 全局模型 visible/enabled 控制已实现，可作为 allow/block 基础。
- [x] 全局模型访问控制已进入主请求路径：hidden/allowlist/blocklist 模型会被 chat/messages/responses 拒绝，不只是从 `/v1/models` 隐藏。
- [x] Node model-access thinking sibling 继承已对齐：allowlist/blocklist 对 base 与 `-thinking` 变体互相继承，其它后缀不自动继承。
- [x] 账号级 blocked models 已实现，并纳入调度过滤。
- [x] Dashboard/API 已支持更新账号级 blocked models。
- [x] Dashboard/API 全局更新模型可见性已实现。
- [x] Anthropic `thinking` 策略基础已完成：请求侧解析 enabled/disabled/budget，Direct prompt 注入 thinking 指令，响应侧输出 thinking block / thinking_delta。
- [x] strict reuse 绑定同账号和账号不可用返回 429 的 fake 验收已完成。
- [x] usage/cache 统计展示基础增强已完成：usage 汇总、cache read、reuse hit、账号分布、延迟桶。
- [x] usage/cache 响应字段基础对齐已完成：真实解析到 `cache_read/cache_write` 时，OpenAI/Responses 输出 `cache_read_input_tokens`、`cache_creation_input_tokens`，Anthropic 输出 cache sibling 字段和 `cache_creation` split；未知值仍为 0。
- [x] Responses native tools 兼容增强已完成：`custom`、`namespace`、`web_search/web_search_preview`、`tool_search` 安全转换为 function tools，并在非流/流式输出中尽量还原为 Responses-native item；`file_search/computer_use/mcp` 仍不伪造 server-side 能力。
- [x] Responses 连续 `function_call` 历史聚合已完成：连续多个 tool call 会合并为同一个 assistant tool_calls turn，再遇到 tool output/message 时 flush，避免破坏多工具调用历史。
- [x] tool_choice 剪枝已完成：当 server-side/unsupported tool 被安全丢弃或当前工具列表为空时，命名 `tool_choice` 不再继续指向不存在的工具，避免上游 400/循环错误。
- [x] usage/cache 真实审计基础已验证：Claude Code 真实会话返回 `cache_creation_input_tokens` / `cache_creation.ephemeral_5m_input_tokens`，Go Anthropic 响应能透传该类 usage 字段；未知字段仍不伪造。

## P3：Admin API 与 Dashboard 后端

- [x] CLI 账号导入工具已存在：`cmd/import-accounts`。
- [x] CLI 账号检查工具已存在：`cmd/account-check`。
- [x] Debug API 已存在：`/debug/accounts`、`/debug/ls`、`/debug/direct`、`/debug/scheduler`。
- [x] Dashboard 当前已通过 `/dashboard/api/*` 展示和管理基础状态。
- [x] `POST /auth/login` 基础 token/api_key 导入已实现。
- [x] `GET /auth/accounts` 已实现。
- [x] `DELETE /auth/accounts/:id` 已实现。
- [x] `GET /auth/status` 已实现。
- [x] HTTP 批量导入账号基础已实现。
- [x] Node Dashboard 账号导入兼容已实现：`POST /dashboard/api/import-accounts` 支持 `accounts[]`、`api_key/token/firebase_token`、`label`、`proxy_url` 和 `email----password----token----auth1` 文本导入，响应不回显原始 token。
- [x] HTTP 账号启停、删除、备注、tier、proxy 基础已实现。
- [x] blocked models HTTP 管理已实现：`GET|PUT|PATCH /auth/accounts/:id/models`。
- [x] Node Dashboard 账号管理别名已实现：`PATCH|DELETE /dashboard/api/accounts/:id` 支持 `status`、`label`、`tier`、`blockedModels`、`resetErrors`，并映射到 Go 账号调度状态。
- [x] `/dashboard/api/overview` 已实现。
- [x] `/dashboard/api/accounts` 已实现。
- [x] `/dashboard/api/stats` 内存窗口统计已实现。
- [x] `/dashboard/api/logs` 基础事件列表已实现。
- [x] `/dashboard/api/cache` 复用缓存快照已实现，caller key 只返回 hash，不泄露原值。
- [x] `/dashboard/api/availability` 基础接口已实现。
- [x] `/dashboard/api/models` 基础模型列表已实现。
- [x] model-access 已实现：`/dashboard/api/model-access` 与 `GET|PUT|PATCH|DELETE /auth/models/:id/access`。
- [x] Node Dashboard model-access 配置语义已对齐：`mode + list` 持久化到 SQLite，GET 不再把 blocklist 反推成 allowlist；add/remove 会更新同一份列表。
- [x] runtime config HTTP API 基础已实现：`GET|PATCH /dashboard/api/config`，敏感字段脱敏。
- [x] Dashboard/运行时代理私网防护已完成：默认拒绝 `localhost`、loopback、RFC1918、link-local、CGNAT、IPv4-mapped private 代理 host；需 `proxy.allow_private=true` 或 `WINDSURFAPI_PROXY_ALLOW_PRIVATE=1` 才允许。
- [x] Node Dashboard 兼容别名基础已完成：`/dashboard/api/auth`、`/dashboard/api/import-accounts`、`/dashboard/api/tier-access`、`/dashboard/api/settings/credentials`、`/dashboard/api/settings/env`、`GET|PUT /dashboard/api/experimental`、`GET|PUT /dashboard/api/availability/config`、`GET|PUT /dashboard/api/model-access`、`POST /dashboard/api/model-access/add`、`POST /dashboard/api/model-access/remove`、`PATCH|DELETE /dashboard/api/accounts/:id`、`DELETE /dashboard/api/stats`、`DELETE /dashboard/api/experimental/conversation-pool`、`/dashboard/api/proxy/global`、`/dashboard/api/proxy/accounts/:id`、`/dashboard/api/drought`、`/dashboard/api/upstream-endpoints`。
- [x] Direct-only 旧路由边界已显式化：local Windsurf import、self-update、service restart、quiet-window auto-update、system prompts、reveal-key、refresh-token、batch login/import、dynamic-proxy legacy routes、langserver update/restart、Windsurf/OAuth login 路由返回明确 unavailable/not implemented，不会误启动 LS、执行自更新、泄露 token 或触发外部登录。
- [x] Node 版关键 Availability 手动操作已用 Go Direct 方式重做：manual model probe、manual account-model probe、prune、clear breaker/cooldown 都是可操作能力，不再属于 legacy unavailable。
- [x] Dashboard 独立 `X-Dashboard-Password` 已实现。
- [x] 非 localhost fail-closed 基础已实现：dashboard password 为空或默认 `admin` 时拒绝远程 Dashboard/Admin API。
- [x] 登录失败 IP lockout 已实现：Dashboard/Admin 认证失败按 IP 计数，5 次失败后 30 分钟锁定。
- [x] 敏感字段脱敏已增强：Dashboard/Admin/debug 账号响应不返回原始 token，账号 proxy password 也只返回 masked URL；Direct 运行态仍使用数据库原始 proxy。
- [x] 日志/错误面脱敏已完成：请求事件、SQLite `request_events`、Dashboard logs stream/export、OpenAI/Anthropic/Responses 流式错误、Direct debug stats、scheduler event、proxy last_error、health last_error 都会脱敏 token、Authorization/Cookie、JWT、AWS key、邮箱和代理密码。
- [x] dynamic proxy 已实现 Node 核心账号绑定生命周期：Dashboard API 可增删/启停/测试代理池，代理页可直接批量选择账号并 bind/rotate/verify/clear/suspend/resume，active binding 优先于静态账号代理，过期绑定自动标记 expired，运行时代理失败会标记 failed 并可自动轮换，独立后台 worker 按 failed/expired、expiring soon、unbound 优先级维护，SQLite 持久化，provider-specific 生成；代理测试目标默认拒绝私网 URL，避免把测试器变成 SSRF 入口。
- [x] 代理绑定变更后的可用性隔离已完成：bind/rotate/verify/clear/suspend/resume 和批量动态代理动作会清理该账号旧 cooldown、breaker、recent error、RPM/inflight 状态，避免旧 IP 的限流污染新 IP。
- [x] Direct client 按账号选择代理出站已实现，健康检查和 CLI 检查也走账号 proxy。

## P4：上线与运维

- [x] `/healthz` 已实现。
- [x] `/readyz` 已实现。
- [x] `/debug/accounts` 已实现。
- [x] `/debug/direct` 已实现。
- [x] `/debug/scheduler` 已实现。
- [x] Dockerfile 已存在。
- [x] docker-compose 已存在，并包含专用 `windsurfapi-go-redis` 服务用于本地/生产 Redis coordinator。
- [x] systemd 模板已存在：`deploy/windsurfapi-go.service`。
- [x] graceful shutdown 基础已实现：收到 SIGINT/SIGTERM 后 `server.Shutdown`。
- [x] fake/direct 相关单测已有基础覆盖。
- [x] `cmd/load-smoke` 已存在。
- [x] `cmd/load-smoke -stream` 已支持 chat/messages/responses 流式验收，不默认消耗 quota。
- [x] 真实 smoke 默认模型按计划使用 `claude-sonnet-4.6`。
- [x] 当前计划仍保持不默认跑 quota-consuming 压测。
- [x] 请求结构化统计基础已实现：`req_id`、route、model、caller_key_hash、account_id、attempt、latency、error_class、retry、usage、tool_call_count、reuse_hit、reuse_miss_reason。
- [x] 持久化 stats 已实现：请求事件写入 SQLite `request_events`，同时保留进程内最近 500 条窗口。
- [x] logs stream/export/filter 已实现：支持 SSE、CSV/JSON/NDJSON、route/model/status/error/account/stream/retry/query 过滤。
- [x] logs/error 安全导出已实现：错误文本在入内存、入 SQLite、导出和 SSE 输出前统一脱敏，不把上游 token/header/cookie/proxy 密码写入运维面。
- [x] 配置 env override 基础已实现，包含 `WINDSURFAPI_PROXY_ALLOW_PRIVATE` 私网代理显式放行开关。
- [x] fake direct 100 并发调度验收已完成：100 goroutine reserve/release 后 inflight 归零，且账号分布不集中。
- [x] Redis/Coordinator 多实例 fake 并发验收已完成：共享 coordinator 账号级 inflight 不超限，满载时会短暂排队。
- [x] 真实 Redis 容器 coordinator smoke 已完成：`windsurfapi-go-redis` + 两个 `account.Manager` + 40 并发 reservation，验证释放后 inflight 归零，跨实例 cooldown 可见。
- [x] Redis reservation 原子性已增强：实际 `Reserve` 使用 Lua 在 Redis 内一次完成 cooldown/max-inflight 检查、inflight 增量、RPM reservation 和 TTL 写入，避免多实例 `CanReserve -> INCR` 竞态穿透。
- [x] 真实账号 smoke 全矩阵脚本已具备：`scripts/client-regression.sh MATRIX=full` 覆盖 chat/messages/responses text、stream、tools、tool-result。
- [x] 真实账号 smoke 全矩阵已执行：`MATRIX=quick`、`streams`、`tools`、`full` 均通过，统一使用 `claude-sonnet-4.6`，主链路未启动 `language_server_macos_arm64`。
- [x] 上游 reset cooldown 解析和持久化已完成：`Reached message rate limit ... Resets in: 30m0s` 会转成约 30 分钟账号-模型 cooldown，而不是固定短冷却反复打爆同一模型。

## Node 功能矩阵

| 功能 | Node 版 | Go 当前状态 | 目标 | 状态 |
| --- | --- | --- | --- | --- |
| OpenAI Chat | 完整 | 已有 direct 主链路，真实 text/stream/tools/tool-result smoke 通过 | 补齐 headers、工具链、错误形状 | [x] 核心完成 |
| Anthropic Messages | 完整 | 已支持 tools/tool_result、thinking、output_config、cache_control、server-side tool 剪枝和结构化 stream error；真实 Claude Code Read+Bash 多轮工具链通过 | 对齐 Claude Code/Cline 常见形态 | [x] Claude Code 核心完成 / [ ] Cline 客户端手工验收未跑 |
| Responses API | 常见子集 | 已支持常见输入/工具/stream/结构化 stream error，真实 smoke 通过 | 对齐 Node 常见输入/stream | [x] 核心完成 / [ ] 完整 Node 边角输入非阻塞 |
| Models catalog | 多 provider | 多 provider catalog 已迁移，Direct 可调用仍默认 Claude | 迁移 catalog，unsupported 显式化 | [x] catalog 完成 / [ ] 非 Claude Direct 验证未完成 |
| 账号调度 | 内存调度 | Reserve/Release/Refund、RPM、cooldown、breaker、drought、Redis coordinator、账号级 blocked models 已完成 | 真实 50/100 并发按账号额度做最终验收 | [x] 功能超过 Node / [ ] 真实大并发验收未跑满 |
| Availability worker | 较完整 | 健康 worker + 手动 Direct probe + prune + clear breaker/cooldown + 错误窗口已完成 | 保留人工干预能力，不做 quota-heavy 自动全量 probe | [x] 核心完成 |
| Dashboard | 静态 HTML | React SPA，账号/模型/可用性/代理/请求/日志/配置均可操作，中文界面，表格排序分页和批量操作已完成 | 后续只补体验细节，不阻塞替代 | [x] 核心完成 |
| Auth/Admin API | 完整 | HTTP 账号管理、runtime config、model-access、stats/cache、Node Dashboard 兼容别名、关键手动 Availability API 已完成 | OAuth/本机导入/自更新保留 unavailable，避免引入 LS/本机副作用 | [x] 生产核心完成 / [ ] 低价值 legacy 不复刻 |
| Dynamic proxy | 完整 | account/global/dynamic proxy direct 出站、账号级绑定生命周期、provider-specific 生成、代理页独立账号选择、批量绑定/轮换/验证/暂停/恢复/解绑、测试、失败冷却/自动轮换、独立后台续绑维护、持久化、Dashboard 操作已完成 | 后续可按实际代理商增加模板 | [x] 核心完成 |
| Stats/logs/cache | 完整 | SQLite request stats + logs stream/export/filter + reuse cache snapshot/clear 基础 | 持久统计 + Dashboard 展示 | [x] 完成 |

## 上线验收矩阵

| 验收项 | 命令或场景 | 要求 | 状态 |
| --- | --- | --- | --- |
| Go 单测 | `go test ./...` | 全通过 | [x] |
| Dashboard 构建 | `npm run build --prefix web/dashboard` | 产物写入 `internal/dashboard/dist` | [x] |
| Dashboard 服务 | `GET /dashboard` | 返回 React SPA | [x] 本轮临时服务已验证返回嵌入 React SPA |
| 主链路进程 | `pgrep -f language_server_macos_arm64` | chat 请求期间不新增 LS | [x] 真实 full smoke 和小并发期间均确认未启动 LS |
| fake 并发 | fake direct 100 并发 | inflight 归零，账号分布不集中 | [x] |
| Redis 并发 | 多实例 fake 并发 | 不重复打爆同一账号 | [x] |
| 真实 smoke | `claude-sonnet-4.6` | chat/messages/responses text、stream、tools 通过 | [x] `MATRIX=quick/streams/tools/full` 均通过 |
| 真实 Claude Code 长工具链 | `claude --bare ... --tools Read,Bash` | 通过 Go `/v1/messages` 执行 Read、Bash、tool_result continuation 和最终回答 | [x] 4 轮成功，未启动 LS，inflight 归零 |
| 真实小并发 | `cmd/load-smoke` | 账号分布不集中，inflight 归零 | [x] 6/12、10/10 通过 |
| 受控真实场景 | `cmd/load-smoke -mode controlled` | 小批账号、模型 fallback、多人/多轮历史、请求收敛 | [x] 工具完成；真实运行证明可按组/模型止损；账号组 `7,8,9` 的 3 请求多轮样本 100% 成功 |
| 真实中高并发 | `cmd/load-smoke` | 50/100 并发稳定成功 | [ ] 当前 12 个 Trial/free-like 账号在 20 并发/50 请求触发官方 30 分钟 Sonnet 限流，不能声明通过 |

## Assumptions

- [x] Dashboard 必须重新设计，不照搬 Node 版静态 HTML。
- [x] 生产前端采用 Vite React SPA，不采用 Next.js。
- [x] Go 服务负责 API、鉴权、静态文件服务和 embed。
- [x] 默认不跑真实压测，避免浪费账号额度。
- [x] 多实例生产部署必须启用 Redis 调度。
