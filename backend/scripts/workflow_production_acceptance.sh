#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
FRONTEND_DIR=${FRONTEND_DIR:-"$ROOT_DIR/../frontend"}

echo "== Backend: go test ./... =="
(
  cd "$ROOT_DIR"
  go test ./...
)

if [ "${RUN_DB_CHECKS:-0}" = "1" ]; then
  echo "== Backend: workflow migration dry-run =="
  (
    cd "$ROOT_DIR"
    go run ./cmd/workflow-migrate --dry-run
  )

  echo "== Backend: workflow migration verify-only =="
  (
    cd "$ROOT_DIR"
    go run ./cmd/workflow-migrate --verify-only
  )
else
  echo "== Backend: workflow migration checks skipped =="
  echo "Set RUN_DB_CHECKS=1 with DATABASE_URL configured to run dry-run and verify-only."
fi

if [ -d "$FRONTEND_DIR" ]; then
  echo "== Frontend: npm run build =="
  (
    cd "$FRONTEND_DIR"
    npm run build
  )

  echo "== Frontend: npm run lint =="
  (
    cd "$FRONTEND_DIR"
    npm run lint
  )

  echo "== Frontend: npm run test -- --run =="
  (
    cd "$FRONTEND_DIR"
    npm run test -- --run
  )
else
  echo "== Frontend checks skipped =="
  echo "FRONTEND_DIR does not exist: $FRONTEND_DIR"
fi
