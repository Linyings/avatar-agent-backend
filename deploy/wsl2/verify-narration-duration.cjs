// Inspect legacy recovery without starting production or changing persisted data.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
(async()=>{
 const id=process.argv[2];assert.match(id??'',/^[0-9a-f-]{36}$/i);
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let entry={};
 for(const line of raw.split(/\r?\n/)){const i=line.indexOf(': ');if(i<0)continue;const k=line.slice(0,i),v=line.slice(i+2);if(k==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:v}}if(k==='Password')entry.password=v}
 if(entry.email&&entry.password)accounts.push(entry);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const output=resolve(__dirname,'../../backend/data/wsl-deployment/narration-duration',new URL(base).hostname);mkdirSync(output,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  let account,headers;
  for(const candidate of accounts){const r=await context.request.post(base+'/api/v1/auth/login',{data:candidate});if(!r.ok())continue;const h={Authorization:'Bearer '+unwrap(await r.json()).tokens.access_token};if(!(await context.request.get(base+'/api/v1/agent/project/'+id,{headers:h})).ok())continue;account=candidate;headers=h;break}
  assert.ok(account,'No saved account with access to this project');
  const snapshot=async()=>{const r=await context.request.get(base+'/api/v1/agent/project/'+id,{headers});assert.equal(r.status(),200);return unwrap(await r.json())};
  const before=await snapshot(),timing=before.audio_timing;
  assert.equal(timing.issue,undefined);
  assert.equal(timing.resume_available,true);
  assert.equal(timing.limit_seconds,undefined);
  assert.equal(before.run.status,'failed');
  assert.equal(timing.seconds,before.run.segments.reduce((sum,s)=>sum+s.duration,0));
  assert.ok(timing.seconds>90);
  const page=await context.newPage(),errors=[],unexpected=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/api/v1/**',route=>{
   const request=route.request(),path=new URL(request.url()).pathname;
   if(!['GET','HEAD','OPTIONS'].includes(request.method())&&!path.startsWith('/api/v1/auth/')){unexpected.push(path);return route.abort()}
   return route.continue();
  });
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name="email"]').fill(account.email);await page.locator('input[name="password"]').fill(account.password);
  await page.locator('button[type="submit"]').click();await expect(page).toHaveURL(base+'/');
  await page.goto(base+'/workspace/'+id,{waitUntil:'domcontentloaded'});
  const status=page.locator('[data-execution-status]');
  await expect(status).toContainText('时长限制已取消，可以继续制作');
  await expect(status).toContainText(`累计 ${timing.seconds.toFixed(2)} 秒`);
  await expect(status.getByRole('button',{name:'继续制作',exact:true})).toBeEnabled();
  await expect(status.getByRole('button',{name:'编辑文案',exact:true})).toHaveCount(0);
  await page.screenshot({path:resolve(output,'workspace.png'),fullPage:true});
  for(const segment of before.run.segments){
   const response=await context.request.get(`${base}/api/v1/agent/runs/${before.run.id}/segments/${encodeURIComponent(segment.id)}/audio`,{headers});
   assert.equal(response.status(),200);assert.ok((await response.body()).length>0);
  }
  await page.reload({waitUntil:'domcontentloaded'});await expect(status).toContainText('时长限制已取消，可以继续制作');
  const after=await snapshot();assert.deepEqual(after.run,before.run);assert.deepEqual(unexpected,[]);assert.deepEqual(errors,[]);
  const report={base,projectId:id,runId:before.run.id,audioTiming:timing,recoveryButton:'enabled',preservedAudio:before.run.segments.length,refresh:'passed',runUnchanged:true,productionRequests:unexpected.length,pageErrors:errors,screenshots:output};
  writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
 }finally{await browser.close()}
})().catch(e=>{console.error(e.message);process.exitCode=1});
