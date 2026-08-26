# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend-build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/shadowflow ./cmd/server && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/collect ./cmd/collect

FROM alpine:3.22
RUN apk add --no-cache ca-certificates sqlite tzdata \
    && addgroup -g 10001 -S shadowflow \
    && adduser -u 10001 -S -G shadowflow shadowflow
WORKDIR /app
COPY --from=backend-build /out/shadowflow /app/shadowflow
COPY --from=backend-build /out/collect /app/collect
COPY --from=frontend-build /src/frontend/dist /app/web
COPY backend/config/trading_calendar.json /app/config/trading_calendar.json
COPY scripts /app/scripts
# The calendar auto-updater rewrites its own file, so /app/config must stay
# writable by the runtime user; everything else is read-only for it.
RUN chown -R shadowflow:shadowflow /app/config
ENV TZ=Asia/Shanghai \
    SHADOWFLOW_DATABASE_PATH=/data/shadowflow.db \
    SHADOWFLOW_CALENDAR_PATH=/app/config/trading_calendar.json \
    SHADOWFLOW_STATIC_DIR=/app/web \
    SHADOWFLOW_LISTEN_ADDR=0.0.0.0:8080
EXPOSE 8080
# Run unprivileged: any RCE in the HTTP stack must not hand out root on the
# host-mounted data and backup volumes. Deployments must chown the mounted
# /data and /backups directories to uid 10001 once before upgrading.
USER 10001:10001
# start-period covers one-off startup migrations over the full database,
# which have taken minutes; marking the container unhealthy mid-migration
# invites an operator (or orchestrator) to kill it at the worst moment.
HEALTHCHECK --interval=30s --timeout=3s --start-period=300s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/health/ready || exit 1
ENTRYPOINT ["/app/shadowflow"]
