import test from 'node:test';
import assert from 'node:assert/strict';
import {avatarFraming,avatarFramingPlan,avatarTrackCovers,avatarWindowBox} from '../src/avatar-framing.ts';

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
   const crop=avatarFraming(box,source,t,faceHeight);
   assert.ok(crop.left<=1e-6&&crop.top<=1e-6);
   assert.ok(crop.left+crop.width>=box.w-1e-6&&crop.top+crop.height>=box.h-1e-6,'window must not expose empty margins');
  }
  for(const row of source.samples){
   const crop=avatarFraming(box,source,row.t,faceHeight),[x,y,w,h]=row.face;
   assert.ok(Math.abs(crop.scale*h-faceHeight)<1e-6);
   for(const [px,py]of [[x+w/2,y-.45*h],[x-.15*w,y-.1*h],[x+1.15*w,y-.1*h],[x,y+h*1.1],[x+w,y+h*1.1]]){
    const dx=crop.left+px*crop.scale-box.w/2,dy=crop.top+py*crop.scale-box.h/2;
    if(shape==='circle')assert.ok(Math.hypot(dx,dy)<box.w/2,'hair, temples and chin must remain visible');
    else assert.ok(Math.abs(dx)<box.w/2&&Math.abs(dy)<box.h/2);
   }
  }
 }
});
test('a close-up that cannot safely fill the window falls back for the whole shot',()=>{
 const source={...track,samples:[{t:0,face:[229,249,230,331]},{t:.5,face:[90,450,489,552]},{t:1,face:[90,450,489,552]}]};
 const box={x:0,y:0,w:200,h:200};
 assert.equal(avatarFramingPlan(box,source,0,1,'circle'),null);
 assert.equal(avatarFramingPlan(box,undefined,0,1,'circle'),null);
 assert.equal(avatarFramingPlan(box,source,0,8,'circle'),null);
});
