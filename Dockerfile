# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:22.23.2-alpine3.24 AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund
COPY frontend/ ./
COPY backend/openapi.yaml /src/backend/openapi.yaml
RUN npm run lint && npm test && npm run build

FROM frontend-build AS frontend-audit
# CI changes this per run so vulnerability results cannot stay cached forever.
ARG VULN_DB_REFRESH=manual
RUN printf 'Vulnerability database refresh: %s\n' "$VULN_DB_REFRESH" \
    && npm audit --audit-level=low --registry=https://registry.npmjs.org

FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine3.24 AS backend-build
ARG TARGETOS
ARG TARGETARCH
ENV GOTOOLCHAIN=local
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go test -count=1 ./... && go vet ./...
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/shadowflow ./cmd/server && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/collect ./cmd/collect

FROM backend-build AS backend-audit
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
ARG VULN_DB_REFRESH=manual
# Scan the target-platform executables, not a host build or just go.mod.
RUN printf 'Vulnerability database refresh: %s\n' "$VULN_DB_REFRESH" \
    && go version -m /out/shadowflow /out/collect \
    && govulncheck -mode=binary /out/shadowflow \
    && govulncheck -mode=binary /out/collect

FROM alpine:3.24.1
ARG VULN_DB_REFRESH=manual
RUN printf 'Runtime package refresh: %s\n' "$VULN_DB_REFRESH" \
    && apk upgrade --no-cache \
    && apk add --no-cache ca-certificates sqlite tzdata \
    && addgroup -g 10001 -S shadowflow \
    && adduser -u 10001 -S -G shadowflow shadowflow
WORKDIR /app
COPY --from=backend-audit /out/shadowflow /app/shadowflow
COPY --from=backend-audit /out/collect /app/collect
COPY --from=frontend-audit /src/frontend/dist /app/web
COPY backend/config/trading_calendar.json /app/config/trading_calendar.json
COPY scripts /app/scripts
# The calendar auto-updater rewrites its own file, so /app/config must stay
# writable by the runtime user; everything else is read-only for it.
# /data and /backups must exist inside the image and be writable by the
# runtime user: the CI smoke test (and any run without bind mounts) starts
# the container bare, and without these the unprivileged server cannot
# create its database. Bind mounts shadow them with host ownership, so
# deployments still chown the host directories to uid 10001 once.
RUN chown -R shadowflow:shadowflow /app/config \
    && mkdir -p /data /backups \
    && chown shadowflow:shadowflow /data /backups
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
