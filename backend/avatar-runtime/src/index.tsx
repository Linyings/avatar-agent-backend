import React from 'react';
import {registerRoot, Composition, AbsoluteFill, Audio, OffthreadVideo, Sequence, staticFile, useCurrentFrame, useVideoConfig, interpolate} from 'remotion';
import ImpactOpenTitle from './talkcraft/impact-open-title';
import HighlighterSweep from './talkcraft/highlighter-sweep';
import ChapterTitleCard from './talkcraft/chapter-title-card';
import {CameraRig} from './talkcraft/camera';
import {hostLayout} from './host-layout.mjs';

type Char = {ch: string; t: number; e: number};
type Scene = {text: string; chars: Char[]; startSec: number; durationSec: number};
type Clip = {start: number; end: number; source_start: number; duration: number; fps: number; width: number; height: number; face_zone: number[]};
type Shot = {id: string; text: string; keyword: string; card: string; layout: string; source: string; clip: Clip};
type Manifest = {fps: number; width: number; height: number; duration: number; title: string; shots: Shot[]; timing: {scenes: Scene[]}};

const empty: Manifest = {fps: 25, width: 720, height: 1280, duration: 1, title: '', shots: [], timing: {scenes: []}};

function Subtitle({scene}: {scene: Scene}) {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const time = frame / fps;
  const active = scene.chars.findIndex(c => c.t <= time && c.e > time);
  const pageStart = active < 0 ? 0 : Math.floor(active / 14) * 14;
  if (time < scene.startSec || time > scene.startSec + scene.durationSec + .15) return null;
  return <div style={{position: 'absolute', bottom: 80, left: 52, right: 52, textAlign: 'center', fontSize: 32, fontWeight: 700, lineHeight: 1.5, color: 'white', background: 'rgba(12,18,28,.9)', borderRadius: 16, padding: '16px 20px'}}>
    {scene.chars.slice(pageStart, pageStart + 14).map((c,i)=><span key={i} style={{color: c.t <= time ? '#ffd465' : '#fff'}}>{c.ch}</span>)}
  </div>;
}

function AvatarShot({shot, scene, title}: {shot: Shot; scene: Scene; title: string}) {
  const frame = useCurrentFrame();
  const {fps} = useVideoConfig();
  const duration = shot.clip.end - shot.clip.start;
  // Preserve the complete source image. The host occupies the upper viewport;
  // cards and subtitles live below it, outside the measured face safe zone.
  const host = hostLayout(shot.clip, shot.layout);
  const keywordIndex = scene.text.indexOf(shot.keyword);
  const keywordTime = scene.chars[keywordIndex]?.t ?? scene.startSec;
  const localKeyword = keywordTime - shot.clip.start;
  const anchorDelay = shot.card === 'highlighter-sweep' ? .7 : shot.card === 'impact-open-title' ? .7 : .98;
  const anchorFrame = Math.round((localKeyword - anchorDelay) * fps);
  const cardProps = {title: shot.keyword, subtitle: shot.card === 'impact-open-title' ? title || '数字人口播' : shot.card === 'chapter-title-card' ? '本期总结' : '重点解读', anchorFrame};
  const Card = shot.card === 'impact-open-title' ? ImpactOpenTitle : shot.card === 'highlighter-sweep' ? HighlighterSweep : ChapterTitleCard;
  const intro = interpolate(frame, [0, Math.max(1, .16 * fps)], [0, 1], {extrapolateRight: 'clamp'});
  return <AbsoluteFill style={{background:'#111827'}}>
    <div style={{position:'absolute',top:0,left:0,width:720,height:805,overflow:'hidden'}}>
      <CameraRig path={[{t:0, scale:1},{t:duration,scale:host.zoom}]} durationSec={duration}>
        <OffthreadVideo src={staticFile(shot.source)} muted trimBefore={Math.round(shot.clip.source_start * fps)} style={{position:'absolute',left:host.left,width:host.width,height:host.height,objectFit:'contain'}} />
      </CameraRig>
      {shot.layout==='split' && <div style={{position:'absolute',left:36,top:240,width:185,fontSize:38,lineHeight:1.5,color:'#ffd465',fontWeight:700,overflowWrap:'anywhere'}}>{shot.keyword}</div>}
    </div>
    <div style={{position:'absolute',top:805,left:0,width:720,height:300,overflow:'hidden',opacity:intro}}>
      <div style={{width:960,height:400,transform:'scale(.75)',transformOrigin:'top left'}}><Card {...cardProps}/></div>
    </div>
  </AbsoluteFill>;
}

export function AvatarMovie({manifest}: {manifest: Manifest}) {
  const {fps} = useVideoConfig();
  const frame=useCurrentFrame();
  const current=manifest.timing.scenes.find(s=>frame/fps>=s.startSec && frame/fps<=s.startSec+s.durationSec+.15);
  return <AbsoluteFill style={{background:'#111827',fontFamily:'"Microsoft YaHei", "Noto Sans CJK SC", sans-serif'}}>
    {manifest.shots.map((shot,i)=>{
      const from=Math.round(shot.clip.start*fps);
      const end=i===manifest.shots.length-1?Math.ceil(manifest.duration*fps):Math.round(shot.clip.end*fps);
      return <Sequence key={shot.id} from={from} durationInFrames={end-from}>
        <AvatarShot shot={shot} scene={manifest.timing.scenes[i]} title={manifest.title}/>
      </Sequence>;
    })}
    {current && <Subtitle scene={current}/>}
    <Audio src={staticFile('voice.wav')}/>
  </AbsoluteFill>;
}

registerRoot(()=> <Composition id="AvatarMVP" component={AvatarMovie} width={720} height={1280} fps={25} durationInFrames={25} defaultProps={{manifest:empty}}
  calculateMetadata={({props})=>({fps:props.manifest.fps,width:props.manifest.width,height:props.manifest.height,durationInFrames:Math.ceil(props.manifest.duration*props.manifest.fps)})}/>);
