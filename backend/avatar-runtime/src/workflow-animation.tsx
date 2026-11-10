import React from 'react';
import {AbsoluteFill,useCurrentFrame,useVideoConfig} from 'remotion';
import {SemanticGraphic,type GraphicProps} from './semantic-graphics';
import {ContentMotionScene,type ContentMotion} from './content-motion';

type Animation=Omit<GraphicProps,'time'> & {fps:number;render_mode?:string;content_motion?:ContentMotion};
export function AnimationScene({animation:a}:{animation:Animation}){
 const {fps}=useVideoConfig();const time=useCurrentFrame()/fps;
 if(/^(mg-|explain-)/.test(a.style)){
  if(!a.content_motion)throw new Error('动效缺少内容方案，禁止使用示例视频替代');
  return <AbsoluteFill><ContentMotionScene motion={a.content_motion} time={time}/></AbsoluteFill>;
 }
 return <AbsoluteFill><SemanticGraphic {...a} time={time}/></AbsoluteFill>;
}
