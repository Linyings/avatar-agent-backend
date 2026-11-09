export type AvatarTrack={version:string;status:string;width:number;height:number;duration:number;step:number;samples:{t:number;face:number[];cut?:boolean}[]};
type Box={x:number;y:number;w:number;h:number};

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

// Choose one face height for the entire shot. If filling the source window and
// preserving the head cannot both be satisfied, retain the full rectangle.
export function avatarFramingPlan(box:Box,track:AvatarTrack|undefined,start:number,end:number,shape:string){
 if(!track||!avatarTrackCovers(track,start,end)||track.width<=0||track.height<=0)return null;
 const times=[start,...track.samples.filter(row=>row.t>start&&row.t<end).map(row=>row.t),end];
 let lower=box.h*.4,upper=Infinity;
 for(const time of times){
  const face=faceAt(track,time);if(!face)return null;
  const [x,y,w,h]=face,cx=x+w/2,cy=y+h/2;
  if(cx<=0||cy<=0||cx>=track.width||cy>=track.height)return null;
  lower=Math.max(lower,h*Math.max(box.w/(2*cx),box.w/(2*(track.width-cx)),box.h/(2*cy),box.h/(2*(track.height-cy))));
  // Head envelope: top of hair, temples, and chin. Hair corners are rounded,
  // so requiring a whole rectangular hair box inside a circle over-shrinks it.
  const ratio=w/h;
  upper=Math.min(upper,shape==='circle'?Math.min(box.w,box.h)*.49/Math.max(.95,Math.hypot(.65*ratio,.6)):Math.min(box.w*.49/(.65*ratio),box.h*.49/.95));
 }
 return lower<=upper?lower:null;
}

// Window geometry stays constant. Uniform source scaling only cancels measured
// changes in source framing; it never stretches width and height independently.
export function avatarFraming(box:Box,track:AvatarTrack|undefined,time:number,faceHeight=box.h*.4){
 const face=faceAt(track,time);if(!face||!track)return null;
 const scale=faceHeight/face[3];
 const left=box.w*.5-(face[0]+face[2]/2)*scale,top=box.h*.5-(face[1]+face[3]/2)*scale;
 return {scale,left,top,width:track.width*scale,height:track.height*scale};
}

export function avatarWindowBox(width:number,height:number,shape:string,side:string):Box{
 const size=Math.min(width,height)*.28;
 const w=shape==='circle'?size:Math.min(width*.36,height*.33),h=shape==='circle'?size:Math.min(height*.31,w*1.15);
 return {x:side.includes('left')?width*.055:width*.945-w,y:side.startsWith('top')?height*.07:height*.88-h,w,h};
}
