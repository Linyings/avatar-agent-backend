"""Offline measurements and review frames; no model or quality pass decision."""
import argparse
import json
import math
from pathlib import Path
import subprocess

p=argparse.ArgumentParser();p.add_argument('task');args=p.parse_args()
task=Path(args.task).resolve()
state=json.loads((task/'workflow-manifest.json').read_text())
info=json.loads(subprocess.check_output(['ffprobe','-v','error','-show_streams','-show_format','-of','json',str(task/'output.mp4')]))
video=next(s for s in info['streams'] if s['codec_type']=='video')
report={'duration':float(info['format']['duration']),'video':{k:video[k] for k in ['width','height','nb_frames','r_frame_rate']},'expected_frames':math.ceil(state['duration']*25),'has_audio':any(s['codec_type']=='audio' for s in info['streams']),'source_shots':[],'quality_review':'frames only; voice/lip/identity require review'}
cursor=0
for shot in state['shots']:
    assert abs(shot['start']-cursor)<.001
    cursor=shot['end']
    for role in ['primary','overlay']:
        material=next((m for m in state['materials'] if m['need_id']==shot.get(role)),None)
        if not material:continue
        offset=shot.get(role+'_in',0)
        assert offset>=0 and offset+shot['end']-shot['start']<=material['duration']+.08
        if material['kind']=='real':
            report['source_shots'].append({'shot':shot['id'],'global_start':shot['start'],'global_end':shot['end'],'source_id':material['source_id'],'source_start':material['source_start']+offset,'source_end':material['source_start']+offset+shot['end']-shot['start']})
assert abs(cursor-state['duration'])<.001
assert int(video['nb_frames'])==report['expected_frames'] and report['has_audio']
step=state['duration']/6
subprocess.run(['ffmpeg','-v','error','-y','-i',str(task/'output.mp4'),'-vf',f'fps=1/{step},scale=640:-1,tile=2x3','-frames:v','1',str(task/'contact-sheet.jpg')],check=True)
for shot in state['shots']:
    sec=(shot['start']+shot['end'])/2
    subprocess.run(['ffmpeg','-v','error','-y','-ss',str(sec),'-i',str(task/'output.mp4'),'-frames:v','1',str(task/(shot['id']+'-review.jpg'))],check=True)
(task/'media-review.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
print(json.dumps(report,ensure_ascii=False))
