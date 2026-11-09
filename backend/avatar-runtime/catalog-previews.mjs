import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';

// Derived gallery media only. Original demonstrations and render inputs remain
// untouched; transcoding happens at build time, never on a browsing request.
const profile={version:1,maxEdge:480,fps:24,crf:28,maxrate:'400k',bufsize:'800k'};
const hash=file=>crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const scale=`scale=w='min(${profile.maxEdge},iw)':h='min(${profile.maxEdge},ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1`;
export function buildCardPreview({source,poster,output,thumbnail}){
 const paths=[source,poster,output,thumbnail].map(p=>path.resolve(p));
 if(new Set(paths).size!==4)throw new Error('Card previews must not overwrite original media');
 const fingerprint=crypto.createHash('sha256').update(JSON.stringify([profile,hash(source),hash(poster)])).digest('hex');
 const stamp=output+'.json';
 try{
  const saved=JSON.parse(fs.readFileSync(stamp,'utf8'));
  if(saved.fingerprint===fingerprint&&fs.statSync(output).size===saved.bytes&&fs.statSync(thumbnail).size===saved.posterBytes)return {...saved,cached:true};
 }catch{/* Missing or stale derived files are rebuilt. */}
 fs.mkdirSync(path.dirname(output),{recursive:true});fs.mkdirSync(path.dirname(thumbnail),{recursive:true});
 const token=crypto.randomUUID(),videoTmp=output+'.'+token+'.tmp.mp4',posterTmp=thumbnail+'.'+token+'.tmp.jpg';
 try{
  execFileSync('ffmpeg',['-v','error','-y','-i',source,'-map','0:v:0','-an','-sn','-dn','-map_metadata','-1','-vf',`${scale},fps=${profile.fps}`,'-c:v','libx264','-preset','slow','-crf',String(profile.crf),'-maxrate',profile.maxrate,'-bufsize',profile.bufsize,'-pix_fmt','yuv420p','-threads','2','-movflags','+faststart',videoTmp]);
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
