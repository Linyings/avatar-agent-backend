"""Fixed workflow media stages. No paid calls, arbitrary code generation, or simulated outputs."""
from __future__ import annotations
import argparse
import base64
import hashlib
import importlib.util
import json
import math
import re
from pathlib import Path
import shutil
import subprocess
import sys
import zipfile
from media_quality import motion_window, audio_metrics, quality_issues
from avatar_framing import analyze_avatar

ROOT=Path(__file__).resolve().parent
PRESENTATION_VERSION='film-design/3'
# Voice-driven clips return the driving narration as their soundtrack (lab
# measurement: correlation 1.0 at zero offset). Re-voiced clips scored < .4.
TALKING_SYNC_THRESHOLD=.9

def read(p):return json.loads(Path(p).read_text(encoding='utf-8'))
def write(p,v):
    p=Path(p);p.parent.mkdir(parents=True,exist_ok=True)
    tmp=p.with_name(p.name+'.tmp');tmp.write_text(json.dumps(v,ensure_ascii=False,indent=2),encoding='utf-8');tmp.replace(p)
def run(argv,**kw):return subprocess.run([str(x) for x in argv],check=True,**kw)
def safe_file(root,relative):
    root=Path(root).resolve();p=Path(relative)
    if p.is_absolute() or '\\' in str(relative):raise ValueError('Invalid media reference')
    p=(root/p).resolve()
    if not p.is_relative_to(root):raise ValueError('Media reference outside task')
    return p

def probe(args,path):
    return json.loads(subprocess.check_output([args.ffprobe,'-v','error','-show_format','-show_streams','-of','json',str(path)]))
def duration(info,kind=None):
    streams=[s for s in info.get('streams',[]) if kind is None or s.get('codec_type')==kind]
    if not streams:raise ValueError('Required media stream missing')
    sec=float(streams[0].get('duration') or info.get('format',{}).get('duration') or 0)
    if not math.isfinite(sec) or sec<=0:raise ValueError('No measurable media duration')
    return sec

def load_upstream(name):
    spec=importlib.util.spec_from_file_location('wf_'+name,ROOT/'talkcraft'/(name+'.py'))
    mod=importlib.util.module_from_spec(spec);spec.loader.exec_module(mod);return mod

def piece_ranges(total,words,limit=14):
    if total<=0:raise ValueError('Missing audio duration')
    words=sorted((w for w in words if w['end']>w['start']),key=lambda w:w['start'])
    gaps=[(left['end']+right['start'])/2 for left,right in zip(words,words[1:]) if right['start']>=left['end']]
    result=[];start=0.0
    while total-start>limit+.001:
        choices=[g for g in gaps if start+.4<g<=start+limit]
        if not choices:raise ValueError('No aligned word boundary within provider duration; revise this paragraph')
        end=round(max(choices),6);result.append((start,end));start=end
    result.append((start,total));return result

def default_shots(segments,materials):
    result=[]
    for i,seg in enumerate(segments):
        items=[m for m in materials if m['segment_id']==seg['id']]
        if not items:raise ValueError('Missing actual material')
        result.append({'id':f'S{i+1}','segment_id':seg['id'],'start':seg['start'],'end':seg['start']+seg['duration'],'primary':items[0]['need_id'],'card':'highlighter-sweep','layout':'full','keyword':''})
    return result

def dimensions(ratio):return {'9:16':(720,1280),'16:9':(1280,720),'1:1':(720,720)}[ratio]

def command_portrait_frames(args):
    # Source is resolved by the backend, never by a model. Bound frame count
    # and image size; sampled vision is not a temporal lip-sync measurement.
    source=Path(args.inp['source']).resolve()
    allowed=(args.task.resolve(),args.task.resolve().parents[1]/'projects')
    if not any(source.is_relative_to(root) for root in allowed):raise ValueError('Portrait source outside workflow storage')
    info=probe(args,source);video=bool(args.inp.get('video'))
    total=duration(info,'video') if video else 0
    times=[min(i*total/7,max(0,total-.08)) for i in range(8)] if video else [0]
    folder=args.task/'portrait'/'frames';folder.mkdir(parents=True,exist_ok=True)
    frames=[]
    for i,t in enumerate(times):
        dest=folder/f'frame-{i}.jpg'
        run([args.ffmpeg,'-y','-v','error','-ss',t,'-i',source,'-frames:v','1','-vf','scale=768:768:force_original_aspect_ratio=decrease','-q:v','3',dest])
        frames.append('data:image/jpeg;base64,'+base64.b64encode(dest.read_bytes()).decode())
    return {'frames':frames,'times':times,'scope':'sampled-visual-review','lip_sync':'not_measured'}

def command_probe(args):
    inp=read(args.task/'probe-input.json');p=Path(inp['path'])
    if inp['kind']=='text':
        data=p.read_bytes()
        if len(data)>1048576:raise ValueError('Reference text exceeds 1MB')
        text=data.decode('utf-8-sig')
        if not text.strip() or '\x00' in text:raise ValueError('Reference must be nonempty UTF-8 text')
        return {'duration':0,'sha256':hashlib.sha256(data).hexdigest()}
    info=probe(args,p)
    kind='video' if inp['kind']=='video' else 'image'
    stream=next(s for s in info['streams'] if s['codec_type']=='video')
    if kind=='image' and (stream.get('codec_name') not in ['png','mjpeg'] or not any(x in info.get('format',{}).get('format_name','') for x in ['image2','png_pipe','jpeg_pipe'])):raise ValueError('Photo must decode as a static PNG or JPEG image')
    if stream['width']<64 or stream['height']<64 or stream['width']>8192 or stream['height']>8192:raise ValueError('Unsupported dimensions')
    if kind=='image' and p.stat().st_size>8<<20:raise ValueError('Image exceeds 8MB')
    # Decode the first frame to reject renamed/non-media uploads.
    run([args.ffmpeg,'-v','error','-i',p,'-frames:v',1,'-f','null','-'],stdout=subprocess.DEVNULL)
    return {'duration':duration(info,'video') if kind=='video' else 0,'sha256':hashlib.file_digest(p.open('rb'),'sha256').hexdigest()}

def command_audio(args):
    seg=args.inp['segment'];folder=args.task/seg['id'];audio=folder/'audio.wav'
    # Normalise actual provider output, never infer duration from text.
    tmp=folder/'normalised.wav';run([args.ffmpeg,'-y','-v','error','-i',audio,'-ar','24000','-ac','1',tmp]);tmp.replace(audio)
    sec=duration(probe(args,audio),'audio');warnings=[]
    aligned=folder/'timestamps.json';digest=hashlib.sha256(audio.read_bytes()+seg['text'].encode()).hexdigest()
    if aligned.is_file() and (folder/'alignment-cache.json').is_file() and read(folder/'alignment-cache.json').get('digest')==digest:return {'duration':sec,'warnings':read(folder/'alignment-cache.json').get('warnings',[])}
    mod=load_upstream('timestamps_cpu')
    if args.asr_backend=='firered':words=mod.asr_firered(str(audio),args.asr_model_dir)
    else:words=mod.asr_whisper(str(audio),args.asr_model_dir or 'small')
    if not words:raise ValueError('Alignment produced no measured words')
    data=mod.align([seg['text']],words)
    if not data.get('sentences') or not data['sentences'][0].get('words'):raise ValueError('Alignment has no usable word mapping')
    if not all(s.get('ok') for s in data['sentences']):warnings.append(seg['id']+'：配音识别/字词对齐存在差异，请试听；继续制作')
    for s in data['sentences']:
        for word in s['words']:
            word['start']=min(sec,max(0,word['start']));word['end']=min(sec,max(word['start'],word['end']))
    data['total']=sec;write(aligned,data);write(folder/'alignment-cache.json',{'digest':digest,'warnings':warnings})
    return {'duration':sec,'warnings':warnings}

def command_pieces(args):
    seg=args.inp['segment'];need=args.inp['need'];aligned=read(args.task/seg['id']/'timestamps.json')
    words=[w for s in aligned['sentences'] for w in s['words']]
    chars=load_upstream('make_timing').expand_chars(seg['text'],words,0)
    ranges=piece_ranges(need['duration'],words);pieces=[]
    for i,(start,end) in enumerate(ranges):
        identifier=f'c{i+1}';rel=f"pieces/{need['id']}-{identifier}.wav";dest=safe_file(args.task,rel);dest.parent.mkdir(exist_ok=True)
        run([args.ffmpeg,'-y','-v','error','-i',safe_file(args.task,seg['audio']),'-af',f'atrim=start={start}:end={end},asetpts=PTS-STARTPTS','-ar','24000','-ac','1',dest])
        text=''.join(c['ch'] for c in chars if start-.001<=c['t']<end-.001)
        pieces.append({'id':identifier,'start':start,'duration':end-start,'audio':rel,'text':text})
    return {'pieces':pieces}

def material_result(need,path,face=None):
    return {'need_id':need['id'],'segment_id':need['segment_id'],'kind':need['kind'],'path':path,'duration':need['duration'],'source_id':need.get('source_id'),'source_start':need.get('source_start',0),'face_zone':face or []}

def audio_diagnostic(args,source,reference,folder):
    """Legacy amplitude envelope correlation is diagnostic only, never a lip-sync score."""
    import numpy as np
    import wave
    extracted=folder/'generated.wav'
    run([args.ffmpeg,'-y','-v','error','-i',source,'-vn','-ar','16000','-ac','1',extracted])
    normal=folder/'reference.wav';run([args.ffmpeg,'-y','-v','error','-i',reference,'-ar','16000','-ac','1',normal])
    def envelope(path):
        with wave.open(str(path),'rb') as f:x=np.frombuffer(f.readframes(f.getnframes()),dtype=np.int16).astype(float)
        x=x[:len(x)//160*160].reshape(-1,160);return np.sqrt(np.mean(x*x,axis=1))
    a,b=envelope(normal),envelope(extracted);n=min(len(a),len(b));score=float(np.corrcoef(a[:n],b[:n])[0,1]) if n>2 else 0
    if not math.isfinite(score):score=0
    write(folder/'audio-check.json',{'method':'Pearson correlation of 10ms RMS envelopes; no time warping','correlation':score,'reference_threshold':.55,'advisory':True,'is_lip_sync_score':False})
    return score

def command_avatar(args):
    need=args.inp['need'];seg=args.inp['segment'];pieces=args.inp['pieces'];warnings=[];normalised=[]
    for piece in pieces:
        folder=args.task/'pieces';source=folder/f"{need['id']}-{piece['id']}.mp4";info=probe(args,source);sec=duration(info,'video')
        if sec+.045<piece['duration']:raise ValueError('Source video cannot cover its actual driven speech; no looping or retiming')
        qa=args.task/'qa'/f"{need['id']}-{piece['id']}";qa.mkdir(parents=True,exist_ok=True)
        for label,t in [('first',0),('middle',piece['duration']/2),('last',max(0,piece['duration']-.08))]:
            run([args.ffmpeg,'-y','-v','error','-ss',t,'-i',source,'-frames:v',1,qa/(label+'.jpg')])
        talking=piece.get('lip_sync')=='talking-avatar'
        score=None
        try:
            if any(x['codec_type']=='audio' for x in info['streams']):
                score=audio_diagnostic(args,source,safe_file(args.task,piece['audio']),qa)
        except Exception:
            if talking:raise ValueError(need['id']+'：数字人口型片段的配音核对未完成，已停止组装；请重试或检查原订单')
            warnings.append(need['id']+'：音频对比检查未完成，继续制作')
        if talking:
            # The clip's soundtrack must be the narration that drove the mouth;
            # anything else means the lips follow different speech.
            if score is None or score<TALKING_SYNC_THRESHOLD:raise ValueError(f"{need['id']}：数字人片段声音与配音不一致（相关度 {0 if score is None else score:.3f}），口型无法保证同步，已停止组装")
        elif score is not None and score<.55:warnings.append(f"{need['id']}：音频包络相关度 {score:.4f}，口型可能与配音不同步；建议改用数字人口型模型重做")
        normal=folder/f"{need['id']}-{piece['id']}-normal.mp4"
        # Talking-avatar output can exceed the frame size; bound it, keep its ratio.
        scale='fps=25,scale=-2:min(ih\\,1280),scale=trunc(iw/2)*2:trunc(ih/2)*2' if talking else 'fps=25,scale=trunc(iw/2)*2:trunc(ih/2)*2'
        run([args.ffmpeg,'-y','-v','error','-i',source,'-t',piece['duration'],'-an','-vf',scale,'-c:v','libx264','-preset','fast','-pix_fmt','yuv420p',normal]);normalised.append(normal)
    dest=args.task/'materials'/(need['id']+'.mp4');concat=args.task/'pieces'/(need['id']+'-concat.txt')
    concat.write_text(''.join("file '"+str(p).replace("'","'\\''")+"'\n" for p in normalised),encoding='utf-8')
    run([args.ffmpeg,'-y','-v','error','-f','concat','-safe','0','-i',concat,'-c','copy',dest])
    result=material_result(need,'materials/'+need['id']+'.mp4')
    framed,checks=enrich_avatar_material(args,result,[p['start'] for p in pieces[1:]])
    return {**result,'face_zone':framed.get('face_zone',[]),'warnings':warnings+checks}


def enrich_avatar_material(args,material,breaks=()):
    item=dict(material);warnings=[]
    source=safe_file(args.task,material['path'])
    info=probe(args,source);video=next(s for s in info['streams'] if s['codec_type']=='video')
    item.update(width=video['width'],height=video['height'])
    if material['kind']!='avatar':return item,warnings
    try:
        cache=safe_file(args.task,'avatar-framing/'+material['need_id']+'.json')
        if not breaks and cache.exists():breaks=read(cache).get('source',{}).get('breaks',[])
        track=analyze_avatar(source,cache,breaks)
        item['avatar_framing']=track
        item['face_zone']=track['safe_zone'] if track['status']=='tracked' else []
        messages={'uncertain_face':'主人物定位不确定，后期保留完整矩形画面，不使用猜测裁剪','source_zoom':'源片人物大小有变化，后期按检测结果稳定头肩大小，请复核','source_reframing':'源片构图有跳变，已分别定位，请复核衔接','multiple_faces':'源片含额外人像，已优先定位最大主人物，请复核是否需要重生干净素材','ambiguous_person':'部分时刻存在大小相近的多张脸，定位不足时保留完整画面'}
        warnings.extend(material['need_id']+'：'+messages[code] for code in track['warnings'])
    except Exception:
        item['face_zone']=[];item.pop('avatar_framing',None)
        warnings.append(material['need_id']+'：主人物检查未完成，保留完整矩形画面，不使用猜测裁剪')
    return item,warnings

def command_motion_context(args):
    """Sample actual same-segment talking footage; reference demos are never inputs."""
    segment=args.inp['segment'];frames=[];identifiers=[]
    for material in args.state.get('materials',[]):
        if material['kind']!='avatar' or material['segment_id']!=segment['id']:continue
        source=safe_file(args.task,material['path'])
        if not source.is_file():raise ValueError('本段人物视频缺失，无法设计配套动效')
        folder=args.task/'motion-context';folder.mkdir(exist_ok=True)
        total=min(float(material['duration']),float(args.inp['need']['duration']))
        for index,time in enumerate([min(.1,total/4),total*.5]):
            target=folder/(material['need_id']+f'-{index}.jpg')
            run([args.ffmpeg,'-y','-v','error','-ss',time,'-i',source,'-frames:v',1,'-vf','scale=640:-2','-q:v',4,target])
            frames.append('data:image/jpeg;base64,'+base64.b64encode(target.read_bytes()).decode())
        identifiers.append(material['need_id'])
        if len(identifiers)>=2:break
    return {'frames':frames,'source':'generated-avatar-frames' if frames else 'no-avatar','avatar_ids':identifiers}

def template_manifest(args,need):
    w,h=dimensions(args.state['input']['ratio']);seg=next(s for s in args.state['segments'] if s['id']==need['segment_id'])
    timing=read(args.task/seg['id']/'timestamps.json')
    phrases=[p.strip() for p in re.split(r'[。；！？\n，、]',seg['text']) if p.strip()]
    selected=need.get('items') or []
    if selected and len(selected)<=4 and all(isinstance(p,str) and p.strip() and p in seg['text'] for p in selected):phrases=selected
    plan=getattr(args,'inp',{}).get('motion_plan')
    if args.state['input']['style'].startswith('mg-') and not plan:raise ValueError('动效缺少本段内容方案，不能使用演示视频替代')
    window=motion_window(args.state.get('needs',[]),need,seg.get('duration',need['duration']))
    required=[need['narration_quote']] if need.get('narration_quote') else []
    return {'width':w,'height':h,'fps':25,'duration':need['duration'],'style':args.state['input']['style'],'template':need.get('template','highlight'),'title':seg.get('chapter') or '', 'brief':need['content'],'text':seg['text'],'items':phrases[:4],'render_mode':'content-motion' if plan else 'semantic','motion_plan':plan,'words':[w for s in timing['sentences'] for w in s['words']],'narration_window':window,'required_quotes':required}


def command_motion_scope(args):
    need=args.inp['need'];seg=args.inp['segment']
    window=motion_window(args.state.get('needs',[]),need,seg['duration'])
    timing=read(args.task/seg['id']/'timestamps.json')
    words=[w for s in timing['sentences'] for w in s['words']]
    return {'narration_window':window,'allowed_quote':need.get('narration_quote',''),'words':[w for w in words if w['end']>window['start'] and w['start']<window['end']]}


def command_audio_review(args):
    rows=[]
    for seg in args.state['segments']:
        aligned=read(args.task/seg['id']/'timestamps.json');words=[w for s in aligned['sentences'] for w in s['words']]
        rows.append({**audio_metrics(safe_file(args.task,seg['audio']),words,args.ffmpeg),'segment_id':seg['id'],'start':seg['start']})
    issues=quality_issues({'shots':[]},rows)
    report={'status':'needs_review' if issues else 'measured','human_review':'not_reviewed','segments':rows,'issues':issues}
    write(args.task/'audio-quality.json',report)
    return {'audio_quality':report,'warnings':[f"{i['time']:.2f}s：{i['message']}" for i in issues]}

def command_material(args):
    need=args.inp['need'];dest=args.task/'materials'/(need['id']+'.mp4');dest.parent.mkdir(exist_ok=True)
    if need['kind']=='animation':
        manifest=template_manifest(args,need);write(args.task/'animation-props.json',{'animation':manifest})
        run([args.node,ROOT/'workflow-render.mjs','animation',args.task,dest],timeout=600)
        report=args.task/'animation-native-report.json'
        if report.exists():shutil.copy2(report,args.task/'materials'/(need['id']+'-native-report.json'))
        (args.task/'materials'/(need['id']+'-prompt.md')).write_text('# 本段内容动效方案\n\n风格参考仅用于配色、材质、线条和运动。以下为当前口播设计；未使用演示视频。\n\n'+json.dumps(manifest,ensure_ascii=False,indent=2),encoding='utf-8')
    else:
        source=Path(args.paths[need['source_id']]);asset=next(a for a in args.state['input']['assets'] if a['id']==need['source_id'])
        cmd=[args.ffmpeg,'-y','-v','error']
        if asset['kind']=='image':cmd+=['-loop','1']
        else:cmd+=['-ss',need.get('source_start',0)]
        cmd+=['-i',source,'-t',need['duration'],'-an','-vf','fps=25,scale=trunc(iw/2)*2:trunc(ih/2)*2','-c:v','libx264','-preset','fast','-pix_fmt','yuv420p',dest];run(cmd)
    duration(probe(args,dest),'video')
    result=material_result(need,'materials/'+need['id']+'.mp4')
    report=args.task/'materials'/(need['id']+'-native-report.json')
    if need['kind']=='animation' and report.exists():
        result['rendered_scenes']=read(report).get('scenes',[])
    return result

def frame_sample_times(start,end,fps):
    # Container duration can extend past the final frame. Seeking .01 seconds
    # from EOF may succeed without producing an image; reserve a full frame.
    margin=min((end-start)/2,max(.1,1/max(fps,.01)))
    return [min(end-margin,start+second+.1) for second in range(math.ceil(end-start))]

def command_frames(args):
    source=args.inp;path=Path(args.paths[source['id']]);info=probe(args,path);total=duration(info,'video');batches=[]
    stream=next(s for s in info['streams'] if s['codec_type']=='video')
    rate=stream.get('avg_frame_rate') or stream.get('r_frame_rate') or '25/1'
    numerator,denominator=(float(x) for x in rate.split('/'));fps=numerator/denominator if denominator else 25
    if total>1800:raise ValueError('This local MVP accepts source videos up to 30 minutes; split longer files')
    folder=args.task/'analysis'/source['id'];folder.mkdir(parents=True,exist_ok=True)
    for index in range(math.ceil(total/20)):
        start=index*20;end=min(total,start+20);frames=[];times=[]
        for second,time in enumerate(frame_sample_times(start,end,fps)):
            dest=folder/f'{index}-{second}.jpg'
            if not dest.exists():run([args.ffmpeg,'-y','-v','error','-ss',time,'-i',path,'-frames:v',1,'-vf','scale=640:-2','-q:v',5,dest])
            frames.append('data:image/jpeg;base64,'+base64.b64encode(dest.read_bytes()).decode());times.append(round(time,3))
        batches.append({'start':start,'end':end,'times':times,'frames':frames})
    return {'batches':batches}

def command_assemble(args):
    state=args.state;sentences=[];timings=[];audio=[];warnings=[]
    for seg in state['segments']:
        local=read(args.task/seg['id']/'timestamps.json');audio.append(safe_file(args.task,seg['audio']))
        for row in local['sentences']:
            entry={**row,'i':len(sentences),'start':row['start']+seg['start'],'end':row['end']+seg['start'],'words':[{**w,'start':w['start']+seg['start'],'end':w['end']+seg['start']} for w in row['words']]};sentences.append(entry)
    timestamps={'total':state['duration'],'sentences':sentences};write(args.task/'timestamps.json',timestamps)
    timing=load_upstream('make_timing').make_timing_data(timestamps);write(args.task/'timing.json',timing)
    captions=[]
    edits=state['input'].get('subtitle_edits') or {}
    for i,row in enumerate(sentences):
        seg=state['segments'][i]
        captions.append(subtitle_row(row,edits.get(seg['id'],row['text'])))
    caption_data={'total':state['duration'],'sentences':captions};write(args.task/'caption-timestamps.json',caption_data)
    write(args.task/'subtitle-timing.json',load_upstream('make_timing').make_timing_data(caption_data))
    listing=args.task/'audio-concat.txt';listing.write_text(''.join("file '"+str(p).replace("'","'\\''")+"'\n" for p in audio),encoding='utf-8')
    run([args.ffmpeg,'-y','-v','error','-f','concat','-safe','0','-i',listing,'-ar','24000','-ac','1',args.task/'voice.wav'])
    for material in state['materials']:
        sec=duration(probe(args,safe_file(args.task,material['path'])),'video')
        if sec+.08<material['duration']:raise ValueError('Required material too short; cannot loop to cover missing footage')
    return {'warnings':warnings}

def subtitle_row(row,text):
    timing=load_upstream('make_timing')
    before=timing.normalized_text(row['text']);after=timing.normalized_text(text)
    if len(before)!=len(after):raise ValueError('Display-only subtitle correction must retain spoken character count; edit narration to change speech')
    spans=[c for c in timing.expand_chars(row['text'],row['words'],row['start']) if timing.normalized_text(c['ch'])]
    words=[{'text':c,'start':span['t'],'end':span['e']} for c,span in zip(after,spans)]
    return {**row,'text':text,'words':words}

def quality_command(args,name,argv):
    try:
        proc=subprocess.run([str(x) for x in argv],cwd=ROOT,capture_output=True,text=True,encoding='utf-8',timeout=90)
        log=(proc.stdout or '')+(proc.stderr or '');(args.task/(name+'.log')).write_text(log,encoding='utf-8')
        return [name+'：检查发现问题，详见工程报告；继续制作'] if proc.returncode or 'FAIL' in log else []
    except Exception:return [name+'：检查未完成，继续制作']

def command_plan(args):
    """Analyze narration before F10 selection; no extra model/paid call.

    Upstream card matches remain advisory references. Only the implemented
    recipes passed by Go are selectable, never all 108 unadapted upstream cards.
    """
    candidates=(args.inp or {}).get('candidates',[])
    by_segment={c['segment_id']:c for c in candidates}
    scripts=ROOT/'talkcraft/flow/scripts'
    warnings=quality_command(args,'design-semantic-init',[sys.executable,scripts/'semantic_annotate.py','--project',args.task,'--timestamps','timestamps.json','--semantics','design-semantics.json','--init','--force'])
    data=read(args.task/'timestamps.json');sentences=[];book=['# 设计候选（素材就绪后、正式分镜前）','']
    for i,seg in enumerate(args.state['segments']):
        sid='S'+str(i+1);c=by_segment.get(seg['id'],{})
        visible=[m for m in args.state['materials'] if m.get('segment_id')==seg['id']]
        media=' · '.join({'avatar':'人','animation':'纯动效','real':'V'}[m['kind']]+'('+m['path']+')' for m in visible)
        book += [f"### {sid} {seg['start']:.3f}–{seg['start']+seg['duration']:.3f}",'- 素材：'+media,'- 意图：'+c.get('semantic','论点'),'']
        for row in data['sentences']:
            if seg['start']-.01<=row['start']<seg['start']+seg['duration']-.001:
                sem=c.get('semantic','论点');sentences.append({'i':row.get('i',len(sentences)),'t':row['start'],'text':row['text'],'shot':sid.lower(),'sem':[sem],'entities':[],'weight':'main','need':['结构' if sem=='步骤' else '对比' if sem=='对比' else '量化' if sem=='数据' else '强调']})
    write(args.task/'design-semantics.json',{'version':1,'source':{'timestamps':'timestamps.json','shots':'DESIGN.md'},'sentences':sentences})
    (args.task/'DESIGN.md').write_text('\n'.join(book),encoding='utf-8')
    warnings+=quality_command(args,'design-card-match',[sys.executable,scripts/'card_match.py','--project',args.task,'--semantics','design-semantics.json','--shotbook','DESIGN.md','--out','design-card-candidates.md','--json','design-card-candidates.json'])
    matches=read(args.task/'design-card-candidates.json') if (args.task/'design-card-candidates.json').exists() else {}
    if not matches:write(args.task/'design-card-candidates.json',{})
    geometry=[]
    for material in args.state['materials']:
        path=args.task/material['path']
        if path.exists():
            item,checks=enrich_avatar_material(args,material);warnings+=checks
            geometry.append({'need_id':material['need_id'],'width':item['width'],'height':item['height'],'face_zone':item.get('face_zone',[]),'fit_policy':'按源时间定位主人物；固定窗口，稳定头肩大小；定位不确定时保留完整矩形画面'})
    plan={'version':PRESENTATION_VERSION,'style':args.state['input']['style'],'ratio':args.state['input']['ratio'],'segments':candidates,'material_geometry':geometry,'upstream_references':matches,'rules':['一个镜头一个视觉主角','只选择已经实现的recipes；上游候选只用于语义参考','动画已含标题和图解，不重复叠信息卡','默认单机位，不增加人物生成','同构图重复只提醒，不强制切镜']}
    write(args.task/'design-plan.json',plan)
    return {'plan':plan,'warnings':warnings}

def command_shotbook(args):
    state=args.state;shots=state['shots'];materials={m['need_id']:m for m in state['materials']};lines=['# SHOTBOOK','','G0 '+state['input']['style']+' / '+state['input']['ratio'],'','视觉版本：'+PRESENTATION_VERSION+'；颜色、字体、容器及字幕统一从 filmTheme 读取。','','| 镜 | 人物形态·方位 | 素材容器 | 主卡 / 配方 |','| --- | --- | --- | --- |'];warnings=[]
    for shot in shots:
        material=materials[shot['primary']]
        host='主画面上半身' if material['kind']=='avatar' else ('画中画上半身' if shot.get('overlay') else '无人物')
        container={'avatar':'人物主画面','animation':'代码动画','real':'视频相框'}[material['kind']]
        lines.append(f"| {shot['id']} | {host} | {container} | {shot['card']} / {shot.get('recipe','历史布局')} |")
    lines.append('')
    for shot in shots:
        material=materials[shot['primary']]
        visible=[material]+([materials[shot['overlay']]] if shot.get('overlay') else [])
        media=' · '.join({'avatar':'人','animation':'纯动效','real':'V'}[m['kind']]+'('+m['path']+')' for m in visible)
        lines += [f"### {shot['id']} {shot['start']:.3f}–{shot['end']:.3f} · 意图：{shot.get('intent') or '解释对应台词'}",'- 素材：'+media,'- 蒙皮行：'+shot['card']+' → '+state['input']['style']+' 统一主题；配方 '+shot.get('recipe','兼容布局'),'- 台词：'+next(s['text'] for s in state['segments'] if s['id']==shot['segment_id'])]
        if material['kind']=='real':lines.append('- 素材边框：视频相框，使用 workflow-entry 的 CSS 边框与轻运镜；不声称已使用其他呈现卡')
        lines.append('')
    missing=[n['id'] for n in state.get('needs',[]) if n['id'] not in materials]
    lines+=['## 未完成 / 未采集清单','、'.join(missing) if missing else '无缺失的必需素材；效果仍待人工审片。未自动联网采集，真实素材来自用户上传。','']
    sources=['# 素材来源与使用记录','','用户上传素材的授权范围待用户确认；此清单不授予商业许可。','配音：用户确认的分段音频，最终 voice.wav 仅混入一次。','']
    for m in state['materials']:
        if m['kind']=='real':origin=f"用户上传 {m.get('source_id','')}；源入点 {m.get('source_start',0):.6f} 秒，截取 {m.get('duration',0):.6f} 秒；未联网检索"
        elif m['kind']=='animation':origin='按当前文案与人物画面设计的代码动画；风格参考与许可见 templates/'+state['input']['style']+'/SOURCE.json'
        else:origin='用户人物照片及对应实际配音驱动；供应商订单与模型配置见运行报告'
        sources.append('- '+m['path']+'：'+origin)
    (args.task/'sources.md').write_text('\n'.join(sources)+'\n',encoding='utf-8')
    (args.task/'SHOTBOOK.md').write_text('\n'.join(lines),encoding='utf-8');write(args.task/'shots.json',shots)
    (args.task/'shots.ts').write_text('export const SHOTS = '+json.dumps(shots,ensure_ascii=False,indent=2)+' as const;\n',encoding='utf-8')
    scripts=ROOT/'talkcraft/flow/scripts'
    warnings+=quality_command(args,'semantic-init',[sys.executable,scripts/'semantic_annotate.py','--project',args.task,'--timestamps','timestamps.json','--shots','shots.json','--init','--force'])
    sem=args.task/'semantics.json'
    if sem.exists():
        doc=read(sem)
        for row in doc.get('sentences',[]):
            match=next((s for s in shots if s['start']-.01<=row.get('t',0)<s['end']-.001),None)
            hints=row.get('hint',[]);sem_tags=(match or {}).get('semantic') or [h for h in hints if not h.startswith('need:')] or ['论点'];need=[h[5:] for h in hints if h.startswith('need:')] or (['结构'] if '步骤' in sem_tags else ['强调'])
            row.update(sem=sem_tags,weight='main',shot=match['id'].lower() if match else '',need=need,entities=row.get('entities') or [])
        write(sem,doc)
    warnings+=quality_command(args,'card-match',[sys.executable,scripts/'card_match.py','--project',args.task,'--out','card-candidates.md','--json','card-candidates.json'])
    beats=[];timing=read(args.task/'timing.json')
    for shot in shots:
        candidates=[(i,c) for i,row in enumerate(timing['scenes']) for c in row['chars'] if shot['start']<=c['t']<shot['end']-.1 and c['e']>c['t']]
        keyword=shot.get('keyword','');selected=None
        for i,row in enumerate(timing['scenes']):
            text=''.join(c['ch'] for c in row['chars']);offset=text.find(keyword) if keyword else -1
            if offset>=0 and shot['start']<=row['chars'][offset]['t']<shot['end']-.1:selected=(i,row['chars'][offset]);break
        if selected is None and candidates:selected=candidates[0]
        if selected:
            i,char=selected;t=char['t'];anchor=keyword if keyword and keyword.startswith(char['ch']) else char['ch'];shot['anchor_frame']=max(0,round((t-shot['start'])*25));beats.append({'t':t,'anchor':anchor,'sentence':i,'what':shot['card'],'label':shot['id'].lower()})
    write(args.task/'shots.json',shots);(args.task/'shots.ts').write_text('export const SHOTS = '+json.dumps(shots,ensure_ascii=False,indent=2)+' as const;\n',encoding='utf-8')
    write(args.task/'beats.json',beats);write(args.task/'anchors.json',beats)
    warnings+=quality_command(args,'beat-lint',[sys.executable,scripts/'beat_lint.py',args.task/'beats.json',args.task/'timestamps.json','--shots',args.task/'shots.json'])
    warnings+=quality_command(args,'preflight',[sys.executable,scripts/'preflight.py','--project',args.task,'--shotbook','SHOTBOOK.md','--shots','shots.json','--semantics','semantics.json'])
    # Face zones are in source pixels; carry real dimensions into the renderer.
    enriched=[]
    for material in state['materials']:
        item=dict(material)
        path=args.task/material['path']
        if path.exists():
            item,checks=enrich_avatar_material(args,material);warnings+=checks
        enriched.append(item)
    w,h=dimensions(state['input']['ratio']);manifest={**state,'materials':enriched,'presentation_version':PRESENTATION_VERSION,'width':w,'height':h,'fps':25,'duration':state['duration'],'timing':read(args.task/'timing.json')}
    if (args.task/'subtitle-timing.json').exists():manifest['caption_timing']=read(args.task/'subtitle-timing.json')
    write(args.task/'workflow-manifest.json',manifest);write(args.task/'workflow-props.json',{'manifest':manifest})
    return {'warnings':warnings}

def artifact(id,name,path,type):return {'id':id,'name':name,'path':path,'media_type':type}

def renderer(args,preview=False):
    # Upgrade only during an explicitly requested render, without paid calls.
    props_path=args.task/'workflow-props.json';props=read(props_path);manifest=props['manifest'];framing_warnings=[]
    if manifest.get('presentation_version')!=PRESENTATION_VERSION:
        enriched=[]
        for material in manifest['materials']:
            item,checks=enrich_avatar_material(args,material);enriched.append(item);framing_warnings+=checks
        manifest.update(materials=enriched,presentation_version=PRESENTATION_VERSION)
        write(props_path,props);write(args.task/'workflow-manifest.json',manifest)
    command=[args.node,ROOT/'workflow-render.mjs','preview' if preview else 'render',args.task]
    run(command)
    warnings=[];log=args.task/'render-quality.json'
    if log.exists():warnings=read(log).get('warnings',[])
    return warnings+framing_warnings

def command_preview(args):
    warnings=renderer(args,True);shots=args.state['shots'];items=[]
    for shot in shots:
        p=args.task/'preview'/(shot['id']+'.mp4')
        if p.exists():items.append(artifact('preview-'+shot['id'],shot['id']+'有声预览.mp4','preview/'+shot['id']+'.mp4','video/mp4'))
    if not items:raise ValueError('No actual preview produced')
    review,review_items=review_rendered_shots(args,True)
    return {'artifacts':items+review_items,'warnings':warnings,'quality_review':review}


def review_rendered_shots(args,preview=False):
    audio=read(args.task/'audio-quality.json').get('segments',[]) if (args.task/'audio-quality.json').exists() else []
    issues=quality_issues(args.state,audio);shots=[];items=[]
    rendered=read(args.task/'render-quality.json') if (args.task/'render-quality.json').exists() else {}
    framing={row['shot_id']:row for row in sorted(rendered.get('avatar_framing',[]),key=lambda r:r.get('layer')=='overlay')}
    props=args.task/'workflow-props.json'
    if props.exists():
        manifest=read(props).get('manifest',{});reported=set()
        for shot in args.state['shots']:
            for material in manifest.get('materials',[]):
                if material.get('kind')!='avatar' or material.get('need_id') not in (shot.get('primary'),shot.get('overlay')) or material['need_id'] in reported:continue
                reported.add(material['need_id']);track=material.get('avatar_framing',{})
                messages={'source_zoom':'源片人物大小有变化，请复核小窗稳定效果；过近而无法安全裁剪的镜头会保留完整矩形画面','source_reframing':'源片出现构图跳变，请检查镜头衔接','multiple_faces':'源片存在额外人像，已优先定位主人物；仍需检查源片是否干净','ambiguous_person':'源片部分时刻人物定位不明确，请检查；定位不足的镜头会保留完整矩形画面'}
                for code in track.get('warnings',[]):
                    if code in messages:issues.append({'code':'avatar_'+code,'severity':'review','shot_id':shot['id'],'time':shot['start'],'message':messages[code]})
                if track.get('status')!='tracked':issues.append({'code':'avatar_framing_unavailable','severity':'review','shot_id':shot['id'],'time':shot['start'],'message':'主人物定位不足，后期保留完整矩形人物画面，请复核'})
    detector=None
    try:
        import cv2
        model=Path.home()/'.cache/koubo/face_detection_yunet_2023mar.onnx'
        if model.exists():detector=cv2.FaceDetectorYN.create(str(model),'',(720,1280),.6,.3,5000)
    except Exception:pass
    materials={m['need_id']:m for m in args.state.get('materials',[])}
    for shot in args.state['shots']:
        duration=shot['end']-shot['start'];margin=min(.15,duration/4)
        decision=framing.get(shot['id'],{})
        if decision.get('mode')=='tight':issues.append({'code':'avatar_tight_crop','severity':'review','shot_id':shot['id'],'time':shot['start'],'message':'源片取景较近，已按人脸裁切填满人物窗口，头顶或肩部可能被少量裁去；如需完整头肩请重做中景人物素材'})
        elif decision.get('mode') in ('centered','full-source'):issues.append({'code':'avatar_centered_crop','severity':'review','shot_id':shot['id'],'time':shot['start'],'message':'人物定位不足，已按上半身居中裁切填满窗口，请复核取景'})
        times=[margin,duration/2,max(margin,duration-margin)];frames=[];face_sizes=[]
        for index,local in enumerate(times):
            relative=f"quality/{shot['id']}-{index+1}.jpg";dest=safe_file(args.task,relative);dest.parent.mkdir(exist_ok=True)
            source=args.task/'preview'/(shot['id']+'.mp4') if preview else args.task/'output.mp4'
            second=local if preview else shot['start']+local
            try:
                run([args.ffmpeg,'-v','error','-y','-ss',second,'-i',source,'-frames:v',1,'-q:v','3',dest],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                if not dest.is_file():raise ValueError('frame not available')
            except Exception:
                issues.append({'code':'sample_missing','severity':'review','shot_id':shot['id'],'time':shot['start']+local,'message':'抽帧未完成，请播放此镜头检查'})
                continue
            aid=f"quality-{shot['id']}-{index+1}";items.append(artifact(aid,f"{shot['id']} {shot['start']+local:.2f}s 检查帧",relative,'image/jpeg'));frames.append({'artifact_id':aid,'time':round(shot['start']+local,3)})
            shape=decision.get('actual_shape') or shot.get('avatar_shape') or args.state['input'].get('avatar_shape')
            if detector is not None and shape=='circle' and materials.get(shot.get('overlay'),{}).get('kind')=='avatar':
                try:
                    im=cv2.imread(str(dest));h,w=im.shape[:2];size=min(w,h)*.28;side=shot.get('avatar_position') or args.state['input'].get('avatar_position') or 'right'
                    x=w*.055 if 'left' in side else w*.945-size;y=h*.07 if side.startswith('top') else h*.88-size
                    crop=im[max(0,int(y)):min(h,int(y+size)),max(0,int(x)):min(w,int(x+size))]
                    detector.setInputSize((crop.shape[1],crop.shape[0]));_,faces=detector.detect(crop)
                    clipped=faces is None or len(faces)==0
                    if not clipped:
                        f=max(faces,key=lambda a:a[14]);face_sizes.append(float(f[3]/size))
                        clipped=any(((float(f[j])-size/2)**2+(float(f[j+1])-size/2)**2)**.5>size*.46 for j in (4,6,10,12))
                    if clipped:issues.append({'code':'circle_face','severity':'review','shot_id':shot['id'],'time':round(shot['start']+local,3),'artifact_id':aid,'message':'圆窗内未检出完整眼部和嘴部，请检查裁切位置'})
                except Exception:
                    issues.append({'code':'face_check_unavailable','severity':'review','shot_id':shot['id'],'time':round(shot['start']+local,3),'artifact_id':aid,'message':'此检查帧未完成脸部检测，请人工检查裁切'})
        if len(face_sizes)>1 and max(face_sizes)>min(face_sizes)*1.3:
            issues.append({'code':'face_scale','severity':'review','shot_id':shot['id'],'time':shot['start'],'message':'人物在圆窗内的脸部大小变化超过 30%，请检查是否发生变焦'})
        shots.append({'shot_id':shot['id'],'frames':frames,'avatar_framing':decision,'status':'needs_review' if any(x.get('shot_id')==shot['id'] for x in issues) else 'sampled'})
    report={'version':'render-review/1','status':'needs_review' if issues else 'awaiting_visual_review','human_review':'not_reviewed','face_check':'sampled' if detector is not None else 'unavailable','scope':['镜头长度','配音节奏测量','逐镜首中尾抽帧','圆窗眼嘴可见性抽查'],'limitations':['抽帧不能证明全片口型同步或人物身份一致','背景连续性、重复文字和新闻事实仍需人工复核'],'issues':issues,'shots':shots}
    write(args.task/'quality-review.json',report);items.append(artifact('quality-review','逐镜质量检查.json','quality-review.json','application/json'))
    return report,items

def command_render(args):
    warnings=renderer(args);out=args.task/'output.mp4';info=probe(args,out);sec=duration(info,'video')
    # Decode actual output; visual/sync/frame-count discrepancies only warn.
    run([args.ffmpeg,'-v','error','-i',out,'-f','null','-'],stdout=subprocess.DEVNULL)
    stream=next(s for s in info['streams'] if s['codec_type']=='video');expected=math.ceil(args.state['duration']*25);actual=int(stream.get('nb_frames') or 0)
    if actual!=expected:warnings.append(f'成片帧数 {actual} 与期望 {expected} 不同，视频可播放；请人工检查')
    if not any(s['codec_type']=='audio' for s in info['streams']):warnings.append('成片音轨检查异常，请人工检查')
    write(args.task/'render-check.json',{'actual_frames':actual,'expected_frames':expected,'duration':sec,'warnings':warnings,'quality_review':'not_reviewed'})
    run([args.ffmpeg,'-y','-v','error','-ss',min(.5,sec/2),'-i',out,'-frames:v',1,args.task/'cover.jpg'])
    def stamp(t):
        ms=round(t*1000);return f'{ms//3600000:02}:{ms//60000%60:02}:{ms//1000%60:02},{ms%1000:03}'
    captions=[];scenes=[]
    for row in read(args.task/'caption-timestamps.json')['sentences']:
        # One subtitle block per short measured character group.
        chars=load_upstream('make_timing').expand_chars(row['text'],row['words'],row['start'])
        scenes.append({'chars':chars})
    width,height=dimensions(args.state['input']['ratio']);font=min(width,height)*.045*(args.state['input'].get('subtitle_scale') or 1)
    payload=json.dumps({'scenes':scenes,'budget':width*.91/font*1.35},ensure_ascii=False)
    groups=json.loads(subprocess.check_output([str(args.node),str(ROOT/'caption-pages.mjs')],input=payload.encode()))
    for group in groups:
        start=group[0]['t'];end=max(start+.02,group[-1]['e']);captions += [str(len(captions)//4+1),stamp(start)+' --> '+stamp(end),''.join(c['ch'] for c in group),'']
    (args.task/'subtitles.srt').write_text('\n'.join(captions),encoding='utf-8')
    review,review_items=review_rendered_shots(args)
    # Self-contained source+media package; no env, secrets or signed URLs.
    project=args.task/'project.zip'
    with zipfile.ZipFile(project,'w',zipfile.ZIP_DEFLATED) as archive:
        for path in ROOT.joinpath('src').rglob('*'):
            if path.is_file():archive.write(path,'src/'+path.relative_to(ROOT/'src').as_posix())
        for name in ['package.json','package-lock.json','tsconfig.json','UPSTREAM.json','TALKCRAFT-LICENSE','workflow-render.mjs','director-motion.mjs','motion-checks.mjs','content-motion.mjs','caption-pages.mjs','catalog-previews.mjs','mg-native.mjs','mg-shell.js','native-styles.json']:
            archive.write(ROOT/name,name)
        for folder in ['talkcraft','templates','mg-upstream']:
            for path in (ROOT/folder).rglob('*'):
                if path.is_file() and '__pycache__' not in str(path):archive.write(path,path.relative_to(ROOT))
        archive.writestr('README.md','# 制作工程\n\n运行 npm ci，然后 node workflow-render.mjs render .。需要 Node 24、FFmpeg/FFprobe 和 Remotion 浏览器依赖。\n素材内人声静音，完整 voice.wav 只混入一次。质量提示记录于 render-quality.json。\n')
        for name in ['workflow-manifest.json','workflow-props.json','timestamps.json','caption-timestamps.json','subtitle-timing.json','timing.json','SHOTBOOK.md','sources.md','shots.ts','shots.json','beats.json','anchors.json','voice.wav','render-check.json']:
            archive.write(args.task/name,name)
        for name in ['design-plan.json','design-semantics.json','DESIGN.md','design-card-candidates.md','design-card-candidates.json','semantics.json','card-candidates.md','card-candidates.json','render-quality.json','preflight.log','beat-lint.log','card-match.log','semantic-init.log']:
            if (args.task/name).is_file():archive.write(args.task/name,name)
        for name in ['quality-review.json','audio-quality.json']:
            if (args.task/name).exists():archive.write(args.task/name,name)
        for folder in ['materials','preview','qa','quality','avatar-framing']:
            for path in (args.task/folder).rglob('*'):
                if path.is_file():archive.write(path,path.relative_to(args.task))
    items=[artifact('video','口播成片.mp4','output.mp4','video/mp4'),artifact('subtitles','字幕.srt','subtitles.srt','application/x-subrip'),artifact('cover','封面.jpg','cover.jpg','image/jpeg'),artifact('project','制作工程.zip','project.zip','application/zip'),artifact('shotbook','SHOTBOOK.md','SHOTBOOK.md','text/markdown'),artifact('mapping','素材时间映射.json','workflow-manifest.json','application/json')]
    for shot in args.state['shots']:
        p=args.task/'preview'/(shot['id']+'.mp4')
        if p.exists():items.append(artifact('preview-'+shot['id'],shot['id']+'有声预览.mp4','preview/'+shot['id']+'.mp4','video/mp4'))
    return {'artifacts':items+review_items,'warnings':warnings,'quality_review':review}

def command_motion_timing(args):
    manifest=template_manifest(args,args.inp['need'])
    source=args.task/'motion-timing-input.json';result=args.task/'motion-timing-result.json'
    write(source,{'mode':'timing','animation':manifest})
    run([args.node,ROOT/'motion-validate.mjs',source,result],timeout=30)
    return {**read(result),'animation':manifest}


def command_motion_validate(args):
    run([args.node, ROOT/'motion-validate.mjs', args.task/'media-input.json'])
    return {'valid': True}

COMMANDS={k[8:]:v for k,v in list(globals().items()) if k.startswith('command_')}
COMMANDS['portrait-frames']=command_portrait_frames

def main():
    parser=argparse.ArgumentParser();parser.add_argument('stage',choices=COMMANDS);parser.add_argument('--task-dir',required=True);parser.add_argument('--ffmpeg',default='ffmpeg');parser.add_argument('--ffprobe',default='ffprobe');parser.add_argument('--node',default='node');parser.add_argument('--asr-backend',default='whisper');parser.add_argument('--asr-model-dir',default='')
    args=parser.parse_args();args.task=Path(args.task_dir).resolve();args.state=read(args.task/'workflow.json') if (args.task/'workflow.json').exists() else {};args.inp=read(args.task/'media-input.json') if (args.task/'media-input.json').exists() else {};args.paths=read(args.task/'asset-paths.json') if (args.task/'asset-paths.json').exists() else {}
    result=COMMANDS[args.stage](args);write(args.task/'media-result.json',result)
if __name__=='__main__':main()
