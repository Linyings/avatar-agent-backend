#!/usr/bin/env python3
"""Regenerate internal/media/models.json from RunningHub's public OpenAPI docs.

Only standard model APIs are kept: workflow-style endpoints ("node##field"),
LoRA/IP templates, deprecated, 3D, editing/extension and upload utilities are
excluded. Run from the backend directory:

    python3 scripts/gen_media_models.py

Requires PyYAML. The output is reviewed and committed; the server never fetches
the docs at runtime.
"""
import collections
import concurrent.futures as cf
import json
import pathlib
import re
import sys
import time
import urllib.error
import urllib.request

import yaml

INDEX = 'https://www.runninghub.ai/runninghub-api-doc-en/llms.txt'
OUT = pathlib.Path(__file__).resolve().parent.parent / 'internal' / 'media' / 'models.json'
# Endpoints RunningHub reports as discontinued or unknown; maintained by probe_media_models.py.
UNAVAILABLE_FILE = pathlib.Path(__file__).resolve().parent / 'media_unavailable.json'
EXCLUDE = re.compile(r'(?i)deprecated|stephen chow|spicy|lora|upload-character|elements|custom-element|3d|tripo|hitem|regeneration|'
                     r'context-ir|draft-to-video|short-play|layer-decomposition|identify-face|lip-sync|motion-control|dreamactor|'
                     r'video-edit|edit-video|image-edit|video-extend|continuation|draft-enhance|/extend|transition|subtitle-erase|'
                     r'kling-elements|extend-song|vocal-clone|voice|tts|speech|separation|extract-|music-cover|minimax/music-2\.6$|files-upload')
FAMILIES = [('seedance-v1', 'seedance-v1'), ('sparkvideo', 'seedance'), ('seedance', 'seedance'), ('rhart-video-v3', 'veo'),
            ('kling-v2-ai-avatar', 'kling-avatar'), ('kling', 'kling'), ('vidu', 'vidu'), ('hailuo-h3', 'minimax-h3'), ('h3-max', 'minimax-h3'),
            ('hailuo', 'hailuo'), ('rhart-video-s', 'sora'), ('wan-', 'wan'), ('happyhorse', 'happyhorse'), ('skyreels', 'skyreels'),
            ('pixverse', 'pixverse'), ('rhart-video-g', 'grok'), ('gemini-omni', 'gemini-omni'), ('flux3', 'flux-video'),
            ('youchuan/image-to-video', 'midjourney-video'), ('youchuan', 'midjourney'), ('rhart-image-n', 'nano-banana'),
            ('rhart-image-v1', 'nano-banana'), ('rhart-image-g', 'gpt-image'), ('rhart-image-x', 'grok-image'), ('rhart-imagine', 'grok-image'),
            ('qwen-image', 'qwen-image'), ('dola-seedream', 'seedream'), ('seedream', 'seedream'), ('jimeng', 'jimeng'),
            ('topazlabs/image', 'topaz-image'), ('topazlabs/video', 'topaz-video'), ('volc-enhance', 'volc-enhance'), ('arklin', 'detailx'),
            ('rhart-video/video-', 'rh-video'), ('minimax/music', 'minimax-music'), ('music-2.5', 'minimax-music'), ('mureka', 'mureka'),
            ('suno', 'suno'), ('seed-audio', 'seed-audio')]


def kind_of(cat, path):
    p = path.lower()
    if 'ai-avatar' in p:
        return 'avatar'
    if cat.startswith('Audio'):
        if 'seed-audio' in p:
            return 'sfx'
        if any(k in p for k in ('suno', 'mureka', 'music')):
            return 'music'
        return None
    if cat.startswith('Image') or 'seedream-v5-flash' in p or 'image-upscale' in p:
        if 'text-to-image' in p or cat.startswith('Image Generation & Processing > text-to-image'):
            return 't2i'
        return 'i2i'
    if cat.startswith('Video') or 'super-resolution' in p:
        if re.search(r'upscal|enhance|fps-increaser|interpolation|denoise|starlight|astra|proteus|super-resolution', p):
            return 'enhance'
        if 'reference-to-video' in p or 'refrence-to-video' in p or 'multimodal' in p:
            return 'r2v'
        if 'text-to-video' in p or p.endswith('/pro') and 'hailuo' in p:
            return 't2v'
        if any(k in p for k in ('image-to-video', 'start-end', 'start-to-end', 'i2v', '/fast', '/standard')):
            return 'i2v'
        return None
    return None


def family_of(path):
    p = path.lower()
    for key, fam in FAMILIES:
        if key in p:
            return fam
    return 'generic'


def request_schema(spec):
    for path, ops in spec.get('paths', {}).items():
        post = ops.get('post') or {}
        body = post.get('requestBody', {}).get('content', {}).get('application/json', {}).get('schema', {})
        return path, body
    return '', {}


def field(name, s):
    out = {'name': name, 'type': s.get('type', '')}
    for key, dst in (('enum', 'enum'), ('default', 'default'), ('minimum', 'min'), ('maximum', 'max'), ('maxItems', 'max_items')):
        if key in s and s[key] not in (None, ''):
            value = s[key]
            if key == 'enum':
                value = [str(v) for v in value]
            elif key == 'default' and (isinstance(value, (list, dict)) or (isinstance(value, str) and (value.startswith('http') or len(value) > 120))):
                # Documentation examples (signed URLs, sample prompts/lyrics) are not usable defaults.
                continue
            out[dst] = value
    if s.get('type') == 'array' and isinstance(s.get('items'), dict) and 'maxItems' in s:
        out['max_items'] = s['maxItems']
    return out


def get(url):
    # The docs site rate-limits bursts; back off instead of failing the run.
    for attempt in range(6):
        try:
            req = urllib.request.Request(url, headers={'User-Agent': 'avatar-agent-registry/1.0'})
            return urllib.request.urlopen(req, timeout=60).read().decode('utf-8')
        except urllib.error.HTTPError as e:
            if e.code not in (403, 429, 502, 503) or attempt == 5:
                raise
            time.sleep(5 * (attempt + 1))


def fetch(row):
    cat, name, url, desc = row
    doc = get(url)
    block = re.search(r'```yaml\n(.*?)\n```', doc, re.S)
    spec = yaml.safe_load(block.group(1)) if block else {}
    path, schema = request_schema(spec or {})
    props = schema.get('properties') or {}
    return {'cat': cat, 'name': name, 'url': url, 'desc': desc, 'path': path,
            'fields': [field(k, v or {}) for k, v in props.items()], 'required': [str(r) for r in schema.get('required') or []]}


def main():
    raw = get(INDEX)
    rows = []
    for line in raw.splitlines():
        m = re.match(r'- (?:Standard Model API|Model API) > (.+?) \[(.+?)\]\((https://[^)]+\.md)\):?\s*(.*)', line)
        if m:
            rows.append((m.group(1).strip(), m.group(2).strip(), m.group(3), m.group(4).strip()))
    with cf.ThreadPoolExecutor(4) as ex:
        docs = list(ex.map(fetch, rows))
    unavailable = json.loads(UNAVAILABLE_FILE.read_text(encoding='utf-8')) if UNAVAILABLE_FILE.exists() else {}
    out, seen = [], set()
    for d in docs:
        path = d['path']
        if not path or path in seen or path.removeprefix('/openapi/v2/') in unavailable or EXCLUDE.search(d['name'] + ' ' + path) or not d['fields'] or any('##' in r for r in d['required']):
            continue
        kind = kind_of(d['cat'], path)
        if not kind:
            continue
        seen.add(path)
        lowered = d['name'].lower()
        channel = 'low-price' if 'low-price' in lowered else 'official' if 'official' in lowered else ''
        out.append({'id': path.removeprefix('/openapi/v2/'), 'name': d['name'], 'kind': kind, 'family': family_of(path), 'path': path,
                    'channel': channel, 'doc': d['url'], 'summary': d['desc'][:240], 'required': d['required'], 'fields': d['fields']})
    out.sort(key=lambda m: (m['kind'], m['family'], m['id']))
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps({'source': INDEX, 'models': out}, ensure_ascii=False, indent=1) + '\n', encoding='utf-8')
    print(len(out), dict(collections.Counter(m['kind'] for m in out)), file=sys.stderr)
    print(dict(collections.Counter(m['family'] for m in out)), file=sys.stderr)


if __name__ == '__main__':
    main()
