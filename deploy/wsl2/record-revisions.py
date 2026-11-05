"""Record independently deployed source revisions without reading credentials."""
import json
from datetime import datetime, timezone
from pathlib import Path
import subprocess
import sys


def revision(source):
    def git(*args):
        return subprocess.check_output(
            ["git", "-C", str(source), *args], text=True, stderr=subprocess.DEVNULL
        ).strip()

    result = {"source": str(Path(source).resolve())}
    try:
        result.update(commit=git("rev-parse", "HEAD"),
                      dirty=bool(git("status", "--porcelain")))
    except (subprocess.CalledProcessError, FileNotFoundError):
        result.update(commit=None, dirty=None)
    result["deployed_at"] = datetime.now(timezone.utc).isoformat()
    return result


target, service, backend_source, frontend_source = sys.argv[1:]
manifest = Path(target) / "DEPLOYED_REVISIONS.json"
data = json.loads(manifest.read_text()) if manifest.exists() else {}
for component, source in (("backend", backend_source), ("frontend", frontend_source)):
    if service in ("all", component):
        data[component] = revision(source)
temporary = manifest.with_suffix(".json.tmp")
temporary.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n")
temporary.replace(manifest)
print(json.dumps({"deployed_revisions": data}, ensure_ascii=False))
