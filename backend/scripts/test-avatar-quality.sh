#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
docker run --rm -v "$PWD":/src -v /opt/aicat/avatar-mvp/.go-test-cache:/root/.cache/go-build -v avatar-gomod:/go/pkg/mod -w /src -e GOPROXY=https://goproxy.cn,direct -e GOFLAGS=-p=2 -e CGO_ENABLED=1 -e GOTOOLCHAIN=auto golang:1.26.2-bookworm go test ./internal/server ./internal/avatar ./internal/agent -run 'Portrait|Talking|Persisted|RunKeeps|Skill|AvatarWorkflow|F06Injects' -count=1
