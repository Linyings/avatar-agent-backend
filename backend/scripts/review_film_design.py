"""Offline layout regression using existing real media; no model requests.

Keeps the original run intact. The deterministic review plan verifies rendering,
not model planning, talking quality, or a new end-to-end model acceptance.
"""
import argparse
import importlib.util
import json
from pathlib import Path
import re
import shutil
import sys
import time
from types import SimpleNamespace

p=argparse.ArgumentParser();p.add_argument('--runtime',required=True);p.add_argument('--source',required=True);p.add_argument('--output',required=True);p.add_argument('--matrix-only',action='store_true');options=p.parse_args()
runtime=Path(options.runtime);source=Path(options.source);task=Path(options.output)
if task.resolve()==source.resolve():raise ValueError('Review cannot overwrite a historical run')
task.mkdir(parents=True,exist_ok=True)
sys.path.insert(0,str(runtime));import workflow as wf
state=json.loads((source/'workflow.json').read_text());state['id']='offline-film-design-review'
for name in ['timestamps.json','timing.json','subtitle-timing.json','caption-timestamps.json','voice.wav']:
    if (source/name).exists():shutil.copy2(source/name,task/name)
for seg in state['segments']:
    for name in ['audio.wav','timestamps.json']:
        dest=task/seg['id']/name;dest.parent.mkdir(exist_ok=True);shutil.copy2(source/seg['id']/name,dest)
for material in state['materials']:
    dest=task/material['path'];dest.parent.mkdir(exist_ok=True);shutil.copy2(source/material['path'],dest)
args=SimpleNamespace(task=task,state=state,inp={},paths={},ffmpeg='ffmpeg',ffprobe='ffprobe',node='node')
report={'kind':'offline layout regression with existing real media; no model calls','source_run':source.name,'model_calls':0,'steps':[]}
def record(name,fn):
    started=time.monotonic();result=fn();report['steps'].append({'step':name,'seconds':round(time.monotonic()-started,3),'cost_status':'local_compute'});wf.write(task/'review-report.json',report);print(json.dumps(report['steps'][-1]),flush=True);return result
for need in state['needs']:
    if need['kind']=='animation':
        args.inp={'need':need}
        material=record('content-animation/'+need['id'],lambda:wf.command_material(args))
        for i,prior in enumerate(state['materials']):
            if prior['need_id']==need['id']:state['materials'][i]=material
needs={n['id']:n for n in state['needs']};materials={m['need_id']:m for m in state['materials']}
for i,shot in enumerate(state['shots']):
    seg=next(s for s in state['segments'] if s['id']==shot['segment_id']);primary=materials[shot['primary']]
    shot['items']=[x.strip() for x in re.split(r'[。；！？\n，、]',seg['text']) if x.strip()][:4]
    if primary['kind']=='animation':
        # Let the animation own the frame in alternate visual sections.
        if i%2:shot.pop('overlay',None)
        shot.update(recipe='visual-pip' if shot.get('overlay') else 'visual-hero',card='none')
    else:
        text=seg['text'];recipe='steps' if any(x in text for x in ['首先','然后','步骤']) else 'compare' if any(x in text for x in ['相比','区别','对比']) else 'evidence' if any(x in text for x in ['数据','%','％','元']) else 'host-explain'
        if i==0:recipe='host-hero'
        if i==len(state['shots'])-1:recipe='summary'
        shot.update(recipe=recipe,card='keyword' if recipe=='host-hero' else 'none')
wf.write(task/'workflow.json',state)
record('shotbook',lambda:wf.command_shotbook(args))
record('layout-matrix',lambda:wf.run(['node',runtime/'film-review.mjs',task]))
if not options.matrix_only:record('render',lambda:wf.command_render(args))
report['shots']=[{k:s.get(k) for k in ['id','recipe','start','end']} for s in state['shots']]
report['status']='passed';wf.write(task/'review-report.json',report)
