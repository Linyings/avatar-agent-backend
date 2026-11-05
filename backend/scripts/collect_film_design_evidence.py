"""Collect offline rendering evidence; reads historical media without modifying it."""
import argparse
import json
from pathlib import Path
import subprocess

p=argparse.ArgumentParser();p.add_argument('--source',required=True);p.add_argument('--review',required=True);a=p.parse_args()
source=Path(a.source);review=Path(a.review)
probe=json.loads(subprocess.check_output(['ffprobe','-v','error','-show_streams','-show_format','-of','json',str(review/'output.mp4')]))
video=next(s for s in probe['streams'] if s['codec_type']=='video');audio=[s for s in probe['streams'] if s['codec_type']=='audio']
assert len(audio)==1,'Final narration must be mixed exactly once'
subprocess.run(['ffmpeg','-v','error','-i',review/'output.mp4','-f','null','-'],check=True)
for label,t in [('host',3),('animation',10),('middle',32),('ending',58)]:
    subprocess.run(['ffmpeg','-y','-v','error','-ss',str(t),'-i',review/'output.mp4','-frames:v','1',review/(label+'.jpg')],check=True)
subprocess.run(['ffmpeg','-y','-v','error','-ss','10','-i',source/'output.mp4','-ss','10','-i',review/'output.mp4','-filter_complex','[0:v]scale=960:540,drawtext=text=Before:x=20:y=16:fontsize=30:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=8[left];[1:v]scale=960:540,drawtext=text=After:x=20:y=16:fontsize=30:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=8[right];[left][right]hstack=inputs=2','-frames:v','1',review/'before-after.jpg'],check=True)
report={'duration':float(probe['format']['duration']),'video_duration':float(video['duration']),'frames':int(video['nb_frames']),'width':video['width'],'height':video['height'],'audio_streams':len(audio),'decode':'passed','model_calls':0,'scope':'existing real media, deterministic review plan; not a fresh model acceptance'}
(review/'media-verification.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report))
