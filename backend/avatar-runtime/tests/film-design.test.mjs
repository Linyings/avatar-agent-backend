import test from 'node:test';
import assert from 'node:assert/strict';
import {filmTheme,shotLayout,overlaps,mediaPlacement,semanticItems,resolveRecipe,captionPage} from '../src/film-design.ts';

test('all eight recipes have usable non-overlapping content and caption regions in three ratios',()=>{
 for(const [w,h] of [[1280,720],[720,1280],[720,720]])for(const recipe of ['host-hero','host-explain','visual-hero','visual-pip','compare','steps','evidence','summary']){
  const l=shotLayout(w,h,recipe,'right');
  for(const box of [l.primary,l.overlay,l.panel,l.caption].filter(Boolean)){
   assert.ok(box.x>=0&&box.y>=0&&box.w>0&&box.h>0,JSON.stringify({recipe,box}));
   assert.ok(box.x+box.w<=w+.01&&box.y+box.h<=h+.01);
  }
  if(l.panel)assert.ok(!overlaps(l.panel,l.primary));
  assert.ok(!overlaps(l.caption,l.primary));
  if(l.overlay)assert.ok(!overlaps(l.caption,l.overlay));
 }
 const hero=shotLayout(1280,720,'host-hero','right');
 assert.ok(hero.primary.h>720*.85,'hero no longer reserves a permanent 27% card band');
});

test('caption pagination preserves English words and does not jump back during pauses',()=>{
 const chars=[...'OpenAI 发布 ChatGPT Images 2.6，支持原生动图。'].map((ch,i)=>({ch,t:i*.2,e:i*.2+.1}));
 const pages=new Set(chars.map(c=>captionPage(chars,c.t,14).map(x=>x.ch).join('')));
 for(const page of pages){if(page.includes('Chat'))assert.ok(page.includes('ChatGPT'));if(page.includes('Imag'))assert.ok(page.includes('Images'));}
 const at=captionPage(chars,4.15,14).map(x=>x.ch).join('');
 assert.equal(at,captionPage(chars,4.1,14).map(x=>x.ch).join(''));
});

test('face-safe fitting never crops the measured head and backgrounds preserve mismatched ratios',()=>{
 const box={x:0,y:0,w:400,h:650};
 const fit=mediaPlacement(box,{width:1920,height:1080,face_zone:[0,0,400,500]},'avatar');
 assert.equal(fit.fit,'contain');
 assert.ok(fit.face.x>=box.x&&fit.face.x+fit.face.w<=box.x+box.w);
 const cover=mediaPlacement(box,{width:1080,height:1920,face_zone:[400,200,250,400]},'avatar');
 assert.equal(cover.fit,'cover');
 assert.ok(cover.face.y>=0&&cover.face.y+cover.face.h<=650);
 assert.equal(mediaPlacement(box,{width:1920,height:1080},'animation').fit,'contain');
});

test('styles skin the entire composition and semantic items retain exact source phrases',()=>{
 const collage=filmTheme('mg-collage'),line=filmTheme('mg-line-art');
 assert.notEqual(collage.canvas,line.canvas);assert.notEqual(collage.accent,line.accent);
 assert.equal(filmTheme('line-art').canvas,line.canvas);
 const items=semanticItems('首先确认声音。然后准备素材。最后完成合成。');
 assert.deepEqual(items,['首先确认声音','然后准备素材','最后完成合成']);
 assert.equal(resolveRecipe({card:'highlighter-sweep',layout:'pip'},{kind:'animation'}),'visual-pip');
 assert.equal(resolveRecipe({recipe:'steps',layout:'full'},{kind:'avatar'}),'steps');
});
