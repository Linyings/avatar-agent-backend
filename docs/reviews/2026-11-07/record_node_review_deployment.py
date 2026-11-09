"""Read-only deployment evidence; never read service credentials."""
import hashlib
import json
from datetime import datetime
from pathlib import Path
import subprocess
from zoneinfo import ZoneInfo

root = Path(__file__).resolve().parents[3]
deployed = Path("/opt/aicat/avatar-mvp/src/backend")
files = ["avatar_workflow.go", "avatar_workflow_execute.go", "avatar_workflow_revision.go", "avatar_workflow_catalog.go", "agent_packages.go", "agent_package_edits.go"]
hashes = []
for name in files:
    relative = Path("internal/server") / name
    source = hashlib.sha256((root / "backend" / relative).read_bytes()).hexdigest()
    live = hashlib.sha256((deployed / relative).read_bytes()).hexdigest()
    hashes.append({"file": str(relative), "source": source, "deployed": live, "match": source == live})
containers = []
for name in ["aicat-avatar-api-1", "aicat-avatar-web-1", "aicat-avatar-postgres-1"]:
    line = subprocess.check_output(["docker", "inspect", "--format", "{{.State.Status}}|{{.State.Health.Status}}|{{.State.StartedAt}}|{{.Image}}", name], text=True).strip()
    status, health, started_at, image = line.split("|")
    containers.append({"name": name, "status": status, "health": health, "started_at": started_at, "image": image})
ip = subprocess.check_output(["hostname", "-I"], text=True).split()[0]
report = {"time": datetime.now(ZoneInfo("Asia/Shanghai")).isoformat(), "ip_url": f"http://{ip}:8080/", "localhost_url": "http://localhost:8080/", "source_hashes": hashes, "containers": containers}
(Path(__file__).parent / "node-adversarial-deployment.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
print(json.dumps(report, ensure_ascii=False, indent=2))
assert all(item["match"] for item in hashes), "deployment sources differ"
assert all(item["status"] == "running" and item["health"] == "healthy" for item in containers), "service health check failed"
