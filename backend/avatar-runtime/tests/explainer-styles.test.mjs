import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {compileMotion,styleProfile} from '../content-motion.mjs';
import {filmTheme} from '../src/film-design.ts';

const styles=JSON.parse(fs.readFileSync(new URL('../explainer-styles.json',import.meta.url),'utf8'));
test('all six explainer selections reach both animation and compositing with their own palette',()=>{
 assert.equal(styles.length,6);
 assert.equal(new Set(styles.map(s=>s.id)).size,6);
 for(const style of styles){
  const profile=styleProfile(style.id);
  assert.deepEqual(filmTheme(style.id).canvas,profile.palette[0]);
  const animation=compileMotion({style:style.id,width:360,height:640,fps:25,duration:4,text:'先输入内容，再查看结果。',words:[],motion_plan:{version:'content-motion/1',tone:'neutral',scenes:[{quote:'先输入内容，再查看结果',title:'输入内容',layout:'steps',elements:[{label:'输入内容',kind:'document'},{label:'查看结果',kind:'code'}]}]}});
  assert.equal(animation.style,style.id);
  assert.deepEqual(animation.profile.palette,profile.palette);
  const method=fs.readFileSync(new URL(`../templates/${style.id}/production.md`,import.meta.url),'utf8');
  assert.match(method,/不新增 TTS/);
  assert.match(method,/当前/);
 }
});
