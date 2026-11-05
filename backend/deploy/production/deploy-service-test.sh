#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
deploy_script="$script_dir/deploy-service.sh"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

bin_dir="$test_root/bin"
deploy_root="$test_root/deploy"
mkdir -p "$bin_dir" "$deploy_root"
printf '%s\n' 'FRONTEND_IMAGE=registry.example/frontend:sha-old' 'BACKEND_IMAGE=registry.example/api:sha-old' > "$deploy_root/.env"
touch "$deploy_root/docker-compose.yml"

cat > "$bin_dir/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s\n' "$*" >> "$DEPLOY_TEST_LOG"
if [[ "$*" == *"compose"*" ps -q "* ]]; then
  printf 'container-id\n'
elif [[ "$*" == *"inspect"* ]]; then
  printf 'healthy\n'
fi
EOF

cat > "$bin_dir/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'curl %s\n' "$*" >> "$DEPLOY_TEST_LOG"
[[ "${DEPLOY_TEST_HEALTH_FAIL:-0}" != 1 ]]
EOF
chmod +x "$bin_dir/docker" "$bin_dir/curl"

export PATH="$bin_dir:$PATH"
export DEPLOY_ROOT="$deploy_root"
export DEPLOY_TEST_LOG="$test_root/calls.log"
export DEPLOY_HEALTH_ATTEMPTS=2
export DEPLOY_HEALTH_INTERVAL=0

if "$deploy_script" unknown image:test; then
  echo "unknown service unexpectedly succeeded" >&2
  exit 1
else
  status=$?
  [[ "$status" == 2 ]] || { echo "unknown service exited $status, expected 2" >&2; exit 1; }
fi

"$deploy_script" frontend registry.example/frontend:sha-new
grep -qx 'FRONTEND_IMAGE=registry.example/frontend:sha-new' "$deploy_root/.env"
grep -q 'docker compose up -d --no-deps frontend' "$DEPLOY_TEST_LOG"
grep -q 'curl .*https://narratoai.cn/health' "$DEPLOY_TEST_LOG"

: > "$DEPLOY_TEST_LOG"
if DEPLOY_TEST_HEALTH_FAIL=1 "$deploy_script" api registry.example/api:sha-bad; then
  echo "failed API health check unexpectedly succeeded" >&2
  exit 1
fi
grep -qx 'BACKEND_IMAGE=registry.example/api:sha-old' "$deploy_root/.env"
[[ "$(grep -c 'docker compose up -d --no-deps api' "$DEPLOY_TEST_LOG")" -ge 2 ]]

echo "deploy-service contract tests passed"
