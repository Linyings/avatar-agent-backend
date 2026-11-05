"""Explicit live acceptance runner. Persists admission before polling; never retries paid failures."""
import argparse
import json
import mimetypes
from pathlib import Path
import time
import urllib.request
import urllib.error
import uuid

p=argparse.ArgumentParser();p.add_argument('--paid',action='store_true',required=True);p.add_argument('--case',default='case-a');p.add_argument('--photo',required=True);p.add_argument('--source');p.add_argument('--voice');p.add_argument('--style',default='flat-vector');p.add_argument('--ratio',default='9:16');p.add_argument('--script');p.add_argument('--topic',default='数字人口播视频的制作流程');p.add_argument('--topic-only',action='store_true');p.add_argument('--direction',default='默认正面上半身讲解，需要至少一段与实际知识点对应的代码动画；不增加额外机位。');p.add_argument('--angles',nargs='*',default=[]);p.add_argument('--pause-at',choices=['script','audio']);p.add_argument('--recover',action='store_true');args=p.parse_args()
base='http://127.0.0.1:8080';root=Path('/opt/aicat/avatar-mvp');folder=root/'acceptance-v22'/args.case;folder.mkdir(parents=True,exist_ok=True);folder.chmod(0o700)
access=dict(line.split(': ',1) for line in (root/'LOCAL_ACCESS.txt').read_text().splitlines() if ': ' in line)
token=''
def request(path,data=None,raw=False,content_type='application/json'):
    headers={'Content-Type':content_type}
    if token:headers['Authorization']='Bearer '+token
    body=data if isinstance(data,bytes) else json.dumps(data,ensure_ascii=False).encode() if data is not None else None
    req=urllib.request.Request(base+path,data=body,headers=headers)
    try:
        with urllib.request.urlopen(req,timeout=300) as response:return response.read() if raw else json.load(response)
    except urllib.error.HTTPError as error:
        # Server's public message only; never print request headers, credentials or URLs.
        try:message=json.load(error).get('message','request rejected')
        except Exception:message='request rejected'
        raise RuntimeError(f'HTTP {error.code}: {message}') from None
def login():
    global token
    response=request('/api/v1/auth/login',{'email':access['Email'],'password':access['Password']});token=response.get('data',response)['tokens']['access_token']
def upload(pid,path,kind):
    boundary='----avatar-'+uuid.uuid4().hex;file=Path(path);mime=mimetypes.guess_type(file.name)[0] or 'application/octet-stream'
    body=(f'--{boundary}\r\nContent-Disposition: form-data; name="kind"\r\n\r\n{kind}\r\n--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{file.name}"\r\nContent-Type: {mime}\r\n\r\n').encode()+file.read_bytes()+f'\r\n--{boundary}--\r\n'.encode()
    return request(f'/api/v1/avatar-mvp/projects/{pid}/assets',body,content_type='multipart/form-data; boundary='+boundary)
def save(name,value):
    dest=folder/name;tmp=dest.with_suffix('.tmp');tmp.write_text(json.dumps(value,ensure_ascii=False,indent=2));tmp.replace(dest)
login();catalog=request('/api/v1/avatar-mvp/catalog')
path=folder/'admission.json'
if path.exists():state=json.loads(path.read_text());rid=state['run_id']
elif (folder/'admission-pending.json').exists():
    pending=json.loads((folder/'admission-pending.json').read_text());pid=pending['project_id']
    run=request(f'/api/v1/avatar-mvp/projects/{pid}/runs',pending.get('payload') or {'revision':pending['revision'],'request_id':pending['request_id']});rid=run['id'];save('admission.json',{'project_id':pid,'run_id':rid})
else:
    script=args.script or '制作数字人口播，先确定主题，再把文案按知识点拆开。每一段单独配音，用真实音频锁定时长。接着按内容准备人物与动画，最后通过分镜和字幕组合成片，让声音和画面一起把重点讲清楚。'
    incoming={'title':'V2.2 新工作流真实验收 '+args.case,'topic':args.topic,'script':'' if args.topic_only else script,'style':args.style,'voice':args.voice or catalog['default_voice'],'ratio':args.ratio,'use_avatar':True,'angles':args.angles,'assets':[],'direction':args.direction}
    project=request('/api/v1/avatar-mvp/projects',incoming);pid=project['id'];save('project.json',project)
    upload(pid,args.photo,'photo')
    if args.source:upload(pid,args.source,'video')
    project=request('/api/v1/avatar-mvp/projects/'+pid);nonce='acceptance-'+uuid.uuid4().hex;save('admission-pending.json',{'project_id':pid,'request_id':nonce,'revision':project['input']['revision']})
    run=request(f'/api/v1/avatar-mvp/projects/{pid}/runs',{'revision':project['input']['revision'],'request_id':nonce});rid=run['id'];save('admission.json',{'project_id':pid,'run_id':rid})
    print(json.dumps({'case':args.case,'run_id':rid,'event':'admitted'},ensure_ascii=False),flush=True)
previous='';started=time.monotonic()
recovered=False
while time.monotonic()-started<3*3600:
    try:r=request('/api/v1/avatar-mvp/runs/'+rid)
    except RuntimeError as err:
        if 'HTTP 401' in str(err):login();continue
        raise
    save('state.json',r);current=r['status']+'/'+r['stage']
    if current!=previous:
        print(json.dumps({'case':args.case,'status':r['status'],'stage':r['stage'],'duration':r['duration'],'events':len(r.get('events') or []),'error':r.get('error')},ensure_ascii=False),flush=True);previous=current
    if r['status']=='awaiting_confirmation':
        kind=r['confirmation'];version=r['confirmation_version']
        if args.pause_at==kind:
            print(json.dumps({'case':args.case,'event':'paused_at_confirmation','kind':kind,'run_id':rid},ensure_ascii=False),flush=True);break
        if kind=='script':
            assert ''.join(s['text'] for s in r['segments']).replace('\n','')==r['script'].replace('\n','')
            save('script-review.json',{'kind':'script','version':version,'review':'automated content/order validation for known acceptance text; human effect review pending'})
        elif kind=='audio':
            assert all(0<s['duration']<90 for s in r['segments'])
            for seg in r['segments']:(folder/(seg['id']+'.wav')).write_bytes(request(f'/api/v1/avatar-mvp/runs/{rid}/artifacts/audio-'+seg['id'],raw=True))
            save('audio-review.json',{'kind':'audio','version':version,'review':'real files downloaded; measured duration verified; human voice/lip review pending'})
        else:raise RuntimeError('unknown confirmation gate')
        request('/api/v1/avatar-mvp/runs/'+rid+'/confirm',{'kind':kind,'version':version})
    if r['status']=='succeeded':
        for artifact in r['artifacts']:
            if artifact['id'] in ['video','report','subtitles','cover','project','mapping','shotbook']:
                (folder/artifact['name']).write_bytes(request(f'/api/v1/avatar-mvp/runs/{rid}/artifacts/'+artifact['id'],raw=True))
        print(json.dumps({'case':args.case,'event':'delivered','duration':r['duration'],'directory':str(folder),'quality_review':'pending'},ensure_ascii=False),flush=True);break
    if r['status'] in ['failed','interrupted'] and args.recover and not recovered:
        if any(s.get('submission') in ['unknown','submitting','terminal_failed'] for s in r['steps']):raise SystemExit('Original order requires manual supplier review; no automatic resubmission')
        request('/api/v1/avatar-mvp/runs/'+rid+'/retry',{});recovered=True;continue
    if r['status'] in ['failed','submission_unknown','interrupted','stopped']:raise SystemExit('Stopped acceptance polling: recorded state retained; no automatic paid retry')
    time.sleep(5)
else:raise SystemExit('Acceptance polling deadline reached; provider orders retained')
