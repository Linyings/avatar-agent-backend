#!/usr/bin/env python3
"""Hold the API's PostgreSQL admission lock across a backend container update.

Inspect parent workflow records, not child processes: waiting on a remote model
is still active work. Existing active tasks block deployment even if their lease
has expired. Never use this wrapper to restart PostgreSQL itself (it owns the lock).
Do not wait for API health under this lock: startup recovery needs the same lock.
Release after container replacement, then perform a --no-recreate readiness check.
"""
import contextlib
import json
from pathlib import Path
import queue
import subprocess
import sys
import threading


LOCK_SQL = r"""
SET lock_timeout = '10s';
SELECT pg_advisory_lock(744834291);
SELECT to_regclass('public.workflow_step_executions') IS NOT NULL AS has_tasks \gset
\if :has_tasks
SELECT 'GUARD:' || COALESCE(json_agg(json_build_object(
  'run_id', workflow_run_id, 'stage', result->>'stage', 'status', status))::text, '[]')
FROM workflow_step_executions
WHERE step_id IN ('avatar.task', 'avatar.workflow.v2')
  AND (status IN ('running', 'stopping') OR result->>'status' IN ('running', 'stopping'));
\else
SELECT 'GUARD:[]';
\endif
"""


class DeploymentBlocked(RuntimeError):
    pass


@contextlib.contextmanager
def database_lock(root):
    # stdin stays open: the session lock is retained until the guarded command
    # finishes. Admission transactions arriving meanwhile cannot start paid work.
    process = subprocess.Popen(
        ["docker", "compose", "exec", "-T", "postgres", "psql", "-XqAt",
         "-v", "ON_ERROR_STOP=1", "-U", "avatar", "-d", "avatar"],
        cwd=root, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL, text=True, bufsize=1,
    )
    results = queue.Queue()

    def read_ready():
        for line in process.stdout:
            if line.startswith("GUARD:"):
                results.put(line.removeprefix("GUARD:"))
                return
        results.put(None)

    reader = threading.Thread(target=read_ready, daemon=True)
    reader.start()
    try:
        process.stdin.write(LOCK_SQL + "\n")
        process.stdin.flush()
        try:
            result = results.get(timeout=20)
        except queue.Empty as exc:
            raise DeploymentBlocked("Cannot obtain task admission lock; no services were restarted.") from exc
        if result is None or process.poll() is not None:
            raise DeploymentBlocked("Cannot verify active tasks in PostgreSQL; no services were restarted.")
        yield json.loads(result)
        if process.poll() is not None:
            raise DeploymentBlocked("Database lock connection was lost during deployment; verify services before proceeding.")
    finally:
        try:
            process.stdin.close()
        except BrokenPipeError:
            pass
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.terminate()
            process.wait(timeout=10)
        reader.join(timeout=1)
        process.stdout.close()


def guarded_run(root, command):
    if "--wait" in command or any(arg.startswith("--wait=") for arg in command):
        raise DeploymentBlocked("Wait for API health after releasing the admission lock; use --no-recreate for the readiness check.")
    with database_lock(root) as active:
        if active:
            details = "; ".join(f"{t['run_id']} stage={t.get('stage')} status={t['status']}" for t in active)
            raise DeploymentBlocked("Active workflow blocks backend deployment: " + details + ". Wait for completion or stop it explicitly; no restart performed.")
        print("No active workflow; admission lock held until service update completes.", flush=True)
        return subprocess.run(command, cwd=root).returncode


if __name__ == "__main__":
    if len(sys.argv) < 4 or sys.argv[2] != "--":
        sys.exit("Usage: deployment-guard.py DEPLOY_ROOT -- COMMAND [ARGS...]")
    try:
        sys.exit(guarded_run(Path(sys.argv[1]), sys.argv[3:]))
    except (RuntimeError, OSError, ValueError) as exc:
        sys.exit(str(exc))
