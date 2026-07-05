# l00prite CLI-OS — Go build. Produces a single fully-static binary (CGO disabled, pure-Go SQLite),
# so the runtime image needs no shared libraries.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags='-s -w' -o /l00prite ./cmd/l00prite

# Runtime: tiny alpine (only for the entrypoint shell script + healthcheck wget). The binary itself is
# static and would run on scratch.
FROM alpine:3.20
RUN adduser -D -u 10001 l00prite
WORKDIR /app
COPY --from=build /l00prite /usr/local/bin/l00prite
COPY install/docker-entrypoint.sh /app/install/docker-entrypoint.sh
RUN chmod +x /app/install/docker-entrypoint.sh

# Container runtime config. Binding 0.0.0.0 is an explicit opt-in (LOOPRITE_ALLOW_INSECURE_BIND);
# put a TLS-terminating reverse proxy in front before exposing this publicly.
ENV LOOPRITE_HOME=/data \
    LOOPRITE_HOST=0.0.0.0 \
    LOOPRITE_PORT=8787 \
    LOOPRITE_ALLOW_INSECURE_BIND=1

RUN mkdir -p /data && chown l00prite:l00prite /data
VOLUME ["/data"]
EXPOSE 8787
USER l00prite

HEALTHCHECK --interval=30s --timeout=4s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8787/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/install/docker-entrypoint.sh"]
