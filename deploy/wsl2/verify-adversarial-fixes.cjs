// Validate review fixes on WSL without any third-party model or media calls.
// Run prepare before deployment, then verify afterwards to check cold history.
// Run verify-ui to repeat browser checks without restarting the API.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync,readFileSync,existsSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');

const out=resolve(__dirname,'../../backend/data/wsl-deployment/adversarial-fixes');
const fixturePath=resolve(out,'fixture.json');
mkdirSync(out,{recursive:true});
const unwrap=body=>body.data??body;

async function connect(){
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let candidate={};
 for(const line of raw.split(/\r?\n/)){
  const i=line.indexOf(': ');if(i<0)continue;const key=line.slice(0,i),value=line.slice(i+2);
  if(key==='Email'){if(candidate.email&&candidate.password)accounts.push(candidate);candidate={email:value}}
  else if(key==='Password')candidate.password=value;
 }
 if(candidate.email&&candidate.password)accounts.push(candidate);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const api=await request.newContext({baseURL:base});
 for(const account of accounts){
  const response=await api.post('/api/v1/auth/login',{data:account});
  if(response.status()===401)continue;
  assert.equal(response.status(),200,'login failed');
  const body=unwrap(await response.json());
  const token=body.tokens?.access_token;
  assert.ok(token,'missing token');
  return {base,api,account,headers:{Authorization:'Bearer '+token}};
 }
 await api.dispose();throw new Error('No valid saved account');
}

(async()=>{
 const mode=process.argv[2]||'verify';const {base,api,account,headers}=await connect();
 let browser;const checks={};let fixture;const errors=[];let passed=false;
 const getStudio=async(id)=>unwrap(await(await api.get('/api/v1/agent/project/'+id,{headers})).json());
 try{
  if(mode==='prepare'||mode==='verify-ui'){
   if(existsSync(fixturePath)){
    const prior=JSON.parse(readFileSync(fixturePath,'utf8'));
    assert.ok(prior.cleaned,'Previous review fixture has not been cleaned');
   }
   const marker='审查修复验收-'+Date.now();
   const created=await api.post('/api/v1/avatar-mvp/projects',{headers,data:{title:marker,topic:marker,style:'flat-vector',use_avatar:false}});
   assert.equal(created.status(),200);
   const id=unwrap(await created.json()).id;
   fixture={id,marker,base,preparedAt:new Date().toISOString(),createdIds:[id],cleaned:false};
   writeFileSync(fixturePath,JSON.stringify(fixture,null,2));
   // Pure status intent persists this user message and never calls an LLM.
   const chat=await api.post('/api/v1/agent/chat',{headers,data:{projectId:id,message:'查看进度 '+marker,requestId:marker,turnId:marker}});
   assert.equal(chat.status(),200);
   assert.equal((await chat.json()).status,'ok');
   if(mode==='prepare'){
    console.log(JSON.stringify({prepared:true,projectId:id,modelCalls:0}));
    return;
   }
  }
  fixture=JSON.parse(readFileSync(fixturePath,'utf8'));
  assert.equal(fixture.cleaned,false,'Prepare a fresh fixture before verification');
  if(mode==='cleanup')return;
  assert.ok(mode==='verify'||mode==='verify-ui','Unknown verification mode');
  if(mode==='verify'){
   const started=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','docker','inspect','--format','{{.State.StartedAt}}','aicat-avatar-api-1'],{encoding:'utf8'}).trim();
   assert.ok(Date.parse(started)>Date.parse(fixture.preparedAt),'API must restart after the history fixture is prepared');
   checks.apiRestartedFromSeed=true;
  }
  // This request must precede any chat after the API container restart.
  const history=await api.get('/api/v1/agent/history',{headers,params:{projectId:fixture.id}});
  assert.equal(history.status(),200);
  assert.ok((await history.json()).messages.some(m=>m.content.includes(fixture.marker)));
  if(mode==='verify')checks.coldHistory=true;
  else checks.historyRead=true;

  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name=email]').fill(account.email);
  await page.locator('input[name=password]').fill(account.password);
  await page.locator('button[type=submit]').click();
  await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  await page.goto(base+'/workspace/'+fixture.id,{waitUntil:'domcontentloaded'});
  await expect(page.getByRole('complementary',{name:'Agent 对话'}).getByText('查看进度 '+fixture.marker,{exact:true})).toBeVisible();

  await page.goto(base+'/projects',{waitUntil:'domcontentloaded'});
  await page.getByRole('textbox',{name:'搜索项目'}).fill(fixture.marker);
  await expect(page.getByTestId('studio-project')).toHaveCount(1);
  await page.getByRole('button',{name:'管理 '+fixture.marker,exact:true}).click();
  await page.getByRole('menuitem',{name:'重命名',exact:true}).click();
  const renamed=fixture.marker+'-已改名';
  await page.getByRole('textbox',{name:'项目名称',exact:true}).fill(renamed);
  await page.getByRole('button',{name:'保存',exact:true}).click();
  await expect.poll(async()=>(await getStudio(fixture.id)).project.input.title).toBe(renamed);
  await page.goto(base+'/workspace/'+fixture.id,{waitUntil:'domcontentloaded'});
  await page.getByRole('button',{name:'制作设置',exact:true}).click();
  await expect(page.getByLabel('项目名称',{exact:true})).toHaveValue(renamed);
  await page.getByLabel('制作要求',{exact:true}).fill('仅用于修复验收，不调用模型。');
  const saved=page.waitForResponse(r=>r.url().includes('/packages/avatar.configure/invoke')&&r.request().method()==='POST');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();assert.equal((await saved).status(),200);
  const snapshot=await getStudio(fixture.id);
  assert.equal(snapshot.project.name,renamed);assert.equal(snapshot.project.input.title,renamed);
  checks.renameSurvivesSettings=true;
  await page.screenshot({path:resolve(out,'rename-settings.png'),fullPage:true});

  await page.goto(base+'/',{waitUntil:'domcontentloaded'});
  await page.getByRole('textbox',{name:'描述视频创意'}).fill('查看进度，附件恢复验收。');
  await page.locator('input[type=file]').setInputFiles({name:'invalid.png',mimeType:'image/png',buffer:Buffer.from('invalid image')});
  const createdPromise=page.waitForResponse(r=>r.url().endsWith('/avatar-mvp/projects')&&r.request().method()==='POST');
  const rejectedPromise=page.waitForResponse(r=>/\/assets$/.test(new URL(r.url()).pathname)&&r.request().method()==='POST');
  await page.getByRole('button',{name:'发送创意',exact:true}).click();
  const created=await createdPromise;assert.equal(created.status(),200);
  const uploadProject=unwrap(await created.json()).id;
  fixture.createdIds.push(uploadProject);writeFileSync(fixturePath,JSON.stringify(fixture,null,2));
  assert.equal((await rejectedPromise).status(),400);
  await expect(page.getByRole('alert')).toContainText('文件无法解码');
  checks.uploadValidationMessage=true;
  await page.screenshot({path:resolve(out,'upload-validation.png'),fullPage:true});
  await expect(page.getByRole('button',{name:'移除invalid.png'})).toBeEnabled();
  await page.getByRole('button',{name:'移除invalid.png'}).click();
  const imagePage=await context.newPage();await imagePage.setViewportSize({width:96,height:96});
  await imagePage.setContent('<body style="margin:0;background:#36586d;color:white">TEST</body>');
  const png=await imagePage.screenshot();await imagePage.close();
  await page.locator('input[type=file]').setInputFiles({name:'replacement.png',mimeType:'image/png',buffer:png});
  const uploaded=page.waitForResponse(r=>r.url().includes('/'+uploadProject+'/assets')&&r.request().method()==='POST');
  const statusChat=page.waitForResponse(r=>r.url().endsWith('/agent/chat')&&r.request().method()==='POST');
  await page.getByRole('button',{name:'发送创意',exact:true}).click();
  assert.equal((await uploaded).status(),200);
  assert.equal((await statusChat).status(),200);
  await expect(page).toHaveURL(new RegExp('/workspace/'+uploadProject));
  const recovered=await getStudio(uploadProject);
  assert.equal(recovered.project.input.assets.length,1);
  assert.equal(recovered.project.input.assets[0].name,'replacement.png');
  assert.equal(recovered.run,null);
  await page.getByRole('button',{name:'素材与产物',exact:true}).click();
  await expect(page.getByAltText('replacement.png')).toBeVisible();
  await expect.poll(()=>page.getByAltText('replacement.png').evaluate(img=>img.complete&&img.naturalWidth>0)).toBe(true);
  checks.uploadRecoverySameProject=true;checks.actualFileValidation=true;
  await page.screenshot({path:resolve(out,'upload-recovered.png'),fullPage:true});
  assert.deepEqual(errors,[]);checks.noBrowserErrors=true;passed=true;
 }finally{
  if(browser)await browser.close();
  if(mode!=='prepare'&&fixture&&!fixture.cleaned){
   const cleanup=[];
   for(const id of fixture.createdIds){const response=await api.delete('/api/v1/projects/'+id,{headers});cleanup.push({id,status:response.status()})}
   checks.cleanup=cleanup;fixture.cleaned=cleanup.every(r=>r.status===200||r.status===204||r.status===404);
   writeFileSync(fixturePath,JSON.stringify(fixture,null,2));
   const report=mode==='verify-ui'?'verification-ui.json':'verification.json';
   writeFileSync(resolve(out,report),JSON.stringify({checkedAt:new Date().toISOString(),mode,passed,base,checks,errors,modelCalls:0},null,2));
   console.log(JSON.stringify({mode,passed,base,checks,errors,modelCalls:0}));
  }
  await api.dispose();
 }
})().catch(e=>{console.error(e.message);process.exitCode=1});
