import {test} from 'node:test';
import assert from 'node:assert/strict';
import {cardTextLayout,cardBandLayout} from '../src/animation-layout.ts';

test('talkcraft card canvas fits the band for all output ratios without cropping',()=>{
 for(const [w,h] of [[1280,720],[720,1280],[720,720]]){
  const b=cardBandLayout(w,h);
  assert.ok(b.left>=0 && b.top>=h*.73);
  assert.ok(b.left+b.width*b.scale<=w+.001);
  assert.ok(b.top+b.height*b.scale<=h*.91+.001);
  assert.equal(b.height,240);
 }
});

test('landscape summary cards reserve enough height for complete Chinese text',()=>{
 const text='做好这几步排查，剪辑才能省时又出彩';
 const box=cardTextLayout(850,87,text,true,true);
 const lines=Math.ceil([...text].length*box.fontSize/box.width);
 assert.ok(box.y+box.height<=87);
 assert.ok(lines*box.fontSize*1.5<=box.height);
 assert.ok(box.fontSize>=20);
});
test('short relationship cards and narrow step cards keep their text within bounds',()=>{
 for(const [w,h] of [[370,112],[203,275],[850,275]]){
  const text='核对人物神情与穿搭是否贴合你的主题';
  const box=cardTextLayout(w,h,text,false,true);
  assert.ok(box.x+box.width<=w && box.y+box.height<=h);
  assert.ok(Math.ceil([...text].length*box.fontSize/box.width)*box.fontSize*1.5<=box.height);
 }
});
