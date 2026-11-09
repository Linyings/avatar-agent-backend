"""Copy one approved segment into an isolated verification folder, read-only source."""
import sys,json,shutil
from pathlib import Path
from types import SimpleNamespace
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import workflow

source=Path(sys.argv[1]);output=Path(sys.argv[2]);output.mkdir(parents=True,exist_ok=True)
state=workflow.read(source/'workflow.json')
need=next(n for n in state['needs'] if n['kind']=='animation')
segment=next(s for s in state['segments'] if s['id']==need['segment_id'])
hosts=[m for m in state['materials'] if m['kind']=='avatar' and m['segment_id']==segment['id']]
assert hosts,'No real same-segment avatar footage'
for host in hosts:
    target=workflow.safe_file(output,host['path']);target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(workflow.safe_file(source,host['path']),target)
target=output/segment['id'];target.mkdir(exist_ok=True)
shutil.copyfile(source/segment['id']/'timestamps.json',target/'timestamps.json')
shutil.copyfile(workflow.safe_file(source,segment['audio']),output/'voice.wav')
inp={'segment':segment,'need':need}
snapshot={'input':state['input'],'segments':[segment],'materials':hosts}
context=workflow.command_motion_context(SimpleNamespace(task=output,state=snapshot,inp=inp,ffmpeg='ffmpeg'))
workflow.write(output/'live-input.json',{'input':state['input'],'segment':segment,'need':need,'context':context})
workflow.write(output/'workflow.json',snapshot);workflow.write(output/'media-input.json',inp)
print(json.dumps({'segment':segment['id'],'actual_frames':len(context['frames']),'style':state['input']['style'],'original_project_modified':False}))
