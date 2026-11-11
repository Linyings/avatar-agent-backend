// Diagnostics contain scene data only; subprocess paths and provider settings
// stay in server logs. The same checks run before encoding and after rendering.
export class MotionValidationError extends Error {
 constructor(code,message,detail={}){super(message);this.diagnostic={code,message,...detail};}
}
export function printMotionError(error){
 const detail=error.diagnostic??{code:'motion_execution',message:String(error.message).slice(0,700)};
 process.stderr.write('AVATAR_MEDIA_ERROR='+JSON.stringify(detail)+'\n');
}
export function localMotionScene(scene){
 const duration=scene.end-scene.start,hold_seconds=Math.min(.6,duration*.2);
 return {...scene,duration,hold_seconds,elements:scene.elements.map(e=>({...e,local_at:Math.max(0,e.at-scene.start),enter_at:Math.max(0,Math.min(e.at-scene.start,duration-hold_seconds-Math.min(.25,duration*.15)))}))};
}
const compact=s=>String(s).replace(/\s/g,'');
export function motionLabelCovered(label,drawn){
 label=compact(label);const parts=drawn.map(compact).filter(Boolean);
 // A number plus its unit must remain intact. Never accept 300 inside 1300,
 // or reconstruct a decimal from unrelated individual digits.
 const numeric=label.match(/\d+(?:[.,]\d+)*(?:%|％|万亿(?:美元|元)?|亿(?:美元|元)?|万(?:美元|元)?|美元|亿元|元|倍|年|月|日|号|秒|人)?/g)||[];
 for(const token of numeric){
  // Only a bare digit edge can be extended into a different number ("300" in
  // "1300"). A unit already ends the token, so "11月" followed by "9号" is fine.
  const guardEnd=/\d$/.test(token);
  if(!parts.some(p=>{let at=p.indexOf(token);while(at>=0){if(!/[\d.]/.test(p[at-1]||'')&&!(guardEnd&&/[\d.]/.test(p[at+token.length]||'')))return true;at=p.indexOf(token,at+1)}return false}))return false;
 }
 const covered=Array(label.length).fill(false);
 for(const part of parts){
  if(part.includes(label))return true;
  // Only original contiguous pieces count; an arbitrary pile of characters
  // elsewhere on the frame cannot assemble a missing financial label.
  if(part.length<2&&!numeric.includes(part))continue;
  let at=label.indexOf(part);while(at>=0){for(let i=at;i<at+part.length;i++)covered[i]=true;at=label.indexOf(part,at+1)}
 }
 return covered.every(Boolean);
}
export function checkMotionFrames(scene,frames,fps,sceneIndex){
 const hold=Math.min(.4,(scene.end-scene.start)*.15),missing=[],short=[];
 for(const element of scene.elements){
  let longest=0,streak=0;
  for(const frame of frames){
   const labels=frame.labels.filter(label=>compact(label)!==compact(scene.title??'')||compact(element.label)===compact(scene.title??''));
   streak=motionLabelCovered(element.label,labels)?streak+1:0;longest=Math.max(longest,streak/fps);
  }
  if(longest===0)missing.push(element.label);else if(longest+1e-6<hold)short.push(element.label);
 }
 const drawn=[...new Set(frames.flatMap(f=>f.labels))];
 // drawn_labels lets the caller tell a real omission from a checker fault: a
 // "missing" label that appears verbatim here was drawn, so redrawing cannot help.
 const detail={scene_index:sceneIndex+1,scene_quote:scene.quote,scene_duration:scene.end-scene.start,missing_labels:missing,short_labels:short,drawn_labels:drawn.slice(0,40)};
 if(missing.length)throw new MotionValidationError('motion_text_missing',`场景 ${sceneIndex+1} 关键内容未完整展示：${missing.join('、')}；场景只有 ${(scene.end-scene.start).toFixed(2)} 秒，请按本场时长调整出场时间`,detail);
 if(short.length)throw new MotionValidationError('motion_text_too_brief',`场景 ${sceneIndex+1} 关键内容展示时间不足：${short.join('、')}；请提前出场并保留阅读时间`,detail);
 return {...detail,drawn,missing_numeric_labels:[]};
}
