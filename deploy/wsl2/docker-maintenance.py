#!/usr/bin/env python3
"""Remove unused application images; retain current, rollback, and dependency base."""
import argparse
import json
from pathlib import Path
import re
import subprocess

APP_REPOS = {'aicat-avatar-api', 'aicat-avatar-web'}
PROTECTED = {repo+':'+tag for repo in APP_REPOS for tag in ['latest', 'previous']}


def removable_image(image, used):
    if image['Id'] in used:
        return False
    tags = image.get('RepoTags') or []
    if any(tag in PROTECTED or tag.split(':')[0] not in APP_REPOS for tag in tags):
        return False
    return bool(tags) or (image.get('Config', {}).get('Labels') or {}).get('com.aicat.project') == 'avatar-agent'


def output(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()


def maintain(root, apply):
    containers = output('ps', '-aq').splitlines()
    used = set()
    if containers:
        used = {c['Image'] for c in json.loads(output('inspect', *containers))}
    ids = list(dict.fromkeys(output('image', 'ls', '-aq', '--no-trunc').splitlines()))
    images = json.loads(output('image', 'inspect', *ids)) if ids else []
    for image in images:
        if removable_image(image, used):
            refs = image.get('RepoTags') or [image['Id']]
            print(json.dumps({'remove_images' if apply else 'would_remove_images': refs}), flush=True)
            if apply:
                # No --force: Docker rechecks references, including new containers.
                subprocess.run(['docker', 'image', 'rm', *refs], check=True)
    # Deployment backups are paired; retain the three newest database snapshots.
    backup = root / 'backups'
    dumps = sorted(p for p in backup.glob('database-*.dump') if re.fullmatch(r'database-\d{8}T\d{6}Z\.dump', p.name))
    for dump in dumps[:-3]:
        for path in [dump, backup / dump.name.replace('database-', 'config-').replace('.dump', '.env')]:
            if path.is_symlink() or path.resolve().parent != backup.resolve():
                raise ValueError('Unsafe backup path')
            print(json.dumps({'remove_backup' if apply else 'would_remove_backup': str(path)}), flush=True)
            if apply:
                path.unlink(missing_ok=True)
    if apply:
        # Preserve recent build reuse; never prune volumes, unrelated images, or
        # manipulate containerd metadata, even when a corrupt cache entry exists.
        result = subprocess.run(['docker', 'builder', 'prune', '--all', '--force', '--filter', 'until=2h'])
        if result.returncode:
            print('WARNING: Docker build-cache cleanup failed; application remains deployed. Inspect daemon cache health.')
    else:
        print('Would prune unused Docker build cache older than 2 hours. No volume cleanup.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path('/opt/aicat/avatar-mvp'))
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    maintain(args.root, args.apply)
