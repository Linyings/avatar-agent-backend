// Exercise the deployed UI with an exhausted-retry response fixture. Real login
// and project reads; all Agent mutations are intercepted, with no provider work.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','-u','root','--exec','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let account={};
 for(const line of raw.split(/\r?\n/)){const i=line.indexOf(': ');if(i<0)continue;const key=line.slice(0,i),value=line.slice(i+2);if(key==='Email'){if(account.email&&account.password)accounts.push(account);account={email:value}}else if(key==='Password')account.password=value}
 if(account.email&&account.password)accounts.push(account);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','-u','root','--exec','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=`http://${ip}:8080`,project='847822ee-4b95-4010-acff-f0db54a03545';
 const output=resolve(__dirname,'../../backend/data/reviews/f07-20261109/browser-handoff');mkdirSync(output,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
 try{
  const context=await browser.newContext({viewport:{width:1600,height:1050}}),page=await context.newPage();
  let valid,headers;const errors=[],mutations=[];
  page.on('pageerror',e=>errors.push(e.message));
  for(const a of accounts){const r=await context.request.post(base+'/api/v1/auth/login',{data:a});if(r.status()!==200)continue;valid=a;headers={Authorization:'Bearer '+unwrap(await r.json()).tokens.access_token};break}
  assert.ok(valid,'Saved account login failed');
  const path='/api/v1/agent/project/'+project;
  const response=await context.request.get(base+path,{headers});assert.equal(response.status(),200);
  const body=await response.json(),state=unwrap(body),run=state.run;
  const need=run.needs.find(n=>n.id==='n_68045969fb437727_1');assert.ok(need);
  const node='F07/'+need.id,id=node+'/scene-2';
  run.status='failed';run.stage=id;run.error_step_id=id;run.confirmation=undefined;
  run.error='场景2关键内容未完整展示：缩水；实际场景 3.07 秒';
  state.proposal=null;state.agent_activity=null;
  let step=run.steps.find(s=>s.id===id);if(!step){step={id,seconds:3};run.steps.push(step)}
  Object.assign(step,{status:'failed',error:run.error,retry:{last_error:run.error,strategy:'按实际时长提前展示图块',automatic_retries:3,exhausted:true},attempts:[{number:1,status:'failed',error:'场景2入场时间超出时长'},{number:2,status:'failed',error:'缩水未完整展示'}]});
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name="email"]').fill(valid.email);await page.locator('input[name="password"]').fill(valid.password);await page.locator('button[type="submit"]').click();await expect(page).toHaveURL(base+'/');
  await page.route('**/api/v1/agent/**',async route=>{
   const req=route.request(),url=new URL(req.url());
   if(req.method()==='GET'&&url.pathname===path)return route.fulfill({json:body});
   if(!['GET','HEAD','OPTIONS'].includes(req.method())){
    mutations.push({path:url.pathname,body:req.postDataJSON()});
    if(url.pathname==='/api/v1/agent/chat')return route.fulfill({json:{reply:'验收模拟：已读取错误，等待用户选择处理方式。'}});
    return route.fulfill({status:409,json:{error:'Verification blocks production mutations'}});
   }
   return route.continue();
  });
  await page.goto(base+'/workspace/'+project,{waitUntil:'domcontentloaded'});
  const status=page.getByRole('region',{name:'制作状态'});
  await expect(status).toContainText('等待你处理');await expect(status).toContainText('3 次');
  await expect(status.getByRole('button',{name:'根据报错修复并重试'})).toHaveCount(0);
  const selector=page.getByRole('combobox',{name:'定位制作步骤'});await selector.fill(node);await page.getByRole('option').filter({hasText:node}).first().click();
  await page.getByTestId('workflow-node-'+node).getByRole('button',{name:/查看.*详情/}).click();
  const detail=page.getByRole('complementary',{name:'步骤详情'});
  await expect(detail.getByText('自动重试已用完，等待你处理',{exact:true})).toBeVisible();
  await expect(detail.getByRole('button',{name:'调整或跳过此步骤'})).toBeVisible();
  await detail.getByText('查看重试错误记录',{exact:true}).click();await expect(detail.getByText('第 1 次：场景2入场时间超出时长')).toBeVisible();
  await page.screenshot({path:resolve(output,'waiting-for-user.png'),fullPage:true});
  await status.getByRole('button',{name:'交给 Agent 排查'}).click();
  await expect.poll(()=>mutations.length).toBe(1);
  assert.equal(mutations[0].path,'/api/v1/agent/chat');assert.ok(mutations[0].body.message.includes('只排查'));assert.ok(mutations[0].body.message.includes(id));
  assert.deepEqual(errors,[]);
  const report={base,fixture:'exhausted retry state only; persisted project unchanged',waitingForUser:true,historyVisible:true,diagnosisDoesNotRetry:true,productionMutations:0,modelCalls:0,pageErrors:errors};
  writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
 }finally{await browser.close()}
})().catch(e=>{console.error(e.message);process.exitCode=1});
