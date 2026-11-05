FROM golang:1.26.2-alpine AS builder

WORKDIR /src

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

RUN apk add --no-cache build-base ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN mkdir -p /out \
  && CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/narratoai-api ./cmd/api \
  && CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/workflow-migrate ./cmd/workflow-migrate \
  && CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/credits-migrate ./cmd/credits-migrate \
  && CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/setup-admin ./scripts/setup_admin.go

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata wget \
  && addgroup -S narratoai \
  && adduser -S -G narratoai narratoai

WORKDIR /app

COPY --from=builder /out/narratoai-api /usr/local/bin/narratoai-api
COPY --from=builder /out/workflow-migrate /usr/local/bin/workflow-migrate
COPY --from=builder /out/credits-migrate /usr/local/bin/credits-migrate
COPY --from=builder /out/setup-admin /usr/local/bin/setup-admin
COPY --from=builder /src/prompt ./prompt

RUN mkdir -p /app/data/wechat_certs \
  && chown -R narratoai:narratoai /app

USER narratoai

ENV TZ=Asia/Shanghai \
  HOST=0.0.0.0 \
  PORT=8000 \
  ENVIRONMENT=production \
  DEBUG=false \
  API_V1_PREFIX=/api/v1 \
  WECHAT_CERT_DIR=/app/data/wechat_certs

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --quiet --tries=1 --output-document=/dev/null "http://127.0.0.1:${PORT:-8000}/health" || exit 1

CMD ["narratoai-api"]
