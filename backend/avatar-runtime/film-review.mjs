// Existing-media visual regression. Generates no model content or paid orders.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {bundle} from '@remotion/bundler';
import {selectComposition,renderStill} from '@remotion/renderer';
const root=path.dirname(fileURLToPath(import.meta.url)),task=path.resolve(process.argv[2]);
const source=JSON.parse(fs.readFileSync(path.join(task,'workflow-props.json'),'utf8')).manifest;
const output=path.join(task,'matrix');fs.mkdirSync(output,{recursive:true});
const serveUrl=await bundle({entryPoint:path.join(root,'src/workflow-entry.tsx'),publicDir:task});
const host=source.materials.find(m=>m.kind==='avatar'),visual=source.materials.find(m=>m.kind==='animation');
if(!host||!visual)throw new Error('Matrix requires actual host and animation footage');
const segment=source.segments.find(s=>s.id===host.segment_id);
for(const [width,height] of [[960,540],[540,960],[720,720]]){
 for(const recipe of ['host-hero','host-explain','visual-hero','visual-pip','compare','steps','evidence','summary']){
  const isVisual=recipe.startsWith('visual-');const primary=isVisual?visual:host;
  const seg=source.segments.find(s=>s.id===primary.segment_id);
  const shot={id:'S1',segment_id:seg.id,start:0,end:Math.min(4,primary.duration,host.duration),primary:primary.need_id,recipe,card:recipe==='host-hero'?'keyword':'none',keyword:seg.chapter,layout:recipe==='visual-pip'?'pip':'full',items:seg.text.split(/[。；！？\n，、]/).filter(Boolean).slice(0,4),primary_in:0,overlay_in:0};
  if(recipe==='visual-pip')shot.overlay=host.need_id;
  const timing={scenes:source.timing.scenes.filter(s=>Math.abs(s.startSec-seg.start)<.1).map(s=>({...s,startSec:s.startSec-seg.start,chars:s.chars.map(c=>({...c,t:c.t-seg.start,e:c.e-seg.start}))}))};
  const manifest={...source,width,height,duration:shot.end,segments:source.segments.map(s=>({...s,start:s.id===seg.id?0:s.start})),shots:[shot],timing,caption_timing:timing};
  const inputProps={manifest},composition=await selectComposition({serveUrl,id:'AvatarWorkflow',inputProps});
  await renderStill({serveUrl,composition,inputProps,frame:Math.min(70,Math.floor(shot.end*25)-1),output:path.join(output,`${recipe}-${width}x${height}.jpg`),imageFormat:'jpeg',overwrite:true});
 }
}
console.log(JSON.stringify({matrix_frames:24,source:'existing real media; visual regression only'}));
