// WSL acceptance: only a disposable project is written. Its supplied script
// stops at F02, before any model/TTS/video call. Existing completed projects are read-only.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/node-operations');
mkdirSync(output,{recursive:true});

(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let entry={};
 for(const line of raw.split(/\r?\n/)){const split=line.indexOf(': ');if(split<0)continue;const key=line.slice(0,split),value=line.slice(split+2);if(key==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:value}}if(key==='Password')entry.password=value}
 if(entry.email&&entry.password)accounts.push(entry);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const api=await request.newContext({baseURL:base});let account,headers,browser,page,id;const checks={},errors=[];
 for(const candidate of accounts){const response=await api.post('/api/v1/auth/login',{data:candidate});if(response.status()===401)continue;assert.equal(response.status(),200);headers={Authorization:'Bearer '+unwrap(await response.json()).tokens.access_token};account=candidate;break}
 assert.ok(account,'No valid saved local account');
 async function getState(project){const res=await api.get('/api/v1/agent/project/'+project,{headers});assert.equal(res.status(),200);return unwrap(await res.json())}
 async function invoke(pkg,input){const res=await api.post(`/api/v1/agent/packages/${pkg}/invoke`,{headers,data:{projectId:id,input,requestId:crypto.randomUUID()}});assert.equal(res.status(),200,`Package ${pkg} failed`);return unwrap(await res.json())}
 try {
  const created=await api.post('/api/v1/avatar-mvp/projects',{headers,data:{title:'节点编辑验收-'+Date.now(),topic:'隔离回归测试',script:'第一句。第二句。',style:'flat-vector',use_avatar:false}});
  assert.equal(created.status(),200);id=unwrap(await created.json()).id;
  const first=await invoke('avatar.script',{});
  await expect.poll(async()=>(await getState(id)).run?.status,{timeout:20000}).toBe('paused');
  let snapshot=await getState(id);
  assert.equal(snapshot.run.target_node,'F02');assert.equal(snapshot.run.stage,'F02');
  assert.ok(snapshot.run.steps.every(s=>!s.provider_task_id&&!s.submission));checks.localOnlyInitialRun=true;
  assert.ok(snapshot.packages.some(p=>p.id==='avatar.rerun'));assert.ok(snapshot.packages.some(p=>p.id==='avatar.edit-node'));checks.packagesDeployed=true;

  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1600,height:1000}});page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  let chatRequests=0;page.on('request',r=>{if(r.method()==='POST'&&r.url().includes('/api/v1/agent/chat'))chatRequests++});
  await page.goto(base+'/login');await page.locator('input[name=email]').fill(account.email);await page.locator('input[name=password]').fill(account.password);await page.locator('button[type=submit]').click();
  await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  async function open(project){await page.goto(base+'/workspace/'+project);await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled({timeout:20000})}
  async function detail(p){
   const input=page.getByRole('combobox',{name:'定位制作步骤',exact:true});await input.fill(`${p.node} ${p.title}`);await page.getByRole('option',{name:`${p.node} ${p.title}`,exact:true}).click();
   await page.getByRole('button',{name:`查看${p.title}详情`,exact:true}).click();await expect(page.getByRole('complementary',{name:'步骤详情'})).toBeVisible();
  }
  await open(id);
  const nodes=snapshot.packages.filter(p=>p.node);
  for(const p of nodes){await detail(p);await page.getByRole('button',{name:'让 Agent 编辑此节点',exact:true}).click();await expect(page.getByRole('textbox',{name:'向 Agent 发送指令'})).toHaveValue(new RegExp(p.node.replace('.','\\.')));await page.getByRole('button',{name:'返回画布'}).click()}
  checks.all13NodeEditContexts=true;
  await detail(nodes.find(p=>p.node==='F02'));
  await page.getByRole('button',{name:'从此步骤重做',exact:true}).click();
  await expect(page.getByText('修改方案 · 等待确认',{exact:true})).toBeVisible();
  snapshot=await getState(id);assert.equal(snapshot.proposal.node,'F02');assert.equal(snapshot.proposal.target_node,'F12');assert.equal(snapshot.run.id,first.run_id);checks.rerunButtonDraftOnly=true;
  await page.reload();await expect(page.getByText('修改方案 · 等待确认',{exact:true})).toBeVisible();checks.proposalPersistsOnRefresh=true;
  await invoke('avatar.edit-node',{node:'F02',script:'修改后的完整口播稿。',changes:'验收全文编辑方案'});
  snapshot=await getState(id);assert.equal(snapshot.proposal.edits.script,'修改后的完整口播稿。');assert.equal(snapshot.run.script,'第一句。第二句。');checks.editDraftPreservesCurrentOutput=true;
  const replacedProposal=snapshot.proposal;
  for(const [pkg,input] of [
   ['avatar.edit-node',{node:'F11',changes:'隔离非法颜色测试',presentation:{subtitle_color:'#zzzzzz'}}],
   ['avatar.edit-node',{node:'F07',changes:'隔离空素材参数测试',needs:null}],
   ['avatar.rerun',{from:'F10',target:'F04',changes:'隔离反向范围测试'}],
  ]){
   const res=await api.post(`/api/v1/agent/packages/${pkg}/invoke`,{headers,data:{projectId:id,input,requestId:crypto.randomUUID()}});
   assert.ok(res.status()>=400&&res.status()<500,`Invalid ${pkg} input was accepted`);
   const unchanged=await getState(id);assert.deepEqual(unchanged.proposal,replacedProposal);assert.equal(unchanged.run.id,first.run_id);
  }
  checks.invalidNodeRequestsDoNotReplaceProposal=true;

  // Confirm only the local F02 -> F02 range. Never confirm the full-chain draft.
  await invoke('avatar.rerun',{from:'F02',target:'F02',changes:'仅重做读取已提供文案的本地步骤'});
  snapshot=await getState(id);const proposal=snapshot.proposal;
  const stale=await api.post('/api/v1/agent/confirm',{headers,data:{projectId:id,accept:true,proposalId:replacedProposal.id,revision:replacedProposal.revision}});
  assert.ok(stale.status()>=400&&stale.status()<500);assert.equal((await getState(id)).run.id,first.run_id);checks.replacedProposalCannotRun=true;
  await page.reload();await page.getByRole('button',{name:'应用修改并重新制作',exact:true}).click();
  await expect.poll(async()=>{const state=await getState(id);return state.run.id!==first.run_id&&state.run.status==='paused'},{timeout:20000}).toBe(true);
  snapshot=await getState(id);assert.equal(snapshot.run.target_node,'F02');assert.equal(snapshot.run.edits.rerun_from,'F02');assert.equal(snapshot.run.reused_from,first.run_id);
  assert.deepEqual(snapshot.run.steps.map(s=>s.id),['F02']);assert.ok(snapshot.run.steps.every(s=>!s.provider_task_id&&!s.submission));checks.confirmHonorsStartAndEnd=true;
  const repeated=await api.post('/api/v1/agent/confirm',{headers,data:{projectId:id,accept:true,proposalId:proposal.id,revision:proposal.revision}});assert.equal(repeated.status(),200);assert.equal((await getState(id)).run.id,snapshot.run.id);checks.duplicateConfirmIsIdempotent=true;
  await page.screenshot({path:resolve(output,'isolated-rerun.png'),fullPage:true});

  const historical='929b12bd-1f75-4d7b-9725-60223b474ca6';
  const before=await getState(historical);await open(historical);await detail(nodes.find(p=>p.node==='F04'));
  await expect(page.getByRole('button',{name:'执行到此步骤',exact:true})).toBeDisabled();await expect(page.getByRole('button',{name:'从此步骤重做',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'让 Agent 编辑此节点',exact:true}).click();await expect(page.getByRole('textbox',{name:'向 Agent 发送指令'})).toHaveValue(/F04.*配音和音色/);
  const after=await getState(historical);assert.equal(after.run.id,before.run.id);assert.deepEqual(after.proposal,before.proposal);checks.completedProjectEditableWithoutMutation=true;
  await page.screenshot({path:resolve(output,'completed-project-edit.png'),fullPage:true});
  assert.equal(chatRequests,0);assert.deepEqual(errors,[]);
  const report={url:base+'/',checks,pageErrors:errors,chatRequests,paidProviderRequests:0,fixture:id};writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
 } finally {
  if(browser)await browser.close();
  if(id){const res=await api.delete('/api/v1/projects/'+id,{headers});assert.ok(res.ok(),'Cannot delete disposable fixture')}
  await api.dispose();
 }
})().catch(e=>{console.error(e.message);process.exitCode=1});
