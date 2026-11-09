"""Isolated node-operation regressions. Never load deployment credentials."""
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[3]
race = "--race" in sys.argv
pattern = "TestPackage.*(Node|Plan|Rerun|Completed)|TestPackageInvalidPlan" if "--focused" in sys.argv else "TestAdversarial|TestAgent|TestPackage|TestAvatarWorkflow|TestTool"
args = ["go", "test", "./internal/agent", "./internal/agentpackages", "./internal/server", "-count=1", "-timeout=120s", "-json", "-run", pattern]
if race:
    args = ["go", "test", "./internal/agent", "-race", "-count=1", "-timeout=120s", "-json"]
r = subprocess.run(["docker", "run", "--rm", "-e", "GOMAXPROCS=2", "-e", "GIN_MODE=release", "-v", f"{root / 'backend'}:/src", "-v", "aicat-go-build-cache:/root/.cache/go-build", "-w", "/src", "aicat-avatar-go-check:latest", *args], capture_output=True, text=True)
events = []
for line in r.stdout.splitlines():
    try: events.append(json.loads(line))
    except ValueError: pass
summary = {"exit_code": r.returncode, "passed": sum(e.get("Action") == "pass" and bool(e.get("Test")) for e in events), "failures": [e for e in events if e.get("Action") == "fail"], "diagnostics": [e.get("Output", "").strip() for e in events if "_test.go:" in e.get("Output", "")], "stderr": r.stderr}
name = "node-race" if race else "node-focused" if "--focused" in sys.argv else "node-regressions"
(Path(__file__).parent / f"{name}.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
print(json.dumps(summary, ensure_ascii=False, indent=2))
raise SystemExit(r.returncode)
