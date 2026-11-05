"""Run one real MVP baseline against WSL; never print credentials or signed URLs.

Run inside Ubuntu-24.04. Submission is explicit and uses a persisted request UUID.
Subsequent invocations only read status; they never retry paid jobs automatically.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import urllib.error
import urllib.request
import uuid

ROOT = Path('/opt/aicat/avatar-mvp')
OUTPUT = Path('/mnt/d/pro/ai_cat/avatar-agent/backend/data/real-test-20261104')
PHOTO = Path('/mnt/c/Users/PC/Desktop/自然光下的静谧侧颜肖像.png')
SCRIPT = '你好，欢迎来到数字人口播体验。只需一张照片和一段文字，就能让创意变成生动的视频。接下来，一起看看不同镜头下的自然表达。'
BASE = 'http://127.0.0.1:8080'


def request(path, data=None, token=None, content_type='application/json'):
    headers = {'Content-Type': content_type}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if isinstance(data, dict):
        data = json.dumps(data).encode()
    req = urllib.request.Request(BASE + path, data=data, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=90) as res:
            return res.status, json.load(res)
    except urllib.error.HTTPError as err:
        return err.code, json.load(err)


def login():
    access = {}
    for line in (ROOT / 'LOCAL_ACCESS.txt').read_text().splitlines():
        if ': ' in line:
            key, value = line.split(': ', 1)
            access.setdefault(key, value)
    status, result = request('/api/v1/auth/login', {'email': access['Email'], 'password': access['Password']})
    if status != 200:
        raise RuntimeError('Local test account login failed: HTTP ' + str(status))
    return result.get('data', result)['tokens']['access_token']


def safe_task(task):
    fields = ('id', 'status', 'stage', 'title', 'script', 'baseline', 'baseline_request_id', 'review_required',
              'provider', 'model', 'voice', 'duration', 'error', 'created_at', 'updated_at',
              'completed_at', 'stage_seconds', 'tts_characters', 'operations')
    result = {k: task[k] for k in fields if k in task}
    result['shots'] = [{k: shot[k] for k in ('id', 'angle', 'status', 'submission',
                       'provider_task_id', 'error', 'started_at', 'completed_at') if k in shot}
                       for shot in task.get('shots', [])]
    return result


def main():
    global OUTPUT
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['init', 'inspect', 'submit', 'status'])
    parser.add_argument('--run', default='')
    args = parser.parse_args()
    if args.run:
        if not all(c.isalnum() or c in '-_' for c in args.run):
            raise ValueError('run name must be a simple directory name')
        OUTPUT = OUTPUT / args.run
    OUTPUT.mkdir(parents=True, exist_ok=True)
    token = login()
    if args.action == 'inspect':
        status, config = request('/api/v1/avatar-mvp/config', token=token)
        print(json.dumps({'config_http': status, 'configuration': config}, ensure_ascii=False))
        query = "SELECT json_build_object('id', workflow_run_id, 'status', result->>'status', 'stage', result->>'stage', 'title', result->>'title', 'provider', result->>'provider', 'model', result->>'model') FROM workflow_step_executions WHERE step_id = 'avatar.task';"
        result = subprocess.run(['docker', 'compose', 'exec', '-T', 'postgres', 'psql', '-U', 'avatar', '-d', 'avatar', '-At', '-c', query], cwd=ROOT, capture_output=True, text=True, check=True)
        print('existing_tasks=' + result.stdout.strip())
        for line in result.stdout.splitlines():
            prior = json.loads(line)
            code, task = request('/api/v1/avatar-mvp/tasks/' + prior['id'], token=token)
            if code == 200:
                print('prior_task=' + json.dumps(safe_task(task.get('data', task)), ensure_ascii=False))
            else:
                print(json.dumps({'prior_task_id': prior['id'], 'read_http': code}))
        photo = PHOTO.read_bytes()
        print(json.dumps({'photo_bytes': len(photo), 'dimensions': struct.unpack('>II', photo[16:24]), 'sha256': hashlib.sha256(photo).hexdigest()}))
        return
    record = OUTPUT / 'request.json'
    if args.action == 'init':
        if not record.exists():
            entry = {'client_request_id': str(uuid.uuid4()), 'title': '自然表达 · 完整成片实测',
                     'script': SCRIPT, 'photo_sha256': hashlib.sha256(PHOTO.read_bytes()).hexdigest()}
            record.write_text(json.dumps(entry, ensure_ascii=False, indent=2))
        print(record.read_text())
        return
    if args.action == 'submit':
        if record.exists():
            entry = json.loads(record.read_text())
            if entry.get('task_id'):
                raise RuntimeError('Task already submitted; use status. No paid resubmission performed.')
        else:
            entry = {'client_request_id': str(uuid.uuid4()), 'title': '自然表达 · 三镜口播实测',
                     'script': SCRIPT, 'photo_sha256': hashlib.sha256(PHOTO.read_bytes()).hexdigest()}
            record.write_text(json.dumps(entry, ensure_ascii=False, indent=2))
        boundary = 'AvatarMVPTest' + uuid.uuid4().hex
        chunks = []
        for key in ('client_request_id', 'title', 'script'):
            chunks.append(('--' + boundary + '\r\nContent-Disposition: form-data; name="' + key + '"\r\n\r\n' + entry[key] + '\r\n').encode())
        chunks += [('--' + boundary + '\r\nContent-Disposition: form-data; name="photo"; filename="portrait.png"\r\nContent-Type: image/png\r\n\r\n').encode(), PHOTO.read_bytes(), ('\r\n--' + boundary + '--\r\n').encode()]
        status, task = request('/api/v1/avatar-mvp/tasks', b''.join(chunks), token, 'multipart/form-data; boundary=' + boundary)
        if status != 202:
            (OUTPUT / 'submission-result.json').write_text(json.dumps({'http': status,
                'message': task.get('message', 'Submission rejected')}, ensure_ascii=False, indent=2))
            print(json.dumps({'http': status, 'error': task}, ensure_ascii=False))
            raise SystemExit(1)
        entry['task_id'] = task['id']
        record.write_text(json.dumps(entry, ensure_ascii=False, indent=2))
    else:
        entry = json.loads(record.read_text())
        status, task = request('/api/v1/avatar-mvp/tasks/' + entry['task_id'], token=token)
        if status != 200:
            raise RuntimeError('Task query HTTP ' + str(status))
        task = task.get('data', task)
    safe = safe_task(task)
    (OUTPUT / 'status.json').write_text(json.dumps(safe, ensure_ascii=False, indent=2))
    print(json.dumps(safe, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
