import fs from 'node:fs';
const styles=JSON.parse(fs.readFileSync(new URL('./native-styles.json',import.meta.url),'utf8'));
export const MOTION_VERSION='content-motion/1';
const kinds=['coin','chart','chip','cloud','people','document','shield','globe','clock','code','spark','box','leaf','link'];
export function styleProfile(id){
 const style=styles.find(s=>s.id===({'flat-vector':'mg-flat-vector','line-art':'mg-line-art'}[id]||id));
 if(!style?.design)throw new Error('动效风格尚未配置内容渲染器');
 return style.design;
}
export function validateMotion(plan,text){
 if(!plan||plan.version!==MOTION_VERSION||!Array.isArray(plan.scenes)||plan.scenes.length<1||plan.scenes.length>5||!['warm','cool','neutral'].includes(plan.tone))throw new Error('缺少有效的内容动效方案');
 let cursor=0;
 for(const scene of plan.scenes){
  const at=typeof scene.quote==='string'&&scene.quote.trim()?text.indexOf(scene.quote,cursor):-1;
  if(at<0||[...scene.quote].length>600)throw new Error('动效场景必须按顺序引用口播原文');cursor=at+scene.quote.length;
  if(!scene.title||[...scene.title].length>24||!scene.quote.includes(scene.title)||!['highlight','data','compare','steps','relationship','summary'].includes(scene.layout)||!Array.isArray(scene.elements)||scene.elements.length<1||scene.elements.length>4)throw new Error('动效场景标题或构图无效');
  for(const item of scene.elements)if(!item.label||[...item.label].length>60||!scene.quote.includes(item.label)||!kinds.includes(item.kind))throw new Error('动效文字或数值缺少原文依据');
 }
 return plan;
}
// Match against measured words, with character interpolation only inside a
// measured word. A proportional fallback is recorded, never disguised as ASR.
function charTimeline(text,words,duration){
 const timeline=[];let cursor=0;
 for(const word of words||[]){
  if(typeof word.text!=='string'||!Number.isFinite(word.start)||!Number.isFinite(word.end)||word.end<=word.start)continue;
  const at=text.indexOf(word.text,cursor);if(at<0)continue;
  for(let i=0;i<word.text.length;i++)timeline.push({index:at+i,time:word.start+(word.end-word.start)*i/word.text.length});
  cursor=at+word.text.length;
 }
 return index=>{const match=timeline.find(c=>c.index>=index);return match?{time:match.time,measured:true}:{time:duration*index/Math.max(1,text.length),measured:false}};
}
export function compileMotion(a){
 validateMotion(a.motion_plan,a.text);const profile=styleProfile(a.style);
 if(!Number.isFinite(a.duration)||a.duration<=0||a.duration>90||![25,30].includes(a.fps)||!Number.isInteger(a.width)||!Number.isInteger(a.height)||a.width<180||a.height<180||a.width>1920||a.height>1920)throw new Error('动效时长或尺寸无效');
 const window=a.narration_window??{start:0,end:a.duration};
 if(!Number.isFinite(window.start)||window.start<0||!Number.isFinite(window.end)||Math.abs(window.end-window.start-a.duration)>.05)throw new Error('动效台词时间窗口无效');
 const timeline=charTimeline(a.text,a.words,Math.max(a.duration,...(a.words||[]).map(w=>w.end||0)));let cursor=0;
 const scenes=a.motion_plan.scenes.map((scene,index)=>{
  const at=a.text.indexOf(scene.quote,cursor);cursor=at+scene.quote.length;const stamp=timeline(at);
  const start=index===0?0:stamp.time;
  return {...scene,start,timing:stamp.measured?'aligned':'estimated',elements:scene.elements.map(item=>({...item,at:Math.max(start,timeline(at+scene.quote.indexOf(item.label)).time)}))};
 });
 for(let i=0;i<scenes.length;i++)scenes[i].end=scenes[i+1]?.start??Math.max(window.end,...(a.words||[]).map(w=>w.end||0));
 const visible=scenes.filter(s=>s.start<window.end-.001&&s.end>window.start+.001).map(s=>({...s,start:Math.max(0,s.start-window.start),end:Math.min(a.duration,s.end-window.start),elements:s.elements.map(e=>({...e,at:Math.max(0,e.at-window.start)}))}));
 if(!visible.length)throw new Error('动效没有覆盖当前台词时间窗口');
 for(const quote of a.required_quotes||[]){
  const index=a.text.indexOf(quote),at=index<0?-1:timeline(index).time;
  const compact=s=>s.replace(/[\s，。；！？、]/g,'');
  if(index<0||at<window.start-.04||at>=window.end-.04||!compact(visible.map(s=>s.quote).join('')).includes(compact(quote)))throw new Error('关键台词或数字超出动效时间范围，不能静默删除');
 }
 for(let i=0;i<visible.length;i++){
  visible[i].end=visible[i+1]?.start??a.duration;
  if(visible[i].end<=visible[i].start)throw new Error('动效场景的语音时间重叠，请检查配音对齐');
 }
 const colors=profile.palette.slice(3,5),warmth=color=>parseInt(color.slice(1,3),16)-parseInt(color.slice(5,7),16);
 const tone=a.motion_plan.tone;
 const accent=tone==='neutral'?colors[0]:[...colors].sort((x,y)=>(warmth(x)-warmth(y))*(tone==='warm'?-1:1))[0];
 return {mode:'content-motion',version:MOTION_VERSION,timing_version:'motion-window/2',narration_window:window,style:a.style,profile,tone,accent,visual_context:a.motion_plan.visual_context||'',scenes:visible,duration:a.duration,width:a.width,height:a.height};
}
