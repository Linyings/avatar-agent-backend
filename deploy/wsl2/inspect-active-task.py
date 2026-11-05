"""Read local task state and query existing supplier orders only. Never submits jobs."""
import json
import subprocess
from datetime import datetime, timezone

query = "SELECT result FROM workflow_step_executions WHERE step_id='avatar.task' ORDER BY updated_at DESC LIMIT 4"
r = subprocess.run(['docker', 'exec', 'aicat-avatar-postgres-1', 'psql', '-U', 'avatar', '-d', 'avatar', '-At', '-c', query], capture_output=True, text=True, check=True)
tasks = [json.loads(line) for line in r.stdout.splitlines() if line.strip()]
print('checked_at=' + datetime.now(timezone.utc).isoformat())
for task in tasks:
    safe = {k: task.get(k) for k in ['id', 'title', 'status', 'stage', 'error', 'created_at', 'updated_at', 'stage_seconds', 'duration']}
    safe['shots'] = [{k: s.get(k) for k in ['id', 'status', 'submission', 'provider_task_id', 'started_at', 'completed_at', 'attempts']} for s in task.get('shots', [])]
    print(json.dumps(safe, ensure_ascii=False))
    if task.get('status') != 'running':
        continue
    for shot in task.get('shots', []):
        job = shot.get('provider_task_id')
        if not job or shot.get('status') != 'running':
            continue
        code = '''import json, os, urllib.request, urllib.error
base = os.environ.get('VIDEO_API_BASE_URL') or os.environ.get('RUNNINGHUB_BASE_URL', 'https://www.runninghub.cn')
key = os.environ.get('VIDEO_API_KEY') or os.environ.get('RUNNINGHUB_API_KEY', '')
req = urllib.request.Request(base.rstrip('/') + '/openapi/v2/query', data=json.dumps({'taskId': JOB}).encode(), headers={'Content-Type':'application/json','Authorization':'Bearer '+key})
try:
    with urllib.request.urlopen(req, timeout=35) as response:
        data = json.load(response)
        safe = {k:data.get(k) for k in ['taskId','status','code','errorCode','usage','requestId']}
        safe['result_count'] = len(data.get('results') or [])
        print(json.dumps(safe, ensure_ascii=False))
except urllib.error.HTTPError as err:
    print(json.dumps({'http_error':err.code}))
except Exception as err:
    print(json.dumps({'query_error_type':type(err).__name__}))
'''.replace('JOB', repr(job))
        result = subprocess.run(['docker', 'exec', 'aicat-avatar-api-1', 'python3', '-c', code], capture_output=True, text=True, check=True)
        print('supplier_query=' + result.stdout.strip())
