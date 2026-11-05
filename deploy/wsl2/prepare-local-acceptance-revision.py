"""Prepare a style/ratio revision with existing narration and talking footage.

Operator tool: cache preflight only, not a replacement for supplier accounting.
No regeneration flags or new prompts are allowed; final run records must still
be checked for unexpected model orders. Reuses the saved admission on rerun.
"""
import argparse
import json
from pathlib import Path
import re
import subprocess
import urllib.request
import uuid

p=argparse.ArgumentParser()
p.add_argument('--base-run',required=True,type=uuid.UUID)
p.add_argument('--case',required=True)
p.add_argument('--style',choices=['flat-vector','line-art'])
p.add_argument('--ratio',choices=['9:16','16:9','1:1'])
args=p.parse_args()
if not re.fullmatch(r'[a-zA-Z0-9_-]{1,64}',args.case):p.error('invalid case name')
root=Path('/opt/aicat/avatar-mvp');folder=root/'acceptance-v22'/args.case
folder.mkdir(parents=True,exist_ok=True);folder.chmod(0o700)
access=dict(line.split(': ',1) for line in (root/'LOCAL_ACCESS.txt').read_text().splitlines() if ': ' in line)
token=''
def api(path,body=None,method=None):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None
    with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8080/api/v1'+path,data=data,headers=headers,method=method),timeout=60) as response:return json.load(response)
login=api('/auth/login',{'email':access['Email'],'password':access['Password']})
token=login.get('data',login)['tokens']['access_token']
if (folder/'admission.json').exists():
    print(json.dumps(json.loads((folder/'admission.json').read_text())))
    raise SystemExit(0)
pending=folder/'admission-pending.json'
if pending.exists():
    saved=json.loads(pending.read_text());body=saved['payload'];project_id=saved['project_id']
else:
    base=api('/avatar-mvp/runs/'+str(args.base_run))
    assert base['status']=='succeeded' and base['audio_confirmed'] and base['script_confirmed']
    assert base['needs'] and base['shots'] and base['materials']
    project_id=base['project_id']
    # Check both run files and the exact fingerprints used by the shared cache.
    paths=[]
    for material in base['materials']:
        if material['kind']!='avatar':continue
        fp=material['fingerprint'];assert re.fullmatch('[0-9a-f]{64}',fp)
        paths.append('/app/data/avatar-mvp/workflow-v2/projects/'+project_id+'/cache/materials/'+fp+'.mp4')
    assert paths
    for segment in base['segments']:
        assert re.fullmatch(r'[a-zA-Z][a-zA-Z0-9_-]{0,63}',segment['id'])
        for name in ['audio.wav','timestamps.json','alignment-cache.json']:
            paths.append('/app/data/avatar-mvp/workflow-v2/runs/'+base['id']+'/'+segment['id']+'/'+name)
    for path in paths:subprocess.run(['docker','exec','aicat-avatar-api-1','test','-s',path],check=True)
    project=api('/avatar-mvp/projects/'+project_id);incoming=dict(base['input'])
    for key in ['title','topic','script','direction','voice','use_avatar','angles','photo','assets','subtitle_edits','subtitle_color','subtitle_scale','avatar_position']:
        assert project['input'].get(key)==base['input'].get(key), 'Draft changed since base run; refusing to overwrite '+key
    incoming['revision']=project['input']['revision']
    if args.style:incoming['style']=args.style
    if args.ratio:incoming['ratio']=args.ratio
    project=api('/avatar-mvp/projects/'+project_id,incoming,'PATCH')
    body={'revision':project['input']['revision'],'request_id':'local-revision-'+args.case,'edits':{'base_run_id':base['id'],'needs':base['needs'],'shots':base['shots']}}
    pending.write_text(json.dumps({'project_id':project_id,'payload':body},ensure_ascii=False,indent=2))
run=api('/avatar-mvp/projects/'+project_id+'/runs',body)
admission={'project_id':project_id,'run_id':run['id']}
(folder/'admission.json').write_text(json.dumps(admission))
print(json.dumps({**admission,'expected_new_model_calls':0,'review':'verify final orders and billing; inherited manual storyboard is not automatic planning evidence'}))
