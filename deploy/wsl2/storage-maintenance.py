#!/usr/bin/env python3
"""Audited cleanup of orphan project storage. Dry run unless --apply is supplied."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import uuid


def valid_id(name):
    try:
        return str(uuid.UUID(name)) == name
    except ValueError:
        return False


def orphan_paths(root, projects, runs):
    result = []
    for parent, keep in [(root / 'workflow-v2/projects', projects), (root / 'workflow-v2/runs', runs), (root, runs)]:
        if parent.is_symlink():
            raise ValueError('Linked storage parent: ' + str(parent))
        if parent.exists():
            result.extend(p for p in parent.iterdir() if valid_id(p.name) and p.name not in keep)
    return result


def checked_path(root, path):
    root = root.absolute()
    path = path.absolute()
    if path == root or not path.is_relative_to(root) or not path.resolve().is_relative_to(root.resolve()):
        raise ValueError('Unsafe cleanup target: ' + str(path))
    for parent in [path, *path.parents]:
        if parent.is_symlink():
            raise ValueError('Linked cleanup target: ' + str(path))
        if parent == root:
            break
    return path


def disk_bytes(path):
    if path.is_file():
        return path.stat().st_blocks * 512
    total = 0
    for base, _, files in os.walk(path, followlinks=False):
        for name in files:
            p = Path(base) / name
            if not p.is_symlink():
                total += p.stat().st_blocks * 512
    return total


def remove_paths(root, paths, apply=False):
    checked = [checked_path(root, path) for path in paths]
    total = 0
    for path in checked:
        size = disk_bytes(path)
        print(json.dumps({'action': 'remove' if apply else 'would_remove', 'path': str(path), 'bytes': size}), flush=True)
        if apply:
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink(missing_ok=True)
        total += size
    return total


def query(root, sql):
    raw = subprocess.check_output(['docker', 'compose', 'exec', '-T', 'postgres', 'psql', '-XqAt', '-v', 'ON_ERROR_STOP=1', '-U', 'avatar', '-d', 'avatar', '-c', sql], cwd=root, text=True)
    return set(raw.splitlines())


def maintain(root, apply, test_files=False):
    spec = importlib.util.spec_from_file_location('guard', Path(__file__).with_name('deployment-guard.py'))
    guard = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(guard)
    with guard.database_lock(root) as active:
        if active:
            print('Storage cleanup skipped: active production tasks.')
            return 0
        # Resolve the actual named volume through the existing container.
        container = subprocess.check_output(['docker', 'compose', 'ps', '-q', 'api'], cwd=root, text=True).strip()
        info = json.loads(subprocess.check_output(['docker', 'inspect', container], text=True))[0]
        mount = next(m for m in info['Mounts'] if m['Destination'] == '/app/data' and m['Type'] == 'volume')
        data = Path(mount['Source'])
        if not data.is_absolute() or not data.is_dir():
            raise ValueError('Task volume unavailable')
        storage = data / 'avatar-mvp'
        projects = query(root, 'SELECT id FROM projects WHERE deleted_at IS NULL')
        runs = query(root, 'SELECT r.id FROM workflow_runs r LEFT JOIN projects p ON p.id=r.project_id WHERE r.project_id IS NULL OR p.deleted_at IS NULL AND p.id IS NOT NULL')
        targets = orphan_paths(storage, projects, runs)
        # Scratch directories never hold reusable materials. Age protects a probe
        # upload that has not yet committed while this admission lock is held.
        scratch = storage / '.scratch'
        if scratch.exists() and not scratch.is_symlink():
            targets += [p for p in scratch.iterdir() if p.name.startswith('media-') and p.stat().st_mtime < time.time()-86400]
        total = remove_paths(storage, targets, apply)
        if test_files:
            # Explicit, reviewed test output names; never guess from project age.
            relative = ['render-regression-v22', 'render-regression-v22-latest', 'export-check-v22-a', 'export-check-v22-a2',
                        'avatar-mvp/diagnostics', 'avatar-mvp/reviews', 'avatar-mvp/design-reviews', 'avatar-mvp/quality-previews']
            total += remove_paths(data, [data/p for p in relative if (data/p).exists()], apply)
            acceptance = root / 'acceptance-v22'
            if acceptance.exists():
                total += remove_paths(root, [acceptance], apply)
        print(json.dumps({'applied': apply, 'reclaimed_bytes' if apply else 'reclaimable_bytes': total, 'live_projects': len(projects), 'retained_runs': len(runs)}))
        return total


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/opt/aicat/avatar-mvp'))
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--test-files', action='store_true', help='Also remove the reviewed legacy test outputs')
    args = parser.parse_args()
    maintain(args.root, args.apply, args.test_files)
