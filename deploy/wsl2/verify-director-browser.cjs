// Real deployment acceptance; creates one isolated draft, no model work.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=b=>b.data??b;
(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','-u','root','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let entry={};
 for(const line of raw.split(/\r?\n/)){const i=line.indexOf(': ');if(i<0)continue;const k=line.slice(0,i),v=line.slice(i+2);if(k==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:v};}else if(k==='Password')entry.password=v;}
 if(entry.email&&entry.password)accounts.push(entry);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=`http://${ip}:8080`,output=resolve(__dirname,'../../backend/data/wsl-deployment/director-workflow/browser');mkdirSync(output,{recursive:true});
 const api=await request.newContext({baseURL:base});let account,headers,browser,id;const errors=[];let generationRequests=0;
 try{
  for(const candidate of accounts){const r=await api.post('/api/v1/auth/login',{data:candidate});if(r.status()===401)continue;assert.equal(r.status(),200);account=candidate;headers={Authorization:'Bearer '+unwrap(await r.json()).tokens.access_token};break;}
  assert.ok(account);
  const created=await api.post('/api/v1/avatar-mvp/projects',{headers,data:{title:'验收：新版口播11步流程',topic:'检查新版流程与文案参考上传',ratio:'9:16',style:'mg-flat-vector',use_avatar:true,use_animation:true}});assert.equal(created.status(),200);id=unwrap(await created.json()).id;
  const snapshot=await api.get('/api/v1/agent/project/'+id,{headers});assert.equal(snapshot.status(),200);const state=unwrap(await snapshot.json());assert.equal(state.workflow_plan.stages.length,11);assert.ok(state.workflow_plan.nodes.includes('F01.4'));
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST'&&/\/api\/v1\/(?:agent\/(?:chat|confirm)|avatar-mvp\/(?:runs|tasks))/.test(r.url()))generationRequests++;});
  await page.goto(base+'/login');await page.locator('input[name="email"]').fill(account.email);await page.locator('input[name="password"]').fill(account.password);await page.locator('button[type="submit"]').click();await expect(page).toHaveURL(base+'/');
  await page.goto(base+'/workspace/'+id);await expect(page.locator('[data-testid^="workflow-node-"]')).toHaveCount(11);
  await expect(page.getByTestId('workflow-node-F01.6')).toContainText('导演稿设计');await expect(page.getByTestId('workflow-node-F11.2')).toContainText('成片预览与修订');
  await page.screenshot({path:resolve(output,'eleven-stages.png'),fullPage:true});
  await page.getByRole('button',{name:'素材与产物',exact:true}).click();
  const chooser=page.waitForEvent('filechooser');await page.getByRole('button',{name:'添加素材',exact:true}).click();
  await (await chooser).setFiles({name:'reference-script.md',mimeType:'text/markdown',buffer:Buffer.from('# 参考文案\n先解释主题，再展示原理，结尾总结要点。')});
  await expect(page.getByRole('heading',{name:'reference-script.md'})).toBeVisible();
  await page.reload();await page.getByRole('button',{name:'素材与产物',exact:true}).click();await expect(page.getByRole('heading',{name:'reference-script.md'})).toBeVisible();
  assert.equal(await page.locator('img[alt="reference-script.md"]').count(),0);
  const saved=unwrap(await (await api.get('/api/v1/agent/project/'+id,{headers})).json());assert.equal(saved.project.input.assets[0].kind,'text');
  await page.screenshot({path:resolve(output,'text-reference.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});await page.getByRole('button',{name:'AI 画布流',exact:true}).click();
  await expect(page.getByTestId('workflow-node-F01.6')).toBeAttached();
  await expect(page.getByTestId('workflow-node-F01')).toContainText('参考文案：reference-script.md');
  await expect(page.locator('img[alt="reference-script.md"]')).toHaveCount(0);
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+2),'mobile page overflows horizontally');
  await page.screenshot({path:resolve(output,'mobile.png'),fullPage:true});
  assert.deepEqual(errors,[]);assert.equal(generationRequests,0);
  const report={url:base,projectId:id,stages:11,realTextUpload:true,persistsAfterReload:true,mobileNoOverflow:true,pageErrors:errors,generationRequests,scope:'Real draft and upload; review gates covered by integration tests',screenshots:output};writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
 }finally{if(browser)await browser.close();await api.dispose();}
})().catch(e=>{console.error(e.message);process.exitCode=1;});
