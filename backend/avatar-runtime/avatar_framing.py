"""Local main-person tracking for postproduction, independent of generation."""
import json
import math
from pathlib import Path

VERSION='avatar-framing/1'
DETECTOR_VERSION='primary-face/2'

def primary_candidates(faces,width,height):
    valid=[f for f in faces if len(f)>=15 and all(math.isfinite(float(v)) for v in f) and float(f[14])>=.6 and f[2]>=width*.025 and f[3]>=height*.025 and f[0]>=-f[2]*.2 and f[1]>=-f[3]*.2 and f[0]+f[2]<=width+f[2]*.2 and f[1]+f[3]<=height+f[3]*.2]
    # YuNet may return two overlapping hypotheses for one close-up face.
    # Suppress these before comparing distinct people's area or confidence.
    kept=[]
    for f in sorted(valid,key=lambda f:float(f[14]),reverse=True):
        duplicate=False
        for other in kept:
            intersection=max(0,min(f[0]+f[2],other[0]+other[2])-max(f[0],other[0]))*max(0,min(f[1]+f[3],other[1]+other[3])-max(f[1],other[1]))
            if intersection/min(f[2]*f[3],other[2]*other[3])>.45:duplicate=True;break
        if not duplicate:kept.append(f)
    return kept

def select_primary_face(faces,width,height):
    valid=primary_candidates(faces,width,height)
    if not valid:return None,False
    # Confidence is not identity: a tiny inset can score higher than the host.
    valid.sort(key=lambda f:float(f[2]*f[3]),reverse=True)
    main=valid[0]
    ambiguous=len(valid)>1 and valid[1][2]*valid[1][3]>main[2]*main[3]*.7
    return main,ambiguous

def summarize_track(samples,width,height,duration,total,step=.5):
    warnings=[];rows=[]
    for sample in samples:
        row=dict(sample);row['face']=list(sample['face']);row['cut']=bool(sample.get('cut'))
        if rows:
            previous=rows[-1]['face'];current=row['face']
            distance=math.hypot((previous[0]+previous[2]/2-current[0]-current[2]/2)/width,(previous[1]+previous[3]/2-current[1]-current[3]/2)/height)
            if distance>.3 or row['t']-rows[-1]['t']>step*2.5:row['cut']=True
        rows.append(row)
    rate=len(rows)/max(1,total)
    if rate<.6:warnings.append('uncertain_face')
    if rows and max(r['face'][3] for r in rows)>min(r['face'][3] for r in rows)*1.3:warnings.append('source_zoom')
    if any(r['cut'] for r in rows):warnings.append('source_reframing')
    safe=[]
    if rows:
        left=max(0,min(r['face'][0]-.2*r['face'][2] for r in rows));right=min(width,max(r['face'][0]+1.2*r['face'][2] for r in rows))
        top=max(0,min(r['face'][1]-.55*r['face'][3] for r in rows));bottom=min(height,max(r['face'][1]+1.1*r['face'][3] for r in rows))
        safe=[round(left,2),round(top,2),round(right-left,2),round(bottom-top,2)]
    return {'version':VERSION,'status':'tracked' if rows and rate>=.6 else 'uncertain','width':width,'height':height,'duration':duration,'step':step,'detect_rate':round(rate,3),'samples':rows,'safe_zone':safe,'warnings':warnings}

def analyze_avatar(source,cache,breaks=()):
    """Use cached local YuNet; never modify source footage or download a model."""
    import cv2
    source=Path(source);cache=Path(cache)
    model=Path.home()/'.cache/koubo/face_detection_yunet_2023mar.onnx'
    stat=source.stat();identity={'version':VERSION,'detector':DETECTOR_VERSION,'size':stat.st_size,'mtime_ns':stat.st_mtime_ns,'breaks':list(breaks),'model_size':model.stat().st_size if model.exists() else 0}
    if cache.exists():
        try:
            saved=json.loads(cache.read_text())
            if saved.get('source')==identity and saved.get('status')=='tracked':return saved
        except (ValueError,OSError):pass
    capture=cv2.VideoCapture(str(source))
    if not capture.isOpened():raise ValueError('人物素材无法读取')
    try:
        width=int(capture.get(cv2.CAP_PROP_FRAME_WIDTH));height=int(capture.get(cv2.CAP_PROP_FRAME_HEIGHT));fps=capture.get(cv2.CAP_PROP_FPS) or 25
        duration=capture.get(cv2.CAP_PROP_FRAME_COUNT)/fps
        if not model.exists():raise ValueError('本地人脸模型未就绪')
        detector=cv2.FaceDetectorYN.create(str(model),'',(width,height),.6,.3,5000)
        step=.5;times={round(min(i*step,max(0,duration-1/fps)),4) for i in range(math.ceil(duration/step)+1)}
        for boundary in breaks:
            for delta in (-1/fps,0):
                if 0<=boundary+delta<duration:times.add(round(boundary+delta,4))
        rows=[];ambiguous=0;multiple=0
        for second in sorted(times):
            capture.set(cv2.CAP_PROP_POS_MSEC,second*1000);ok,frame=capture.read()
            if not ok:continue
            _,faces=detector.detect(frame)
            if faces is None:continue
            main,uncertain=select_primary_face(faces,width,height)
            if len(primary_candidates(faces,width,height))>1:multiple+=1
            if uncertain:ambiguous+=1;continue
            if main is None:continue
            rows.append({'t':second,'face':[round(float(v),3) for v in main[:4]],'score':round(float(main[14]),3),'cut':any(abs(second-b)<1/fps/2 for b in breaks)})
        report=summarize_track(rows,width,height,duration,len(times),step)
        if multiple:report['warnings'].append('multiple_faces')
        if ambiguous:report['warnings'].append('ambiguous_person')
        report['source']=identity
        cache.parent.mkdir(parents=True,exist_ok=True)
        temp=cache.with_suffix('.tmp');temp.write_text(json.dumps(report,ensure_ascii=False,indent=2));temp.replace(cache)
        return report
    finally:capture.release()
