#!/usr/bin/env bash
set -euo pipefail
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
runtime="$project_root/backend/avatar-runtime"
docker exec -u root aicat-avatar-api-1 mkdir -p /tmp/film-v3-runtime
tar -C "$runtime" -cf - src workflow.py workflow-render.mjs film-review.mjs mg-native.mjs mg-shell.js native-styles.json package.json package-lock.json tsconfig.json UPSTREAM.json TALKCRAFT-LICENSE |
  docker exec -u root -i aicat-avatar-api-1 tar -xf - -C /tmp/film-v3-runtime
docker exec -u root aicat-avatar-api-1 sh -c 'for item in node_modules mg-upstream templates talkcraft; do ln -sfn "/app/avatar-runtime/$item" "/tmp/film-v3-runtime/$item"; done; chown -R node:node /tmp/film-v3-runtime'
docker cp "$project_root/backend/scripts/review_film_design.py" aicat-avatar-api-1:/tmp/review_film_design.py
docker exec -w /tmp/film-v3-runtime aicat-avatar-api-1 /opt/avatar-venv/bin/python /tmp/review_film_design.py \
  --runtime /tmp/film-v3-runtime \
  --source /app/data/avatar-mvp/workflow-v2/runs/56f29cd8-f58d-4dff-982c-efbe566d5306 \
  --output /app/data/avatar-mvp/design-reviews/film-design-v1
