import React from 'react';
import {filmTheme,semanticItems} from './film-design';

export type GraphicProps={width:number;height:number;style:string;template:string;text:string;title?:string;items?:string[];time:number;duration:number;words?:{text:string;start:number;end:number}[];chrome?:boolean};
// Trusted, deterministic components. No model-generated code is executed.
// Content comes from the actual narration; data mode never invents chart values.
export function SemanticGraphic(a:GraphicProps){
 const theme=filmTheme(a.style),wide=a.width>a.height*1.2,W=1000,H=a.height/a.width*W;
 const items=(a.items?.length?a.items:semanticItems(a.text)).slice(0,a.template==='compare'?2:4);
 const title=a.title||'';const top=title?Math.min(H*.24,140):H*.07;
 const usable=H-top-H*.09;const size=Math.max(25,Math.min(47,wide?40:44));
 const timing=(index:number)=>{
  const prefix=items[index]?.slice(0,3);const words=a.words||[];const at=words.findIndex((_,i)=>words.slice(i,i+4).map(w=>w.text).join('').startsWith(prefix));
  return at>=0?words[at].start:index*Math.min(1.3,a.duration/Math.max(2,items.length+1));
 };
 const progress=(i:number)=>Math.max(0,Math.min(1,(a.time-timing(i))/theme.motion));
 const label=(text:string,x:number,y:number,w:number,h:number,fontSize:number,color=theme.ink)=>{
  let f=fontSize;while(f>16&&Math.ceil([...text].length*f/w)*f*1.4>h)f--;
  return <foreignObject x={x} y={y} width={w} height={h}><div style={{fontSize:f,color,fontWeight:650,lineHeight:1.4,overflowWrap:'anywhere'}}>{text}</div></foreignObject>;
 };
 const node=(s:string,i:number,x:number,y:number,w:number,h:number)=>{
  const p=progress(i),data=a.template==='data',match=data?s.match(/(?:\d+(?:\.\d+)?\s*(?:[%％万亿倍元秒个年])?|百分之[一二三四五六七八九十百点]+)/):null;
  const line=a.style.includes('line-art'),morph=a.style==='mg-morph',small=w<300;
  const number=a.template==='summary'?'✓':String(i+1).padStart(2,'0');
  const tx=small?x+20:x+90,ty=small?y+72:y+24,tw=small?w-40:w-112,th=small?h-90:h-42;
  return <g key={i} opacity={p} transform={`translate(${x+w/2},${y+h/2+(1-p)*18}) scale(${morph?.95+.05*p:1}) translate(${-x-w/2},${-y-h/2})`}>
   <rect x={x} y={y} width={w} height={h} rx={morph?12+theme.radius*p:theme.radius} fill={theme.surface} stroke={line?theme.accent:theme.line} strokeWidth={line?2.5:2} pathLength={1} strokeDasharray={line?`${p} 1`:undefined}/>
   {a.style==='mg-bauhaus'&&<><circle cx={x+w-20} cy={y+20} r={14} fill={theme.accent}/><path d={`M${x+12} ${y+h-12}h34v-34z`} fill={theme.line}/></>}
   {theme.paper&&<path d={`M${x+12} ${y+8}h${w-24}`} stroke={theme.line} strokeDasharray="6 8"/>}
   {match? <>{label(match[0],x+24,y+18,w-48,h*.39,70,theme.accent)}{label(s,x+24,y+h*.49,w-48,h*.43,size*.8)}</>:
    <><circle cx={x+42} cy={y+42} r={24} fill={theme.canvas} stroke={theme.line}/>{label(number,x+23,y+25,52,44,24,theme.accent)}{label(s,tx,ty,tw,th,size)}</>}
  </g>;
 };
 const horizontal=wide&&['compare','steps'].includes(a.template)&&items.length<=3;
 const relation=a.template==='relationship'&&items.length>1;
 const cols=horizontal?items.length:relation?2:1,rows=Math.ceil(items.length/cols),gap=24;
 const bw=(900-gap*(cols-1))/cols,bh=Math.max(55,(usable-gap*(rows-1))/Math.max(1,rows));
 return <div style={{width:a.width,height:a.height,background:a.chrome===false?'transparent':theme.canvas,color:theme.ink,fontFamily:'"Noto Sans CJK SC",sans-serif'}}>
  <svg width={a.width} height={a.height} viewBox={`0 0 ${W} ${H}`}>
   {theme.paper&&<rect width={W} height={H} fill="url(#paper-grain)" opacity={.35}/>}
   <defs><pattern id="paper-grain" width="12" height="12" patternUnits="userSpaceOnUse"><circle cx="2" cy="5" r=".7" fill={theme.line}/><circle cx="9" cy="10" r=".5" fill={theme.line}/></pattern></defs>
   {title&&label(title,50,H*.045,900,top-H*.06,Math.min(51,H*.11))}
   {a.template==='relationship'&&<path d={`M${50+bw} ${top+bh/2}H${50+bw+gap} M${50+bw/2} ${top+bh}V${top+bh+gap}`} stroke={theme.accent} strokeWidth={4} opacity={progress(1)}/>}
   {items.map((s,i)=>node(s,i,50+(i%cols)*(bw+gap),top+Math.floor(i/cols)*(bh+gap),bw,bh))}
   {(a.template==='steps'||relation)&&items.slice(1).map((_,i)=>{
    const x=horizontal?50+(i+1)*(bw+gap)-gap/2:500,y=horizontal?top+bh/2:top+(i+1)*(bh+gap)-gap/2;
    return <path key={i} d={horizontal?`M${x-8} ${y-6}l8 6 -8 6`:`M${x-6} ${y-8}l6 8 6 -8`} stroke={theme.accent} strokeWidth={3} fill="none" opacity={progress(i+1)}/>;
   })}
  </svg>
 </div>;
}
