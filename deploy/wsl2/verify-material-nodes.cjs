// Real browser/API checks against the existing WSL deployment. No model jobs.
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
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const output=resolve(__dirname,'../../backend/data/wsl-deployment/material-nodes');mkdirSync(output,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
 try{
  const context=await browser.newContext({viewport:{width:1600,height:1050}}),page=await context.newPage();
  let valid,headers;const errors=[],actions=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST'&&r.url().includes('/materials/')){const body=r.postDataJSON();actions.push(body.action);assert.ok(['save'].includes(body.action),'Browser verification must not start model work')}});
  for(const a of accounts){const r=await context.request.post(base+'/api/v1/auth/login',{data:a});if(r.status()!==200)continue;valid=a;headers={Authorization:'Bearer '+unwrap(await r.json()).tokens.access_token};break}
  assert.ok(valid,'Saved account login failed');
  const api=async path=>{const r=await context.request.get(base+path,{headers});assert.equal(r.status(),200);return unwrap(await r.json())};
  const projects=await api('/api/v1/projects/?limit=30&offset=0&sort_by=created_at&sort_order=desc');
  let state;
  for(const p of projects.projects??projects.items??projects){const s=await api('/api/v1/agent/project/'+p.id);if(s.workflow_plan?.stages?.length&&s.run?.needs?.length){state=s;break}}
  assert.ok(state,'No existing project with planned per-video nodes');
  const run=state.run,project=state.project.id;
  const need=run.needs.find(n=>n.kind!=='real'&&run.artifacts?.some(a=>a.id==='material-'+n.id))||run.needs.find(n=>n.kind!=='real');
  assert.ok(need);const node=`${({avatar:'F06',animation:'F07',real:'F08'})[need.kind]}/${need.id}`;
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name="email"]').fill(valid.email);await page.locator('input[name="password"]').fill(valid.password);await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(base+'/');
  await page.goto(base+'/workspace/'+project,{waitUntil:'domcontentloaded'});
  await expect(page.getByTestId('workflow-canvas')).toBeVisible();
  await expect(page.locator('[data-testid^="workflow-node-F06/"], [data-testid^="workflow-node-F07/"], [data-testid^="workflow-node-F08/"]')).toHaveCount(run.needs.length);
  const choose=async id=>{const selector=page.getByRole('combobox',{name:'定位制作步骤'});await selector.fill(id);await page.getByRole('option').filter({hasText:id}).first().click();await page.waitForTimeout(350)};
  await choose(node);
  const card=page.getByTestId('workflow-node-'+node);await expect(card).toBeVisible();
  await page.screenshot({path:resolve(output,'individual-video-node.png'),fullPage:true});
  await card.getByRole('button',{name:/查看.*详情/}).click();
  const detail=page.getByRole('complementary',{name:'步骤详情'});
  await expect(detail.getByText('当前视频使用的提示词',{exact:true})).toBeVisible();
  let playback=false;
  if(run.artifacts?.some(a=>a.id==='material-'+need.id)){
   const video=detail.locator('video');await expect(video).toBeVisible({timeout:60000});
   await expect.poll(()=>video.evaluate(v=>v.readyState),{timeout:60000}).toBeGreaterThan(1);
   await video.evaluate(async v=>{v.muted=true;await v.play()});
   await expect.poll(()=>video.evaluate(v=>v.currentTime)).toBeGreaterThan(0);
   await video.evaluate(v=>v.pause());playback=true;
  }
  const prompt=state.material_drafts?.[need.id]?.prompt??need.prompt??'';
  const editor=detail.getByLabel('提示词草稿');await expect(editor).toHaveValue(prompt);
  // Persist the identical value: production inputs and outputs must not change.
  await editor.fill(prompt);
  await detail.getByRole('button',{name:'保存提示词'}).click();
  await expect(detail.getByText('提示词已保存，视频尚未重新生成。')).toBeVisible();
  const after=await api('/api/v1/agent/project/'+project);
  assert.equal(after.run.id,run.id);assert.deepEqual(after.run.needs,run.needs);assert.deepEqual(after.run.materials,run.materials);assert.deepEqual(after.run.artifacts,run.artifacts);
  assert.equal(after.material_drafts[need.id].prompt,prompt);
  await page.screenshot({path:resolve(output,'video-prompt-editor.png'),fullPage:true});
  await page.reload({waitUntil:'domcontentloaded'});await choose(node);await page.getByTestId('workflow-node-'+node).getByRole('button',{name:/查看.*详情/}).click();
  await expect(page.getByLabel('提示词草稿')).toHaveValue(prompt);
  await page.getByRole('button',{name:'返回画布',exact:true}).click();await choose('F10');
  const merge=page.getByTestId('workflow-node-F10');await expect(merge).toContainText('素材汇总与镜头组装');
  await merge.getByRole('button',{name:/查看.*详情/}).click();
  await expect(page.getByRole('complementary',{name:'步骤详情'}).getByRole('button',{name:'查看视频与提示词'})).toHaveCount(run.needs.length);
  await page.screenshot({path:resolve(output,'material-merge.png'),fullPage:true});
  assert.deepEqual(errors,[]);assert.deepEqual(actions,['save']);
  const report={base,ip,port:8080,project,run:run.id,videos:run.needs.length,individualNodes:true,mergeInputs:run.needs.length,promptPersistence:true,playback,modelJobsSubmitted:0,errors};
  writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
 }finally{await browser.close()}
})().catch(e=>{console.error(e.message);process.exitCode=1});
