# Stage 1: Build static Go binary
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Install CA certificates and git for module downloads
RUN apk add --no-cache ca-certificates

# Cache dependencies before copying source code
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a stripped, statically linked binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# Stage 2: Minimal non-root runtime image
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget && \
    addgroup -g 10001 -S gateway && \
    adduser -u 10001 -S -G gateway -H -s /sbin/nologin gateway

WORKDIR /app

COPY --from=builder /out/gateway /app/gateway
COPY configs/gateway.yaml /app/configs/gateway.yaml

USER 10001:10001

EXPOSE 8080 9091

HEALTHCHECK --interval=10s --timeout=2s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:9091/healthz || exit 1

ENTRYPOINT ["/app/gateway"]
CMD ["-config", "/app/configs/gateway.yaml"]