# WindsurfAPI-Go

Direct-only Windsurf API proxy written in Go. The production path talks to Windsurf cloud directly and does not start the local Windsurf Language Server.

## Features

- OpenAI-compatible `POST /v1/chat/completions`
- Anthropic-compatible `POST /v1/messages`
- OpenAI Responses-compatible `POST /v1/responses`
- `GET /v1/models`
- Claude-only production target, verified around `claude-sonnet-4.6`
- Account scheduler with cooldown, RPM reservation, quota weighting and optional Redis coordination
- Dynamic proxy binding and rotation
- Chinese React Dashboard embedded into the Go binary
- Docker / docker-compose deployment

## Requirements

- Go 1.25+
- Node.js 22+ if you want to rebuild the Dashboard locally
- Docker if you want container deployment
- Windsurf Devin session tokens in `account.txt`

## Local Run

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

Default API key in `configs/default.yaml`:

```text
sk-windsurf-default
```

Default Dashboard login:

```text
admin / admin
```

Change these before exposing the service.

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

Important environment variables:

```bash
WINDSURFAPI_API_KEYS=sk-windsurf-default
WINDSURFAPI_DASHBOARD_PASSWORD=change-me-dashboard-password
WINDSURFAPI_DB_PATH=/app/data/windsurf.db
WINDSURFAPI_REDIS_ADDR=windsurfapi-go-redis:6379
WINDSURFAPI_SCHEDULER_REDIS_ENABLED=true
WINDSURFAPI_SCHEDULER_REDIS_FAIL_CLOSED=true
```

For production, change `WINDSURFAPI_API_KEYS` and `WINDSURFAPI_DASHBOARD_PASSWORD`.

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
- Keep `account.txt`, `data/`, `.env*`, local `default.yaml`, logs and binaries out of git.

More deployment details are in `PRODUCTION.md`.
