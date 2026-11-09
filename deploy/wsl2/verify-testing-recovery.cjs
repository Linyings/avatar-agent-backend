// Read-only by default. --execute clicks ONE real recovery button on the chosen project.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const {createHash,randomUUID}=require('node:crypto');
const assert=require('node:assert/strict');
const {chromium,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
const hash=x=>createHash('sha256').update(JSON.stringify(x)).digest('hex');
(async()=>{
 const id=process.argv[2];assert.match(id??'',/^[0-9a-f-]{36}$/i);
 const execute=process.argv.includes('--execute');
 const audioChoice=process.argv.includes('--audio-choice');
 assert.ok(!(execute&&audioChoice),'Choose either recovery execution or read-only audio choice verification');
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let account={};
 for(const line of raw.split(/\r?\n/)){
  const split=line.indexOf(': ');if(split<0)continue;
  const key=line.slice(0,split),value=line.slice(split+2);
  if(key==='Email'){if(account.email&&account.password)accounts.push(account);account={email:value}}
  if(key==='Password')account.password=value;
 }
 if(account.email&&account.password)accounts.push(account);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const output=resolve(__dirname,'../../backend/data/wsl-deployment',audioChoice?'audio-choice':'testing-recovery',new URL(base).hostname);
 mkdirSync(output,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
 const report={base,projectId:id,execute,controlRequests:0,chatRequests:0,confirmationRequests:0,pageErrors:[]};
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  let credentials,headers;
  for(const candidate of accounts){
   const response=await context.request.post(base+'/api/v1/auth/login',{data:candidate});
   if(!response.ok())continue;
   const candidateHeaders={Authorization:'Bearer '+unwrap(await response.json()).tokens.access_token};
   if(!(await context.request.get(base+'/api/v1/agent/project/'+id,{headers:candidateHeaders})).ok())continue;
   credentials=candidate;headers=candidateHeaders;break;
  }
  assert.ok(credentials,'Saved account cannot access selected project');
  const get=async path=>{const r=await context.request.get(base+path,{headers});assert.equal(r.status(),200,path);return unwrap(await r.json())};
  const before=await get('/api/v1/agent/project/'+id);
  assert.equal(before.testing_mode,true);
  const original=await get('/api/v1/avatar-mvp/runs/'+before.run.id);
  report.before={runId:original.id,status:original.status,draftVoice:before.project.input.voice};
  const page=await context.newPage();
  page.on('pageerror',e=>report.pageErrors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST'&&r.url().includes('/packages/avatar.control/invoke'))report.controlRequests++});
  page.on('request',r=>{if(r.method()!=='POST')return;if(new URL(r.url()).pathname==='/api/v1/agent/chat')report.chatRequests++;if(/\/avatar-mvp\/runs\/[^/]+\/confirm$/.test(new URL(r.url()).pathname))report.confirmationRequests++});
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name="email"]').fill(credentials.email);
  await page.locator('input[name="password"]').fill(credentials.password);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(base+'/');
  await page.goto(base+'/workspace/'+id,{waitUntil:'domcontentloaded'});
  const status=page.locator('[data-execution-status]');
  await expect(status).toBeVisible();
  await expect(status).not.toContainText('核对供应商订单');
  if(['submission_unknown','failed','interrupted'].includes(before.run.status)){
   await expect(page.getByRole('button',{name:'重新执行并继续',exact:true})).toHaveCount(1);
  }
  await page.screenshot({path:resolve(output,'before.png'),fullPage:true});
  assert.equal(report.controlRequests,0,'Reading a workspace must not resume it');
  if(audioChoice){
   assert.equal(original.status,'awaiting_confirmation');assert.equal(original.confirmation,'audio');
   assert.ok(original.segments.length>0&&original.segments.every(s=>s.audio&&s.duration>0));
   for(const action of ['retry','resume']){
    const r=await context.request.post(base+'/api/v1/agent/packages/avatar.control/invoke',{headers,data:{projectId:id,requestId:'audio-choice-'+randomUUID(),input:{action}}});
    assert.equal(r.status(),200);const out=unwrap(await r.json());
    assert.equal(out.submitted,false);assert.equal(out.confirmation,'audio');assert.equal(out.audio_completed,original.segments.length);
    assert.match(out.note,/直接继续制作/);
   }
   const retry=await context.request.post(base+'/api/v1/avatar-mvp/runs/'+original.id+'/retry',{headers});
   assert.equal(retry.status(),202);assert.equal(unwrap(await retry.json()).status,'awaiting_confirmation');
   report.staleControlNoop='passed';
   await expect(status).toContainText(`全部 ${original.segments.length} 段配音已完成`);
   await expect(page.getByRole('button',{name:'直接继续制作',exact:true})).toBeEnabled();
   await page.getByRole('textbox',{name:'向 Agent 发送指令'}).fill('继续');
   await page.getByRole('button',{name:'发送消息',exact:true}).click();
   await expect(page.getByText('请在本对话下方的配音卡片选择：先「查看并试听配音」，或点击「直接继续制作」。所有段落统一处理，不需要逐段确认，也无需重试配音。',{exact:true})).toBeVisible();
   await expect(page.getByRole('button',{name:'直接继续制作',exact:true})).toBeInViewport();
   await page.screenshot({path:resolve(output,'choice.png'),fullPage:true});
   await page.getByRole('button',{name:'查看并试听配音',exact:true}).click();
   await expect(status.locator('audio')).toHaveCount(original.segments.length);
   await expect(page.getByRole('button',{name:'已确认，继续',exact:true})).toBeEnabled();
   await status.locator('audio').first().scrollIntoViewIfNeeded();
   await page.screenshot({path:resolve(output,'batch-preview.png'),fullPage:true});
   assert.equal(report.controlRequests,0);assert.equal(report.chatRequests,0);assert.equal(report.confirmationRequests,0);
   assert.equal(hash(await get('/api/v1/avatar-mvp/runs/'+original.id)),hash(original));
   report.batchChoice='passed';report.originalRunUnchanged=true;
  }
  if(execute){
   const responsePromise=page.waitForResponse(r=>r.request().method()==='POST'&&r.url().includes('/packages/avatar.control/invoke'));
   await page.getByRole('button',{name:'重新执行并继续',exact:true}).click();
   const response=await responsePromise;
   assert.equal(response.status(),200,'Real recovery request failed');
   const accepted=unwrap(await response.json());
   assert.equal(accepted.submitted,true);
   report.accepted=accepted;
   let current,last='';
   const deadline=Date.now()+10*60*1000;
   do{
    current=(await get('/api/v1/agent/project/'+id)).run;
    const progress=JSON.stringify({status:current.status,stage:current.stage,audio:(current.segments??[]).filter(s=>s.audio).length});
    if(progress!==last){console.log(progress);last=progress}
    if(!['running','stopping'].includes(current.status))break;
    await new Promise(r=>setTimeout(r,5000));
   }while(Date.now()<deadline);
   report.after={runId:current.id,status:current.status,stage:current.stage,voice:current.input.voice,scriptConfirmed:current.script_confirmed,confirmation:current.confirmation,error:current.error,
    segments:(current.segments??[]).map(s=>({id:s.id,audio:!!s.audio,duration:s.duration})),steps:(current.steps??[]).map(s=>({id:s.id,status:s.status,submission:s.submission,hasTask:!!s.provider_task_id,error:s.error}))};
   if(current.id!==original.id){
    const history=await get('/api/v1/avatar-mvp/runs/'+original.id);
    assert.equal(hash(history),hash(original),'Original failure history changed');
    report.originalHistoryUnchanged=true;
   }
   assert.equal(report.controlRequests,1);
   await page.reload({waitUntil:'domcontentloaded'});
   await expect(status).toBeVisible();
   await expect(status).not.toContainText('核对供应商订单');
   if(current.status==='awaiting_confirmation'&&current.confirmation==='audio'){
    await page.getByRole('button',{name:'查看并试听配音',exact:true}).click();
    const players=status.locator('audio');
    await expect(players).toHaveCount(current.segments.length);
    await expect.poll(()=>players.evaluateAll(items=>items.every(a=>a.readyState>=1&&Number.isFinite(a.duration)&&a.duration>0)),{timeout:30000}).toBe(true);
    await players.first().evaluate(a=>a.play());
    await expect.poll(()=>players.first().evaluate(a=>a.currentTime),{timeout:10000}).toBeGreaterThan(0);
    await players.first().evaluate(a=>a.pause());
    report.audioBrowserPlayback='passed';
   }
   await page.screenshot({path:resolve(output,'after.png'),fullPage:true});
  }
  assert.deepEqual(report.pageErrors,[]);
  writeFileSync(resolve(output,execute?'execution.json':'readonly.json'),JSON.stringify(report,null,2)+'\n');
  if(execute)writeFileSync(resolve(output,`execution-from-${original.id}.json`),JSON.stringify(report,null,2)+'\n');
  console.log(JSON.stringify(report,null,2));
  if(process.argv.includes('--require-audio'))assert.equal(report.audioBrowserPlayback,'passed','Audio did not reach playable confirmation');
 }finally{await browser.close()}
})().catch(e=>{console.error(e.message);process.exitCode=1});
