import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {openBrowser} from '@remotion/renderer';
import {compileMotion} from './content-motion.mjs';
import {parse} from 'acorn';
import {MotionValidationError,localMotionScene,checkMotionFrames} from './motion-checks.mjs';

// This syntax check improves diagnostics. The opaque-origin sandbox, CSP and
// request interception are the isolation boundary, not a source-code blacklist.
export function validateMotionCode(code){
 const reject=(reason='语法或长度无效')=>{throw new MotionValidationError('motion_code_invalid','专用动效源码不符合确定性 Canvas 绘制约定：'+reason)};
 if(typeof code!=='string'||code.length<30||code.length>18000)reject();
 let ast;try{ast=parse(`function draw(ctx,t,w,h,scene,palette){\n${code}\n}`,{ecmaVersion:2022})}catch{reject()}
 const banned=new Set(['fetch','XMLHttpRequest','WebSocket','Worker','import','require','eval','Function','window','document','globalThis','parent','top','location','navigator','Date','setTimeout','setInterval','requestAnimationFrame','constructor','__proto__']);
 const blockedProperties=new Set(['constructor','__proto__','prototype','canvas']);
 const constant=n=>n?.type==='Literal'?n.value:n?.type==='BinaryExpression'&&n.operator==='+'&&typeof constant(n.left)==='string'&&typeof constant(n.right)==='string'?constant(n.left)+constant(n.right):n?.type==='TemplateLiteral'&&n.expressions.length===0?n.quasis[0].value.cooked:undefined;
 const names=(n,set)=>{if(!n)return;if(n.type==='Identifier')set.add(n.name);else if(n.type==='RestElement')names(n.argument,set);else if(n.type==='AssignmentPattern')names(n.left,set);else if(n.type==='ArrayPattern')n.elements.forEach(x=>names(x,set));else if(n.type==='ObjectPattern')n.properties.forEach(x=>names(x.value??x.argument,set))};
 const isFunction=n=>['FunctionDeclaration','FunctionExpression','ArrowFunctionExpression'].includes(n?.type);
 const hoisted=(n,set)=>{if(!n||isFunction(n))return;if(n.type==='VariableDeclaration'&&n.kind==='var')n.declarations.forEach(d=>names(d.id,set));for(const v of Object.values(n)){if(Array.isArray(v))v.forEach(x=>hoisted(x,set));else if(v&&typeof v==='object')hoisted(v,set)}};
 function visit(n,parent,key,scopes=[]){
  if(!n||typeof n!=='object')return;
  if(isFunction(n)){const bound=new Set();names(n.id,bound);n.params.forEach(p=>names(p,bound));hoisted(n.body,bound);scopes=[...scopes,bound]}
  if(n.type==='BlockStatement'||n.type==='Program'){const bound=new Set();for(const item of n.body){if(item.type==='VariableDeclaration'&&item.kind!=='var')item.declarations.forEach(d=>names(d.id,bound));if(item.type==='FunctionDeclaration')names(item.id,bound)}scopes=[...scopes,bound]}
  if(['ForStatement','ForOfStatement','ForInStatement'].includes(n.type)){const decl=n.init??n.left,bound=new Set();if(decl?.type==='VariableDeclaration'&&decl.kind!=='var')decl.declarations.forEach(d=>names(d.id,bound));scopes=[...scopes,bound]}
  if(n.type==='CatchClause'){const bound=new Set();names(n.param,bound);scopes=[...scopes,bound]}
  if(['WhileStatement','DoWhileStatement','ImportExpression','NewExpression','ThisExpression','WithStatement'].includes(n.type))reject('不支持 '+n.type);
  if(n.type==='Identifier'&&banned.has(n.name)){
   const property=parent?.type==='MemberExpression'&&key==='property'&&!parent.computed;
   const objectKey=parent?.type==='Property'&&key==='key'&&!parent.computed&&!parent.shorthand;
   if(!property&&!objectKey&&!scopes.some(scope=>scope.has(n.name)))reject('不允许访问全局变量 '+n.name);
  }
  if(n.type==='MemberExpression'){
   const name=n.computed?constant(n.property):n.property.name;
   if(blockedProperties.has(name)||name==='random')reject();
   // ctx methods must be named explicitly; dynamic keys could reach its canvas.
   if(n.computed&&n.object.type==='Identifier'&&n.object.name==='ctx'&&typeof name!=='string')reject();
  }
  if(n.type==='Property'&&blockedProperties.has(n.computed?constant(n.key):n.key.name??n.key.value))reject();
  for(const [k,value]of Object.entries(n)){if(Array.isArray(value))value.forEach(child=>visit(child,n,k,scopes));else if(value&&typeof value==='object')visit(value,n,k,scopes)}
 }
 visit(ast);
 return code;
}
const safeJSON=value=>JSON.stringify(value).replaceAll('<','\\u003c');
// Preserve the model's text and font size, but prevent long labels leaving the
// canvas. This helper is serialized into the sandbox and has no outer state.
export function drawMotionText(raw,method,quote,text,x,y,width,maxWidth){
 text=String(text);if(!quote.includes(text))throw Error('画面文字缺少原文依据');
 const matrix=raw.getTransform();
 if(Math.abs(matrix.b)>.0001||Math.abs(matrix.c)>.0001||matrix.a<=0)return raw[method](text,x,y,maxWidth);
 const left=(width*.06-matrix.e)/matrix.a,right=(width*.94-matrix.e)/matrix.a;
 x=Math.min(right,Math.max(left,x));
 const align=raw.textAlign;let available=align==='center'?2*Math.min(x-left,right-x):align==='right'||align==='end'?x-left:right-x;
 if(Number.isFinite(maxWidth)&&maxWidth>0)available=Math.min(available,maxWidth);
 if(available<width*.12/matrix.a){x=(left+right)/2;available=(right-left)/2;}
 const lines=[];let line='';
 for(const ch of text){if(line&&raw.measureText(line+ch).width>available){lines.push(line);line='';}line+=ch;}
 if(line)lines.push(line);
 const lineHeight=Number(raw.font.match(/([\d.]+)px/)?.[1]||20)*1.3;
 const top=raw.textBaseline==='middle'?y-(lines.length-1)*lineHeight/2:y;
 for(let i=0;i<lines.length;i++)raw[method](lines[i],x,top+i*lineHeight,available);
}
export function motionDocument(a,code){
 validateMotionCode(code);
 return `<!doctype html><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; connect-src 'none'; form-action 'none'; base-uri 'none'"><style>html,body{margin:0;overflow:hidden}canvas{display:block}</style><canvas width="${a.width}" height="${a.height}"></canvas><script>
 // The response additionally sets CSP sandbox allow-scripts (no same-origin).
 const canvas=document.querySelector('canvas'),raw=canvas.getContext('2d');
 const drawText=${drawMotionText.toString()};
 const draw=new Function('ctx','t','w','h','scene','palette',${safeJSON(code)});
 addEventListener('message',event=>{const m=event.data;if(m?.type!=='draw')return;try{
 const scene=m.scene,labels=[];const ctx=new Proxy(raw,{get(target,key){if(key==='canvas')return undefined;const value=target[key];if(key==='fillText'||key==='strokeText')return (text,x,y,maxWidth)=>{if(target.globalAlpha>=.2&&labels.length<100)labels.push(String(text));return drawText(target,key,scene.quote,text,x,y,canvas.width,maxWidth)};return typeof value==='function'?value.bind(target):value},set(target,key,value){target[key]=value;return true}});
 raw.reset();raw.fillStyle=m.palette[0];raw.fillRect(0,0,canvas.width,canvas.height);draw(ctx,m.time,canvas.width,canvas.height,scene,m.palette);
 parent.postMessage({type:'drawn',index:m.index,labels},'*');
 }catch(e){parent.postMessage({type:'drawn',index:m.index,error:String(e.message).slice(0,300)},'*')}});
 parent.postMessage({type:'ready'},'*');</script>`;
}
export async function renderDirectorMotion(a,output,options={}){
 const code=validateMotionCode(a.motion_plan.code),plan=compileMotion(a);
 const server=http.createServer((req,res)=>{
  if(req.url==='/scene'){
   res.setHeader('Content-Type','text/html; charset=utf-8');res.setHeader('Content-Security-Policy',"sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; connect-src 'none'; form-action 'none'; base-uri 'none'");res.end(motionDocument(a,code));
  }else if(req.url==='/'){
   res.setHeader('Content-Type','text/html');res.end(`<!doctype html><style>html,body{margin:0;overflow:hidden}iframe{border:0;width:100vw;height:100vh}</style><iframe sandbox="allow-scripts" src="/scene"></iframe><script>window.ready=false;window.results={};addEventListener('message',e=>{if(e.source!==document.querySelector('iframe').contentWindow)return;if(e.data.type==='ready')window.ready=true;else if(e.data.type==='drawn')window.results[e.data.index]=e.data;});</script>`);
  }else{res.writeHead(404);res.end()}
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));let browser,encoder;const errors=[];
 const deadline=(promise,ms=8000)=>{let timer;return Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('专用动效执行超时，请修改该动效')),ms)})]).finally(()=>clearTimeout(timer))};
 try{
  browser=await openBrowser('chrome',{chromiumOptions:{gl:'swangle'},logLevel:'error'});
  const page=await browser.newPage({context:()=>null,logLevel:'error',indent:false,pageIndex:0,onBrowserLog:null,onLog:()=>{}});
  await page.setViewport({width:a.width,height:a.height,deviceScaleFactor:1});
  const origin=`http://127.0.0.1:${server.address().port}`;
  await page._client().send('Fetch.enable',{patterns:[{urlPattern:'*'}]});
  page._client().on('Fetch.requestPaused',event=>{const p=event.params??event;const allowed=p.request?.url===origin+'/'||p.request?.url===origin+'/scene';void page._client().send(allowed?'Fetch.continueRequest':'Fetch.failRequest',allowed?{requestId:p.requestId}:{requestId:p.requestId,errorReason:'BlockedByClient'}).catch(()=>{});});
  await page.goto({url:origin+'/',timeout:15000});
  const ready=Date.now();while(!await deadline(page.evaluate(()=>window.ready))){if(Date.now()-ready>12000)throw Error('动效初始化失败');await new Promise(r=>setTimeout(r,50))}
  if(output){fs.mkdirSync(path.dirname(output),{recursive:true});
  encoder=spawn('ffmpeg',['-v','error','-y','-f','image2pipe','-vcodec','mjpeg','-framerate',String(a.fps),'-i','pipe:0','-an','-c:v','libx264','-preset','fast','-crf','19','-pix_fmt','yuv420p','-movflags','+faststart',output],{stdio:['pipe','ignore','pipe']});
  encoder.stderr.on('data',b=>errors.push(b.toString().slice(0,1000)));encoder.stdin.on('error',()=>{});}
  const done=encoder?once(encoder,'close'):null;
  const sceneFrames=new Map(plan.scenes.map(scene=>[scene.quote,[]]));
  for(let index=0;index<Math.ceil(a.duration*a.fps);index++){
   const t=index/a.fps,scene=plan.scenes.find(s=>t>=s.start&&t<s.end)??plan.scenes[0];
   const sceneIndex=plan.scenes.indexOf(scene);if(options.sceneIndex!=null&&sceneIndex!==options.sceneIndex)continue;
   try{
   const labels=await deadline(page.evaluate(async data=>{document.querySelector('iframe').contentWindow.postMessage(data,'*');const start=performance.now();while(!window.results[data.index]){if(performance.now()-start>3000)throw Error('帧绘制超时');await new Promise(r=>setTimeout(r,5));}const result=window.results[data.index];delete window.results[data.index];if(result.error)throw Error(result.error);return result.labels||[];},{type:'draw',index,time:t-scene.start,scene:localMotionScene(scene),palette:plan.profile.palette}));
   sceneFrames.get(scene.quote).push({time:t,labels});
   if(!output)continue;
   const result=await deadline(page._client().send('Page.captureScreenshot',{format:'jpeg',quality:92,fromSurface:true,captureBeyondViewport:false}));
   const bytes=Buffer.from(result.value.data,'base64');if(!encoder.stdin.write(bytes))await Promise.race([once(encoder.stdin,'drain'),done.then(()=>{throw Error('动效编码中断')})]);
   }catch(error){if(!error.diagnostic)throw new MotionValidationError('motion_draw_failed',`场景 ${sceneIndex+1} 绘制失败：${error.message}`,{scene_index:sceneIndex+1,scene_quote:scene.quote});throw error}
  }
  if(encoder){encoder.stdin.end();const [exit]=await done;if(exit!==0)throw Error('动效编码失败：'+errors.join('').slice(0,500));}
  const textChecks=plan.scenes.flatMap((scene,i)=>options.sceneIndex!=null&&options.sceneIndex!==i?[]:[checkMotionFrames(scene,sceneFrames.get(scene.quote),a.fps,i)]);
  return {...plan,text_checks:textChecks,mode:'director-code',frames:Math.ceil(a.duration*a.fps),demo_video_used:false};
 }finally{if(encoder&&encoder.exitCode===null)encoder.kill();if(browser)await browser.close({silent:true});await new Promise(r=>server.close(r));}
}
