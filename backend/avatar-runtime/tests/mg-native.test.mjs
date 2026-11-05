import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import crypto from 'node:crypto';
import {nativeStyles,prepareNativeSource,sceneClock,validateAnimation,makeContent} from '../mg-native.mjs';

test('all fifteen original templates expose original previews and their actual production method',()=>{
 assert.equal(nativeStyles.length,15);
 assert.equal(new Set(nativeStyles.map(s=>s.source)).size,15);
 for(const s of nativeStyles){assert.match(s.id,/^mg-/);assert.ok(s.entry);assert.ok(s.signature);assert.equal(s.preview_kind,'upstream-original');assert.ok(['native-source','original-video'].includes(s.production_mode));assert.ok(s.preview_width>0&&s.preview_height>0);}
 assert.equal(nativeStyles.find(s=>s.source==='18-hanazi').preview_width,1080);
 const renderer=fs.readFileSync(new URL('../workflow-render.mjs',import.meta.url),'utf8');
 assert.ok(!renderer.includes("inputProps.animation.render_mode==='native-demo'&&"),'MG selection must never silently fall back to semantic graphics');
 const shell=fs.readFileSync(new URL('../mg-shell.js',import.meta.url),'utf8');
 assert.ok(!shell.includes('title.textContent'),'no extra application title over original composition');
});
test('production substitutes original brands and reads actual content without executing it',()=>{
 const a={style:'mg-flat-vector',width:720,height:1280,fps:25,duration:6.7,title:'测试主题',text:'先确认声音。再制作动画。',brief:'解释声音到动画的流程',words:[]};
 const content=makeContent(a);
 assert.equal(content.title,'测试主题');assert.ok(content.phrases.includes('先确认声音'));
 const html=prepareNativeSource('<body><script>const WORD = "popwıse";</script></body>','text/html',nativeStyles[0],content);
 assert.ok(html.includes('window.MG_CONTENT'));assert.ok(html.includes('测试主题'));
 const malicious=prepareNativeSource('<body></body>','text/html',nativeStyles[0],makeContent({...a,title:'</script><script>alert(1)</script>'}));
 assert.ok(!malicious.includes('<script>alert(1)</script>'));
});
test('animation clocks use measured duration with no looping or unbounded samples',()=>{
 assert.equal(sceneClock(0,20),0);assert.equal(sceneClock(10,20),5);
 assert.equal(sceneClock(20,20),9.96);assert.equal(sceneClock(99,20),9.96);
 assert.throws(()=>validateAnimation({style:'../../etc',width:720,height:1280,fps:25,duration:5}));
 assert.throws(()=>validateAnimation({style:'mg-line-art',width:720,height:1280,fps:25,duration:NaN}));
});
test('imported sources match provenance and production removes original brand drawing',()=>{
 for(const s of nativeStyles){
  const info=JSON.parse(fs.readFileSync(new URL(`../templates/${s.id}/SOURCE.json`,import.meta.url)));
  assert.equal(crypto.createHash('sha256').update(fs.readFileSync(new URL(`../catalog/${s.id}.mp4`,import.meta.url))).digest('hex'),info.reference_sha256);
  assert.equal(crypto.createHash('sha256').update(fs.readFileSync(new URL(`../catalog/${s.id}.jpg`,import.meta.url))).digest('hex'),info.poster_sha256);
  assert.equal(crypto.createHash('sha256').update(fs.readFileSync(new URL(`../templates/${s.id}/original-prompt.md`,import.meta.url))).digest('hex'),info.prompt_sha256);
  for(const [name,digest] of Object.entries(info.files))assert.equal(crypto.createHash('sha256').update(fs.readFileSync(new URL('../mg-upstream/'+name,import.meta.url))).digest('hex'),digest);
 }
 const s=nativeStyles.find(s=>s.id==='mg-morph');
 const source=fs.readFileSync(new URL('../mg-upstream/'+s.entry,import.meta.url),'utf8');
 const result=prepareNativeSource(source,'text/html',s,makeContent({title:'当前主题',text:'本片实际文案。',brief:'解释当前内容',words:[],duration:8}));
 assert.ok(result.includes('window.MG_TITLE(ctx,t);'));assert.ok(!result.includes('  drawLetters(t);'));
 assert.ok(result.includes('本片实际文案'));
});
