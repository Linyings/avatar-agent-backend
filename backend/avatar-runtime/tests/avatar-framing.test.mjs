import test from 'node:test';
import assert from 'node:assert/strict';
import {avatarFraming,avatarFramingDecision,avatarFramingPlan,avatarTrackCovers,avatarWindowBox} from '../src/avatar-framing.ts';

const track={version:'avatar-framing/1',status:'tracked',width:720,height:1280,duration:10,step:.5,samples:[{t:0,face:[240,180,240,320]},{t:.5,face:[120,80,480,640]},{t:1,face:[120,80,480,640]}]};
test('postproduction compensates source zoom with uniform scale and preserves the whole head',()=>{
 const box={x:0,y:0,w:200,h:200};
 const first=avatarFraming(box,track,0),second=avatarFraming(box,track,.5);
 assert.ok(first&&second);
 assert.equal(first.scale*320,second.scale*640);
 for(const [crop,face]of [[first,track.samples[0].face],[second,track.samples[1].face]]){
  for(const [x,y]of [[face[0]-.15*face[2],face[1]-.45*face[3]],[face[0]+1.15*face[2],face[1]-.45*face[3]],[face[0],face[1]+face[3]],[face[0]+face[2],face[1]+face[3]]]){
   const dx=crop.left+x*crop.scale-100,dy=crop.top+y*crop.scale-100;
   assert.ok(Math.hypot(dx,dy)<98,'head is outside circular mask');
  }
 }
});
test('local source time selects the correct face and missing evidence never invents a crop',()=>{
 const box={x:0,y:0,w:200,h:200};
 assert.equal(avatarFraming(box,undefined,0),null);
 assert.equal(avatarFraming(box,{...track,status:'uncertain'},0),null);
 assert.equal(avatarFraming(box,track,7),null);
 const cropped=avatarFraming(box,track,.75);
 assert.equal(cropped.scale,avatarFraming(box,track,.5).scale);
 assert.equal(avatarTrackCovers(track,0,1),true);
 assert.equal(avatarTrackCovers(track,0,8),false);
 assert.equal(avatarTrackCovers({...track,samples:[track.samples[0],{...track.samples[1],t:3}]},0,3),false);
});
test('circle and rectangular windows remain stationary and fit inside the subtitle safe area',()=>{
 for(const [width,height]of [[720,1280],[1280,720],[720,720]])for(const shape of ['circle','rounded','rectangle'])for(const side of ['top-left','top-right','bottom-left','bottom-right']){
  const b=avatarWindowBox(width,height,shape,side);
  assert.ok(b.x>=0&&b.y>=0&&b.x+b.w<=width&&b.y+b.h<=height*.9);
  if(shape==='circle')assert.equal(b.w,b.h);
 }
});
test('one shot uses a constant face size, fills its mask, and retains the head',()=>{
 const source={...track,samples:[{t:0,face:[229,249,230,331]},{t:.5,face:[230,248,227,334]},{t:1,face:[233,249,226,333]}]};
 for(const shape of ['circle','rounded','rectangle']){
  const box=avatarWindowBox(720,1280,shape,'bottom-right');
  const faceHeight=avatarFramingPlan(box,source,0,1,shape);
  assert.ok(faceHeight);
  for(const t of [0,.25,.5,.75,1]){
   const crop=avatarFraming(box,source,t,faceHeight,shape);
   assert.ok(crop.left<=1e-6&&crop.top<=1e-6);
   assert.ok(crop.left+crop.width>=box.w-1e-6&&crop.top+crop.height>=box.h-1e-6,'window must not expose empty margins');
  }
  for(const row of source.samples){
   const crop=avatarFraming(box,source,row.t,faceHeight,shape),[x,y,w,h]=row.face;
   assert.ok(Math.abs(crop.scale*h-faceHeight)<1e-6);
   for(const [px,py]of [[x+w/2,y-.45*h],[x-.15*w,y-.1*h],[x+1.15*w,y-.1*h],[x,y+h*1.1],[x+w,y+h*1.1]]){
    const dx=crop.left+px*crop.scale-box.w/2,dy=crop.top+py*crop.scale-(shape==='circle'?box.h/2:box.h*.42);
    if(shape==='circle')assert.ok(Math.hypot(dx,dy)<box.w/2,'hair, temples and chin must remain visible');
    else assert.ok(Math.abs(dx)<box.w/2&&Math.abs(dy)<box.h/2);
   }
  }
 }
});
const fills=(crop,box)=>crop.left<=1e-6&&crop.top<=1e-6&&crop.left+crop.width>=box.w-1e-6&&crop.top+crop.height>=box.h-1e-6;
const faceCenterInside=(crop,face,box)=>{const x=crop.left+(face[0]+face[2]/2)*crop.scale,y=crop.top+(face[1]+face[3]/2)*crop.scale;return x>0&&x<box.w&&y>0&&y<box.h;};
// Real tracking rows from a 9:16 close-up talking head (run fbfc23d4).
const closeUp={...track,duration:2,samples:[{t:0,face:[154.5,261.7,378.9,521]},{t:.5,face:[170.7,268.2,358.4,502.6]},{t:1,face:[180.8,265.9,359.3,519.2]},{t:1.5,face:[181.8,266,357,515]},{t:2,face:[180,266,358,516]}]};
test('a close-up is cropped to fill the window instead of letterboxing the whole frame',()=>{
 for(const [box,shape]of [[avatarWindowBox(720,1280,'circle','bottom-right'),'circle'],[{x:32,y:32,w:655,h:550},'rectangle'],[avatarWindowBox(720,1280,'rounded','bottom-right'),'rounded']]){
  const decision=avatarFramingDecision(box,closeUp,0,2,shape);
  assert.ok(decision,'a tracked face must always produce a crop');
  assert.equal(decision.mode,'tight');
  for(const t of [0,.4,1,1.7,2]){
   const crop=avatarFraming(box,closeUp,t,decision.faceHeight,shape);
   assert.ok(fills(crop,box),'window must not expose empty margins');
   assert.ok(faceCenterInside(crop,faceAtRow(closeUp,t),box),'face must stay in the window');
  }
 }
 assert.equal(avatarFramingPlan({x:0,y:0,w:201,h:201},closeUp,0,2,'circle'),avatarFramingDecision({x:0,y:0,w:201,h:201},closeUp,0,2,'circle').faceHeight);
});
test('a tight crop zooms only as far as needed to fill the window',()=>{
 const box={x:32,y:32,w:655,h:550};
 const decision=avatarFramingDecision(box,closeUp,0,2,'rectangle');
 const fill=Math.max(...closeUp.samples.map(r=>r.face[3]))*Math.max(box.w/closeUp.width,box.h/closeUp.height);
 assert.ok(Math.abs(decision.faceHeight-fill)<1e-6,`face height ${decision.faceHeight} should be the minimum fill ${fill}`);
 assert.ok(decision.faceHeight<box.h*.9,'the face must not outgrow the window');
});
test('a medium shot is framed as a bust at the target size with the head retained',()=>{
 const medium={...track,duration:1,samples:[{t:0,face:[300,260,120,165]},{t:.5,face:[300,262,120,165]},{t:1,face:[301,261,120,165]}]};
 const box={x:0,y:0,w:655,h:550};
 const decision=avatarFramingDecision(box,medium,0,1,'rectangle');
 assert.equal(decision.mode,'tracked');
 assert.ok(Math.abs(decision.faceHeight-box.h*.34)<1e-6,'bust target face height');
 const crop=avatarFraming(box,medium,.5,decision.faceHeight,'rectangle');
 assert.ok(fills(crop,box));
 const [x,y,w,h]=medium.samples[1].face;
 assert.ok(crop.top+(y-.45*h)*crop.scale>=0,'hair stays inside the window');
});
test('missing or partial tracking never invents a crop',()=>{
 const box={x:0,y:0,w:200,h:200};
 assert.equal(avatarFramingDecision(box,undefined,0,1,'circle'),null);
 assert.equal(avatarFramingDecision(box,closeUp,0,8,'circle'),null);
 assert.equal(avatarFramingDecision(box,{...closeUp,status:'uncertain'},0,1,'circle'),null);
});
function faceAtRow(t,time){return t.samples.reduce((a,b)=>Math.abs(b.t-time)<Math.abs(a.t-time)?b:a).face;}
