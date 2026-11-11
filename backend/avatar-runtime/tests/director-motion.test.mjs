import test from 'node:test';
import assert from 'node:assert/strict';
import {validateMotionCode,motionDocument,drawMotionText} from '../director-motion.mjs';
import {motionLabelCovered,checkMotionFrames,localMotionScene} from '../motion-checks.mjs';

test('local names do not become browser globals and cannot escape their scope',()=>{
 for(const code of ['const top=h*.1;ctx.fillRect(0,top,w,10);','function box(top){ctx.fillRect(0,top,w,10)}box(20);','{let top=20;ctx.fillRect(0,top,w,10)}'])assert.doesNotThrow(()=>validateMotionCode(code));
 for(const code of ['ctx.fillRect(0,top,w,10);','{let top=20;}ctx.fillRect(0,top,w,10);','function box(top){}ctx.fillRect(0,top,w,10);','const top=window.top;ctx.fillRect(0,top,w,10);'])assert.throws(()=>validateMotionCode(code));
});
test('numeric labels may be split into value/unit and caption without accepting a wrong number',()=>{
 assert.equal(motionLabelCovered('1.4 万亿美元估值',['1.4 万亿美元','估值']),true);
 assert.equal(motionLabelCovered('融资 300 亿美元',['300 亿美元','融资']),true);
 for(const labels of [['1300 亿美元','融资'],['300','融资'],['30 亿美元','融资'],['300 亿美元']])assert.equal(motionLabelCovered('融资 300 亿美元',labels),false);
 assert.equal(motionLabelCovered('1.4 万亿美元估值',['1','4 万亿美元','估值']),false);
});
test('dates with units between numbers are recognised when drawn verbatim',()=>{
 // Regression: "11 月 9 号" was reported missing on every frame although it was
 // drawn, because the unit-bearing token "11月" is followed by the digit 9.
 for(const label of ['11 月 9 号','11月9号','2026年11月','2026 年 11 月 9 日','3 月 15 日'])assert.equal(motionLabelCovered(label,[label]),true,label);
 assert.equal(motionLabelCovered('11 月 9 号',['11 月 9 号 发布']),true);
 assert.equal(motionLabelCovered('11 月 9 号',['11 月','9 号']),true);
 // Still reject a different date or a digit-extended number.
 for(const drawn of [['11 月 19 号'],['1 月 9 号'],['11 月 8 号'],['111 月 9 号']])assert.equal(motionLabelCovered('11 月 9 号',drawn),false,drawn[0]);
 assert.equal(motionLabelCovered('2026年11月',['2026年1月']),false);
});
test('coverage is checked within frames and requires a readable hold, not fragments from different times',()=>{
 const scene={quote:'融资 300 亿美元',start:0,end:2,elements:[{label:'融资 300 亿美元',at:1}]};
 const make=labels=>Array.from({length:50},(_,i)=>({time:i/25,labels:labels(i)}));
 assert.doesNotThrow(()=>checkMotionFrames(scene,make(()=>['300 亿美元','融资']),25,0));
 assert.throws(()=>checkMotionFrames(scene,make(i=>i<25?['300 亿美元']:['融资']),25,0),/未完整展示/);
 assert.throws(()=>checkMotionFrames(scene,make(i=>i===49?['融资 300 亿美元']:[]),25,0),/展示时间/);
 assert.throws(()=>checkMotionFrames({...scene,title:'融资 300 亿美元说明'},make(()=>['融资 300 亿美元说明']),25,0),/未完整展示/);
});
test('code receives an explicit scene duration and local element clock',()=>{
 const scene=localMotionScene({start:6.78,end:9.84525,elements:[{label:'缩水',at:9.02}]});
 assert.ok(Math.abs(scene.duration-3.06525)<1e-8);
 assert.ok(Math.abs(scene.elements[0].local_at-2.24)<1e-8);
 assert.ok(scene.elements[0].enter_at<=scene.duration-scene.hold_seconds);
 assert.equal(scene.start,6.78);assert.equal(scene.elements[0].at,9.02);
});
test('motion validation distinguishes data and comments from executable identifiers',()=>{
 for(const code of ["var kind='document';ctx.fillRect(0,0,10,10);", "// window document while constructor\nctx.fillText(scene.title,20,40);", "var text=`document`;ctx.fillRect(0,0,w,h);"])
  assert.equal(validateMotionCode(code),code);
 for(const code of ['document.title;ctx.fillRect(0,0,w,h);', 'var x=`${document.title}`;ctx.fillRect(0,0,w,h);', 'var x=docum\\u0065nt;ctx.fillRect(0,0,w,h);', 'ctx["canvas"].width=10;ctx.fillRect(0,0,w,h);', 'var x=ctx["con"+"structor"];ctx.fillRect(0,0,w,h);', 'Math["random"]();ctx.fillRect(0,0,w,h);', 'while(true){ctx.fillRect(0,0,w,h)}', 'ctx.fillRect(0,0,w,h); var = ;'])
  assert.throws(()=>validateMotionCode(code),code);
});
test('long generated labels wrap within canvas margins without changing text',()=>{
 const drawn=[];const ctx={font:'bold 24px sans-serif',textAlign:'left',textBaseline:'middle',measureText:text=>({width:[...text].length*20}),getTransform:()=>({a:1,b:0,c:0,d:1,e:0,f:0}),fillText:(text,x,y)=>drawn.push({text,x,y})};
 const quote='Bloomberg 援引知情人士报道';
 drawMotionText(ctx,'fillText',quote,quote,230,300,540);
 assert.equal(drawn.map(x=>x.text).join(''),quote);assert.ok(drawn.length>1);
 for(const line of drawn)assert.ok(line.x+ctx.measureText(line.text).width<=540*.94);
 assert.throws(()=>drawMotionText(ctx,'fillText',quote,'invented claim',0,0,540));
});
test('custom motion source runs only in an isolated canvas document',()=>{
 const code='ctx.fillStyle=palette[0];ctx.fillRect(0,0,w,h);ctx.fillText(scene.title,20,40);';
 assert.equal(validateMotionCode(code),code);
 const doc=motionDocument({width:360,height:640},code);
 assert.match(doc,/default-src 'none'/);assert.match(doc,/sandbox allow-scripts/);assert.match(doc,/Content-Security-Policy/);
 for(const value of ['fetch("https://example.com")','while(true){}','ctx.fillText(Date.now(),0,0)','import("fs")'])assert.throws(()=>validateMotionCode(value));
});
