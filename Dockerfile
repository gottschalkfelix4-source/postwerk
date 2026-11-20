# syntax=docker/dockerfile:1

# ---- web interface ----
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# vite writes to ../server/internal/webui/dist
RUN mkdir -p ../server/internal/webui && npm run build

# ---- server ----
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS server
ARG TARGETOS TARGETARCH
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /src/server/internal/webui/dist ./internal/webui/dist
ARG VERSION=0.1.0
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/postwerk ./cmd/postwerk

# ---- runtime ----
FROM alpine:3.21
LABEL org.opencontainers.image.title="Postwerk" \
      org.opencontainers.image.description="Selbst gehosteter Mailserver mit Webmail, Verwaltung und lokalen KI-Funktionen" \
      org.opencontainers.image.source="https://github.com/gottschalkfelix4-source/postwerk"
RUN apk add --no-cache ca-certificates tzdata su-exec && mkdir -p /data
COPY --from=server /out/postwerk /usr/local/bin/postwerk
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# The process runs as PUID:PGID (dropped by the entrypoint), so all services
# listen on unprivileged ports; map them to 25/587/465/... on the host.
ENV POSTWERK_DATA_DIR=/data \
    POSTWERK_HTTP_ADDR=:8080 \
    POSTWERK_HTTPS_ADDR=:8443 \
    POSTWERK_SMTP_ADDR=:2525 \
    POSTWERK_SUBMISSION_ADDR=:5870 \
    POSTWERK_SUBMISSIONS_ADDR=:4650 \
    POSTWERK_POP3_ADDR=:1100 \
    POSTWERK_POP3S_ADDR=:9950 \
    PUID=1000 \
    PGID=1000 \
    TZ=Europe/Berlin

VOLUME /data
EXPOSE 8080 8443 2525 5870 4650 1100 9950
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
