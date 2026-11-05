"""Fetch the official CPU speaker verifier in resumable chunks; no model API calls."""
from concurrent.futures import ThreadPoolExecutor, as_completed
import hashlib
import json
from pathlib import Path
import time
import subprocess
from urllib.request import Request, urlopen

root = Path(__file__).resolve().parents[2] / 'backend/data/real-test-20261104/voice-model'
root.mkdir(parents=True, exist_ok=True)
release = json.load(urlopen('https://api.github.com/repos/k2-fsa/sherpa-onnx/releases/tags/speaker-recongition-models', timeout=20))
asset = next(a for a in release['assets'] if a['name'] == '3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx')
url, size = asset['browser_download_url'], asset['size']
chunk_size = 1024 * 1024


def fetch(index):
    start, end = index * chunk_size, min(size, (index + 1) * chunk_size) - 1
    part = root / f'chunk-{index:03d}'
    marker = part.with_suffix('.validated')
    if marker.exists() and part.exists() and part.stat().st_size == end - start + 1:
        return index
    for attempt in range(4):
        try:
            headers = part.with_suffix('.headers')
            subprocess.run(['curl.exe', '-sS', '-L', '--range', f'{start}-{end}', '--max-time', '45',
                            '-D', str(headers), '-o', str(part), url + f'?part={index}&attempt={attempt}'], check=True)
            if f'content-range: bytes {start}-{end}/{size}' not in headers.read_text().lower():
                raise RuntimeError('Official asset returned an incorrect byte range')
            data = part.read_bytes()
            if len(data) != end - start + 1:
                raise RuntimeError('Incomplete model chunk')
            part.write_bytes(data)
            marker.write_text('content-range verified')
            return index
        except Exception:
            if attempt == 3:
                raise RuntimeError(f'Model chunk {index} could not be downloaded') from None
            time.sleep(1)


count = (size + chunk_size - 1) // chunk_size
with ThreadPoolExecutor(max_workers=8) as pool:
    futures = [pool.submit(fetch, i) for i in range(count)]
    for done, future in enumerate(as_completed(futures), 1):
        future.result()
        print(f'speaker model chunks: {done}/{count}', flush=True)
target = root / asset['name']
with target.open('wb') as out:
    for i in range(count):
        out.write((root / f'chunk-{i:03d}').read_bytes())
assert target.stat().st_size == size
print(json.dumps({'model': asset['name'], 'bytes': size, 'sha256': hashlib.sha256(target.read_bytes()).hexdigest()}))
