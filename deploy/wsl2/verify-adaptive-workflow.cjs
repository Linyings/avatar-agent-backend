// Real WSL acceptance uses disposable projects and supplied scripts/segments.
// F02/F03 target limits prevent model, TTS or paid video generation.
// Running/error/activity snapshots below are explicitly labelled UI simulations.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync,readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/adaptive-workflow');
mkdirSync(output,{recursive:true});
const wsl=args=>execFileSync('wsl.exe',['-d','Ubuntu-24.04','--',...args],{encoding:'utf8'});

(async()=>{
 const accounts=[];let entry={};
 for(const line of wsl(['cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt']).split(/\r?\n/)){
  const split=line.indexOf(': ');if(split<0)continue;const key=line.slice(0,split),value=line.slice(split+2);
  if(key==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:value}}
  if(key==='Password')entry.password=value;
 }
 if(entry.email&&entry.password)accounts.push(entry);
 const ip=wsl(['hostname','-I']).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const api=await request.newContext({baseURL:base});const created=[],checks={},simulatedChecks={},errors=[];
 let account,headers,browser,page,chatRequests=0;
 for(const candidate of accounts){
  const response=await api.post('/api/v1/auth/login',{data:candidate});if(response.status()===401)continue;
  assert.equal(response.status(),200);account=candidate;headers={Authorization:'Bearer '+unwrap(await response.json()).tokens.access_token};break;
 }
 assert.ok(account,'No valid saved deployment account');
 async function getState(id){const res=await api.get('/api/v1/agent/project/'+id,{headers});assert.equal(res.status(),200);return unwrap(await res.json())}
 async function invoke(id,pkg,input){const res=await api.post(`/api/v1/agent/packages/${pkg}/invoke`,{headers,data:{projectId:id,input,requestId:crypto.randomUUID()}});assert.equal(res.status(),200,`Package ${pkg}: ${res.status()} ${await res.text()}`);return unwrap(await res.json())}
 async function create(input){const res=await api.post('/api/v1/avatar-mvp/projects',{headers,data:{title:'动态流程验收-'+Date.now(),topic:'隔离本地验收',script:'第一句。第二句。',style:'flat-vector',use_avatar:true,...input}});assert.equal(res.status(),200);const id=unwrap(await res.json()).id;created.push(id);return id}
 async function statusIs(id,status){await expect.poll(async()=>(await getState(id)).run?.status,{timeout:25000}).toBe(status)}
 try{
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1600,height:1000}});
  page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(r.method()==='POST'&&new URL(r.url()).pathname==='/api/v1/agent/chat')chatRequests++});
  await page.goto(base+'/login');await page.locator('input[name=email]').fill(account.email);await page.locator('input[name=password]').fill(account.password);await page.locator('button[type=submit]').click();
  await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  const status=()=>page.getByRole('region',{name:'制作状态',exact:true});
  async function open(id){await page.goto(base+'/workspace/'+id);await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled({timeout:20000})}
  async function nodeIDs(){return page.locator('.react-flow__node').evaluateAll(nodes=>nodes.map(n=>n.getAttribute('data-id')))}
  function noUnneededNodes(snapshot){for(const node of ['F01.4','F07','F08'])assert.ok(!snapshot.workflow_plan.nodes.includes(node),`Unneeded ${node}`);assert.ok(snapshot.workflow_plan.nodes.includes('F06'))}

  // Preserve the user's unfinished project; read it without sending chat.
  const existing='5daa1faa-a31d-428d-8eaa-521d0eca73d5';
  const original=await getState(existing);assert.equal(original.run,null);noUnneededNodes(original);
  await open(existing);await expect(status()).toContainText('制作尚未启动');await expect(status().getByRole('button',{name:'让 Agent 开始制作',exact:true})).toBeVisible();
  const visible=await nodeIDs();assert.ok(!visible.includes('F01.4'));checks.existingProjectShowsNotStartedAndAdaptivePlan=true;
  await page.screenshot({path:resolve(output,'existing-project-not-started.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});await expect(status()).toBeVisible();
  const bounds=await status().boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=391,'Status overflows mobile viewport');
  await page.screenshot({path:resolve(output,'mobile-status.png'),fullPage:true});checks.mobileStatusVisible=true;
  await page.setViewportSize({width:1600,height:1000});

  const id=await create({use_animation:false});
  await invoke(id,'avatar.script',{});await statusIs(id,'paused');
  let snapshot=await getState(id);assert.equal(snapshot.run.target_node,'F02');noUnneededNodes(snapshot);
  await open(id);await expect(status()).toContainText('制作已暂停');await expect(status()).toContainText('指定步骤 F02');
  checks.actualPauseAndTargetVisible=true;
  await invoke(id,'avatar.edit-node',{node:'F03',changes:'本地验收分段',segments:[{id:'p1',chapter:'开场',text:'第一句。'},{id:'p2',chapter:'正文',text:'第二句。'}]});
  snapshot=await getState(id);assert.equal(snapshot.proposal.target_node,'F03','Only local target may be confirmed');
  await expect(page.getByRole('button',{name:'应用修改并重新制作',exact:true})).toBeVisible({timeout:15000});
  await page.getByRole('button',{name:'应用修改并重新制作',exact:true}).click();
  await statusIs(id,'awaiting_confirmation');await expect(status()).toContainText('等待你确认文案',{timeout:15000});
  snapshot=await getState(id);assert.equal(snapshot.run.confirmation,'script');assert.equal(snapshot.run.target_node,'F03');
  assert.ok(snapshot.run.steps.every(s=>!s.provider_task_id&&!s.submission));checks.actualApprovalGateVisible=true;
  await page.reload();await expect(status()).toContainText('等待你确认文案');
  await page.screenshot({path:resolve(output,'waiting-script-confirmation.png'),fullPage:true});checks.approvalSurvivesRefresh=true;
  await status().getByRole('button',{name:'确认文案并继续',exact:true}).click();
  await statusIs(id,'paused');await expect(status()).toContainText('制作已暂停',{timeout:15000});
  snapshot=await getState(id);assert.equal(snapshot.run.script_confirmed,true);assert.equal(snapshot.run.target_node,'F03');
  assert.ok(!snapshot.run.steps.some(s=>s.id.startsWith('F04')));assert.ok(snapshot.run.steps.every(s=>!s.provider_task_id&&!s.submission));checks.confirmationUpdatesWithoutTTS=true;
  await invoke(id,'avatar.control',{action:'stop'});await expect(status()).toContainText('制作已停止',{timeout:15000});checks.actualStopVisible=true;

  // An old draft without the new option must keep it absent when saving a title.
  const legacy=await create({});await open(legacy);
  await page.getByRole('button',{name:'制作设置',exact:true}).click();
  await page.getByRole('textbox',{name:'项目名称',exact:true}).fill('旧项目设置兼容验收');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(async()=>(await getState(legacy)).project.input.title).toBe('旧项目设置兼容验收');
  assert.equal((await getState(legacy)).project.input.use_animation,undefined);checks.legacyAnimationPreferencePreserved=true;
  const media=resolve(__dirname,'../../backend/data/wsl-deployment/home-attachments/reference-one.mp4');
  const upload=await api.post(`/api/v1/avatar-mvp/projects/${legacy}/assets`,{headers,multipart:{kind:'video',file:{name:'reference.mp4',mimeType:'video/mp4',buffer:readFileSync(media)}}});
  assert.equal(upload.status(),200);snapshot=await getState(legacy);assert.ok(snapshot.workflow_plan.nodes.includes('F01.4'));checks.videoAddsVisualUnderstanding=true;
  await invoke(legacy,'avatar.configure',{revision:snapshot.project.input.revision,use_avatar:false,use_animation:true});
  snapshot=await getState(legacy);assert.ok(snapshot.workflow_plan.nodes.includes('F07'));assert.ok(!snapshot.workflow_plan.nodes.includes('F06'));checks.animationOnlyPlan=true;
  await open(legacy);await expect.poll(async()=>await nodeIDs()).toContain('F01.4');
  await page.screenshot({path:resolve(output,'video-animation-plan.png'),fullPage:true});

  // UI-only snapshots cover slow/failed providers without invoking one.
  const routePattern=`**/api/v1/agent/project/${legacy}`;
  async function preview(state){await context.unroute(routePattern);await context.route(routePattern,r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({data:state})}));await open(legacy)}
  const baseline=await getState(id);
  const running=structuredClone(baseline);running.run.status='running';running.run.stage='F02';running.run.target_node='F12';running.run.created_at=new Date().toISOString();
  await preview(running);await expect(status()).toContainText('正在执行 F02');await expect(status()).toContainText('后台任务持续执行');await expect(status()).toContainText('已运行');simulatedChecks.runningWithProgress=true;
  const failed=structuredClone(running);failed.run.status='failed';failed.run.error='验收模拟：服务请求超时';
  await preview(failed);await expect(status()).toContainText('制作异常');await expect(status()).toContainText('服务请求超时');await expect(status().getByRole('button',{name:'恢复当前运行',exact:true})).toBeVisible();simulatedChecks.errorWithRecoveryAction=true;
  const thinking=structuredClone(snapshot);thinking.run=null;thinking.agent_activity={status:'running',phase:'thinking',turn_id:'ui-test',started_at:new Date().toISOString(),updated_at:new Date().toISOString()};
  await preview(thinking);await expect(status()).toContainText('Agent 正在处理需求');await expect(page.getByRole('button',{name:'停止回复',exact:true})).toBeVisible();await page.reload();await expect(status()).toContainText('Agent 正在处理需求');simulatedChecks.agentActivitySurvivesRefresh=true;
  await context.unroute(routePattern);
  const unchanged=await getState(existing);assert.equal(unchanged.run,null);assert.deepEqual(unchanged.project,original.project);assert.deepEqual(unchanged.proposal,original.proposal);checks.userProjectUnchanged=true;
  assert.equal(chatRequests,0);assert.deepEqual(errors,[]);
  const report={url:base+'/',checks,simulatedChecks,pageErrors:errors,chatRequests,paidProviderRequests:0,fixtures:created};
  writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
 }finally{
  if(browser)await browser.close();
  for(const id of created){const res=await api.delete('/api/v1/projects/'+id,{headers});assert.ok(res.ok(),'Disposable fixture cleanup failed')}
  await api.dispose();
 }
})().catch(e=>{console.error(e.message);process.exitCode=1});
