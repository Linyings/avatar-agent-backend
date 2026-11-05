"""Explicit, fixed acceptance storyboard using already measured source/audio.

Does not retry the original uncertain F05 call. Creates a version with manually
authored needs and shots, then the normal workflow produces the real materials.
"""
import argparse
import json
from pathlib import Path
import urllib.request

parser=argparse.ArgumentParser();parser.add_argument('--paid',action='store_true',required=True);parser.parse_args()
base='http://127.0.0.1:8080/api/v1'
root=Path('/opt/aicat/avatar-mvp')
access=dict(line.split(': ',1) for line in (root/'LOCAL_ACCESS.txt').read_text().splitlines() if ': ' in line)
token=''
def api(path,body=None):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None
    with urllib.request.urlopen(urllib.request.Request(base+path,data=data,headers=headers),timeout=60) as response:return json.load(response)
login=api('/auth/login',{'email':access['Email'],'password':access['Password']});token=login.get('data',login)['tokens']['access_token']
original=api('/avatar-mvp/runs/a28ac13f-7c30-47f7-9c41-66c7c0bd309a')
assert original['status']=='submission_unknown' and original['stage']=='F05'
assert original['audio_confirmed'] and len(original['segments'])==5 and len(original['descriptions'])==3
needs=[];shots=[]
for i,seg in enumerate(original['segments']):
    pid=seg['id'];duration=seg['duration'];start=seg['start'];avatar='host-'+pid;animation='animation-'+pid
    needs.append({'id':avatar,'segment_id':pid,'kind':'avatar','duration':duration,'angle':'正面' if i%2==0 else '左前方20度','content':seg['text'],'prompt':'使用参考人物的上半身，保持真实皮肤纹理，严格按对应音频说话，不改变身份或声音。'})
    needs.append({'id':animation,'segment_id':pid,'kind':'animation','duration':duration,'template':['steps','compare','highlight','relationship','summary'][i],'content':seg['text']})
    cursor=start
    if i in [2,3]:
        desc=original['descriptions'][1 if i==2 else 2];length=min(desc['end']-desc['start'],duration,2)
        real='source-'+pid
        needs.append({'id':real,'segment_id':pid,'kind':'real','duration':length,'content':seg['text'],'source_id':desc['asset_id'],'source_start':desc['start'],'description_id':desc['id']})
        shots.append({'id':f'S{len(shots)+1}','segment_id':pid,'start':cursor,'end':cursor+length,'primary':real,'overlay':avatar,'layout':'pip','card':'highlighter-sweep','primary_in':0,'overlay_in':0,'keyword':'嘴型与面容' if i==2 else '镜头构图','semantic':['机制'],'intent':'用原视频对应源区间说明当前检查重点','selection_reason':desc['text']})
        cursor+=length
    shots.append({'id':f'S{len(shots)+1}','segment_id':pid,'start':cursor,'end':start+duration,'primary':avatar if i==0 else animation,'overlay':'' if i==0 else avatar,'layout':'full' if i==0 else 'pip','card':'impact-open-title' if i==0 else 'chapter-title-card' if i==4 else 'highlighter-sweep','primary_in':cursor-start,'overlay_in':cursor-start,'keyword':['四步检查','身份与形象','嘴型与面容','镜头构图','字幕区域'][i],'semantic':['步骤'],'intent':'按确认配音时长呈现解释动画与对应人物','selection_reason':seg['text']})
project=api('/avatar-mvp/projects/'+original['project_id'])
body={'revision':project['input']['revision'],'request_id':'acceptance-v22-source-manual-requirements','edits':{'base_run_id':original['id'],'needs':needs,'shots':shots}}
folder=root/'acceptance-v22/case-b-source-manual';folder.mkdir(parents=True,exist_ok=True);folder.chmod(0o700)
(folder/'request.json').write_text(json.dumps(body,ensure_ascii=False,indent=2))
(folder/'admission-pending.json').write_text(json.dumps({'project_id':project['id'],'revision':body['revision'],'request_id':body['request_id'],'payload':body}))
run=api('/avatar-mvp/projects/'+project['id']+'/runs',body)
(folder/'admission.json').write_text(json.dumps({'project_id':project['id'],'run_id':run['id']}))
print(json.dumps({'run_id':run['id'],'manual_nodes':['F05','F10'],'reuses':'confirmed audio, source understanding and script','original_unknown_call_preserved':True}))
