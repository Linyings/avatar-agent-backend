import test from 'node:test';
import assert from 'node:assert/strict';
import {filmTheme,shotLayout,overlaps,mediaPlacement,semanticItems,resolveRecipe,captionPage,resolveComposition} from '../src/film-design.ts';

const kindsOf=map=>id=>map[id];
test('side-by-side puts the motion in the large box and the presenter in a narrow column',()=>{
 for(const side of ['right','left']){
  const l=shotLayout(1280,720,'visual-split',side);
  assert.ok(l.primary.w>=1280*.62,`motion too narrow: ${l.primary.w}`);
  assert.ok(l.overlay.w<=1280*.28,`presenter too wide: ${l.overlay.w}`);
  assert.ok(!overlaps(l.primary,l.overlay));assert.ok(!overlaps(l.caption,l.primary));assert.ok(!overlaps(l.caption,l.overlay));
  assert.equal(l.overlay.x>l.primary.x,side==='right');
 }
 const p=shotLayout(720,1280,'visual-split','right');
 assert.ok(p.primary.h>p.overlay.h*1.8,'portrait: motion leads vertically');
 assert.ok(!overlaps(p.primary,p.overlay)&&!overlaps(p.caption,p.overlay));
});
test('a host shot with motion beside it is rendered motion-first',()=>{
 const kinds=kindsOf({host:'avatar',anim:'animation',real:'real'});
 // The planner's old split: presenter primary, motion in the side panel.
 const c=resolveComposition({primary:'host',overlay:'anim',primary_in:2,overlay_in:.5,recipe:'host-explain',layout:'split',card:'none'},kinds,'explain-podcast');
 assert.equal(c.recipe,'visual-split');assert.equal(c.primary,'anim');assert.equal(c.overlay,'host');
 assert.equal(c.primaryIn,.5);assert.equal(c.overlayIn,2);
 assert.equal(resolveComposition({primary:'host',overlay:'real',layout:'pip',card:'none'},kinds,'x').recipe,'visual-pip');
 assert.equal(resolveComposition({primary:'anim',overlay:'host',recipe:'visual-split',card:'none'},kinds,'x').recipe,'visual-split');
 assert.equal(resolveComposition({primary:'anim',overlay:'host',recipe:'visual-pip',card:'none'},kinds,'x').recipe,'visual-pip');
 // Motion alone, narration only: no presenter at all.
 const solo=resolveComposition({primary:'anim',recipe:'visual-hero',card:'none'},kinds,'explain-podcast');
 assert.equal(solo.recipe,'visual-hero');assert.equal(solo.overlay,undefined);
 assert.equal(resolveComposition({primary:'host',card:'none'},kinds,'explain-podcast').recipe,'host-hero');
});

test('captions keep decimal amounts, units and Chinese words on the same page',()=>{
 const text='这轮融资目前还没有完全关闭，投后估值来到1.25万亿美元。';
 const chars=[...text].map((ch,i)=>({ch,t:i*.2,e:i*.2+.18}));
 for(const budget of [12,13,14,15,16,17,18]){
  const pages=[...new Set(chars.map(c=>captionPage(chars,c.t,budget).map(x=>x.ch).join('')))];
  assert.equal(pages.join(''),text);
  assert.ok(pages.some(p=>p.includes('关闭')));
  assert.ok(pages.some(p=>p.includes('1.25万亿美元')));
 }
});

test('all recipes have usable non-overlapping content and caption regions in three ratios',()=>{
 for(const [w,h] of [[1280,720],[720,1280],[720,720]])for(const recipe of ['host-hero','host-explain','visual-hero','visual-pip','visual-split','compare','steps','evidence','summary']){
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

test('talking heads always fill their box and other media preserve mismatched ratios',()=>{
 const box={x:0,y:0,w:400,h:650};
 // Untracked host footage fills its box with a top-biased crop, never letterboxing.
 for(const source of [{width:1920,height:1080,face_zone:[0,0,400,500]},{width:720,height:1280},{width:720,height:1280,face_zone:[154,0,379,782]}]){
  const fit=mediaPlacement(box,source,'avatar');
  assert.equal(fit.fit,'cover');
  assert.equal(fit.position,'50% 28%');
 }
 const cover=mediaPlacement(box,{width:1080,height:1920,face_zone:[400,200,250,400]},'avatar');
 assert.equal(cover.fit,'cover');
 assert.ok(cover.face.y>=0&&cover.face.y+cover.face.h<=650);
 assert.equal(mediaPlacement(box,{width:1920,height:1080},'animation').fit,'contain');
});

test('styles skin the entire composition and semantic items retain exact source phrases',()=>{
 const collage=filmTheme('mg-collage'),line=filmTheme('mg-line-art');
 assert.notEqual(collage.canvas,line.canvas);assert.notEqual(collage.accent,line.accent);
 assert.ok(filmTheme('line-art').canvas);assert.notEqual(filmTheme('mg-hud').canvas,filmTheme('mg-3d-render').canvas);
 const items=semanticItems('首先确认声音。然后准备素材。最后完成合成。');
 assert.deepEqual(items,['首先确认声音','然后准备素材','最后完成合成']);
 assert.equal(resolveRecipe({card:'highlighter-sweep',layout:'pip'},{kind:'animation'}),'visual-pip');
 assert.equal(resolveRecipe({recipe:'steps',layout:'full'},{kind:'avatar'}),'steps');
});
