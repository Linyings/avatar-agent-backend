import React from 'react';
import {AbsoluteFill,useCurrentFrame,useVideoConfig} from 'remotion';
import {filmTheme,shotLayout,type Box} from './film-design';
import {SemanticGraphic} from './semantic-graphics';

// Explicitly illustrative presenter: never a fake model result or acceptance clip.
export function StylePreview({style}:{style:string}){
 const {width,height,fps}=useVideoConfig(),time=useCurrentFrame()/fps,theme=filmTheme(style);
 const pip=time>=2.5,layout=shotLayout(width,height,pip?'visual-pip':'host-explain','right');
 const host=pip?layout.overlay!:layout.primary,graphic=pip?layout.primary:layout.panel!;
 const css=(b:Box)=>({position:'absolute' as const,left:b.x,top:b.y,width:b.w,height:b.h});
 return <AbsoluteFill style={{background:theme.canvas,color:theme.ink,fontFamily:'"Noto Sans CJK SC",sans-serif'}}>
  <div style={{...css(host),background:theme.surface,border:`1px solid ${theme.line}`,borderRadius:theme.radius,overflow:'hidden'}}>
   <svg width="100%" height="100%" viewBox="0 0 320 360"><rect width="320" height="360" fill={theme.surface}/><circle cx="160" cy="107" r="52" fill={theme.muted}/><path d="M55 340V250Q55 175 160 175Q265 175 265 250V340" fill={theme.ink}/><path d="M147 130Q160 137 173 130" fill="none" stroke={theme.surface} strokeWidth={3}/></svg>
   <div style={{position:'absolute',bottom:10,left:0,right:0,textAlign:'center',fontSize:12,color:theme.surface}}>示意人物 · 布局演示</div>
  </div>
  <div style={css(graphic)}><SemanticGraphic width={graphic.w} height={graphic.h} style={style} template={pip?'compare':'steps'} title={pip?'两种表达，同一风格':'让内容决定画面'} text={pip?'人物解释观点。图形展示过程。':'确认声音。准备素材。组合成片。'} time={pip?time-2.5:time} duration={2.5}/></div>
  <div style={{...css(layout.caption),fontSize:height*.035,textAlign:'center',fontWeight:650,color:theme.accent}}>声音、图形与字幕，共同讲清一个主题</div>
 </AbsoluteFill>;
}
