"""Deployment must check durable tasks, including provider-only work, under admission lock."""
import contextlib
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("deployment_guard", Path(__file__).with_name("deployment-guard.py"))
guard = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guard)


class DeploymentGuardTests(unittest.TestCase):
    def test_health_wait_under_admission_lock_is_rejected(self):
        with patch.object(guard, "database_lock") as lock, patch.object(guard.subprocess, "run") as run:
            with self.assertRaisesRegex(guard.DeploymentBlocked, "health after releasing"):
                guard.guarded_run(Path("/deployment"), ["docker", "compose", "up", "--wait", "api"])
            lock.assert_not_called()
            run.assert_not_called()

    def test_remote_only_running_task_blocks_restart(self):
        @contextlib.contextmanager
        def locked(_):
            yield [{"run_id": "run-1", "stage": "F10", "status": "running"}]
        with patch.object(guard, "database_lock", locked), patch.object(guard.subprocess, "run") as run:
            with self.assertRaisesRegex(guard.DeploymentBlocked, "run-1.*F10"):
                guard.guarded_run(Path("/deployment"), ["docker", "compose", "up"])
            run.assert_not_called()

    def test_lock_held_until_restart_finishes(self):
        held = []
        @contextlib.contextmanager
        def locked(_):
            held.append(True)
            try:
                yield []
            finally:
                held.pop()
        def restart(*args, **kwargs):
            self.assertEqual(held, [True])
            return type("Result", (), {"returncode": 7})()
        with patch.object(guard, "database_lock", locked), patch.object(guard.subprocess, "run", restart):
            self.assertEqual(guard.guarded_run(Path("/deployment"), ["docker", "compose", "up"]), 7)
        self.assertEqual(held, [])

    def test_database_error_does_not_restart(self):
        @contextlib.contextmanager
        def locked(_):
            raise RuntimeError("database unavailable")
            yield
        with patch.object(guard, "database_lock", locked), patch.object(guard.subprocess, "run") as run:
            with self.assertRaises(RuntimeError):
                guard.guarded_run(Path("/deployment"), ["docker", "compose", "up"])
            run.assert_not_called()

    def test_guard_uses_same_admission_lock_and_only_parent_markers(self):
        self.assertIn("744834291", guard.LOCK_SQL)
        self.assertIn("avatar.workflow.v2", guard.LOCK_SQL)
        self.assertIn("avatar.task", guard.LOCK_SQL)
        self.assertIn("stopping", guard.LOCK_SQL)
        self.assertNotIn("lease_expires_at", guard.LOCK_SQL)


if __name__ == "__main__":
    unittest.main()
