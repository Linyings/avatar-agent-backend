// Opt-in integration check: render actual upstream sources, never model calls.
import fs from 'node:fs';
import path from 'node:path';
import {nativeStyles,renderNative} from '../mg-native.mjs';
const output=process.argv[2]||'/tmp/mg-source-smoke';
fs.mkdirSync(output,{recursive:true});const prior=process.env.MG_RESUME&&fs.existsSync(path.join(output,'report.json'))?JSON.parse(fs.readFileSync(path.join(output,'report.json'))):[];
const results=Object.values(Object.fromEntries(prior.map(r=>[r.id,r])));
for(const s of nativeStyles){
 if(results.some(r=>r.id===s.id&&r.status==='passed'&&r.mode===s.production_mode))continue;
 const began=Date.now();
 try{
  const folder=path.join(output,s.id);
  const a={style:s.id,width:s.preview_height>s.preview_width?270:480,height:s.preview_height>s.preview_width?480:270,fps:25,duration:10,title:'动效模板测试',text:'直接复用原始源码。保留原始图形与运镜。',words:[]};
  const report=await renderNative(a,folder,{frameSamples:[1,5,9]});
  results.push({id:s.id,status:'passed',mode:report.mode,elapsed_ms:Date.now()-began});
 }catch(e){results.push({id:s.id,status:'failed',error:e.message,elapsed_ms:Date.now()-began});}
 console.log(JSON.stringify(results.at(-1)));
 fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(results,null,2));
}
fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(results,null,2));
if(results.some(r=>r.status!=='passed'))process.exitCode=1;
