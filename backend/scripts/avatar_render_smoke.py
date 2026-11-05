"""Synthetic renderer test ONLY. This is not a Seedance/MVP acceptance sample."""
import argparse
import json
import math
from pathlib import Path
import subprocess
import numpy as np

parser=argparse.ArgumentParser()
parser.add_argument('--ffmpeg', required=True)
args=parser.parse_args()
backend=Path(__file__).resolve().parents[1]
task=backend/'data'/'avatar-render-smoke'
task.mkdir(parents=True,exist_ok=True)
def run(*cmd):
    subprocess.run([str(v) for v in cmd],check=True)
def write(path,obj):
    path.write_text(json.dumps(obj,ensure_ascii=False,indent=2),encoding='utf-8')
run(args.ffmpeg,'-y','-v','error','-f','lavfi','-i','sine=frequency=440:sample_rate=16000:duration=6.125',task/'voice.wav')
edges=[0,2.137,4.346,6.125]
texts=['开场展示。','强调重点。','总结收尾。']
cards=['impact-open-title','highlighter-sweep','chapter-title-card']
shots=[]
scenes=[]
for i in range(3):
    shot_id=f'shot-{i+1}'
    directory=task/shot_id
    directory.mkdir(exist_ok=True)
    frames=np.zeros((75,640,360,3),dtype=np.uint8)
    frames[:]=[(i+1)*45,35,100]
    for n in range(75):
        frames[n,80:240,50+n:150+n]=[180,180,230]
        frames[n,400:420,:,:]=[235,200,60]
    subprocess.run([str(v) for v in [args.ffmpeg,'-y','-v','error','-f','rawvideo','-pix_fmt','rgb24','-s','360x640','-r','25','-i','-','-an','-c:v','libx264','-pix_fmt','yuv420p',directory/'source.mp4']],input=frames.tobytes(),check=True)
    start,end=edges[i:i+2]
    text=texts[i]
    clip={'start':start,'end':end,'duration':end-start,'source_start':0,'source_duration':3,'fps':25,'width':360,'height':640,'face_zone':[90,40,180,180]}
    shots.append({'id':shot_id,'text':text,'keyword':text[:4],'card':cards[i],'layout':'split' if i==1 else 'full','source':f'{shot_id}/source.mp4','clip':clip})
    scenes.append({'text':text,'startSec':start,'durationSec':end-start,'chars':[{'ch':ch,'t':start+(end-start)*j/len(text),'e':start+(end-start)*(j+1)/len(text)} for j,ch in enumerate(text)]})
write(task/'manifest.json',{'synthetic_render_fixture':True,'title':'渲染测试素材','width':720,'height':1280,'fps':25,'duration':edges[-1],'shots':shots,'timing':{'scenes':scenes}})
run('node',backend/'avatar-runtime'/'render.mjs',task)
run(args.ffmpeg,'-y','-v','error','-ss','3.4','-i',task/'output.mp4','-frames:v','1',task/'preview.jpg')
print('Synthetic render smoke output:',task)
