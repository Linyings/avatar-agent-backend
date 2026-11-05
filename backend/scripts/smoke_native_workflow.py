"""Offline F07 -> talkcraft -> MP4 regression. Synthetic tone, zero model calls."""
import json,sys,time
from pathlib import Path
from types import SimpleNamespace

runtime=Path('/app/avatar-runtime');sys.path.insert(0,str(runtime))
import workflow as wf

root=Path(sys.argv[1]);root.mkdir(parents=True,exist_ok=True);reports=[]
styles=json.loads((runtime/'native-styles.json').read_text(encoding='utf-8'))
if len(sys.argv)>2:
    selected=set(sys.argv[2:]);styles=[s for s in styles if s['id'] in selected]
    if len(styles)!=len(selected):raise ValueError('Unknown requested style')
for i,style in enumerate(styles):
    ratio=['16:9','9:16','1:1'][i%3]
    task=root/style['id'];task.mkdir(exist_ok=True)
    text='图形跟随声音。';duration=3.16
    seg={'id':'p1','text':text,'chapter':'创意变成现实','audio':'p1/audio.wav','start':0,'duration':duration}
    state={'id':'offline-native-smoke','project_id':'local-render-regression','version':'avatar-workflow/2.2','input':{'style':style['id'],'ratio':ratio,'title':'原生动画渲染验证','assets':[]},'segments':[seg],'duration':duration,'materials':[],'needs':[],'shots':[]}
    folder=task/'p1';folder.mkdir(exist_ok=True)
    wf.run(['ffmpeg','-y','-v','error','-f','lavfi','-i',f'sine=frequency=440:duration={duration}','-ar','24000','-ac','1',folder/'audio.wav'])
    words=[{'text':char,'start':.12+j*.42,'end':.5+j*.42} for j,char in enumerate(text[:-1])]
    wf.write(folder/'timestamps.json',{'total':duration,'sentences':[{'i':0,'text':text,'start':0,'end':duration,'words':words,'ok':True}]})
    need={'id':'n1','segment_id':'p1','kind':'animation','duration':duration,'template':'highlight','content':'用当前文案解释图形与声音的关联'};state['needs']=[need]
    args=SimpleNamespace(task=task,state=state,inp={'need':need},paths={},ffmpeg='ffmpeg',ffprobe='ffprobe',node='node')
    stages=[];warnings=[]
    for name in ['material','assemble','shotbook','preview','render']:
        began=time.monotonic()
        result=wf.COMMANDS[name](args)
        warnings.extend(result.get('warnings',[]))
        if name=='material':
            state['materials']=[result];state['shots']=wf.default_shots([seg],[result]);wf.write(task/'workflow.json',state)
        stages.append({'step':name,'seconds':round(time.monotonic()-began,3),'cost_status':'local_compute','model_calls':0})
        print(json.dumps({'style':style['id'],'step':name,'status':'passed','seconds':stages[-1]['seconds']}),flush=True)
    info=wf.probe(args,task/'output.mp4');v=next(s for s in info['streams'] if s['codec_type']=='video')
    assert (v['width'],v['height'])==wf.dimensions(ratio)
    assert int(v['nb_frames'])==79
    assert any(s['codec_type']=='audio' for s in info['streams'])
    assert abs(float(info['format']['duration'])-duration)<.1
    assert (task/'materials/n1-native-report.json').is_file()
    reports.append({'style':style['id'],'ratio':ratio,'duration':duration,'frames':79,'has_audio':True,'stages':stages,'quality_warnings':warnings,'output':str(task/'output.mp4')})
    wf.write(root/'report.json',{'fixture':'synthetic tone; validates rendering and delivery, not real model quality','model_calls':0,'results':reports})
    for j,second in enumerate([.3,1.5,2.9]):
        contact=root/'contact-frames';contact.mkdir(exist_ok=True)
        wf.run(['ffmpeg','-y','-v','error','-ss',second,'-i',task/'output.mp4','-frames:v','1','-vf','scale=320:240:force_original_aspect_ratio=decrease,pad=320:240:(ow-iw)/2:(oh-ih)/2:color=white',contact/f'{i}-{j}.jpg'])
wf.run(['ffmpeg','-y','-v','error','-pattern_type','glob','-i',str(root/'contact-frames/*.jpg'),'-vf','tile=3x5','-frames:v','1',root/'contact.jpg'])
