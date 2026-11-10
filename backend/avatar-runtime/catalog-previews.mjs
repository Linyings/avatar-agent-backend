import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';

// Derived gallery media only. Original demonstrations and render inputs remain
// untouched; transcoding happens at build time, never on a browsing request.
const profile={version:1,maxEdge:480,fps:24,crf:28,maxrate:'400k',bufsize:'800k'};
const hash=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const scale=`scale=w='min(${profile.maxEdge},iw)':h='min(${profile.maxEdge},ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1`;
export function buildCardPreview({source,poster,output,thumbnail,seconds,start=0}){
 const paths=[source,poster,output,thumbnail].map(p=>path.resolve(p));
 if(new Set(paths).size!==4)throw new Error('Card previews must not overwrite original media');
 const fingerprint=crypto.createHash('sha256').update(JSON.stringify([profile,hash(source),hash(poster),seconds??null,start])).digest('hex');
 const stamp=output+'.json';
 try{
  const saved=JSON.parse(fs.readFileSync(stamp,'utf8'));
  if(saved.fingerprint===fingerprint&&fs.statSync(output).size===saved.bytes&&fs.statSync(thumbnail).size===saved.posterBytes)return {...saved,cached:true};
 }catch{/* Missing or stale derived files are rebuilt. */}
 fs.mkdirSync(path.dirname(output),{recursive:true});fs.mkdirSync(path.dirname(thumbnail),{recursive:true});
 const token=crypto.randomUUID(),videoTmp=output+'.'+token+'.tmp.mp4',posterTmp=thumbnail+'.'+token+'.tmp.jpg';
 try{
  execFileSync('ffmpeg',['-v','error','-y',...(start?['-ss',String(start)]:[]),'-i',source,...(seconds?['-t',String(seconds)]:[]),'-map','0:v:0','-an','-sn','-dn','-map_metadata','-1','-vf',`${scale},fps=${profile.fps}`,'-c:v','libx264','-preset','slow','-crf',String(profile.crf),'-maxrate',profile.maxrate,'-bufsize',profile.bufsize,'-pix_fmt','yuv420p','-threads','2','-movflags','+faststart',videoTmp]);
  execFileSync('ffmpeg',['-v','error','-y','-i',poster,'-vf',scale,'-frames:v','1','-q:v','6','-threads','2',posterTmp]);
  fs.renameSync(videoTmp,output);fs.renameSync(posterTmp,thumbnail);
  const result={fingerprint,profile,bytes:fs.statSync(output).size,posterBytes:fs.statSync(thumbnail).size,sourceBytes:fs.statSync(source).size,cached:false};
  fs.writeFileSync(stamp,JSON.stringify(result));return result;
 }finally{
  for(const file of [videoTmp,posterTmp]){try{fs.unlinkSync(file)}catch(error){if(error.code!=='ENOENT')throw error}}
 }
}

export function buildCardCatalog(root,styles){
 return styles.map(style=>{
  const base=path.join(root,'catalog',style.id);
  return {id:style.id,...buildCardPreview({source:base+'.mp4',poster:base+'.jpg',output:base+'-card.mp4',thumbnail:base+'-card.jpg'})};
 });
}

export function buildExplainerCatalog(root,styles){
 return styles.map(style=>{
  if(!/^explain-[a-z0-9-]+$/.test(style.id))throw new Error('Invalid explainer catalog ID');
  const base=path.join(root,'catalog',style.id),poster=base+'-reference.jpg';
  if(!fs.existsSync(poster))execFileSync('ffmpeg',['-v','error','-y','-i',base+'-reference.webp','-frames:v','1','-q:v','2','-threads','1',poster]);
  // A poster-only source must never masquerade as a moving preview.
  if(!fs.existsSync(base+'-reference.mp4'))return {id:style.id,posterOnly:true};
  const start=style.preview_start??0,frame=base+'-preview-frame.jpg';
  const key=JSON.stringify([hash(base+'-reference.mp4'),start+2]);
  if(!fs.existsSync(frame)||!fs.existsSync(frame+'.json')||fs.readFileSync(frame+'.json','utf8')!==key){
   execFileSync('ffmpeg',['-v','error','-y','-ss',String(start+2),'-i',base+'-reference.mp4','-frames:v','1','-q:v','2','-threads','1',frame]);
   fs.writeFileSync(frame+'.json',key);
  }
  return {id:style.id,...buildCardPreview({source:base+'-reference.mp4',poster:frame,output:base+'-card.mp4',thumbnail:base+'-card.jpg',seconds:12,start})};
 });
}
