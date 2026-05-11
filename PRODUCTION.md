# WindsurfAPI-Go Production Notes

##上线前检查

- `go test ./...`
- `go run ./cmd/account-check -model claude-sonnet-4.6 -smoke=false`
- 确认 `server.max_request_body_bytes` 或 `WINDSURFAPI_MAX_REQUEST_BODY_BYTES` 足够覆盖 Claude Code 长上下文，但不要无限制；默认是 25MB。
- 服务启动后先跑轻量真实回归：`BASE_URL=http://127.0.0.1:3456 API_KEY=sk-windsurf-default ./scripts/client-regression.sh`
- 生产验收前再跑完整真实矩阵：`MATRIX=full BASE_URL=http://127.0.0.1:3456 API_KEY=sk-windsurf-default ./scripts/client-regression.sh`
- `go run ./cmd/load-smoke -route chat -scenario text -concurrency 10 -requests 50`
- `go run ./cmd/load-smoke -route chat -scenario tools -concurrency 10 -requests 50`
- `go run ./cmd/load-smoke -route chat -scenario tool-result -concurrency 10 -requests 50`
- 流式压测另跑：`go run ./cmd/load-smoke -route chat -scenario text -stream -concurrency 10 -requests 50`
- 同样对 `-route messages` 和 `-route responses` 各跑一轮。
- 主链路不应出现 `language_server_macos_arm64` 进程。

##账号容量基线

默认调度 RPM：`free=10/min`，`unknown=20/min`，`pro=60/min`。真实可用容量还会受 Windsurf 上游账号限制影响，所以建议按 60%-70% 安全水位规划。

| 有效账号数 | free 安全 RPM | unknown 安全 RPM | pro 安全 RPM | 建议并发起点 |
| --- | ---: | ---: | ---: | ---: |
| 10 | 60/min | 120/min | 360/min | 20-50 |
| 20 | 120/min | 240/min | 720/min | 50-100 |
| 50 | 300/min | 600/min | 1800/min | 100-300 |

单账号不建议长期高并发集中压测。调度器会按 inflight、quota、RPM headroom 和 last_used 分散流量，但如果账号池少于 10 个，100 并发只适合短 smoke，不适合持续生产压测。

##健康检查

- `/healthz`：进程存活。
- `/readyz`：服务可接流量；如果 `health.ready_require_check=true`，需要至少一次健康刷新通过。
- `/debug/accounts`：账号、RPM、cooldown、quota 和健康摘要。
- `/debug/direct`：direct host、成功失败计数和最近错误。
- `/debug/scheduler`：最近调度事件和 reuse 状态。

##部署

本阶段默认启用 Redis 调度协调；`configs/default.yaml` 在 Redis 不可用时会降级到单进程调度，`docker-compose.yml` 生产路径通过 `WINDSURFAPI_SCHEDULER_REDIS_FAIL_CLOSED=true` 强制 Redis 不可用则拒绝启动。可以用 Docker。镜像构建会先运行 Dashboard 的 `npm ci && npm run build`，再编译 Go，因此生产镜像不依赖本机已有的 `internal/dashboard/dist`：

```bash
docker compose up -d --build
```

只构建镜像：

```bash
make docker-build
```

也可以用 `deploy/windsurfapi-go.service` 作为 systemd 模板。部署时把二进制、`configs/`、`data/` 放到 `/opt/windsurfapi-go`，再按实际路径调整 service。

多实例部署前不要直接横向扩容共享同一个账号池，否则多个进程会各自维护 RPM/inflight，可能重复打爆同一账号。需要多实例时，先启动 Redis，并设置：

```yaml
scheduler:
  redis_enabled: true
  redis_fail_closed: true
  max_inflight_per_account: 4
  reservation_ttl_seconds: 180
```

Redis coordinator 会共享每账号 inflight、短期 RPM reservation 和 cooldown。SQLite 仍是账号元数据来源。多实例生产建议打开 `redis_fail_closed`，Redis 不可用时直接拒绝启动，避免各实例退回单机调度后重复打爆同一批账号。

本地推荐用项目自带的专用 Redis 容器，避免和其它项目共用状态：

```bash
docker compose up -d windsurfapi-go-redis
docker exec windsurfapi-go-redis redis-cli ping
go run ./cmd/redis-coord-smoke -redis 127.0.0.1:6380 -db data/redis-coord-smoke.db -accounts 4 -workers 40 -max-inflight 2
```

`cmd/redis-coord-smoke` 不会调用 Windsurf/Claude 上游，不消耗账号额度。它会创建两个独立 `account.Manager` 共享同一个 Redis coordinator，验证并发 reservation 不超过每账号 inflight 上限、释放后 inflight 归零，并验证一个实例写入的账号-模型 cooldown 能被另一个实例看到。

##Nginx / 反向代理

Node 版的 `nginx.conf` 主要用于多副本部署时做反向代理和负载均衡：

- `hash $auth_token consistent`：按 `Authorization: Bearer ...` 的 API key 做 sticky session，让同一个调用方稳定打到同一个 Node 进程。Node 版很多 cascade/reuse 状态在进程内存里，非 sticky 容易丢上下文。
- `proxy_buffering off`、`proxy_cache off`、`chunked_transfer_encoding on`：保证 SSE/streaming 不被 nginx 缓冲。
- `client_max_body_size 25m`：限制长上下文请求体大小。
- `limit_req`：做一层简单 IP 限流。

Go 版单实例不需要 nginx。Go 版多实例仍然可以放 nginx、Caddy、Traefik 或云 LB 前面，但 sticky 不再是硬性依赖：Go 的生产方向是 Direct-only，加 Redis coordinator 共享 reservation/cooldown/RPM，reuse 也带 caller scope。反向代理在 Go 多实例里主要负责 TLS、SSE 不缓冲、外层限流、健康检查和流量分发。若没有启用 Redis，多实例必须继续 sticky 或直接禁止横向扩容，否则多个 Go 进程会重复打同一批账号。

##当前限制

- Direct-only，不依赖 LS；LS 代码只保留 legacy smoke/debug。
- Claude-only，不恢复 Node 版多 provider。
- Responses API 是生产常用最小兼容，不是 Node 全量协议全集。
- Redis 分布式调度已提供可选 coordinator，但默认关闭；多实例上线前仍需单独做压测和故障演练。
