"""Measured diagnostics and explicit narration windows; no paid model calls."""
import math
import re
import statistics
import subprocess


def motion_window(needs, need, total):
    start=need.get('narration_start')
    if start is None:
        start=0.0
        for other in needs:
            if other.get('id')==need.get('id'):break
            if other.get('kind')=='animation' and other.get('segment_id')==need.get('segment_id'):
                start+=other['duration']
    end=start+need['duration']
    if not all(math.isfinite(x) for x in (start,end,total)) or start<0 or end>total+.045 or end<=start:
        raise ValueError('动效台词窗口超出配音范围，请检查本段素材拆分')
    return {'start':start,'end':end}


def audio_metrics(path, words, ffmpeg='ffmpeg'):
    import numpy as np
    data=subprocess.run([ffmpeg,'-v','error','-i',str(path),'-vn','-ac','1','-ar','24000','-f','f32le','-'],capture_output=True,check=True).stdout
    pcm=np.frombuffer(data,dtype='<f4')
    if not len(pcm):raise ValueError('配音没有可测采样')
    blocks=len(pcm)//480
    rms=np.sqrt(np.mean(pcm[:blocks*480].reshape(blocks,480)**2,axis=1)) if blocks else np.array([])
    active=rms>.01; pauses=[];begin=None
    for i,on in enumerate(active):
        if not on and begin is None:begin=i
        if on and begin is not None:
            if i-begin>=25:pauses.append({'start':round(begin*.02,2),'duration':round((i-begin)*.02,2)})
            begin=None
    if begin is not None and blocks-begin>=25:pauses.append({'start':round(begin*.02,2),'duration':round((blocks-begin)*.02,2)})
    chinese=[w for w in words if re.fullmatch(r'[\u4e00-\u9fff]+',w.get('text',''))]
    chars=sum(len(w['text']) for w in chinese);seconds=sum(max(0,w['end']-w['start']) for w in chinese)
    return {'method':'20ms RMS; Chinese aligned character intervals exclude English and numerals; not a listening or lip-sync score','chinese_chars':chars,'chinese_chars_per_second':round(chars/seconds,3) if seconds and chars>=10 else None,'rms_db':round(float(20*np.log10(np.sqrt(np.mean(pcm*pcm))+1e-10)),2),'long_pauses':pauses,'duration':len(pcm)/24000}


def quality_issues(state, audio):
    issues=[]
    def add(code,message,time,**extra):issues.append({'code':code,'message':message,'time':round(time,3),'severity':'review',**extra})
    for shot in state.get('shots',[]):
        if shot['end']-shot['start']<1:
            add('short_shot','镜头不足 1 秒，请检查是否造成闪切',shot['start'],shot_id=shot['id'])
    rates=[a['chinese_chars_per_second'] for a in audio if a.get('chinese_chars_per_second')]
    center=statistics.median(rates) if rates else None
    levels=[a['rms_db'] for a in audio if a.get('rms_db') is not None]
    for row in audio:
        sid=row['segment_id'];offset=row.get('start',0);rate=row.get('chinese_chars_per_second')
        if center and rate and abs(rate/center-1)>.2:
            add('speech_pace',f'本段中文对齐语速约 {rate:.1f} 字/秒，与全片中位数差异超过 20%，请试听',offset,segment_id=sid)
        if levels and abs(row['rms_db']-statistics.median(levels))>4:
            add('speech_level','本段平均音量与其他段落差异较大，请试听',offset,segment_id=sid)
        for pause in row.get('long_pauses',[]):
            if pause['duration']>=.6:add('speech_pause',f"此处低音量间隔约 {pause['duration']:.2f} 秒，请判断是否为正常停顿",offset+pause['start'],segment_id=sid)
    return issues
