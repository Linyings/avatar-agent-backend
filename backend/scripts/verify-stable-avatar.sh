#!/bin/sh
set -eu
name=avatar-quality-local
docker run -d --name "$name" --network none --cpus 2 --memory 3g --shm-size 256m -v aicat-avatar_task_data:/app/data --entrypoint sleep aicat-avatar-api infinity
trap 'docker rm -f "$name" >/dev/null' EXIT
AVATAR_RENDER_CONTAINER="$name" sh "$(dirname "$0")/rerender-stable-avatar.sh"
