#!/bin/sh
# Local-only regression render. Copies artifacts; never updates the source run.
set -eu
container=${AVATAR_RENDER_CONTAINER:-aicat-avatar-api-1}
run=79d65810-f9eb-410e-89d2-f05f268c8c9e
docker exec "$container" python3 -c "import pathlib,shutil,json; src=pathlib.Path('/app/data/avatar-mvp/workflow-v2/runs/$run'); dst=pathlib.Path('/app/data/avatar-mvp/quality-previews/stable-$run'); dst.mkdir(parents=True,exist_ok=True); [shutil.copytree(p,dst/p.name,dirs_exist_ok=True) if p.is_dir() else shutil.copy2(p,dst/p.name) for p in src.iterdir() if p.name not in ('segments','preview','render-cache','output.mp4','assembled.mp4') and p.suffix!='.zip']; (dst/'asset-paths.json').write_text('{}'); print(dst)"
docker exec "$container" sh -c "cd /app/avatar-runtime && node workflow-render.mjs render /app/data/avatar-mvp/quality-previews/stable-$run"
docker exec "$container" python3 -c "import pathlib,json; p=pathlib.Path('/app/data/avatar-mvp/quality-previews/stable-$run'); q=json.loads((p/'render-quality.json').read_text()); print(json.dumps(q,ensure_ascii=False)); print('output_bytes', (p/'output.mp4').stat().st_size)"
