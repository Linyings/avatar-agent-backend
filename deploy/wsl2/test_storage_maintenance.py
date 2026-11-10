import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('maintenance', Path(__file__).with_name('storage-maintenance.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class StorageMaintenanceTests(unittest.TestCase):
    def test_orphans_only_and_dry_run(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            live = '11111111-1111-4111-8111-111111111111'
            dead = '22222222-2222-4222-8222-222222222222'
            for base in ['workflow-v2/projects', 'workflow-v2/runs', '']:
                for key in [live, dead, 'unknown-data']:
                    path = root / base / key
                    path.mkdir(parents=True)
                    (path / 'data').write_bytes(b'123')
            candidates = m.orphan_paths(root, {live}, {live})
            self.assertEqual(len(candidates), 3)
            self.assertTrue(all(p.name == dead for p in candidates))
            m.remove_paths(root, candidates, apply=False)
            self.assertTrue(all(p.exists() for p in candidates))
            m.remove_paths(root, candidates, apply=True)
            self.assertFalse(any(p.exists() for p in candidates))
            self.assertTrue((root / 'workflow-v2/projects' / live / 'data').exists())

    def test_symlinks_cannot_escape(self):
        with tempfile.TemporaryDirectory() as folder, tempfile.TemporaryDirectory() as outside:
            root = Path(folder)
            external = Path(outside)
            (external / 'keep').write_text('keep')
            (root / 'link').symlink_to(external, target_is_directory=True)
            with self.assertRaises(ValueError):
                m.remove_paths(root, [root / 'link' / 'keep'], apply=True)
            self.assertTrue((external / 'keep').exists())
            with self.assertRaises(ValueError):
                m.remove_paths(root, [root], apply=True)

if __name__ == '__main__':
    unittest.main()
