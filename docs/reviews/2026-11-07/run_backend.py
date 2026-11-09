"""Run permanent regression tests in WSL; no deployment data or API keys."""
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[3]
here = Path(__file__).resolve().parent
race = "--race" in sys.argv
packages = ["./internal/agent"] if race else ["./internal/agent", "./internal/agentpackages", "./internal/server"]
args = ["go", "test", *packages, "-count=1", "-timeout=120s", "-json"]
if race:
    args += ["-race"]
else:
    args += ["-run", "TestAdversarial|TestAgent|TestPackage|TestAvatarWorkflow|TestTool"]
result = subprocess.run([
    "docker", "run", "--rm", "-e", "GOMAXPROCS=2", "-e", "GIN_MODE=release",
    "-v", f"{root / 'backend'}:/src",
    "-v", "aicat-go-build-cache:/root/.cache/go-build",
    "-w", "/src", "aicat-avatar-go-check:latest", *args,
], capture_output=True, text=True)
events, diagnostics = [], []
for line in result.stdout.splitlines():
    try:
        event = json.loads(line)
    except ValueError:
        continue
    if event.get("Action") in ("fail", "pass", "skip"):
        events.append(event)
    output = event.get("Output", "")
    if any(marker in output for marker in ("regression_test.go:", "--- FAIL:", "panic:", "WARNING: DATA RACE")):
        diagnostics.append(output.strip())
name = "fix-agent-race-results" if race else "fix-backend-results"
(here / f"{name}.json").write_text(json.dumps({
    "exit_code": result.returncode, "tests": events, "diagnostics": diagnostics, "stderr": result.stderr
}, ensure_ascii=False, indent=2), encoding="utf-8")
if result.returncode:
    print(result.stdout[-18000:])
print(json.dumps({"exit_code": result.returncode, "passed": sum(e.get("Action") == "pass" and bool(e.get("Test")) for e in events), "failures": [e for e in events if e.get("Action") == "fail"], "diagnostics": diagnostics}, ensure_ascii=False, indent=2))
print(result.stderr[-3000:])
raise SystemExit(result.returncode)
