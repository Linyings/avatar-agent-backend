"""Copy one existing real segment for isolated acceptance; never write the source run."""
from pathlib import Path
import json,shutil,sys
from types import SimpleNamespace
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import workflow
root=Path(sys.argv[1]);dest=Path(sys.argv[2]);dest.mkdir(parents=True,exist_ok=True)
for source in sorted(root.glob('*/workflow.json'),key=lambda p:p.stat().st_mtime,reverse=True):
    state=workflow.read(source)
    candidates=[m for m in state.get('materials',[]) if m['kind']=='avatar' and (source.parent/m['path']).is_file()]
    if not candidates:continue
    host=candidates[0];seg=next((s for s in state.get('segments',[]) if s['id']==host['segment_id'] and s.get('audio') and (source.parent/s['audio']).is_file()),None)
    if not seg:continue
    need=next((n for n in state.get('needs',[]) if n['kind']=='animation' and n['segment_id']==seg['id']),None)
    if need is None:continue
    for relative in [host['path'],seg['audio'],seg['id']+'/timestamps.json']:
        target=dest/relative;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(source.parent/relative,target)
    shutil.copyfile(source.parent/seg['audio'],dest/'voice.wav')
    seg={**seg,'start':0};need={**need,'duration':seg['duration']}
    inp={**state['input'],'assets':[],'reference_links':[],'script':seg['text']}
    snapshot={'version':'avatar-workflow/3.0','input':inp,'segments':[seg],'materials':[host],'needs':[need],'duration':seg['duration']}
    context=workflow.command_motion_context(SimpleNamespace(task=dest,state=snapshot,inp={'segment':seg,'need':need},ffmpeg='ffmpeg'))
    workflow.write(dest/'live-input.json',{'input':inp,'segment':seg,'need':need,'context':context})
    workflow.write(dest/'workflow.json',snapshot)
    print(json.dumps({'prepared':True,'duration':seg['duration'],'frames':len(context['frames']),'originalProjectModified':False}));break
else:raise RuntimeError('No real avatar segment available')
