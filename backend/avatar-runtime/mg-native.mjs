import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {fileURLToPath} from 'node:url';
import {spawn,execFileSync} from 'node:child_process';
import {once} from 'node:events';
import {openBrowser} from '@remotion/renderer';

const root=path.dirname(fileURLToPath(import.meta.url));
export const nativeStyles=JSON.parse(fs.readFileSync(path.join(root,'native-styles.json'),'utf8'));
const safeJSON=x=>JSON.stringify(x).replaceAll('<','\\u003c').replaceAll('\u2028','\\u2028').replaceAll('\u2029','\\u2029');
export function validateAnimation(a){
 if(!nativeStyles.some(s=>s.id===a.style))throw new Error('Unknown native animation style');
 if(!Number.isFinite(a.duration)||a.duration<=0||a.duration>90)throw new Error('Invalid animation duration');
 if(![25,30].includes(a.fps)||!Number.isInteger(a.width)||!Number.isInteger(a.height)||a.width<180||a.height<180||a.width>1920||a.height>1920)throw new Error('Invalid animation dimensions');
}
export function sceneClock(t,duration){return Math.max(0,Math.min(9.96,t/duration*10));}
export function makeContent(a){
 const phrases=String(a.text||'').split(/[，。；！？\n]/).map(x=>x.trim()).filter(Boolean);
 const title=String(a.title||a.brief||phrases[0]||'内容解读').slice(0,24);
 const words=Array.isArray(a.words)?a.words:[];
 let chars=0;const total=phrases.join('').length||1;
 const events=phrases.map((text,i)=>{let n=0;const word=words.find(w=>{n+=String(w.text).length;return n>chars});const start=word?.start??a.duration*chars/total;chars+=text.length;return {text,start:Math.max(0,Math.min(a.duration,start)),index:i};});
 return {title,shortTitle:[...title].slice(0,7).join(''),brief:String(a.brief||'').slice(0,100),phrases:phrases.length?phrases:['内容解读'],events,width:a.width,height:a.height,duration:a.duration,template:a.template||'highlight'};
}
// The upstream files on disk stay byte-identical. Only these reviewed literal
// substitutions run in the source browser, never model-generated/user code.
export function prepareNativeSource(text,mime,style,c){
 const value=x=>safeJSON(x);
 if(style.id==='mg-flat-vector'){
  text=text.replace("const WORD = 'popwıse'",()=>`const WORD = ${value(c.shortTitle.padEnd(7,' '))}`)
   .replace("const CN = '每天 聪明一点点'",()=>`const CN = ${value(c.phrases[0].slice(0,12))}`)
   .replaceAll('CNDOTS[0].sx','(CNDOTS[0]?.sx ?? 960)').replaceAll('CNDOTS[0].sy','(CNDOTS[0]?.sy ?? CNB)')
   .replaceAll('EVERY DAY, A LITTLE WISER','');
 }
 if(style.id==='mg-line-art')text=text.replace("const word = 'ATELIER LINEA'",()=>`const word = ${value(c.shortTitle)}`).replace("const tag = '始于一线'",()=>`const tag = ${value(c.phrases[0].slice(0,16))}`);
 if(style.id==='mg-morph'){
  text=text.replace(/const WORDS = \[[^\n]+\];/,()=>`const WORDS = ${value(Array.from({length:7},(_,i)=>c.phrases[i%c.phrases.length].slice(0,8)))};`)
   .replace(/const EN = \[[^\n]+\];/,()=>`const EN = ${value(Array(7).fill(''))};`)
   .replaceAll('drawLetters(t);','window.MG_TITLE(ctx,t);')
   .replaceAll("['一', '形', '万', '象']",()=>value([...c.shortTitle.padEnd(4,' ')].slice(0,4)))
   .replaceAll('ONE SHAPE · EVERY STORY','');
 }
 if(style.id==='mg-bauhaus'){
  const rw=Math.min(1280,Math.max(480,c.width||1280));const rh=Math.round(rw*9/16);
  text=text.replace("'BAUHAUS'.split('')",()=>`${value(c.shortTitle.padEnd(4,' '))}.split('')`)
   .replaceAll("'FORM · FARBE · FUNKTION'",()=>value(c.phrases[0].slice(0,18)))
   .replaceAll('BOGEN 09 — KONSTRUKTION IN 20 SCHLÄGEN · 120 BPM · M = 120 PX','')
   .replace("const out = document.getElementById('out');",()=>`const out = document.getElementById('out'); out.width=${rw};out.height=${rh};`)
   .replace('gl.viewport(0, 0, W, H);','gl.viewport(0, 0, out.width, out.height);')
   .replace('vec2 p = vec2(gl_FragCoord.x, RES.y - gl_FragCoord.y);',()=>`vec2 p = vec2(gl_FragCoord.x * ${(1920/rw).toFixed(9)}, RES.y - gl_FragCoord.y * ${(1080/rh).toFixed(9)});`);
 }
 if(style.id==='mg-collage'){
  for(const [from,to] of [['NOGGIN QUARTERLY — Nº06 — AUTUMN 2026',c.shortTitle],['NOGGIN QUARTERLY — ¥19.60',''],['THE MIND IS A COLLAGE',c.phrases[0].slice(0,22)],['NOGGIN',c.shortTitle],['脑洞季刊',c.shortTitle],['沿虚线剪开',c.phrases[0].slice(0,7)],['CUT ALONG THE LINE',''],['随刊附赠',c.phrases[1]?.slice(0,6)||c.shortTitle],['剪刀一把',''],['SCISSORS INCLUDED','']]){
   // Only literal string tokens change; input never becomes source syntax.
   text=text.replaceAll("'"+from+"'",()=>value(to));
  }
 }
 if(mime==='text/html'){
  text=text.replace(/@font-face\{font-family:'Outfit Morph';[^}]+\}/,'');
  const injected=`<script>window.MG_CONTENT=${safeJSON(c)};window.MG_TITLE=(ctx,t)=>{if(t<8)return;ctx.save();ctx.fillStyle='#F4EEE3';ctx.textAlign='center';ctx.font='700 110px "Noto Sans CJK SC"';ctx.fillText(window.MG_CONTENT.shortTitle,960,570);ctx.restore();};window.addEventListener('error',e=>{window.MG_ERROR=String(e.message).slice(0,200)});window.addEventListener('unhandledrejection',e=>{window.MG_ERROR=String(e.reason?.message||e.reason).slice(0,200)});</script>`;
  text=/<head>/i.test(text)?text.replace(/<head>/i,()=>'<head>'+injected):injected+text;
 }
 return text;
}

export async function renderNative(a,output,{frameSamples}={}){
 validateAnimation(a);const style=nativeStyles.find(s=>s.id===a.style);const content=makeContent(a);
 if((style.reference_mode||style.production_mode)==='original-video'){
  const source=path.join(root,'catalog',style.id+'-reference.mp4');
  const fit=`scale=${a.width}:${a.height}:force_original_aspect_ratio=decrease,pad=${a.width}:${a.height}:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1`;
  if(frameSamples){fs.mkdirSync(output,{recursive:true});for(let i=0;i<frameSamples.length;i++)execFileSync('ffmpeg',['-v','error','-y','-ss',String(sceneClock(frameSamples[i],a.duration)),'-i',source,'-vf',fit,'-frames:v','1',path.join(output,`frame-${i}.jpg`)]);}
  else{fs.mkdirSync(path.dirname(output),{recursive:true});execFileSync('ffmpeg',['-v','error','-y','-i',source,'-an','-vf',`setpts=${a.duration/10}*(PTS-STARTPTS),${fit},fps=${a.fps}`,'-t',String(a.duration),'-c:v','libx264','-preset','fast','-pix_fmt','yuv420p','-movflags','+faststart',output]);}
  return {style:style.id,version:style.version,source:style.source,mode:'original-video',duration:a.duration,frames:Math.ceil(a.duration*a.fps),note:style.description,content};
 }
 const mime={'.html':'text/html','.js':'text/javascript','.mjs':'text/javascript','.json':'application/json','.css':'text/css','.png':'image/png','.jpg':'image/jpeg','.jpeg':'image/jpeg','.webp':'image/webp','.svg':'image/svg+xml','.ttf':'font/ttf','.otf':'font/otf','.woff2':'font/woff2'};
 const errors=[];
 const server=http.createServer((req,res)=>{
  try{
   const url=new URL(req.url,'http://localhost');let pathname=decodeURIComponent(url.pathname);
   if(pathname.split('/').includes('..')||pathname.includes('\\')){res.writeHead(404);res.end();return;}
   if(pathname==='/'){
    res.setHeader('Content-Type','text/html; charset=utf-8');
    res.end(`<!doctype html><html><head><meta charset="utf-8"><script>window.MG_CONFIG=${safeJSON({...content,style,entry:style.entry})}</script><script src="/mg-shell.js" defer></script></head><body></body></html>`);return;
   }
   if(pathname==='/mg-shell.js'){res.setHeader('Content-Type','text/javascript');res.end(fs.readFileSync(path.join(root,'mg-shell.js')));return;}
   const isModule=pathname.startsWith('/node_modules/');
   if(isModule&&!/^\/node_modules\/(three|d3|topojson-client|simplex-noise|opentype\.js)\//.test(pathname)){res.writeHead(404);res.end();return;}
   const base=isModule?root:path.join(root,'mg-upstream');const file=path.resolve(base,'.'+pathname);
   if(!file.startsWith(base+path.sep)||!fs.existsSync(file)||!fs.statSync(file).isFile()){res.writeHead(404);res.end();return;}
   const type=mime[path.extname(file)]||'application/octet-stream';res.setHeader('Content-Type',type+'; charset=utf-8');
   res.setHeader('Content-Security-Policy',"default-src 'self' data: blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; font-src 'self' data:;");
   const bytes=fs.readFileSync(file);res.end(['text/html','text/javascript'].includes(type)?prepareNativeSource(bytes.toString(),type,style,content):bytes);
  }catch{res.writeHead(500);res.end();}
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));let browser,encoder;
 try{
  browser=await openBrowser('chrome',{chromiumOptions:{gl:'swangle'},logLevel:'error'});
  const page=await browser.newPage({context:()=>null,logLevel:'error',indent:false,pageIndex:0,onBrowserLog:null,onLog:()=>{}});
  await page.setViewport({width:a.width,height:a.height,deviceScaleFactor:1});
  await page.goto({url:`http://127.0.0.1:${server.address().port}/`,timeout:90000});
  const started=Date.now();while(!(await page.evaluate(()=>window.MG_READY===true))){
   const error=await page.evaluate(()=>window.MG_ERROR||document.querySelector('iframe')?.contentWindow?.MG_ERROR);
   if(error||Date.now()-started>90000)throw new Error('Native source initialization failed: '+(error||'timeout'));
   await new Promise(resolve=>setTimeout(resolve,100));
  }
  const capture=async t=>{
   await page.evaluate(async ({t,source})=>window.MG_RENDER(t,source),{t,source:sceneClock(t,a.duration)});
   const shot=await page._client().send('Page.captureScreenshot',{format:'jpeg',quality:92,fromSurface:true,captureBeyondViewport:false});return Buffer.from(shot.value.data,'base64');
  };
  if(frameSamples){
   fs.mkdirSync(output,{recursive:true});for(let i=0;i<frameSamples.length;i++)fs.writeFileSync(path.join(output,`frame-${i}.jpg`),await capture(frameSamples[i]));
  }else{
   fs.mkdirSync(path.dirname(output),{recursive:true});
   encoder=spawn('ffmpeg',['-v','error','-y','-f','image2pipe','-vcodec','mjpeg','-framerate',String(a.fps),'-i','pipe:0','-an','-c:v','libx264','-preset','fast','-crf','19','-pix_fmt','yuv420p','-movflags','+faststart',output],{stdio:['pipe','ignore','pipe']});
   encoder.stderr.on('data',x=>errors.push(x.toString().slice(0,1000)));encoder.stdin.on('error',()=>{});
   const done=once(encoder,'close');
   for(let f=0;f<Math.ceil(a.duration*a.fps);f++){
    if(encoder.exitCode!==null)throw new Error('Native encoder stopped');
    const buffer=await capture(f/a.fps);if(!encoder.stdin.write(buffer))await Promise.race([once(encoder.stdin,'drain'),done.then(()=>{throw new Error('Native encoder stopped')})]);
   }
   encoder.stdin.end();const [code]=await done;if(code!==0)throw new Error('Native encoding failed: '+errors.join('').slice(0,600));
  }
 }finally{
  if(encoder&&encoder.exitCode===null)encoder.kill('SIGTERM');
  if(browser)await browser.close({silent:true});await new Promise(resolve=>server.close(resolve));
 }
 return {style:style.id,version:style.version,source:style.source,mode:'native-source',frames:Math.ceil(a.duration*a.fps),duration:a.duration,content};
}
