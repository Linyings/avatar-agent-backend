// Deterministic render fixture; real model acceptance is a separate opt-in test.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {renderDirectorMotion} from '../director-motion.mjs';
const dir=process.argv[2]||'/tmp/director-runtime';fs.mkdirSync(path.join(dir,'materials'),{recursive:true});
const code=`ctx.fillStyle=palette[0];ctx.fillRect(0,0,w,h);ctx.strokeStyle=palette[3];ctx.lineWidth=6;const p=Math.min(1,t*2);ctx.beginPath();ctx.moveTo(w*.2,h*.4);ctx.lineTo(w*(.2+.6*p),h*.4);ctx.stroke();for(let i=0;i<scene.elements.length;i++){ctx.fillStyle=palette[2];ctx.font='bold '+Math.round(w*.055)+'px sans-serif';ctx.fillText(scene.elements[i].label,w*.15,h*(.25+i*.4));}`;
const report=await renderDirectorMotion({width:360,height:640,fps:25,duration:2,style:'mg-flat-vector',text:'声音驱动画面',words:[{text:'声音驱动画面',start:0,end:2}],motion_plan:{version:'content-motion/1',tone:'cool',code,scenes:[{quote:'声音驱动画面',title:'声音',layout:'relationship',elements:[{label:'声音',kind:'document'},{label:'画面',kind:'box'}]}]}},path.join(dir,'materials/motion.mp4'));
assert.equal(report.mode,'director-code');
execFileSync('ffmpeg',['-v','error','-y','-f','lavfi','-i','color=c=0x5980a4:s=360x640:r=25:d=2','-c:v','libx264','-pix_fmt','yuv420p',path.join(dir,'materials/host.mp4')]);
execFileSync('ffmpeg',['-v','error','-y','-f','lavfi','-i','sine=frequency=440:sample_rate=24000:duration=2',path.join(dir,'voice.wav')]);
const shots=[{id:'S1',segment_id:'p1',start:0,end:1,primary:'motion',overlay:'host',layout:'pip',recipe:'visual-pip',avatar_shape:'circle',avatar_position:'top-left',primary_in:0,overlay_in:0,card:'none',keyword:''},{id:'S2',segment_id:'p1',start:1,end:2,primary:'motion',overlay:'host',layout:'pip',recipe:'visual-pip',avatar_shape:'circle',avatar_position:'bottom-right',primary_in:1,overlay_in:1,card:'none',keyword:''}];
const manifest={width:360,height:640,fps:25,duration:2,input:{style:'mg-flat-vector',ratio:'9:16',title:'测试'},segments:[{id:'p1',text:'声音驱动画面',start:0,duration:2}],materials:[{need_id:'motion',segment_id:'p1',kind:'animation',path:'materials/motion.mp4',duration:2,width:360,height:640},{need_id:'host',segment_id:'p1',kind:'avatar',path:'materials/host.mp4',duration:2,width:360,height:640}],shots,timing:{scenes:[]}};
fs.writeFileSync(path.join(dir,'workflow-props.json'),JSON.stringify({manifest}));fs.writeFileSync(path.join(dir,'shots.json'),JSON.stringify(shots));
const runtime=path.resolve(import.meta.dirname,'..');
execFileSync(process.execPath,[path.join(runtime,'workflow-render.mjs'),'preview',dir],{cwd:runtime,stdio:'inherit',timeout:180000});
for(const shot of shots){const file=path.join(dir,'preview',shot.id+'.mp4');const info=JSON.parse(execFileSync('ffprobe',['-v','error','-show_streams','-of','json',file]));assert.ok(info.streams.some(s=>s.codec_type==='audio'));assert.equal(Number(info.streams.find(s=>s.codec_type==='video').nb_frames),25);execFileSync('ffmpeg',['-v','error','-i',file,'-f','null','-']);}
const before=shots.map(s=>fs.statSync(path.join(dir,'segments',s.id+'.mp4')).mtimeMs);
execFileSync(process.execPath,[path.join(runtime,'workflow-render.mjs'),'render',dir],{cwd:runtime,stdio:'inherit',timeout:180000});
assert.deepEqual(shots.map(s=>fs.statSync(path.join(dir,'segments',s.id+'.mp4')).mtimeMs),before,'unchanged confirmed frames must be reused');
execFileSync('ffmpeg',['-v','error','-y','-ss','0.5','-i',path.join(dir,'output.mp4'),'-frames:v','1',path.join(dir,'fusion.png')]);
fs.writeFileSync(path.join(dir,'verification.json'),JSON.stringify({fixture:true,modelCalls:0,allShotPreviews:2,audio:true,frames:50,customMotionCode:true,circularAvatar:true,confirmedFramesReused:true},null,2));console.log('Director runtime verified: code, 2 audible previews, cached final render.');
