# WindsurfAPI-Go

Direct-only Windsurf API proxy written in Go. The production path talks to Windsurf cloud directly and does not start the local Windsurf Language Server.

## Features

- OpenAI-compatible `POST /v1/chat/completions`
- Anthropic-compatible `POST /v1/messages`
- OpenAI Responses-compatible `POST /v1/responses`
- `GET /v1/models`
- Claude-only production target, verified around `claude-sonnet-4.6`
- Account scheduler with cooldown, RPM reservation, quota weighting and optional Redis coordination
- Dynamic proxy binding, verification, rotation, renewal and Dashboard batch control
- Chinese React Dashboard embedded into the Go binary
- Docker / docker-compose deployment

## Requirements

- Go 1.25+
- Node.js 22+ if you want to rebuild the Dashboard locally
- Docker if you want container deployment
- Windsurf Devin session tokens in `account.txt`

## Local Run

Create a local config from the tracked example first:

```bash
cp configs/default.example.yaml configs/default.yaml
```

`configs/default.yaml` is intentionally ignored by git. Put server-specific API keys, Dashboard password and proxy credentials there. Docker Compose mounts this file into the container as read-only config.

Start Redis first. The default config enables Redis scheduler coordination and falls back to single-process mode if Redis is unavailable:

```bash
cd /Users/zhangyu/code/myProject/supertoken-projects/WindsurfAPI-Go
docker compose up -d windsurfapi-go-redis
```

Build Dashboard assets, then run the Go server:

```bash
cd web/dashboard && npm ci && npm run build
cd ../..
go run ./cmd/server -config configs/default.yaml
```

Default endpoints:

- API: `http://127.0.0.1:3456`
- Dashboard: `http://127.0.0.1:3456/dashboard`
- Health: `http://127.0.0.1:3456/healthz`
- Ready: `http://127.0.0.1:3456/readyz`

Default API key in `configs/default.example.yaml`:

```text
sk-windsurf-default
```

Default Dashboard login:

```text
admin / admin
```

Change these in your local `configs/default.yaml` before exposing the service.

## Import Accounts

Put account tokens in `account.txt`, then dry-run first:

```bash
go run ./cmd/import-accounts -file account.txt
```

Apply the import:

```bash
go run ./cmd/import-accounts -file account.txt -apply
```

Check account health without consuming chat quota:

```bash
go run ./cmd/account-check -model claude-sonnet-4.6 -smoke=false
```

Check and write health state back to SQLite:

```bash
go run ./cmd/account-check -model claude-sonnet-4.6 -smoke=false -apply
```

Run a real direct chat smoke only when you are ready to spend quota:

```bash
go run ./cmd/direct-smoke -account 1 -model claude-sonnet-4.6 -timeout 45s -probe-api-chat
```

## Model Routing Notes

Public model IDs are intentionally small:

- `claude-opus-4-7`
- `claude-opus-4-6`
- `claude-opus-4-6-thinking`
- `claude-sonnet-4-6`
- `claude-haiku-4-5`
- `claude-haiku-4-5-20251001`

`reasoning_effort` / `output_config.effort` routes Opus 4.7 requests to Windsurf's low/medium/high/xhigh/max UIDs. It does not inject local "thinking" prompt text into the upstream request. Direct tool mode defaults to `native`, which keeps the upstream Claude request shape closest to Windsurf and avoids injecting local tool protocol text into model-fingerprint or conformance tests. If Opus 4.7 receives `tools` with `tool_choice=auto` and upstream rejects native tools with an internal error, Go retries once as a plain native text request and records the event in `/debug/direct` as `tool_fallbacks`. If a Claude Code/Cline workload proves that Opus 4.7 native tools fail for real tool use, switch `direct.tool_mode` to `auto` or `emulated` for that deployment.

Runtime/config switches:

```yaml
direct:
  tool_mode: "native"    # default, best for model fidelity
  # tool_mode: "emulated"  # text tool protocol compatibility mode
  # tool_mode: "auto"      # older behavior: Opus 4.7 tools use emulation
```

```bash
# Environment override if needed.
WINDSURFAPI_DIRECT_TOOL_MODE=native
```

## Dynamic Proxy Binding

Configure the provider in local `configs/default.yaml` or Dashboard Settings:

```yaml
proxy:
  account_binding: true
  auto_bind_new_accounts: false
  renew_before_ms: 900000
  worker_interval_ms: 60000
  provider: "novproxy"
  protocol: "http"
  host: "us.novproxy.io"
  port: 1000
  username_template: "xxx-region-{region}-st-{state}-sid-{sid}-t-{ttl}"
  password: "your-proxy-password"
  region: "US"
  state: "New Jersey"
  ttl_minutes: 120
```

Dashboard path: `/dashboard/proxy`.

The Proxy page has its own account table. Select accounts there, then use:

- `绑定已选账号 IP`
- `更新/换 IP`
- `验证已选 IP`
- `暂停已选绑定`
- `恢复已选绑定`
- `解绑已选账号`
- `自动检测并续绑`

The background worker runs every `worker_interval_ms` and renews failed, expired and soon-to-expire bindings. If `auto_bind_new_accounts=true`, it also binds enabled accounts that do not have an active binding.

## Docker

Build the image:

```bash
docker build -t windsurfapi-go:local .
```

Or use Make:

```bash
make docker-build
```

Start the service and Redis:

```bash
docker compose up -d --build
```

Compose mounts `configs/default.yaml` into the container. The mount uses `create_host_path: false`, so Docker fails fast if the local config file does not exist instead of creating a directory by accident.

```yaml
services:
  windsurfapi-go:
    volumes:
      - ./data:/app/data
      - type: bind
        source: ./configs/default.yaml
        target: /app/configs/default.yaml
        read_only: true
        bind:
          create_host_path: false
```

View logs:

```bash
docker compose logs -f windsurfapi-go
```

Stop:

```bash
docker compose down
```

The Docker image build is reproducible: it runs Dashboard `npm ci && npm run build`, then compiles the Go server and embeds the generated assets.

## Docker Compose Defaults

`docker-compose.yml` exposes:

- Go API on `3456`
- Redis on host `6380`, container `6379`
- SQLite and Redis data under local `./data`

Important config values for Docker Compose:

```yaml
sqlite:
  path: "/app/data/windsurf.db"

redis:
  addr: "windsurfapi-go-redis:6379"
```

For production, change `server.api_keys` and `dashboard.password` in local `configs/default.yaml`. Proxy provider settings can also be supplied through local `configs/default.yaml` or Dashboard Settings after login.

## Smoke Tests

Run unit tests:

```bash
go test ./...
```

Run quick API regression against a running server:

```bash
BASE_URL=http://127.0.0.1:3456 \
API_KEY=sk-windsurf-default \
MODEL=claude-sonnet-4.6 \
./scripts/client-regression.sh
```

Run the fuller real client matrix only when you accept quota usage:

```bash
MATRIX=full \
BASE_URL=http://127.0.0.1:3456 \
API_KEY=sk-windsurf-default \
MODEL=claude-sonnet-4.6 \
./scripts/client-regression.sh
```

## Production Notes

- Main `/v1/*` routes are Direct-only and should not start `language_server_macos_arm64`.
- Single-instance testing does not require nginx.
- Public production should still use nginx, Caddy, Traefik or a cloud load balancer for TLS, request body limits, SSE no-buffering and outer rate limits.
- Multi-instance deployment must enable Redis scheduler coordination. Without Redis, do not scale multiple Go instances against the same account pool.
- Keep `account.txt`, `data/`, `.env*`, local `default.yaml`, local `configs/default.yaml`, logs and binaries out of git.

More deployment details are in `PRODUCTION.md`.
