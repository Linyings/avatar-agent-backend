"""Local rendering regression fixtures; synthetic tone/timestamps, NOT real model acceptance."""
import argparse
import json
from pathlib import Path
import time
from types import SimpleNamespace
import workflow as wf

p=argparse.ArgumentParser();p.add_argument('--output',required=True);p.add_argument('--all',action='store_true');options=p.parse_args()
root=Path(options.output);root.mkdir(parents=True,exist_ok=True);reports=[]
variants=[(style,ratio) for style in ['flat-vector','line-art'] for ratio in ['9:16','16:9','1:1']] if options.all else [('flat-vector','9:16')]
for style,ratio in variants:
 task=root/(style+'-'+ratio.replace(':','x'));task.mkdir(exist_ok=True)
 segments=[{'id':'p1','text':'确定主题。','audio':'p1/audio.wav','start':0,'duration':2},{'id':'p2','text':'准备素材。','audio':'p2/audio.wav','start':2,'duration':2}]
 state={'id':'render-regression-only','project_id':'synthetic-fixture','version':'avatar-workflow/2.2','input':{'style':style,'ratio':ratio,'title':'本地渲染测试','assets':[]},'segments':segments,'duration':4,'materials':[],'needs':[],'shots':[]}
 args=SimpleNamespace(task=task,state=state,inp={},paths={},ffmpeg='ffmpeg',ffprobe='ffprobe',node='node')
 for seg in segments:
  folder=task/seg['id'];folder.mkdir(exist_ok=True)
  wf.run(['ffmpeg','-y','-v','error','-f','lavfi','-i','sine=frequency=440:duration=2','-ar','24000','-ac','1',folder/'audio.wav'])
  words=[{'text':char,'start':i*.45,'end':(i+1)*.45} for i,char in enumerate(seg['text'][:-1])]
  wf.write(folder/'timestamps.json',{'total':2,'sentences':[{'i':0,'text':seg['text'],'start':0,'end':2,'words':words,'ok':True}]})
  need={'id':'n'+seg['id'],'segment_id':seg['id'],'kind':'animation','duration':2,'template':'steps','content':seg['text']};state['needs'].append(need);args.inp={'need':need}
  started=time.monotonic();state['materials'].append(wf.command_material(args));reports.append({'variant':task.name,'stage':'template/'+seg['id'],'seconds':time.monotonic()-started,'cost_status':'local_compute'})
 state['shots']=wf.default_shots(segments,state['materials']);wf.write(task/'workflow.json',state)
 for stage in ['assemble','shotbook','preview','render']:
  started=time.monotonic();result=wf.COMMANDS[stage](args);reports.append({'variant':task.name,'stage':stage,'seconds':time.monotonic()-started,'cost_status':'local_compute','warnings':result.get('warnings',[])})
  print(json.dumps({'variant':task.name,'stage':stage,'status':'passed','seconds':reports[-1]['seconds']},ensure_ascii=False),flush=True)
 wf.write(root/'render-regression.json',{'fixture':'synthetic tone; rendering regression only; no model quality acceptance','results':reports})
