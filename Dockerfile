FROM node:22-bookworm-slim AS dashboard-build

WORKDIR /src
COPY web/dashboard/package*.json ./web/dashboard/
RUN cd web/dashboard && npm ci
COPY web/dashboard ./web/dashboard
RUN cd web/dashboard && npm run build

FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=dashboard-build /src/internal/dashboard/dist ./internal/dashboard/dist
RUN CGO_ENABLED=1 go build -o /out/windsurfapi-go ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl sqlite3 \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/windsurfapi-go /usr/local/bin/windsurfapi-go
COPY configs ./configs

EXPOSE 3456
VOLUME ["/app/data"]

CMD ["windsurfapi-go", "-config", "configs/default.yaml"]
