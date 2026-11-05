"""Read-only post-deploy check; does not retry or start a workflow."""
import json
import runpy
import subprocess
import sys
from pathlib import Path

ns = runpy.run_path(str(Path(__file__).with_name('verify.py')))
request, token = ns['request'], ns['token']
run_id = '56f29cd8-f58d-4dff-982c-efbe566d5306'
status, _ = request('/api/v1/avatar-mvp/runs/' + run_id, token=token)
# The saved local deployment account does not own this user's test project.
assert status == 404, status
query = "select jsonb_build_object('status',result::jsonb->>'status','stage',result::jsonb->>'stage','materials',jsonb_array_length(result::jsonb->'materials'),'shots',jsonb_array_length(result::jsonb->'shots')) from workflow_step_executions where workflow_run_id='" + run_id + "' and step_id='avatar.workflow.v2'"
run = json.loads(subprocess.check_output(['docker','exec','aicat-avatar-postgres-1','psql','-U','avatar','-d','avatar','-Atc',query]))
assert run['status'] == 'failed' and run['stage'] == 'F10' and run['materials'] == 8 and run['shots'] == 0
query = "select elem->>'attempt' from workflow_step_executions, jsonb_array_elements(result::jsonb->'steps') elem where workflow_run_id='" + run_id + "' and step_id='avatar.workflow.v2' and elem->>'id'='F10'"
attempt = int(subprocess.check_output(['docker','exec','aicat-avatar-postgres-1','psql','-U','avatar','-d','avatar','-Atc',query]))
assert attempt == 2
status, response = request('/api/v1/avatar-mvp/catalog', token=token)
assert status == 200
catalog = response.get('data', response)
assert len([s for s in catalog['styles'] if not s['legacy'] and s['published']]) == 5
report = {'run_id': run_id, 'original_task_preserved_in_database': True, 'cross_user_task_access': 404, 'run_status': run['status'], 'stage': run['stage'], 'materials': run['materials'], 'f10_original_attempts': attempt, 'native_styles': 5, 'login': 'passed', 'model_calls': 0, 'task_retry_started': False}
Path(sys.argv[1]).write_text(json.dumps(report, ensure_ascii=False, indent=2))
print(json.dumps(report))
