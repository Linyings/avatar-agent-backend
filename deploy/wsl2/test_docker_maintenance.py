import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('docker_maintenance', Path(__file__).with_name('docker-maintenance.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class DockerPolicyTests(unittest.TestCase):
    def test_protects_running_runtime_rollback_and_unrelated_images(self):
        base = {'Id': 'image', 'Config': {'Labels': {'com.aicat.project': 'avatar-agent'}}}
        for tag in ['aicat-avatar-api:latest', 'aicat-avatar-api:previous', 'aicat-avatar-runtime-cache:local', 'postgres:17-alpine', 'another-project:old']:
            self.assertFalse(m.removable_image({**base, 'RepoTags': [tag]}, set()))
        self.assertFalse(m.removable_image({**base, 'RepoTags': ['aicat-avatar-api:old']}, {'image'}))
        self.assertTrue(m.removable_image({**base, 'RepoTags': ['aicat-avatar-api:old']}, set()))
        self.assertTrue(m.removable_image({**base, 'RepoTags': []}, set()))
        self.assertFalse(m.removable_image({'Id': 'unknown', 'Config': {}, 'RepoTags': []}, set()))

if __name__ == '__main__': unittest.main()
