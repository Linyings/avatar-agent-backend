// Local rendering acceptance. Plans below are labelled fixtures; no provider
// calls are made. This exercises the same compiler and composition as F07.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {bundle} from '@remotion/bundler';
import {openBrowser,renderStill,renderMedia,selectComposition} from '@remotion/renderer';
import {compileMotion} from '../content-motion.mjs';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const output=process.argv[2]||'/tmp/content-motion-verification';fs.mkdirSync(output,{recursive:true});
const styles=JSON.parse(fs.readFileSync(path.join(root,'native-styles.json'),'utf8'));
const topics=[
 {name:'finance',text:'研发投入110亿元，算力规模扩大。',scene:{quote:'研发投入110亿元，算力规模扩大',title:'研发投入',layout:'data',elements:[{label:'110亿元',kind:'coin'},{label:'算力规模扩大',kind:'chip'}]}},
 {name:'environment',text:'保护森林，减少排放，改善生态。',scene:{quote:'保护森林，减少排放，改善生态',title:'保护森林',layout:'relationship',elements:[{label:'保护森林',kind:'leaf'},{label:'减少排放',kind:'cloud'},{label:'改善生态',kind:'globe'}]}}
];
const props=(style,topic,width=360,height=640)=>{
 const a={style:style.id,width,height,fps:25,duration:3.2,text:topic.text,words:[],motion_plan:{version:'content-motion/1',tone:'neutral',visual_context:'验收固定方案，未调用模型',scenes:[topic.scene]}};
 return {animation:{...a,content_motion:compileMotion(a)}};
};
const serveUrl=await bundle({entryPoint:path.join(root,'src/workflow-entry.tsx'),publicDir:output});
const browser=await openBrowser('chrome',{chromiumOptions:{gl:'swangle'},logLevel:'error'});
const report={planning:'deterministic fixtures; provider contract tested separately',styles:[],encoded:[],paidProviderRequests:0};
try{
 for(const style of styles){
  for(const topic of topics){
   const inputProps=props(style,topic),composition=await selectComposition({serveUrl,id:'AnimationTemplate',inputProps,puppeteerInstance:browser});
   await renderStill({serveUrl,composition,inputProps,puppeteerInstance:browser,frame:65,imageFormat:'png',output:path.join(output,style.id+'-'+topic.name+'.png')});
  }
  assert.notDeepEqual(fs.readFileSync(path.join(output,style.id+'-finance.png')),fs.readFileSync(path.join(output,style.id+'-environment.png')));
  report.styles.push({id:style.id,treatment:style.design.treatment,topicsProduceDifferentFrames:true});
 }
 for(const [id,w,h] of [['mg-3d-render',360,640],['mg-hud',640,360],['mg-liquid',360,360]]){
  const style=styles.find(s=>s.id===id),inputProps=props(style,topics[0],w,h),composition=await selectComposition({serveUrl,id:'AnimationTemplate',inputProps,puppeteerInstance:browser});
  const target=path.join(output,id+'.mp4');
  await renderMedia({serveUrl,composition,inputProps,puppeteerInstance:browser,codec:'h264',muted:true,outputLocation:target,concurrency:1,overwrite:true});
  const info=JSON.parse(execFileSync('ffprobe',['-v','error','-show_streams','-of','json',target]));const video=info.streams.find(s=>s.codec_type==='video');
  assert.equal(video.width,w);assert.equal(video.height,h);assert.equal(Number(video.nb_frames),80);assert.equal(info.streams.some(s=>s.codec_type==='audio'),false);
  execFileSync('ffmpeg',['-v','error','-i',target,'-f','null','-']);
  report.encoded.push({style:id,width:w,height:h,frames:80,decoded:true,demoVideoUsed:false});
 }
 fs.writeFileSync(path.join(output,'verification.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report));
}finally{await browser.close({silent:true})}
