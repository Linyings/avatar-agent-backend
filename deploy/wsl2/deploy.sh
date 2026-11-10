#!/usr/bin/env bash
set -euo pipefail
source_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
target_root="${AICAT_DEPLOY_ROOT:-/opt/aicat/avatar-mvp}"
service="${1:-all}"
frontend_source="${2:-${AICAT_FRONTEND_SOURCE:-$source_root/frontend}}"
case "$service" in
  all) projects=(backend frontend);;
  backend) projects=(backend);;
  frontend) projects=(frontend);;
  *) printf 'Usage: %s [all|backend|frontend] [frontend-repository-path]\n' "$0" >&2; exit 2;;
esac
# Validate every source before rsync or configuration changes. A backend-only
# checkout need not contain the separately cloned frontend repository.
for project in "${projects[@]}"; do
  project_source="$source_root/backend"
  required_file=go.mod
  if [[ "$project" == frontend ]]; then project_source="$frontend_source"; required_file=package-lock.json; fi
  if [[ ! -f "$project_source/$required_file" ]]; then
    printf 'Missing %s repository at %s. Set -FrontendPath / AICAT_FRONTEND_SOURCE for a separate frontend checkout.\n' "$project" "$project_source" >&2
    exit 2
  fi
done
if [[ "$service" != all ]] && { [[ ! -f "$target_root/compose.yaml" ]] || [[ ! -f "$target_root/.env" ]]; }; then
  printf 'Single-service updates require an existing deployment; run an all deployment first.\n' >&2
  exit 2
fi
mkdir -p "$target_root/src"
# Serialize deployment writers as well as guarding task admission at restart.
exec 9>"$target_root/.deploy.lock"
flock -n 9 || { printf 'Another deployment is already running.\n' >&2; exit 2; }
for project in "${projects[@]}"; do
  project_source="$source_root/backend"
  if [[ "$project" == frontend ]]; then project_source="$frontend_source"; fi
  mkdir -p "$target_root/src/$project"
  rsync -a --delete --exclude=.git --exclude=node_modules --exclude=.venv-avatar \
    --exclude=data --exclude=dist --exclude=.cache --exclude=.remotion \
    --exclude=__pycache__ --exclude=test-results --exclude=.env --exclude='.env.*' \
    --exclude='*.exe' --exclude='*.log' "$project_source/" "$target_root/src/$project/"
done
if [[ "$service" == all ]]; then
cp "$source_root/deploy/wsl2/compose.yaml" "$target_root/compose.yaml"
cp "$source_root/deploy/wsl2/nginx.conf" "$target_root/nginx.conf"
if [[ ! -f "$target_root/.env" ]]; then
  python3 - "$target_root" <<'PY'
import pathlib, secrets, sys
root=pathlib.Path(sys.argv[1])
password=secrets.token_urlsafe(18)
values={
 'POSTGRES_PASSWORD':secrets.token_hex(24), 'SECRET_KEY':secrets.token_hex(32),
 'WEB_PORT':'8080', 'API_PORT':'18000', 'ADMIN_PASSWORD':password,
 'AVATAR_MVP_BASELINE_APPROVED':'false', 'QWEN_TTS_MODEL':'qwen3-tts-flash',
 'RUNNINGHUB_BASE_URL':'https://www.runninghub.cn', 'RUNNINGHUB_API_KEY':'',
 'VIDEO_PROVIDER':'runninghub', 'VIDEO_API_PROTOCOL':'', 'VIDEO_API_BASE_URL':'',
 'VIDEO_API_KEY':'', 'VIDEO_MODEL':'', 'VIDEO_RESOLUTION':'', 'MINIMAX_API_KEY':'',
 'DASHSCOPE_API_KEY':'', 'OPENAI_FLASH_API_KEY':'',
 'OPENAI_FLASH_API_BASE_URL':'https://api.openai.com/v1', 'OPENAI_FLASH_MODEL':'gpt-4.1-mini',
 'CORS_ORIGINS':'http://localhost:8080,http://127.0.0.1:8080',
}
env=root/'.env'
env.write_text(''.join(f'{k}={v}\n' for k,v in values.items()))
env.chmod(0o600)
access=root/'LOCAL_ACCESS.txt'
access.write_text(f'URL: http://localhost:8080/\nEmail: localadmin@localhost.test\nPassword: {password}\n')
access.chmod(0o600)
PY
fi
python3 - "$target_root/.env" <<'PY'
import pathlib, sys
env = pathlib.Path(sys.argv[1])
existing = env.read_text()
keys = {line.split('=', 1)[0].strip() for line in existing.splitlines() if '=' in line and not line.lstrip().startswith('#')}
defaults = {'VIDEO_PROVIDER': 'runninghub', 'VIDEO_API_PROTOCOL': '', 'VIDEO_API_BASE_URL': '',
            'AVATAR_WORKFLOW_TEST_MODE': 'false',
            'RUNNINGHUB_LLM_BASE_URL': '', 'RUNNINGHUB_LLM_MODEL': 'google/gemini-3.1-flash-lite-preview',
            'VIDEO_API_KEY': '', 'VIDEO_MODEL': '', 'VIDEO_RESOLUTION': '', 'MINIMAX_API_KEY': '',
            'TTS_MODEL': 'doubao-seed-tts-2.0', 'TTS_SPEAKER': '',
            'TTS_LANGUAGE': 'zh-cn', 'TTS_FORMAT': 'wav',
            'VIDEO_REFERENCE_AUDIO_SUPPORTED': 'false', 'VIDEO_UPLOAD_PATH': '',
            'VIDEO_GENERATION_PATH': '', 'VIDEO_QUERY_PATH': '',
            'TAVILY_API_KEY': ''}
missing = ''.join(f'{key}={value}\n' for key, value in defaults.items() if key not in keys)
if missing:
    env.write_text(existing.rstrip('\n') + '\n' + missing)
env.chmod(0o600)
PY
fi
cd "$target_root"
# Preserve the images actually serving users, even after an earlier failed build
# has moved :latest. One rollback tag per service is sufficient.
case "$service" in
  all) updated_services=(api web);;
  backend) updated_services=(api);;
  frontend) updated_services=(web);;
esac
for app_service in "${updated_services[@]}"; do
  container="$(docker compose ps -q "$app_service")"
  if [[ -n "$container" ]]; then
    running_image="$(docker inspect --format '{{.Image}}' "$container")"
    if docker image inspect "$running_image" >/dev/null 2>&1; then
      docker tag "$running_image" "aicat-avatar-$app_service:previous"
    else
      printf 'WARNING: %s container image metadata is missing; cannot create a new rollback tag. Existing service remains running during build.\n' "$app_service" >&2
    fi
  fi
done
if docker compose ps --status running --services | grep -qx api; then
  python3 "$source_root/deploy/wsl2/storage-maintenance.py" --root "$target_root" --apply
fi
if [[ "$service" != frontend ]]; then
runtime_matches() {
  local candidate="$1" local_sum cache_sum
  docker image inspect "$candidate" >/dev/null 2>&1 || return 1
  for runtime_file in package.json package-lock.json requirements.txt; do
    local_sum="$(sha256sum "src/backend/avatar-runtime/$runtime_file" | cut -d' ' -f1)"
    cache_sum="$(docker run --rm --entrypoint sha256sum "$candidate" "/app/avatar-runtime/$runtime_file" 2>/dev/null | cut -d' ' -f1 || true)"
    [[ "$local_sum" == "$cache_sum" ]] || return 1
  done
  docker run --rm --entrypoint test "$candidate" -d /app/.remotion 2>/dev/null
}
# Prefer the stable base. A fully built image from an interrupted deployment can
# also supply matching dependencies; application code is always rebuilt below.
for candidate in aicat-avatar-runtime-cache:local aicat-avatar-api:previous aicat-avatar-api:latest; do
  if runtime_matches "$candidate"; then
    if [[ "$candidate" != aicat-avatar-runtime-cache:local ]]; then
      docker tag "$candidate" aicat-avatar-runtime-cache:local
    fi
    export AVATAR_API_DOCKERFILE=deploy/Dockerfile.avatar-wsl.incremental
    printf 'Reusing verified installed media dependencies and browser.\n'
    break
  fi
done
fi
docker compose config --quiet
if docker compose ps --status running --services | grep -qx postgres; then
  backup_stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p backups
  chmod 700 backups
  docker compose exec -T postgres pg_dump -U avatar -d avatar -Fc > "backups/database-$backup_stamp.dump"
  chmod 600 "backups/database-$backup_stamp.dump"
  cp -p .env "backups/config-$backup_stamp.env"
  printf 'Database/config backup: %s/backups/%s\n' "$target_root" "$backup_stamp"
fi
# Web bundling and Chromium catalog rendering can together exceed the WSL
# memory budget. Build serially; keep the current services serving throughout.
case "$service" in
  all)
    docker compose build api
    docker compose build web
    # The guard session lives in postgres: never recreate it under that lock.
    # Existing database configuration upgrades require separate maintenance.
    if ! docker compose ps --status running --services | grep -qx postgres; then
      docker compose up -d --no-build --wait --wait-timeout 300 postgres
    fi
    python3 "$source_root/deploy/wsl2/deployment-guard.py" "$target_root" -- \
      docker compose up -d --no-build --no-deps api
    # Startup recovery takes the same admission lock, so health readiness must
    # be awaited AFTER release. --no-recreate makes this a readiness-only check.
    docker compose up -d --no-build --no-deps --no-recreate --wait --wait-timeout 300 api
    docker compose up -d --no-build --no-deps --wait --wait-timeout 300 web
    ;;
  backend)
    docker compose build api
    python3 "$source_root/deploy/wsl2/deployment-guard.py" "$target_root" -- \
      docker compose up -d --no-build --no-deps api
    docker compose up -d --no-build --no-deps --no-recreate --wait --wait-timeout 300 api
    ;;
  frontend)
    api_before="$(docker compose ps -q api)"
    docker compose build web
    docker compose up -d --no-build --no-deps --wait --wait-timeout 300 web
    test "$api_before" = "$(docker compose ps -q api)"
    ;;
esac
if [[ ! -f .admin-ready ]]; then
  docker compose exec -T api setup-admin --create --username localadmin \
    --email localadmin@localhost.test --display-name '本地管理员' --yes
  touch .admin-ready
fi
docker compose exec -T api /opt/avatar-venv/bin/python /app/avatar-runtime/pipeline.py doctor
curl --fail --silent http://127.0.0.1:8080/api-health
python3 "$source_root/deploy/wsl2/verify.py"
python3 "$source_root/deploy/wsl2/record-revisions.py" "$target_root" "$service" "$source_root" "$frontend_source"
if [[ "$service" != frontend ]] && [[ "${AVATAR_API_DOCKERFILE:-deploy/Dockerfile.avatar-wsl}" == deploy/Dockerfile.avatar-wsl ]]; then
  # Seed the stable dependency base only after a full build has passed health.
  docker tag "$(docker inspect --format '{{.Image}}' "$(docker compose ps -q api)")" aicat-avatar-runtime-cache:local
fi
python3 "$source_root/deploy/wsl2/docker-maintenance.py" --root "$target_root" --apply
printf '\nFrontend: http://%s:8080/\n' "$(hostname -I | awk '{print $1}')"
docker compose ps
