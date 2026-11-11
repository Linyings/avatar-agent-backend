import React from 'react';
import {AbsoluteFill,Audio,Composition,OffthreadVideo,Sequence,registerRoot,staticFile,useCurrentFrame,useVideoConfig} from 'remotion';
import {CameraRig} from './talkcraft/camera';
import {AnimationScene} from './workflow-animation';
import {StylePreview} from './style-preview';
import {SemanticGraphic} from './semantic-graphics';
import {filmTheme,mediaPlacement,overlaps,resolveComposition,semanticItems,shotLayout,captionPage,type Box} from './film-design';
import {stableAvatarCrop,stableAvatarPlan,avatarWindowBox,type AvatarTrack} from './avatar-framing';

type Word={text:string;start:number;end:number};
type Animation={width:number;height:number;fps:number;duration:number;style:string;template:string;text:string;words:Word[]};
type Segment={id:string;text:string;start:number;duration:number;chapter?:string};
type Material={need_id:string;segment_id:string;kind:string;path:string;duration:number;width?:number;height?:number;face_zone?:number[];avatar_framing?:AvatarTrack};
type Shot={id:string;segment_id:string;start:number;end:number;primary:string;overlay?:string;keyword:string;card:string;layout:string;recipe?:string;items?:string[];primary_in?:number;overlay_in?:number;anchor_frame?:number;avatar_position?:string;avatar_shape?:string};
type Scene={text:string;startSec:number;durationSec:number;chars:{ch:string;t:number;e:number}[]};
type Manifest={width:number;height:number;fps:number;duration:number;input:{style:string;ratio:string;title:string;subtitle_color?:string;subtitle_scale?:number;avatar_position?:string};segments:Segment[];materials:Material[];shots:Shot[];timing:{scenes:Scene[]};caption_timing?:{scenes:Scene[]}};
const position=(b:Box)=>({position:'absolute' as const,left:b.x,top:b.y,width:b.w,height:b.h});

function Media({material:m,box,offset,theme,duration,avatarWindow=false,shape='rounded'}:{material:Material;box:Box;offset:number;theme:ReturnType<typeof filmTheme>;duration:number;avatarWindow?:boolean;shape?:string}){
 const {fps}=useVideoConfig();const frame=useCurrentFrame();const placement=mediaPlacement(box,m,m.kind);
 const framed=box.x>0||box.y>0;const video=staticFile(m.path);
 // Every talking-head layer smaller than the frame is face-cropped to fill it.
 const cropShape=avatarWindow?shape:'rectangle';
 const decision=React.useMemo(()=>m.kind==='avatar'&&(avatarWindow||framed)?stableAvatarPlan(box,m.avatar_framing,offset,offset+duration,cropShape):null,[avatarWindow,framed,m.kind,m.avatar_framing,box.w,box.h,offset,duration,cropShape]);
 const crop=decision?stableAvatarCrop(decision,offset+frame/fps):null;
 const cropStyle=crop?{position:'absolute' as const,width:crop.width,height:crop.height,left:crop.left,top:crop.top}:undefined;
 const radius=avatarWindow?(shape==='circle'?'50%':shape==='rectangle'?0:Math.min(box.w,box.h)*.09):framed?theme.radius:0;
 return <div style={{...position(box),overflow:'hidden',borderRadius:radius,background:theme.surface,border:framed?`1px solid ${theme.line}`:undefined}}>
  <CameraRig path={[{t:0,scale:1},{t:duration,scale:m.kind==='real'?1.012:1}]} durationSec={duration}>
   <OffthreadVideo src={video} muted trimBefore={Math.round(offset*fps)} style={cropStyle??{width:'100%',height:'100%',objectFit:placement.fit,objectPosition:placement.position}}/>
  </CameraRig>
 </div>;
}

function FilmShot({shot,manifest:m}:{shot:Shot;manifest:Manifest}){
 const {width,height,fps}=useVideoConfig();const local=useCurrentFrame()/fps;
 const segment=m.segments.find(s=>s.id===shot.segment_id)!;
 const kindOf=(id:string)=>m.materials.find(s=>s.need_id===id)?.kind;
 const comp=resolveComposition(shot,kindOf,m.input.style);
 const primary=m.materials.find(s=>s.need_id===comp.primary)!;const overlay=comp.overlay?m.materials.find(s=>s.need_id===comp.overlay):undefined;
 const recipe=comp.recipe;
 const side=shot.avatar_position||m.input.avatar_position||'right';
 const layout=shotLayout(width,height,recipe,side),theme=filmTheme(m.input.style);
 if(recipe==='visual-pip'&&overlay?.kind==='avatar'&&layout.overlay){layout.primary={x:0,y:0,w:width,h:height};layout.overlay=avatarWindowBox(width,height,shot.avatar_shape||'rounded',side);}
 const offset=shot.start-segment.start,duration=shot.end-shot.start;
 const panelTemplate=recipe==='compare'?'compare':recipe==='steps'?'steps':recipe==='summary'?'summary':recipe==='evidence'?'data':'highlight';
 const scene=m.timing.scenes.find(s=>s.startSec<=shot.start+.1&&s.startSec+s.durationSec>=shot.start);
 const words=scene?.chars.map(c=>({text:c.ch,start:c.t-shot.start,end:c.e-shot.start}))||[];
 const title=shot.keyword||segment.chapter||'';
 const panel=layout.panel&&primary.kind!=='animation'?layout.panel:undefined;
 const primaryOffset=comp.primaryIn??(primary.kind==='avatar'?offset:0);
 const face=primary.kind==='avatar'?mediaPlacement(layout.primary,primary,'avatar').face:undefined;
 const keywordBox={x:width*.055,y:height*.06,w:width*.42,h:height*.15};
 const showKeyword=!panel&&primary.kind==='avatar'&&['keyword','impact-open-title','highlighter-sweep'].includes(shot.card)&&title&&(!face||!overlaps(keywordBox,face));
 return <AbsoluteFill style={{background:theme.canvas,color:theme.ink}}>
  {theme.paper&&<AbsoluteFill style={{backgroundImage:`radial-gradient(${theme.line} 0.6px, transparent 0.6px)`,backgroundSize:'8px 8px',opacity:.22}}/>}
  <Media material={primary} box={layout.primary} offset={primaryOffset} theme={theme} duration={duration}/>
  {overlay&&layout.overlay&&<Media material={overlay} box={layout.overlay} offset={comp.overlayIn??(overlay.kind==='avatar'?offset:0)} theme={theme} duration={duration} avatarWindow={overlay.kind==='avatar'} shape={recipe==='visual-split'?'rounded':shot.avatar_shape||'rounded'}/>}
  {overlay&&panel&&<Media material={overlay} box={panel} offset={comp.overlayIn??(overlay.kind==='avatar'?offset:0)} theme={theme} duration={duration} avatarWindow={overlay.kind==='avatar'} shape={shot.avatar_shape||'rounded'}/>}
  {panel&&!overlay&&<div style={position(panel)}><SemanticGraphic width={panel.w} height={panel.h} style={m.input.style} template={panelTemplate} text={segment.text} title={title} items={shot.items||semanticItems(segment.text)} time={local} duration={duration} words={words} chrome={false}/></div>}
  {showKeyword&&<div style={{...position(keywordBox),height:'auto',maxHeight:keywordBox.h,background:theme.surface,borderLeft:`5px solid ${theme.accent}`,padding:'12px 18px',boxSizing:'border-box',borderRadius:theme.radius,fontSize:Math.min(width,height)*.048,fontWeight:800,opacity:Math.min(1,Math.max(0,(local-(shot.anchor_frame||0)/fps)*5)),overflow:'hidden',overflowWrap:'anywhere'}}>{title}</div>}
 </AbsoluteFill>;
}
export function WorkflowMovie({manifest:m}:{manifest:Manifest}){
 const {fps,width,height}=useVideoConfig();const time=useCurrentFrame()/fps,theme=filmTheme(m.input.style);
 const scene=(m.caption_timing||m.timing).scenes.find(s=>time>=s.startSec&&time<s.startSec+s.durationSec);
 const caption=shotLayout(width,height,'host-hero').caption;
 const fontSize=Math.min(width,height)*.045*(m.input.subtitle_scale||1);
 const page=captionPage(scene?.chars||[],time,caption.w/fontSize*1.35);
 return <AbsoluteFill style={{fontFamily:'"Noto Sans CJK SC",sans-serif',background:theme.canvas}}>
  {m.shots.map((shot,i)=>{const from=Math.round(shot.start*fps);const end=i===m.shots.length-1?Math.ceil(m.duration*fps):Math.round(shot.end*fps);return <Sequence key={shot.id} from={from} durationInFrames={Math.max(1,end-from)}><FilmShot shot={shot} manifest={m}/></Sequence>})}
  {scene&&<div style={{...position(caption),display:'flex',alignItems:'center',justifyContent:'center',textAlign:'center',fontSize,lineHeight:1.35,fontWeight:650,color:'#f8fafc'}}><span style={{background:'rgba(0,0,0,.74)',borderRadius:8,padding:'4px 10px',boxDecorationBreak:'clone'}}>{page.map((c,i)=><span key={i} style={{color:time>=c.t?(m.input.subtitle_color||theme.accent):'#f8fafc'}}>{c.ch}</span>)}</span></div>}
  <Audio src={staticFile('voice.wav')}/>
 </AbsoluteFill>;
}
const empty:Manifest={width:720,height:1280,fps:25,duration:1,input:{style:'flat-vector',ratio:'9:16',title:''},segments:[],materials:[],shots:[],timing:{scenes:[]}};
const animation:Animation={width:720,height:1280,fps:25,duration:3,style:'flat-vector',template:'steps',text:'确定主题，生成声音，准备素材，组合成片',words:[]};
registerRoot(()=><><Composition id="AvatarWorkflow" component={WorkflowMovie} defaultProps={{manifest:empty}} width={720} height={1280} fps={25} durationInFrames={25} calculateMetadata={({props})=>({width:props.manifest.width,height:props.manifest.height,fps:25,durationInFrames:Math.ceil(props.manifest.duration*25)})}/><Composition id="AnimationTemplate" component={AnimationScene} defaultProps={{animation}} width={720} height={1280} fps={25} durationInFrames={75} calculateMetadata={({props})=>({width:props.animation.width,height:props.animation.height,fps:25,durationInFrames:Math.ceil(props.animation.duration*25)})}/><Composition id="StylePreview" component={StylePreview} defaultProps={{style:'mg-flat-vector'}} width={640} height={360} fps={25} durationInFrames={125}/></>);
