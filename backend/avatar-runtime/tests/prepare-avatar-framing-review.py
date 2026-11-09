"""Build an isolated crop regression preview from existing footage; no providers."""
import json
import shutil
import sys
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import workflow

source=Path(sys.argv[1]).resolve();target=Path(sys.argv[2]).resolve()
if source==target or source in target.parents:raise ValueError('Use an independent review directory')
target.mkdir(parents=True,exist_ok=False);(target/'materials').mkdir()
state=workflow.read(source/'workflow.json');args=SimpleNamespace(task=target,ffmpeg='ffmpeg',ffprobe='ffprobe')
segments=[];materials=[];shots=[];audio=[];offset=0;reports=[]
for index,(sid,start,length,shape) in enumerate([('p2',8,6,'circle'),('p4',6,4,'circle'),('p4',2,2,'rounded'),('p4',10,2,'rectangle')]):
    seg=next(s for s in state['segments'] if s['id']==sid)
    person=next(m for m in state['materials'] if m['kind']=='avatar' and m['segment_id']==sid)
    motion=next(m for m in state['materials'] if m['kind']=='animation' and m['segment_id']==sid)
    sample_id=f'review-{index+1}'
    segments.append({**seg,'id':sample_id,'start':offset,'duration':length})
    pair=[]
    for original in (person,motion):
        destination=target/original['path']
        if not destination.exists():shutil.copy2(source/original['path'],destination)
        item={**original,'need_id':sample_id+'-'+original['kind'],'segment_id':sample_id}
        item,checks=workflow.enrich_avatar_material(args,item)
        materials.append(item);pair.append(item)
        if item['kind']=='avatar':reports.append({'segment':sid,'shape':shape,'warnings':checks,'detect_rate':item.get('avatar_framing',{}).get('detect_rate')})
    shots.append({'id':sample_id,'segment_id':sample_id,'start':offset,'end':offset+length,'primary':pair[1]['need_id'],'overlay':pair[0]['need_id'],'primary_in':0,'overlay_in':start,'avatar_shape':shape,'avatar_position':'bottom-right','layout':'pip','recipe':'visual-pip','card':'none','keyword':''})
    clip=target/(sample_id+'.wav')
    workflow.run(['ffmpeg','-v','error','-y','-i',source/seg['audio'],'-af',f'atrim=start={start}:end={start+length},asetpts=PTS-STARTPTS','-ar','24000','-ac','1',clip])
    audio.append(clip);offset+=length
listing=target/'audio-list.txt';listing.write_text(''.join("file '"+str(p).replace("'","'\\''")+"'\n" for p in audio))
workflow.run(['ffmpeg','-v','error','-y','-f','concat','-safe','0','-i',listing,'-c','copy',target/'voice.wav'])
shutil.copy2(target/'voice.wav',target/'full-mix.wav')
manifest={'width':720,'height':1280,'fps':25,'duration':offset,'presentation_version':workflow.PRESENTATION_VERSION,'input':state['input'],'segments':segments,'materials':materials,'shots':shots,'timing':{'scenes':[]}}
workflow.write(target/'workflow-props.json',{'manifest':manifest});workflow.write(target/'shots.json',shots);workflow.write(target/'source-review.json',reports)
print(json.dumps({'directory':str(target),'seconds':offset,'shots':len(shots),'source_reports':reports,'original_run_modified':False},ensure_ascii=False))
