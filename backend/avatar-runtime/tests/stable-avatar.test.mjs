import test from 'node:test';
import assert from 'node:assert/strict';
import {stableAvatarPlan,stableAvatarCrop} from '../src/avatar-framing.ts';

const box={x:0,y:0,w:240,h:240};
const track={version:'avatar-framing/1',status:'tracked',width:956,height:1280,duration:4,step:.5,samples:Array.from({length:9},(_,i)=>({t:i*.5,face:[400+(i%2)*3,245,360,550+(i%3)*20]}))};
test('face size noise never zooms the source or moves a safe composition',()=>{
 const plan=stableAvatarPlan(box,track,0,4,'circle');
 assert.ok(plan);
 const first=stableAvatarCrop(plan,0);
 for(let f=0;f<=100;f++)assert.deepEqual(stableAvatarCrop(plan,f/25),first);
 assert.ok(first.left<=0&&first.top<=0&&first.left+first.width>=box.w&&first.top+first.height>=box.h);
});
test('isolated bad detection is rejected, gaps hold position, and frame evaluation is deterministic',()=>{
 const noisy=structuredClone(track);noisy.samples[4].face=[10,10,100,150];
 const plan=stableAvatarPlan(box,noisy,0,4,'rectangle');
 const expected=stableAvatarCrop(plan,1.7);
 stableAvatarCrop(plan,3.9);stableAvatarCrop(plan,.1);
 assert.deepEqual(stableAvatarCrop(plan,1.7),expected);
 assert.deepEqual(stableAvatarCrop(plan,2),stableAvatarCrop(plan,0));
 const gap={...track,samples:track.samples.filter(r=>r.t<1||r.t>2).map(r=>({...r,cut:r.t===2.5}))};
 const held=stableAvatarPlan(box,gap,0,4,'circle');
 assert.ok(held);assert.equal(held.segments.length,1);assert.equal(stableAvatarCrop(held,1.5).scale,stableAvatarCrop(held,0).scale);
});
test('actual cuts replan immediately without interpolation across compositions',()=>{
 const cut={...track,samples:track.samples.map(r=>r.t<2?r:{...r,cut:r.t===2,face:[150,200,180,260]})};
 const plan=stableAvatarPlan(box,cut,0,4,'circle');
 assert.equal(plan.segments.length,2);
 assert.equal(stableAvatarCrop(plan,1.99).scale,stableAvatarCrop(plan,0).scale);
 assert.equal(stableAvatarCrop(plan,2).scale,stableAvatarCrop(plan,3).scale);
 assert.notEqual(stableAvatarCrop(plan,2).scale,stableAvatarCrop(plan,0).scale);
});
test('large sustained motion uses bounded translation, never scale pumping or empty margins',()=>{
 const moving={...track,width:1920,samples:track.samples.map((r,i)=>({...r,face:[300+i*110,350,180,260]}))};
 const plan=stableAvatarPlan(box,moving,0,4,'rectangle');
 let last=stableAvatarCrop(plan,0);
 for(let f=1;f<=100;f++){
  const crop=stableAvatarCrop(plan,f/25);
  assert.equal(crop.scale,last.scale);
  assert.ok(Math.abs(crop.left-last.left)<box.w*.07/25+1e-6);
  assert.ok(crop.left<=0&&crop.top<=0&&crop.left+crop.width>=box.w-1e-6&&crop.top+crop.height>=box.h-1e-6);
  last=crop;
 }
});
