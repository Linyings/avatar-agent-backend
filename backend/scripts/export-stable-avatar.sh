#!/bin/sh
set -eu
out=/mnt/c/Users/PC/AppData/Local/Temp/opencode/frames
mkdir -p "$out"
p=/app/data/avatar-mvp/quality-previews/stable-79d65810-f9eb-410e-89d2-f05f268c8c9e
docker exec aicat-avatar-api-1 ffmpeg -v error -y -ss 15 -i "$p/output.mp4" -frames:v 1 -vf scale=540:-1 "$p/frame.jpg"
docker cp "aicat-avatar-api-1:$p/output.mp4" "$out/stable-avatar.mp4"
docker cp "aicat-avatar-api-1:$p/frame.jpg" "$out/stable-avatar.jpg"
docker cp "aicat-avatar-api-1:$p/render-quality.json" "$out/stable-avatar-quality.json"
