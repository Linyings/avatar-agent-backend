"""Read existing media; write independent review artifacts. Never call providers."""
import json
import subprocess
import sys
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import workflow
from media_quality import audio_metrics, quality_issues

source=Path(sys.argv[1]).resolve()
destination=Path(sys.argv[2]).resolve()
if source == destination or source in destination.parents:
    raise ValueError('Review destination must be outside the original run')
destination.mkdir(parents=True,exist_ok=False)
state=workflow.read(source/'workflow.json')
audio=[]
for segment in state['segments']:
    rows=workflow.read(source/segment['id']/'timestamps.json')['sentences']
    words=[word for row in rows for word in row['words']]
    audio.append({**audio_metrics(source/segment['audio'],words),'segment_id':segment['id'],'start':segment['start']})
workflow.write(destination/'audio-quality.json',{'segments':audio,'issues':quality_issues(state,audio)})
(destination/'output.mp4').symlink_to(source/'output.mp4')
review,_=workflow.review_rendered_shots(SimpleNamespace(task=destination,state=state,ffmpeg='ffmpeg'))
material=next(m for m in state['materials'] if m['need_id']=='n_4c96608597fc1059_2')
segment=next(s for s in state['segments'] if s['id']==material['segment_id'])
words=[word for row in workflow.read(source/segment['id']/'timestamps.json')['sentences'] for word in row['words']]
manifest={'width':720,'height':1280,'fps':25,'duration':material['duration'],'style':state['input']['style'],'text':segment['text'],'words':words,'motion_plan':material['motion_plan'],'narration_window':{'start':11,'end':11+material['duration']}}
workflow.write(destination/'motion-input.json',manifest)
print(json.dumps({'review':str(destination),'shots':len(review['shots']),'frames':sum(len(s['frames']) for s in review['shots']),'face_check':review['face_check'],'issues':{code:sum(i['code']==code for i in review['issues']) for code in sorted(set(i['code'] for i in review['issues']))}},ensure_ascii=False))
