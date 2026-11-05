"""Regression checks for independent repository deployment (no Docker needed)."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent


class RepositoryDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="avatar-split-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "backend checkout"
        scripts = self.source / "deploy/wsl2"
        scripts.mkdir(parents=True)
        shutil.copy2(SCRIPTS / "deploy.sh", scripts / "deploy.sh")
        (self.source / "backend").mkdir()
        (self.source / "backend/go.mod").write_text("module example.test/avatar\n")
        self.target = self.root / "deployment"

    def deploy(self, service, frontend=None):
        args = ["bash", str(self.source / "deploy/wsl2/deploy.sh"), service]
        if frontend is not None:
            args.append(str(frontend))
        env = {**os.environ, "AICAT_DEPLOY_ROOT": str(self.target)}
        env.pop("AICAT_FRONTEND_SOURCE", None)
        return subprocess.run(args, env=env, capture_output=True, text=True)

    def test_unknown_service_does_not_touch_target(self):
        result = self.deploy("invalid")
        self.assertEqual(result.returncode, 2)
        self.assertIn("Usage:", result.stderr)
        self.assertFalse(self.target.exists())

    def test_missing_frontend_fails_before_partial_sync(self):
        result = self.deploy("all", self.root / "missing frontend")
        self.assertEqual(result.returncode, 2)
        self.assertIn("Missing frontend repository", result.stderr)
        self.assertFalse(self.target.exists())

    def test_backend_only_does_not_require_frontend_checkout(self):
        result = self.deploy("backend", self.root / "missing frontend")
        self.assertEqual(result.returncode, 2)
        self.assertIn("require an existing deployment", result.stderr)
        self.assertNotIn("Missing frontend", result.stderr)
        self.assertFalse(self.target.exists())

    def test_revisions_track_independent_commits_and_preserve_other_component(self):
        frontend = self.source / "frontend"
        frontend.mkdir()
        (self.source / ".gitignore").write_text("/frontend/\n")
        (frontend / "package.json").write_text('{"private":true}\n')
        for repo in (self.source, frontend):
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
            subprocess.run(["git", "-C", str(repo), "-c", "user.name=Split Test",
                            "-c", "user.email=split-test@localhost", "commit", "-qm", "snapshot"], check=True)
        self.target.mkdir()

        def record(service):
            subprocess.run([sys.executable, str(SCRIPTS / "record-revisions.py"),
                            str(self.target), service, str(self.source), str(frontend)],
                           check=True, capture_output=True)
            return json.loads((self.target / "DEPLOYED_REVISIONS.json").read_text())

        before = record("all")
        self.assertFalse(before["backend"]["dirty"])
        self.assertFalse(before["frontend"]["dirty"])
        self.assertNotEqual(before["backend"]["commit"], before["frontend"]["commit"])
        (frontend / "package.json").write_text('{"private":true,"version":"2.0.0"}\n')
        after = record("frontend")
        self.assertEqual(before["backend"], after["backend"])
        self.assertTrue(after["frontend"]["dirty"])
        self.assertEqual(before["frontend"]["commit"], after["frontend"]["commit"])


if __name__ == "__main__":
    unittest.main()
