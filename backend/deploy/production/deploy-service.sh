#!/usr/bin/env bash
set -euo pipefail

service="${1:-}"
image="${2:-}"
case "$service" in
  frontend) image_key=FRONTEND_IMAGE; health_url=https://narratoai.cn/health ;;
  api) image_key=BACKEND_IMAGE; health_url=https://narratoai.cn/api/health ;;
  *) echo "usage: $0 <frontend|api> <image>" >&2; exit 2 ;;
esac

if [[ ! "$image" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+$ ]]; then
  echo "invalid full image reference: $image" >&2
  exit 2
fi

deploy_root="${DEPLOY_ROOT:-/root/narratoai}"
env_file="$deploy_root/.env"
compose_file="$deploy_root/docker-compose.yml"
lock_file="$deploy_root/.deploy.lock"
health_attempts="${DEPLOY_HEALTH_ATTEMPTS:-30}"
health_interval="${DEPLOY_HEALTH_INTERVAL:-2}"

[[ -f "$env_file" ]] || { echo "missing $env_file" >&2; exit 1; }
[[ -f "$compose_file" ]] || { echo "missing $compose_file" >&2; exit 1; }
[[ "$health_attempts" =~ ^[1-9][0-9]*$ ]] || { echo "invalid DEPLOY_HEALTH_ATTEMPTS" >&2; exit 2; }

exec 9>"$lock_file"
flock -x 9
cd "$deploy_root"

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="$deploy_root/.env.backup.$timestamp.$$"
cp -- "$env_file" "$backup"

update_image() {
  local key="$1" value="$2" source="$3" destination="$4"
  awk -v key="$key" -v value="$value" '
    BEGIN { replaced = 0 }
    index($0, key "=") == 1 { print key "=" value; replaced = 1; next }
    { print }
    END { if (!replaced) print key "=" value }
  ' "$source" > "$destination"
}

wait_until_healthy() {
  local attempt container_id container_health
  for ((attempt = 1; attempt <= health_attempts; attempt++)); do
    container_id="$(docker compose ps -q "$service")"
    container_health=""
    if [[ -n "$container_id" ]]; then
      container_health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container_id" 2>/dev/null || true)"
    fi
    if [[ "$container_health" == healthy || "$container_health" == running ]]; then
      if curl --fail --silent --show-error --max-time 10 "$health_url" >/dev/null; then
        return 0
      fi
    fi
    sleep "$health_interval"
  done
  return 1
}

rollback() {
  echo "deployment failed; restoring previous $service image" >&2
  cp -- "$backup" "$env_file"
  docker compose up -d --no-deps "$service" || true
}

next_env="$(mktemp "$deploy_root/.env.next.XXXXXX")"
trap 'rm -f "$next_env"' EXIT
update_image "$image_key" "$image" "$env_file" "$next_env"
chmod --reference="$env_file" "$next_env"
mv -- "$next_env" "$env_file"

if ! docker compose pull "$service" ||
   ! docker compose up -d --no-deps "$service" ||
   ! wait_until_healthy; then
  rollback
  exit 1
fi

mapfile -t old_backups < <(find "$deploy_root" -maxdepth 1 -type f -name '.env.backup.*' -printf '%T@ %p\n' | sort -rn | tail -n +6 | cut -d' ' -f2-)
if ((${#old_backups[@]})); then
  rm -f -- "${old_backups[@]}"
fi

echo "$service deployed successfully: $image"
