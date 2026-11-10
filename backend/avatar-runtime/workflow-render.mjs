import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync,spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {bundle} from '@remotion/bundler';
import {selectComposition,renderMedia} from '@remotion/renderer';
import {compileMotion} from './content-motion.mjs';
import {buildCardCatalog} from './catalog-previews.mjs';
import {renderDirectorMotion} from './director-motion.mjs';
import {printMotionError} from './motion-checks.mjs';
import {avatarFramingReport} from './src/framing-report.ts';
const root=path.dirname(fileURLToPath(import.meta.url));
const nativeStyles=JSON.parse(fs.readFileSync(path.join(root,'native-styles.json'),'utf8'));
const [mode,taskArg,outArg]=process.argv.slice(2);const task=path.resolve(taskArg||root);
if(mode==='animation'){
 const inputProps=JSON.parse(fs.readFileSync(path.join(task,'animation-props.json'),'utf8'));
 if(inputProps.animation.motion_plan?.code){try{const report=await renderDirectorMotion(inputProps.animation,outArg);fs.writeFileSync(path.join(task,'animation-native-report.json'),JSON.stringify(report));process.exit(0);}catch(error){printMotionError(error);process.exit(1);}}
 const content=String(inputProps.animation.style).startsWith('mg-')?compileMotion(inputProps.animation):undefined;
 if(content)inputProps.animation.content_motion=content;
 const serveUrl=await bundle({entryPoint:path.join(root,'src/workflow-entry.tsx'),publicDir:task});
 const composition=await selectComposition({serveUrl,id:'AnimationTemplate',inputProps});
 await renderMedia({serveUrl,composition,inputProps,codec:'h264',muted:true,outputLocation:outArg,concurrency:1,overwrite:true});
 fs.writeFileSync(path.join(task,'animation-native-report.json'),JSON.stringify(content?{...content,source:'current narration and avatar context; style reference only',demo_video_used:false}:{mode:'semantic',version:'film-design/1',style:inputProps.animation.style,template:inputProps.animation.template,duration:inputProps.animation.duration}));
 process.exit(0);
}
if(mode==='catalog'){
 fs.mkdirSync(path.join(root,'catalog'),{recursive:true});
 for(const style of ['flat-vector','line-art']){
  const scratch=path.join(root,'.cache','catalog-'+style);fs.mkdirSync(scratch,{recursive:true});
  fs.writeFileSync(path.join(scratch,'animation-props.json'),JSON.stringify({animation:{width:360,height:640,fps:25,duration:4,style,template:'steps',text:'确定主题，锁定配音，准备素材，组合成片',words:[]}}));
  execFileSync(process.execPath,[fileURLToPath(import.meta.url),'animation',scratch,path.join(root,'catalog',style+'.mp4')],{cwd:root,stdio:'inherit'});
  execFileSync('ffmpeg',['-v','error','-y','-ss','1.5','-i',path.join(root,'catalog',style+'.mp4'),'-frames:v','1',path.join(root,'catalog',style+'.jpg')]);
 }
 for(const style of nativeStyles){
  // The gallery is the actual upstream film and poster, never a new drawing.
  for(const ext of ['mp4','jpg'])fs.copyFileSync(path.join(root,'catalog',style.id+'-reference.'+ext),path.join(root,'catalog',style.id+'.'+ext));
 }
 execFileSync('python3',[path.join(root,'package_mg_styles.py')],{stdio:'inherit'});
 const cards=buildCardCatalog(root,nativeStyles);
 console.log(JSON.stringify({card_previews:cards.length,bytes:cards.reduce((sum,item)=>sum+item.bytes,0)}));
 process.exit(0);
}
const props=JSON.parse(fs.readFileSync(path.join(task,'workflow-props.json'),'utf8'));const manifest=props.manifest;
const shots=manifest.shots;
const segDir=path.join(task,'segments');fs.mkdirSync(segDir,{recursive:true});
const paths=fs.existsSync(path.join(task,'asset-paths.json'))?JSON.parse(fs.readFileSync(path.join(task,'asset-paths.json'),'utf8')):{};
const shared=paths.__cache?path.join(paths.__cache,'renders'):path.join(task,'render-cache');fs.mkdirSync(shared,{recursive:true});
const hash=(buffer)=>crypto.createHash('sha256').update(buffer).digest('hex');
const sources=fs.readdirSync(path.join(root,'src'),{recursive:true}).filter(s=>/\.(tsx|ts|mjs)$/.test(s)).map(s=>fs.readFileSync(path.join(root,'src',s)));
const sourceHash=hash(Buffer.concat(sources));
const fingerprints=new Map();
for(const shot of shots){
 const segment=manifest.segments.find(s=>s.id===shot.segment_id);
 const deps=[shot.primary,shot.overlay].filter(Boolean).map(id=>{const m=manifest.materials.find(m=>m.need_id===id);return [m,hash(fs.readFileSync(path.join(task,m.path)))];});
 const fp=hash(JSON.stringify([sourceHash,shot,deps,segment,manifest.input.style,manifest.input.ratio,manifest.input.title,manifest.input.subtitle_color,manifest.input.subtitle_scale,manifest.input.avatar_position,(manifest.caption_timing||manifest.timing).scenes.filter(s=>s.startSec>=segment.start-.01&&s.startSec<segment.start+segment.duration)]));
 fingerprints.set(shot.id,fp);
 const cached=path.join(shared,fp+'.mp4'),local=path.join(segDir,shot.id+'.mp4'),stamp=path.join(segDir,shot.id+'.hash');
 if(fs.existsSync(local)&&(!fs.existsSync(stamp)||fs.readFileSync(stamp,'utf8')!==fp))fs.unlinkSync(local);
 if(!fs.existsSync(local)&&fs.existsSync(cached)){fs.copyFileSync(cached,local);fs.writeFileSync(stamp,fp);}
}
const script=path.join(root,'talkcraft/flow/scripts/render_shots.mjs');
const args=[script,'--shots',path.join(task,'shots.json'),'--entry','src/workflow-entry.tsx','--comp','AvatarWorkflow','--props','@'+path.join(task,'workflow-props.json'),'--public-dir',task,'--seg-dir',segDir,'--parallel','1'];
if(mode!=='preview')args.push('--concat',path.join(task,'assembled.mp4'),'--audio',path.join(task,'full-mix.wav'),'--audio-concurrency','1','--preview-dir',path.join(task,'preview'),'--mux',path.join(task,'output.mp4'));
const proc=spawnSync(process.execPath,args,{cwd:root,encoding:'utf8',maxBuffer:16*1024*1024});
const output=(proc.stdout||'')+'\n'+(proc.stderr||'');
fs.writeFileSync(path.join(task,'render.log'),output);
if(proc.status!==0)throw new Error('talkcraft rendering failed; inspect task render.log');
if(mode==='preview'){
 fs.mkdirSync(path.join(task,'preview'),{recursive:true});
 for(const shot of shots){execFileSync('ffmpeg',['-v','error','-y','-i',path.join(segDir,shot.id+'.mp4'),'-ss',String(shot.start),'-i',path.join(task,'voice.wav'),'-t',String(shot.end-shot.start),'-map','0:v:0','-map','1:a:0','-c:v','copy','-c:a','aac','-b:a','192k','-movflags','+faststart',path.join(task,'preview',shot.id+'.mp4')]);}
}
const framing=avatarFramingReport(manifest,shots);
const framingWarning=r=>r.mode==='tight'?`${r.shot_id}：源片取景较近，已按人脸裁切填满人物窗口，头顶或肩部可能被少量裁去`:`${r.shot_id}：人物定位不足，已按上半身居中裁切填满窗口，请复核取景`;
fs.writeFileSync(path.join(task,'render-quality.json'),JSON.stringify({warnings:[...output.split('\n').filter(s=>s.includes('QUALITY:')),...framing.filter(r=>r.mode!=='tracked').map(framingWarning)],avatar_framing:framing}));
for(const shot of shots){const local=path.join(segDir,shot.id+'.mp4');if(fs.existsSync(local)){const fp=fingerprints.get(shot.id);fs.copyFileSync(local,path.join(shared,fp+'.mp4'));fs.writeFileSync(path.join(segDir,shot.id+'.hash'),fp);}}
