import test from 'node:test';
import assert from 'node:assert/strict';
import {compileMotion,validateMotion,styleProfile} from '../content-motion.mjs';

const text='研发投入110亿元，算力规模扩大。下一步开放接口。';
const plan={version:'content-motion/1',tone:'cool',scenes:[
 {quote:'研发投入110亿元，算力规模扩大',title:'研发投入',layout:'data',elements:[{label:'110亿元',kind:'coin'},{label:'算力规模扩大',kind:'chip'}]},
 {quote:'下一步开放接口',title:'开放接口',layout:'steps',elements:[{label:'开放接口',kind:'code'}]}
]};
const animation={style:'mg-3d-render',width:720,height:1280,fps:25,duration:8,text,motion_plan:plan,words:[{text:'研发投入110亿元，算力规模扩大。',start:0,end:5.5},{text:'下一步开放接口。',start:5.5,end:8}]};

test('a later material rebases scene and element times instead of dropping valuation',()=>{
 const result=compileMotion({...animation,duration:3,narration_window:{start:5,end:8}});
 assert.equal(result.scenes.length,2);
 assert.equal(result.scenes[0].start,0);
 assert.equal(result.scenes[1].start,.5);
 assert.equal(result.scenes[1].elements[0].at,compileMotion(animation).scenes[1].elements[0].at-5);
 assert.equal(result.scenes[1].end,3);
});

test('required excerpt outside a material window is an error, not a silent omission',()=>{
 assert.throws(()=>compileMotion({...animation,duration:3,narration_window:{start:0,end:3},required_quotes:['下一步开放接口']}),/时间|范围|覆盖/);
});
test('content scenes follow measured narration instead of a ten-second demo clock',()=>{
 const result=compileMotion(animation);
 assert.equal(result.scenes[1].start,5.5);assert.equal(result.scenes.at(-1).end,8);
 assert.equal(result.scenes[0].elements[0].label,'110亿元');assert.equal(result.mode,'content-motion');
 assert.throws(()=>compileMotion({...animation,motion_plan:undefined}),/方案/);
});
test('every selected style has an actual content rendering treatment',()=>{
 const ids=['cel-boil','isometric','flat-vector','line-art','3d-render','morph','paperclip','hud','collage','aurora-glass','bauhaus','synthwave','pixel','liquid','hanazi'];
 assert.equal(new Set(ids.map(id=>styleProfile('mg-'+id).treatment)).size,15);
 for(const id of ids)assert.equal(compileMotion({...animation,style:'mg-'+id}).mode,'content-motion');
});

test('avatar warm/cool context chooses the matching accent inside the selected palette',()=>{
 const make=tone=>compileMotion({...animation,motion_plan:{...plan,tone}});
 assert.equal(make('warm').accent,'#E7A79C');
 assert.equal(make('cool').accent,'#A28BDF');
 assert.equal(make('neutral').accent,'#A28BDF');
});

test('headlines stay brief instead of duplicating an entire narration sentence',()=>{
 const long='这是一段必须使用短标题来概括的较长口播句子避免整句占满画面';
 const p={...plan,scenes:[{quote:long,title:long,layout:'highlight',elements:[{label:'短标题',kind:'document'}]}]};
 assert.throws(()=>validateMotion(p,long),/标题/);
});
test('demo copy, unsupported graphics and fabricated data cannot reach the renderer',()=>{
 const bad=structuredClone(plan);bad.scenes[0].elements[0].label='SOFT';
 assert.throws(()=>validateMotion(bad,text));
 bad.scenes[0].elements[0]={label:'999亿元',kind:'coin'};assert.throws(()=>validateMotion(bad,text));
 bad.scenes[0].elements[0]={label:'110亿元',kind:'script'};assert.throws(()=>validateMotion(bad,text));
});
