import React from 'react';
import {AbsoluteFill,useCurrentFrame,useVideoConfig} from 'remotion';
import {SemanticGraphic,type GraphicProps} from './semantic-graphics';

type Animation=Omit<GraphicProps,'time'> & {fps:number;render_mode?:string};
export function AnimationScene({animation:a}:{animation:Animation}){
 const {fps}=useVideoConfig();const time=useCurrentFrame()/fps;
 return <AbsoluteFill><SemanticGraphic {...a} time={time}/></AbsoluteFill>;
}
