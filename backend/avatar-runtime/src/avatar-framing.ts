export type AvatarTrack={version:string;status:string;width:number;height:number;duration:number;step:number;samples:{t:number;face:number[];cut?:boolean}[]};
type Box={x:number;y:number;w:number;h:number};
// tracked: bust framing with the whole head kept. tight: the source is too
// close to keep the whole head, so the window is filled face-first and the
// hair or shoulders may be trimmed; never letterboxed.
export type AvatarFramingDecision={mode:'tracked'|'tight';faceHeight:number};

export function avatarTrackCovers(track:AvatarTrack|undefined,start:number,end:number){
 if(!track||track.status!=='tracked'||track.version!=='avatar-framing/1'||!track.samples.length)return false;
 const rows=track.samples,margin=Math.max(.65,track.step*1.3);
 if(rows[0].t>start+margin||rows[rows.length-1].t<end-margin)return false;
 for(let i=1;i<rows.length;i++)if(rows[i-1].t<end&&rows[i].t>start&&rows[i].t-rows[i-1].t>track.step*2.5)return false;
 return true;
}

function faceAt(track:AvatarTrack|undefined,time:number){
 if(!track||track.version!=='avatar-framing/1'||track.status!=='tracked'||!track.samples.length)return null;
 const rows=track.samples;let right=rows.findIndex(r=>r.t>=time);if(right<0)right=rows.length-1;
 const a=rows[Math.max(0,right-(rows[right].t>time?1:0))],b=rows[right];
 if(Math.min(Math.abs(a.t-time),Math.abs(b.t-time))>Math.max(.65,track.step*1.3))return null;
 // A cut/gap must not interpolate between different source compositions.
 let face=a.face;
 if(b.cut||b.t-a.t>track.step*2.5)face=time>=b.t?b.face:a.face;
 else if(b.t>a.t){const p=Math.max(0,Math.min(1,(time-a.t)/(b.t-a.t)));face=a.face.map((v,i)=>v+(b.face[i]-v)*p);}
 if(face.length!==4||face.some(v=>!Number.isFinite(v))||face[2]<=0||face[3]<=0)return null;
 return face;
}

// Vertical position of the face centre in the window: circles centre the
// face; rectangles sit it higher so the shoulders read as a bust.
export const avatarAnchor=(shape:string)=>shape==='circle'?.5:.42;
const bustFaceHeight=(box:Box,shape:string)=>shape==='circle'?Math.min(box.w,box.h)*.42:box.h*.34;

// Choose one face height for the entire shot.
export function avatarFramingDecision(box:Box,track:AvatarTrack|undefined,start:number,end:number,shape:string):AvatarFramingDecision|null{
 if(!track||!avatarTrackCovers(track,start,end)||track.width<=0||track.height<=0)return null;
 const anchor=avatarAnchor(shape);
 const times=[start,...track.samples.filter(row=>row.t>start&&row.t<end).map(row=>row.t),end];
 const cover=Math.max(box.w/track.width,box.h/track.height);
 let lower=0,upper=Infinity,fill=0;
 for(const time of times){
  const face=faceAt(track,time);if(!face)return null;
  const [x,y,w,h]=face,cx=x+w/2,cy=y+h/2;
  if(cx<=0||cy<=0||cx>=track.width||cy>=track.height)return null;
  fill=Math.max(fill,h*cover);
  // Smallest face height whose uniform scale still fills the window.
  lower=Math.max(lower,h*Math.max(box.w/(2*cx),box.w/(2*(track.width-cx)),anchor*box.h/cy,(1-anchor)*box.h/(track.height-cy)));
  // Head envelope: top of hair, temples, and chin. Hair corners are rounded,
  // so requiring a whole rectangular hair box inside a circle over-shrinks it.
  const ratio=w/h;
  upper=Math.min(upper,shape==='circle'?Math.min(box.w,box.h)*.49/Math.max(.95,Math.hypot(.65*ratio,.6)):Math.min(box.w*.49/(.65*ratio),anchor*box.h*.98/.95,(1-anchor)*box.h*.98/.5));
 }
 if(lower<=upper)return {mode:'tracked',faceHeight:Math.max(lower,Math.min(bustFaceHeight(box,shape),upper))};
 // Too close to both centre the face and keep the head: fill the window with
 // the least zoom and let avatarFraming clamp the face as near centre as fits.
 return {mode:'tight',faceHeight:fill};
}

export function avatarFramingPlan(box:Box,track:AvatarTrack|undefined,start:number,end:number,shape:string){
 return avatarFramingDecision(box,track,start,end,shape)?.faceHeight??null;
}

// Window geometry stays constant. Uniform source scaling only cancels measured
// changes in source framing; it never stretches width and height independently.
export function avatarFraming(box:Box,track:AvatarTrack|undefined,time:number,faceHeight=box.h*.4,shape='circle'){
 const face=faceAt(track,time);if(!face||!track)return null;
 const scale=faceHeight/face[3],width=track.width*scale,height=track.height*scale,anchor=avatarAnchor(shape);
 let left=box.w*.5-(face[0]+face[2]/2)*scale,top=box.h*anchor-(face[1]+face[3]/2)*scale;
 // Interpolated rows may drift a fraction of a pixel; never expose a margin.
 if(width>=box.w&&height>=box.h){left=Math.min(0,Math.max(box.w-width,left));top=Math.min(0,Math.max(box.h-height,top));}
 return {scale,left,top,width,height};
}

export function avatarWindowBox(width:number,height:number,shape:string,side:string):Box{
 const size=Math.min(width,height)*.28;
 const w=shape==='circle'?size:Math.min(width*.36,height*.33),h=shape==='circle'?size:Math.min(height*.31,w*1.15);
 return {x:side.includes('left')?width*.055:width*.945-w,y:side.startsWith('top')?height*.07:height*.88-h,w,h};
}

type Crop={scale:number;left:number;top:number;width:number;height:number};
type StableSegment={start:number;end:number;crop:Crop;positions:{t:number;left:number;top:number}[]};
export type StableAvatarPlan={version:'stable-crop/1';segments:StableSegment[]};
const median=(values:number[])=>{const a=[...values].sort((a,b)=>a-b);return a.length%2?a[Math.floor(a.length/2)]:(a[a.length/2-1]+a[a.length/2])/2;};
const clamp=(x:number,lo:number,hi:number)=>Math.min(hi,Math.max(lo,x));

// Offline planning: render workers may request frames out of order. Never keep
// mutable tracking state in React or change scale in response to face-box noise.
export function stableAvatarPlan(box:Box,track:AvatarTrack|undefined,start:number,end:number,shape:string):StableAvatarPlan|null{
 if(!track||track.status!=='tracked'||track.version!=='avatar-framing/1'||track.width<=0||track.height<=0||end<=start)return null;
 const valid=track.samples.filter(r=>Number.isFinite(r.t)&&r.face.length===4&&r.face.every(Number.isFinite)&&r.face[2]>0&&r.face[3]>0);
 if(!valid.length)return null;
 // The detector also labels long missing-data gaps as cuts; hold rather than
 // treating a reacquired face as a new camera setup.
 const cuts=[start,...valid.filter((r,i)=>r.cut&&r.t>start&&r.t<end&&(i===0||r.t-valid[i-1].t<=track.step*2.5)).map(r=>r.t),end];
 const segments:StableSegment[]=[];
 for(let k=0;k<cuts.length-1;k++){
  const a=cuts[k],b=cuts[k+1];
  let rows=valid.filter(r=>r.t>=a&&r.t<b);
  if(!rows.length){const previous=valid.filter(r=>r.t<=a).at(-1);if(previous)rows=[previous];else continue;}
  // Reject isolated spikes only; sustained changes and real cuts remain data.
  rows=rows.filter((r,i,all)=>{
   if(i===0||i===all.length-1)return true;
   const p=all[i-1].face,n=all[i+1].face,f=r.face;
   const distance=(u:number[],v:number[])=>Math.hypot((u[0]+u[2]/2-v[0]-v[2]/2)/track.width,(u[1]+u[3]/2-v[1]-v[3]/2)/track.height);
   return !(distance(p,n)<.08&&(distance(p,f)>.16||Math.abs(Math.log(f[3]/Math.sqrt(p[3]*n[3])))>.25));
  });
  const h=median(rows.map(r=>r.face[3])),cx=median(rows.map(r=>r.face[0]+r.face[2]/2)),cy=median(rows.map(r=>r.face[1]+r.face[3]/2));
  const cover=Math.max(box.w/track.width,box.h/track.height),anchor=avatarAnchor(shape);
  const scale=Math.max(cover,Math.min(bustFaceHeight(box,shape)/h,...rows.map(r=>Math.min(box.w/(r.face[2]*1.5),box.h/(r.face[3]*1.65)))));
  const width=track.width*scale,height=track.height*scale;
  let left=clamp(box.w/2-cx*scale,box.w-width,0),top=clamp(box.h*anchor-cy*scale,box.h-height,0);
  const crop={scale,width,height,left,top},positions=[{t:a,left,top}];
  let vx=0,vy=0;
  const dt=1/25,maxV=Math.min(box.w,box.h)*.06,maxA=maxV*.6;
  for(let t=a+dt;t<b+dt/2;t+=dt){
   const row=rows.filter(r=>r.t<=t).at(-1)??rows[0];
   // Missing detections hold the camera; a face can move naturally in the
   // central safe region. Only sustained near-edge motion moves the crop.
   const fresh=Math.abs(t-row.t)<=Math.max(.65,track.step*1.3);
   const x=left+(row.face[0]+row.face[2]/2)*scale,y=top+(row.face[1]+row.face[3]/2)*scale;
   const dx=fresh?clamp(x,box.w*.25,box.w*.75)-x:0,dy=fresh?clamp(y,box.h*.22,box.h*.72)-y:0;
   vx+=clamp(clamp(dx*2,-maxV,maxV)-vx,-maxA*dt,maxA*dt);
   vy+=clamp(clamp(dy*2,-maxV,maxV)-vy,-maxA*dt,maxA*dt);
   if(!fresh){vx=0;vy=0;}
   left=clamp(left+vx*dt,box.w-width,0);top=clamp(top+vy*dt,box.h-height,0);
   positions.push({t:Math.min(t,b),left,top});
  }
  segments.push({start:a,end:b,crop,positions});
 }
 return segments.length?{version:'stable-crop/1',segments}:null;
}

export function stableAvatarCrop(plan:StableAvatarPlan,time:number):Crop{
 const segment=plan.segments.filter(s=>s.start<=time).at(-1)??plan.segments[0];
 const rows=segment.positions,i=rows.findIndex(r=>r.t>=time);
 const b=rows[i<0?rows.length-1:i],a=rows[Math.max(0,(i<0?rows.length-1:i)-1)];
 const p=b.t>a.t?clamp((time-a.t)/(b.t-a.t),0,1):0;
 return {...segment.crop,left:a.left+(b.left-a.left)*p,top:a.top+(b.top-a.top)*p};
}
