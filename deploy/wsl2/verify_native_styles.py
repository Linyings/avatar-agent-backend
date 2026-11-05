"""Read-only live catalog checks; never starts a model request."""
import io
import hashlib
import json
import sys
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

base = 'http://127.0.0.1:8080'
access = {}
for line in Path('/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt').read_text().splitlines():
    if ': ' in line:
        key, value = line.split(': ', 1)
        access.setdefault(key, value)


def request(path, token=None, data=None, headers=None):
    h = dict(headers or {})
    if token:
        h['Authorization'] = 'Bearer ' + token
    if data is not None:
        h['Content-Type'] = 'application/json'
    req = urllib.request.Request(base + path, headers=h, data=json.dumps(data).encode() if data is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=60) as res:
            return res.status, dict(res.headers), res.read()
    except urllib.error.HTTPError as err:
        return err.code, dict(err.headers), err.read()


status, _, raw = request('/api/v1/auth/login', data={'email': access['Email'], 'password': access['Password']})
assert status == 200, status
login = json.loads(raw)
token = login.get('data', login)['tokens']['access_token']
status, _, raw = request('/api/v1/avatar-mvp/catalog', token)
assert status == 200, status
catalog = json.loads(raw)
styles = catalog.get('data', catalog)['styles']
native = [s for s in styles if not s['legacy']]
assert len(native) == 15 and all(s['published'] for s in native)
assert all(s['preview_kind']=='upstream-original' for s in native)
results = []
for style in native:
    sid = style['id']
    root = '/api/v1/avatar-mvp/catalog/' + sid
    row = {'id': sid, 'published': True}
    hashes={}
    for kind in ['preview', 'reference', 'prompt', 'source']:
        status, _, _ = request(root + '/' + kind)
        assert status == 401, (sid, kind, status)
        status, headers, body = request(root + '/' + kind, token)
        assert status == 200, (sid, kind, status)
        if kind in ['preview', 'reference']:
            hashes[kind]=hashlib.sha256(body).hexdigest()
            assert 'video/mp4' in headers.get('Content-Type', '') and b'ftyp' in body[:64]
            status, _, chunk = request(root + '/' + kind, token, headers={'Range': 'bytes=0-63'})
            assert status == 206 and len(chunk) == 64
        elif kind == 'prompt':
            assert len(body.decode('utf-8')) > 100
        else:
            z = zipfile.ZipFile(io.BytesIO(body))
            files = z.namelist()
            for name in ['mg-native.mjs', 'mg-shell.js', 'package-lock.json', 'render-example.mjs', 'mg-upstream/LICENSE', 'templates/' + sid + '/SOURCE.json', 'templates/' + sid + '/original-prompt.md', 'mg-upstream/' + style['entry']]:
                assert name in files, (sid, name)
            assert not any(Path(n).name in ['.env', 'LOCAL_ACCESS.txt'] or '..' in Path(n).parts for n in files)
            row['package_files'] = len(files)
        row[kind] = {'status': 'passed', 'bytes': len(body), 'anonymous': 401}
    assert hashes['preview']==hashes['reference'], (sid,'preview must be upstream original')
    status, _, body=request(root+'/poster',token)
    assert status==200 and body[:2]==b'\xff\xd8'
    row['preview_matches_original']=True
    row['production_mode']=style['production_mode']
    results.append(row)
report = {'model_calls': 0, 'native_styles': results, 'legacy_count': len(styles) - len(native)}
out = Path(sys.argv[1]); out.parent.mkdir(parents=True, exist_ok=True)
out.write_text(json.dumps(report, ensure_ascii=False, indent=2))
print(json.dumps(report, ensure_ascii=False))
