#!/usr/bin/env python3
"""Find registry models that RunningHub no longer serves, without paying.

Each model receives an empty JSON body. A live endpoint rejects it with a
missing-field error (1007) before creating any order; discontinued (1026) or
unknown (1001) endpoints are written to scripts/media_unavailable.json, which
gen_media_models.py excludes. Run where the RunningHub key is available, e.g.
inside the API container:

    python probe_media_models.py models.json media_unavailable.json
"""
import json
import os
import sys
import time
import urllib.request

UNAVAILABLE = {'1001', '1026'}


def main():
    models_path, out_path = sys.argv[1], sys.argv[2]
    base = os.environ.get('RUNNINGHUB_BASE_URL') or os.environ['VIDEO_API_BASE_URL']
    key = os.environ.get('RUNNINGHUB_API_KEY') or os.environ['VIDEO_API_KEY']
    models = json.load(open(models_path, encoding='utf-8'))['models']
    try:
        unavailable = json.load(open(out_path, encoding='utf-8'))
    except FileNotFoundError:
        unavailable = {}
    for m in models:
        if not m['required']:
            # An empty body could be a valid order; never probe these.
            continue
        req = urllib.request.Request(base.rstrip('/') + m['path'], data=b'{}', method='POST',
                                     headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
        body = json.loads(urllib.request.urlopen(req, timeout=60).read().decode())
        if body.get('taskId'):
            print('WARNING: order created for', m['id'], body['taskId'], file=sys.stderr)
            continue
        code = str(body.get('errorCode'))
        if code in UNAVAILABLE:
            unavailable[m['id']] = code + ' ' + str(body.get('errorMessage', ''))[:120]
        else:
            unavailable.pop(m['id'], None)
        time.sleep(0.3)
    with open(out_path, 'w', encoding='utf-8', newline='\n') as f:
        json.dump(dict(sorted(unavailable.items())), f, ensure_ascii=False, indent=1)
        f.write('\n')
    print(len(unavailable), 'unavailable', file=sys.stderr)


if __name__ == '__main__':
    main()
