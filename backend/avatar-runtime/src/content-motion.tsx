import React from 'react';
import {MotionObjects} from './motion-objects';

export type MotionElement={label:string;kind:string;at?:number};
export type MotionScene={quote:string;title:string;layout:string;elements:MotionElement[];start:number;end:number};
export type MotionProfile={treatment:string;motion:string;palette:string[];features:string};
export type ContentMotion={version:string;style:string;profile:MotionProfile;tone:string;accent:string;scenes:MotionScene[];width:number;height:number;duration:number};
const clamp=(n:number)=>Math.max(0,Math.min(1,n));
function ease(t:number,motion:string){const p=clamp(t);return motion==='spring'?1-Math.cos(p*Math.PI*2.5)*Math.exp(-6*p)*(1-p):motion==='snap'?Math.round(p*4)/4:1-Math.pow(1-p,3);}

// Symbols are authored locally. The model chooses their meaning and layout,
// never executable markup or an external image/video.
function Symbol({kind,color,fill,draw=1,pixel=false}:{kind:string;color:string;fill:string;draw?:number;pixel?:boolean}){
 const paths:Record<string,React.ReactNode>={
  coin:<><ellipse cx="50" cy="67" rx="31" ry="14"/><path d="M19 52v15m62-15v15"/><ellipse cx="50" cy="52" rx="31" ry="14"/><path d="M19 37v15m62-15v15"/><ellipse cx="50" cy="37" rx="31" ry="14"/><path d="M43 33h14m-14 8h14m-7-13v18"/></>,
  chart:<><path d="M18 20v62h66"/>{[29,49,69].map(x=><rect key={x} x={x} y="32" width="12" height="49"/>)}</>,
  chip:<><rect x="25" y="25" width="50" height="50" rx="8"/><rect x="38" y="38" width="24" height="24" rx="3"/>{[32,50,68].map(v=><path key={v} d={`M${v} 14v11m0 50v11M14 ${v}h11m50 0h11`}/>)}</>,
  cloud:<><path d="M26 68c-24-5-16-36 5-34C35 10 68 12 72 35c25 2 26 31 4 33Z"/><path d="M50 48v35m-12-12 12 12 12-12"/></>,
  people:<><circle cx="50" cy="30" r="14"/><circle cx="23" cy="40" r="10"/><circle cx="77" cy="40" r="10"/><path d="M29 81V66c0-25 42-25 42 0v15ZM8 78V65c0-12 10-18 19-14m65 27V65c0-12-10-18-19-14"/></>,
  document:<><path d="M25 14h35l17 18v54H25Zm35 0v18h17"/><path d="M36 46h29M36 58h29M36 70h19"/></>,
  shield:<><path d="M50 12 82 26v24c0 21-17 33-32 40-15-7-32-19-32-40V26Z"/><path d="m32 50 12 12 25-27"/></>,
  globe:<><circle cx="50" cy="50" r="35"/><ellipse cx="50" cy="50" rx="16" ry="35"/><path d="M15 50h70M21 30h58M21 70h58"/></>,
  clock:<><circle cx="50" cy="50" r="34"/><path d="M50 26v25l19 12M50 16v5m34 29h-5M50 84v-5M16 50h5"/></>,
  code:<><rect x="13" y="21" width="74" height="60" rx="7"/><path d="M13 34h74m-54 12-11 12 11 12m34-24 11 12-11 12m-13-27-8 30"/></>,
  spark:<><path d="m50 12 11 26 27 12-27 11-11 27-12-27-26-11 26-12Z"/><path d="m79 12 3 7 7 3-7 3-3 7-3-7-7-3 7-3"/></>,
  box:<><path d="m50 12 36 19v39L50 89 14 70V31Zm0 38L14 31m36 19 36-19M50 50v39M32 22l36 19v17"/></>,
  leaf:<><path d="M20 78C-2 30 44 16 83 17 84 58 65 90 20 78Zm0 0 45-43M36 62l-7-20m22 5 21 3"/></>,
  link:<><path d="m38 64-7 7c-19 19-42-5-23-23l20-20c14-14 34 0 30 12M62 36l7-7c19-19 42 5 23 23L72 72c-14 14-34 0-30-12M33 66l34-33"/></>,
 };
 return <svg viewBox="0 0 100 100" width="100%" height="100%" fill={fill} stroke={color} strokeWidth={pixel?5:3} strokeLinecap={pixel?'square':'round'} strokeLinejoin={pixel?'miter':'round'} style={{overflow:'visible'}}><g pathLength={1} opacity={clamp(draw*3)}>{paths[kind]||paths.box}</g></svg>;
}
type Slot={x:number;y:number;w:number;h:number};
function slots(scene:MotionScene,W:number,H:number):Slot[]{
 const n=scene.elements.length,top=H*.27,bottom=H*.88,space=bottom-top,wide=W>H;
 if(scene.layout==='relationship'&&n>1){
  const boxW=W*(wide?.26:.38),boxH=space*.40,childW=Math.min(boxW,W*.86/(n-1)-24);
  return scene.elements.map((_,i)=>i===0?{x:W*.5-boxW/2,y:top+space*.02,w:boxW,h:boxH}:{x:W*(.07+(i-.5)*.86/(n-1))-childW/2,y:top+space*.57,w:childW,h:boxH});
 }
 if(scene.layout==='data'||scene.layout==='highlight'){
  if(n===1)return [{x:W*.1,y:top,w:W*.8,h:space}];
  return scene.elements.map((_,i)=>i===0?{x:W*.08,y:top,w:W*.84,h:space*.53}:{x:W*.06+(i-1)*W*.88/(n-1),y:top+space*.59,w:W*.88/(n-1)-W*.02,h:space*.38});
 }
 const cols=scene.layout==='compare'?Math.min(2,n):wide?n:1,rows=Math.ceil(n/cols),gap=Math.min(22,space*.04);
 return scene.elements.map((_,i)=>({x:W*.07+(i%cols)*(W*.86+gap)/cols,y:top+Math.floor(i/cols)*(space+gap)/rows,w:(W*.86-gap*(cols-1))/cols,h:(space-gap*(rows-1))/rows}));
}
export function ContentMotionScene({motion:m,time}:{motion:ContentMotion;time:number}){
 const scene=[...m.scenes].reverse().find(s=>time>=s.start)||m.scenes[0];
 const W=1000,H=m.height/m.width*W,wide=W>H,t=m.profile.treatment;
 const [bg,paper,ink,accent,second]=m.profile.palette;
 const color=m.accent;
 const stageTime=['sketch','collage','pixel'].includes(t)?Math.floor(time*12)/12:time;
 const elapsed=stageTime-scene.start;
 const boxes=slots(scene,W,H),line=t==='stroke'||t==='hud',paperStyle=['sketch','paper','collage'].includes(t),depth=['isometric','sculpted'].includes(t);
 const glow=['hud','neon'].includes(t),radius=['geometric','pixel','collage'].includes(t)?0:t==='morph'?30+Math.sin(time*1.2)*15:28;
 const unique='motion-'+m.style;
 const text=(copy:string,box:Slot,size:number,bold=true,fill=ink)=>{
  let font=size;while(font>18&&Math.ceil([...copy].length*font/box.w)*font*1.3>box.h)font--;
  return <foreignObject {...{x:box.x,y:box.y,width:box.w,height:box.h}}><div style={{height:'100%',display:'flex',alignItems:'center',justifyContent:'center',fontSize:font,fontWeight:bold?800:500,lineHeight:1.3,textAlign:'center',overflowWrap:'anywhere',color:fill,textShadow:t==='comic'?`2px 3px 0 ${paper},-2px -2px 0 ${paper},4px 6px 0 ${second}`:undefined}}>{copy}</div></foreignObject>;
 };
 const objects=boxes.map((b,i)=>{const compact=b.h<b.w*.52,hero=i===0&&['data','highlight'].includes(scene.layout),size=Math.min(b.w*.46,b.h*(compact?.65:hero?.48:.46));return {kind:scene.elements[i].kind,x:compact?b.x+b.w*.06:b.x+b.w/2-size/2,y:compact?b.y+(b.h-size)/2:b.y+b.h*.06,size,progress:ease((stageTime-(scene.elements[i].at??scene.start+i*.18))/.65,m.profile.motion)}});
 return <div style={{position:'relative',width:m.width,height:m.height,background:bg,fontFamily:'"Noto Sans CJK SC",sans-serif'}}>
  {depth&&<MotionObjects width={m.width} height={m.height} viewHeight={H} objects={objects} color={color} secondary={second} paper={paper} time={time} isometric={t==='isometric'}/>}
  <svg width={m.width} height={m.height} viewBox={`0 0 ${W} ${H}`}>
   <defs>
    <radialGradient id={unique+'-volume'} cx="26%" cy="20%" r="85%"><stop stopColor={paper}/><stop offset=".35" stopColor={color}/><stop offset="1" stopColor={second}/></radialGradient>
    <linearGradient id={unique+'-sheen'} x2=".7" y2="1"><stop stopColor={paper} stopOpacity=".8"/><stop offset="1" stopColor={color} stopOpacity=".22"/></linearGradient>
    <pattern id={unique+'-dots'} width={t==='pixel'?20:12} height={t==='pixel'?20:12} patternUnits="userSpaceOnUse"><circle cx="2" cy="2" r={paperStyle?1:.6} fill={ink} opacity=".16"/></pattern>
    <pattern id={unique+'-grid'} width="50" height="50" patternUnits="userSpaceOnUse"><path d="M50 0H0V50" fill="none" stroke={color} strokeOpacity=".15"/></pattern>
    <filter id={unique+'-blur'}><feGaussianBlur stdDeviation="45"/></filter>
    <filter id={unique+'-goo'}><feGaussianBlur stdDeviation="13"/><feColorMatrix values="1 0 0 0 0 0 1 0 0 0 0 0 1 0 0 0 0 0 22 -9"/></filter>
   </defs>
   {(paperStyle||t==='pixel')&&<rect width={W} height={H} fill={`url(#${unique}-dots)`}/>}
   {(glow||t==='isometric'||t==='geometric')&&<rect width={W} height={H} fill={`url(#${unique}-grid)`}/>}
   {t==='glass'&&<g filter={`url(#${unique}-blur)`}>{[color,second,accent].map((c,i)=><ellipse key={i} cx={W*(.2+i*.3)+Math.sin(time*.25+i)*W*.1} cy={H*(.25+i*.24)} rx={W*.25} ry={H*.21} fill={c} opacity=".35"/>)}</g>}
   {t==='liquid'&&<g filter={`url(#${unique}-goo)`} fill={color} opacity=".22">{Array.from({length:5},(_,i)=><ellipse key={i} cx={W*(.15+i*.18)+Math.sin(time*.8+i)*45} cy={H*(.22+i*.14)+Math.cos(time*.55+i)*50} rx={95} ry={100}/>)}</g>}
   {t==='neon'&&<g stroke={second} opacity=".2">{Array.from({length:11},(_,i)=><path key={i} d={`M500 ${H*.6}L${i*180-400} ${H}`}/>)}{Array.from({length:8},(_,i)=><path key={i} d={`M0 ${H*.6+H*.4*(i/7)**2}h1000`}/>)}</g>}
   {t==='geometric'&&<g opacity=".45"><circle cx="92" cy={H*.17} r="36" fill={second}/><rect x="852" y={H*.125} width="66" height="66" fill={accent} transform={`rotate(${Math.floor(time*2)*90} 885 ${H*.125+33})`}/></g>}
   <g opacity={.65}><path d={`M70 ${H*.065}h55`} stroke={color} strokeWidth="6"/><path d={`M135 ${H*.065}h${790*clamp(time/m.duration)}`} stroke={color} strokeWidth="2"/></g>
   {text(scene.title,{x:W*.09,y:H*.095,w:W*.82,h:H*.135},wide?55:64)}
   {scene.layout==='relationship'&&boxes.slice(1).map((b,i)=><path key={i} d={`M${boxes[0].x+boxes[0].w/2} ${boxes[0].y+boxes[0].h*.8}Q500 ${b.y-25} ${b.x+b.w/2} ${b.y+b.h*.15}`} fill="none" stroke={color} strokeWidth={line?2:4} pathLength={1} strokeDasharray={`${clamp(elapsed/.8)} 1`} opacity=".65"/>)}
   {boxes.map((box,i)=>{
    const item=scene.elements[i],at=item.at??scene.start+i*.18,p=ease((stageTime-at)/.65,m.profile.motion);
    const compact=box.h<box.w*.52,hero=i===0&&['data','highlight'].includes(scene.layout),center=box.x+box.w/2;
    const iconSize=Math.min(box.w*.46,box.h*(compact?.65:hero?.48:.46));
    const iconX=compact?box.x+box.w*.06:center-iconSize/2,iconY=compact?box.y+(box.h-iconSize)/2:box.y+box.h*.06;
    const labelBox=compact?{x:box.x+box.w*.31,y:box.y+box.h*.12,w:box.w*.62,h:box.h*.76}:{x:box.x+box.w*.06,y:iconY+iconSize+box.h*.05,w:box.w*.88,h:box.h-(iconY-box.y)-iconSize-box.h*.10};
    const angle=paperStyle?Math.sin(i*4+Math.floor(time*12)*.1)*(t==='sketch'?.5:1.2):0;
    const offset=(1-p)*(t==='isometric'?45:24),scale=t==='comic'?.82+.18*p:t==='morph'?.85+.15*p:1;
    return <g key={i} opacity={clamp(p*1.8)} transform={`translate(${center},${box.y+box.h/2+offset}) rotate(${angle}) scale(${scale}) translate(${-center},${-box.y-box.h/2})`}>
     {!['highlight','data','relationship'].includes(scene.layout)&&<rect x={box.x} y={box.y} width={box.w} height={box.h} rx={radius} fill={t==='glass'?`url(#${unique}-sheen)`:line?'none':paper} stroke={line?color:t==='paper'||t==='comic'?'white':color} strokeOpacity={line?1:.25} strokeWidth={t==='paper'?8:line?2:1.5}/>}
     {t==='collage'&&<rect x={center-34} y={box.y-8} width="68" height="18" fill={second} opacity=".4" transform={`rotate(-8 ${center} ${box.y})`}/>}
     {t==='hud'&&<path d={`M${box.x+15} ${box.y+35}v-20h24m${box.w-54} 0h24v20m0 ${box.h-70}v20h-24m${54-box.w} 0h-24v-20`} stroke={color} fill="none" strokeWidth="3"/>}
     {!depth&&<g transform={`translate(${iconX},${iconY})`}>
      {t==='comic'&&<path d="M50 0 65 18 90 8 80 38 100 50 81 65 92 92 61 80 50 100 37 81 10 90 20 60 0 50 18 35 9 9 35 18Z" fill={second} opacity=".25" transform={`scale(${iconSize/100})`}/>}
      <foreignObject x={t==='sculpted'?iconSize*.16:0} y={t==='sculpted'?iconSize*.12:0} width={iconSize*(t==='sculpted'?.68:1)} height={iconSize*(t==='sculpted'?.68:1)} style={{filter:glow?`drop-shadow(0 0 7px ${color})`:undefined}}><Symbol kind={item.kind} color={t==='sculpted'?paper:color} fill={line?'none':color+'14'} draw={p} pixel={t==='pixel'}/></foreignObject>
     </g>}
     {text(item.label,labelBox,hero&&/[0-9]/.test(item.label)?(wide?72:86):compact?36:Math.min(46,box.w*.10),true,ink)}
     {scene.layout==='steps'&&<circle cx={box.x+17} cy={box.y+17} r="6" fill={i===Math.min(scene.elements.length-1,Math.floor(elapsed/.8))?color:second}/>}
    </g>;
   })}
   <g opacity=".6">{m.scenes.map((_,i)=><rect key={i} x={W*.5-m.scenes.length*13+i*26} y={H*.94} width={i===m.scenes.indexOf(scene)?18:7} height="5" rx="2" fill={i===m.scenes.indexOf(scene)?color:second}/>)}</g>
  </svg>
 </div>;
}
