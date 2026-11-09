// Uses recorded incident data and deterministic local fixtures. No model calls,
// task mutations, or overwriting of the original material.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {renderDirectorMotion,validateMotionCode} from '../director-motion.mjs';
import {compileMotion} from '../content-motion.mjs';
const [source,rejectedFile,output]=process.argv.slice(2);
const a=JSON.parse(fs.readFileSync(source)).animation;
fs.mkdirSync(output,{recursive:true});
const original=await renderDirectorMotion(a,null,{sceneIndex:0});
assert.deepEqual(original.text_checks[0].missing_labels,[]);
let late;
try{await renderDirectorMotion(a,null,{sceneIndex:1})}catch(e){late=e.diagnostic}
assert.equal(late?.code,'motion_text_missing');assert.ok(late.missing_labels.includes('缩水'));
validateMotionCode(JSON.parse(fs.readFileSync(rejectedFile)).code);
const plan=compileMotion(a);
assert.ok(Math.abs((plan.scenes[1].end-plan.scenes[1].start)-3.06525)<.001);
const fixture=structuredClone(a);
fixture.width=640;fixture.height=360;
fixture.motion_plan.code=`ctx.fillStyle=palette[0];ctx.fillRect(0,0,w,h);ctx.fillStyle=palette[2];ctx.font='24px sans-serif';for(let i=0;i<scene.elements.length;i++){const e=scene.elements[i],p=Math.min(1,Math.max(0,(t-e.enter_at)/Math.min(.2,scene.duration*.1)));ctx.globalAlpha=p;ctx.fillText(e.label,w*.12,h*(.24+i*.2));}ctx.globalAlpha=1;`;
await renderDirectorMotion(fixture,null);
const video=path.join(output,'timing-fixture.mp4');
const result=await renderDirectorMotion(fixture,video);
const info=JSON.parse(execFileSync('ffprobe',['-v','error','-show_streams','-of','json',video]));
assert.equal(Number(info.streams.find(s=>s.codec_type==='video').nb_frames),247);
execFileSync('ffmpeg',['-v','error','-i',video,'-f','null','-']);
const report={modelCalls:0,originalSplitLabelsPassed:true,originalLocalVariablePassed:true,lateAnimation:late,fixtureOnly:true,encodedFrames:247,textChecks:result.text_checks};
fs.writeFileSync(path.join(output,'verification.json'),JSON.stringify(report,null,2));
console.log(JSON.stringify(report,null,2));
