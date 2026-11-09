"""Isolated adversarial node tests; no production credentials or state."""
import argparse
import json
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--label", default="after")
parser.add_argument("--full", action="store_true")
parser.add_argument("--race", action="store_true")
options = parser.parse_args()
root = Path(__file__).resolve().parents[3]
pattern = "TestAdversarialNode" if not options.full else "TestAdversarial|TestAgent|TestPackage|TestAvatarWorkflow|TestTool|TestWorkflowPlan|TestWorkflowAnimation"
args = ["go", "test", "./internal/agent", "./internal/agentpackages", "./internal/avatar", "./internal/server", "-count=1", "-timeout=180s", "-json", "-run", pattern]
if options.race:
    args.append("-race")
result = subprocess.run(["docker", "run", "--rm", "-e", "GOMAXPROCS=2", "-e", "GIN_MODE=release", "-v", f"{root / 'backend'}:/src", "-v", "aicat-go-build-cache:/root/.cache/go-build", "-w", "/src", "aicat-avatar-go-check:latest", *args], capture_output=True, text=True)
events = []
for line in result.stdout.splitlines():
    try: events.append(json.loads(line))
    except ValueError: pass
summary = {"exit_code": result.returncode, "passed": sum(e.get("Action") == "pass" and bool(e.get("Test")) for e in events), "failures": [e for e in events if e.get("Action") == "fail"], "diagnostics": [e.get("Output", "").strip() for e in events if "_test.go:" in e.get("Output", "") or "WARNING: DATA RACE" in e.get("Output", "")], "stderr": result.stderr}
(Path(__file__).parent / f"node-adversarial-{options.label}.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
print(json.dumps(summary, ensure_ascii=False, indent=2))
raise SystemExit(result.returncode)
