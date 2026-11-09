// Confirm UX acceptance: real disposable F02/F03 runs, no paid generation.
// Tool events, audio players and API-failure toast are labelled UI simulations.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/chat-confirmation');
mkdirSync(output,{recursive:true});
const wsl=args=>execFileSync('wsl.exe',['-d','Ubuntu-24.04','--',...args],{encoding:'utf8'});
function wav(){const rate=8000,samples=rate/2,b=Buffer.alloc(44+samples*2);b.write('RIFF');b.writeUInt32LE(b.length-8,4);b.write('WAVEfmt ',8);b.writeUInt32LE(16,16);b.writeUInt16LE(1,20);b.writeUInt16LE(1,22);b.writeUInt32LE(rate,24);b.writeUInt32LE(rate*2,28);b.writeUInt16LE(2,32);b.writeUInt16LE(16,34);b.write('data',36);b.writeUInt32LE(samples*2,40);return b}

(async()=>{
 const accounts=[];let entry={};
 for(const line of wsl(['cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt']).split(/\r?\n/)){
  const split=line.indexOf(': ');if(split<0)continue;const key=line.slice(0,split),value=line.slice(split+2);
  if(key==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:value}}
  if(key==='Password')entry.password=value;
 }
 if(entry.email&&entry.password)accounts.push(entry);
 const ip=wsl(['hostname','-I']).trim().split(/\s+/)[0];const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const api=await request.newContext({baseURL:base});const checks={},simulatedChecks={},errors=[],confirmBodies=[];
 let account,headers,browser,page,id,chatRequests=0;
 for(const candidate of accounts){const r=await api.post('/api/v1/auth/login',{data:candidate});if(r.status()===401)continue;assert.equal(r.status(),200);account=candidate;headers={Authorization:'Bearer '+unwrap(await r.json()).tokens.access_token};break}
 assert.ok(account,'No saved local account');
 async function getState(project){const r=await api.get('/api/v1/agent/project/'+project,{headers});assert.equal(r.status(),200);return unwrap(await r.json())}
 async function invoke(pkg,input){const r=await api.post(`/api/v1/agent/packages/${pkg}/invoke`,{headers,data:{projectId:id,input,requestId:crypto.randomUUID()}});assert.equal(r.status(),200,`Package ${pkg} failed: ${await r.text()}`);return unwrap(await r.json())}
 async function statusIs(value){await expect.poll(async()=>(await getState(id)).run?.status,{timeout:25000}).toBe(value)}
 try{
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1600,height:1000}});page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{const path=new URL(r.url()).pathname;if(r.method()==='POST'&&path==='/api/v1/agent/chat')chatRequests++;if(r.method()==='POST'&&/\/avatar-mvp\/runs\/[^/]+\/confirm$/.test(path))confirmBodies.push(r.postDataJSON())});
  await page.goto(base+'/login');await page.locator('input[name=email]').fill(account.email);await page.locator('input[name=password]').fill(account.password);await page.locator('button[type=submit]').click();await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  const chat=()=>page.getByRole('complementary',{name:'Agent 对话'});
  const status=()=>chat().getByRole('region',{name:'制作状态',exact:true});
  async function open(project){await page.goto(base+'/workspace/'+project);await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled({timeout:20000})}
  async function editFrom(node='F02'){
   await page.getByRole('combobox',{name:'定位制作步骤',exact:true}).click();
   await page.getByRole('option',{name:new RegExp('^'+node+' ')}).click();
   await page.getByTestId('workflow-node-'+node).getByRole('button',{name:node==='F02'?'编辑口播文案':'编辑第 1 段文案',exact:true}).click();
   const dialog=page.getByRole('dialog',{name:'编辑口播文案'});await expect(dialog).toBeVisible();return dialog;
  }
  const existing=process.env.AVATAR_REVIEW_PROJECT_ID||'132a5c74-fe66-4dbd-912b-3aec0cc9eb31',original=await getState(existing);
  assert.equal(original.run.status,'awaiting_confirmation');assert.equal(original.run.confirmation,'script');await open(existing);
  await expect(status()).toContainText('等待你确认文案');
  await expect(status().getByRole('region',{name:'待确认文案',exact:true})).toHaveCount(0);
  await expect(status().getByRole('button',{name:/查看文案|收起文案/})).toHaveCount(0);
  await expect(status()).not.toContainText(original.run.segments[0].text);
  await expect(status().getByRole('button',{name:'已确认，继续',exact:true})).toBeEnabled();
  checks.existingUserChatHasOnlyConfirmation=true;
  const originalTop=await page.locator('.react-flow').boundingBox();assert.ok(originalTop.y<200,'A status banner still displaces the canvas');
  assert.ok((await chat().boundingBox()).y<150,'Chat was pushed down by a banner');checks.onlyChatStateNoTopRecovery=true;
  await page.screenshot({path:resolve(output,'live-script-confirmation.png'),fullPage:true});
  const liveEditor=await editFrom();await expect(liveEditor.getByRole('textbox',{name:'第 1 段文案'})).toHaveValue(original.run.segments[0].text);
  await page.screenshot({path:resolve(output,'live-node-script-editor.png'),fullPage:true});
  await liveEditor.getByRole('button',{name:'取消',exact:true}).click();checks.existingUserNodeOpensEditableScript=true;

  const created=await api.post('/api/v1/avatar-mvp/projects',{headers,data:{title:'对话确认验收-'+Date.now(),topic:'本地交互验收',script:'第一句。第二句。',style:'flat-vector',use_avatar:true,use_animation:false}});assert.equal(created.status(),200);id=unwrap(await created.json()).id;
  await invoke('avatar.script',{});await statusIs('paused');await open(id);
  await invoke('avatar.edit-node',{node:'F03',changes:'本地分段确认验收',segments:[{id:'p1',chapter:'开场',text:'第一句。'},{id:'p2',chapter:'正文',text:'第二句。'}]});
  assert.equal((await getState(id)).proposal.target_node,'F03');
  await expect(page.getByRole('button',{name:'应用修改并重新制作',exact:true})).toBeVisible({timeout:15000});await page.getByRole('button',{name:'应用修改并重新制作',exact:true}).click();
  await statusIs('awaiting_confirmation');await expect(status()).toContainText('等待你确认文案',{timeout:15000});
  await expect(page.getByRole('button',{name:'已确认，继续',exact:true})).toHaveCount(1);
  await expect(status().getByRole('button',{name:'已确认，继续',exact:true})).toBeEnabled();
  await expect(status()).not.toContainText('第一句。');await expect(status()).not.toContainText('第二句。');
  await expect(status().getByRole('button',{name:/查看文案|收起文案/})).toHaveCount(0);
  checks.noDuplicateScriptInChat=true;
  const beforeEdit=await getState(id),replacement='手动修改后的第一句，保留到后续制作。';
  let editor=await editFrom();await editor.getByRole('textbox',{name:'第 1 段文案'}).fill('取消后不应保存。');
  await editor.getByRole('button',{name:'取消',exact:true}).click();
  assert.deepEqual((await getState(id)).run,beforeEdit.run);checks.cancelEditDoesNotChangeRun=true;
  editor=await editFrom('F03');await editor.getByRole('textbox',{name:'第 1 段文案'}).fill(replacement);
  await editor.getByRole('button',{name:'保存文案',exact:true}).click();await expect(editor).toHaveCount(0);
  const saved=await getState(id);assert.equal(saved.run.status,'awaiting_confirmation');assert.equal(saved.run.script_confirmed,false);
  assert.equal(saved.run.script,replacement+'第二句。');assert.equal(saved.run.segments[0].text,replacement);assert.equal(saved.run.segments[1].text,'第二句。');
  assert.notEqual(saved.run.confirmation_version,beforeEdit.run.confirmation_version);assert.equal(confirmBodies.length,0);checks.saveKeepsReviewAndRotatesVersion=true;
  await page.reload();await expect(page.getByTestId('workflow-node-F02')).toContainText(replacement);await expect(page.getByTestId('workflow-node-F03')).toContainText(replacement);
  editor=await editFrom();await expect(editor.getByRole('textbox',{name:'第 1 段文案'})).toHaveValue(replacement);await editor.getByRole('button',{name:'取消',exact:true}).click();
  checks.savedScriptPersistsInBothNodes=true;
  const oldApproval=await api.post(`/api/v1/avatar-mvp/runs/${saved.run.id}/confirm`,{headers,data:{kind:'script',version:beforeEdit.run.confirmation_version}});assert.equal(oldApproval.status(),409);
  assert.equal((await getState(id)).run.status,'awaiting_confirmation');checks.staleApprovalRejected=true;
  await page.screenshot({path:resolve(output,'script-review-on-canvas.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});editor=await editFrom();
  await expect(editor.getByRole('textbox',{name:'第 1 段文案'})).toHaveValue(replacement);
  await editor.getByRole('textbox',{name:'第 1 段文案'}).fill(replacement+'手机编辑');
  const mobileSave=editor.getByRole('button',{name:'保存文案',exact:true});await mobileSave.scrollIntoViewIfNeeded();await expect(mobileSave).toBeInViewport();await expect(mobileSave).toBeEnabled();
  const editorBounds=await editor.boundingBox();assert.ok(editorBounds.x>=0&&editorBounds.x+editorBounds.width<=391);
  await page.screenshot({path:resolve(output,'mobile-script-editor.png'),fullPage:true});await editor.getByRole('button',{name:'取消',exact:true}).click();
  checks.mobileScriptEditorFitsAndActionsReachable=true;await page.setViewportSize({width:1600,height:1000});await open(id);
  await status().getByRole('button',{name:'取消',exact:true}).click();await expect(status()).toContainText('已取消本次确认');
  assert.equal((await getState(id)).run.status,'awaiting_confirmation');assert.equal(confirmBodies.length,0);checks.cancelDoesNotApproveOrStart=true;
  await page.reload();await expect(status()).toContainText('等待你确认文案');
  await page.getByRole('textbox',{name:'向 Agent 发送指令'}).fill('已确认');await page.getByRole('button',{name:'发送消息',exact:true}).click();
  await expect(chat().getByText(/文字回复不会直接批准/)).toBeVisible();assert.equal(confirmBodies.length,0);assert.equal(chatRequests,0);checks.typedConfirmationIsGuidanceOnly=true;
  const beforeApproval=await getState(id);
  await status().getByRole('button',{name:'已确认，继续',exact:true}).click();
  await statusIs('paused');await expect(status()).toContainText('制作已暂停',{timeout:15000});
  const afterApproval=await getState(id);assert.equal(afterApproval.run.script_confirmed,true);assert.equal(afterApproval.run.target_node,'F03');assert.ok(afterApproval.run.steps.every(s=>!s.id.startsWith('F04')&&!s.provider_task_id&&!s.submission));
  assert.equal(afterApproval.run.script,replacement+'第二句。');assert.equal(afterApproval.run.segments[0].text,replacement);checks.continuationUsesEditedScript=true;
  assert.deepEqual(confirmBodies,[{kind:'script',version:beforeApproval.run.confirmation_version}]);checks.realVersionedConfirmationContinues=true;

  // Feed deterministic SSE tool events through the actual stream parser and
  // hold the simulated reply until the compact in-progress display is checked.
  let publishEvents,completeReply;
  const streamEvents=new Promise(resolve=>{publishEvents=resolve}),replyReady=new Promise(resolve=>{completeReply=resolve});
  await context.route('**/api/v1/agent/stream?*',async route=>{
   const events=await streamEvents;
   await route.fulfill({status:200,contentType:'text/event-stream',body:events.map(event=>'data: '+JSON.stringify(event)+'\n\n').join('')});
  });
  await context.route('**/api/v1/agent/chat',async route=>{
   const {turnId}=route.request().postDataJSON();
   publishEvents(['get_project_state',beforeApproval.packages.find(p=>p.node==='F01')?.tool||'configure_project','plan_project'].map((tool,i)=>({type:'tool',turnId,callId:'check-'+i,tool,status:'done'})));
   await replyReady;
   await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({status:'ok',turnId,reply:'验收回复：项目检查已完成。'})});
  });
  await page.reload();await expect(status()).toContainText('制作已暂停');
  await page.getByRole('textbox',{name:'向 Agent 发送指令'}).fill('验收过程记录显示');await page.getByRole('button',{name:'发送消息',exact:true}).click();
  await expect(chat().getByRole('group',{name:'Agent 执行过程'})).toHaveCount(1);
  const processLog=chat().getByRole('group',{name:'Agent 执行过程'}).first();
  await expect(processLog.locator('li')).toHaveCount(3);
  assert.equal(await processLog.evaluate(el=>el.open),false);
  await processLog.locator('summary').click();await expect(processLog.locator('li').filter({hasText:'规划制作流程'})).toBeVisible();
  completeReply();await expect(processLog.locator('summary')).toHaveText('执行过程 · 3 项');
  await expect.poll(()=>processLog.evaluate(el=>el.open)).toBe(false);
  assert.ok((await processLog.boundingBox()).height<=35,'Collapsed tool process takes too much space');
  await expect(chat().locator('[data-chat-item="message"]:has([data-agent-process])')).toContainText('验收回复：项目检查已完成。');
  await page.screenshot({path:resolve(output,'compact-agent-process.png'),fullPage:true});
  await processLog.locator('summary').click();await page.screenshot({path:resolve(output,'expanded-agent-process.png'),fullPage:true});
  await page.getByRole('textbox',{name:'向 Agent 发送指令'}).fill('下一条对话');await page.getByRole('button',{name:'发送消息',exact:true}).click();
  const nextMessage=chat().getByText('下一条对话',{exact:true});
  assert.ok(await processLog.evaluate((el,text)=>!!(el.compareDocumentPosition([...document.querySelectorAll('[data-chat-item="message"]')].find(node=>node.textContent===text))&Node.DOCUMENT_POSITION_FOLLOWING),'下一条对话'));
  await expect(nextMessage).toBeVisible();
  await expect(chat().getByText('验收回复：项目检查已完成。',{exact:true})).toHaveCount(2);
  simulatedChecks.toolProcessCollapsedInReplyAndKeepsChronology=true;
  await context.unroute('**/api/v1/agent/chat');await context.unroute('**/api/v1/agent/stream?*');

  // Mock only the following snapshots/media/error response for UI coverage.
  const route=`**/api/v1/agent/project/${id}`;
  async function preview(snapshot){await context.unroute(route);await context.route(route,r=>r.fulfill({status:200,contentType:'application/json',body:JSON.stringify({data:snapshot})}));await open(id)}
  const audio=structuredClone(afterApproval);audio.run.status='awaiting_confirmation';audio.run.confirmation='audio';audio.run.confirmation_version='ui-audio';audio.run.stage='F04';audio.run.segments.forEach(s=>{s.audio=`audio/${s.id}.wav`;s.duration=.5});
  await context.route('**/api/v1/agent/runs/*/segments/*/audio',r=>r.fulfill({status:200,contentType:'audio/wav',body:wav()}));
  await preview(audio);await status().getByRole('button',{name:'查看并试听配音',exact:true}).click();
  await expect(status().locator('audio')).toHaveCount(2);await expect.poll(()=>status().locator('audio').evaluateAll(nodes=>nodes.every(n=>n.readyState>=1&&n.duration>0))).toBe(true);
  await page.screenshot({path:resolve(output,'audio-review-in-chat.png'),fullPage:true});simulatedChecks.audioPreviewLoadsEverySegment=true;
  await status().getByRole('button',{name:'取消',exact:true}).click();assert.equal(confirmBodies.length,1);

  const failed=structuredClone(afterApproval);failed.testing_mode=false;failed.run.status='failed';failed.run.error='验收模拟：服务请求失败';
  await preview(failed);await context.route('**/api/v1/agent/packages/avatar.control/invoke',r=>r.fulfill({status:400,contentType:'application/json',body:JSON.stringify({error:'验收模拟：恢复请求未被接受'})}));
  const beforeCanvas=await page.locator('.react-flow').boundingBox(),beforeChat=await chat().boundingBox();
  await status().getByRole('button',{name:'恢复当前运行',exact:true}).click();await expect(page.getByRole('alert')).toContainText('恢复请求未被接受');
  const notice=page.locator('[data-workspace-notice]');assert.equal(await notice.evaluate(el=>getComputedStyle(el).position),'fixed');assert.ok(Number(await notice.evaluate(el=>getComputedStyle(el).zIndex))>100);
  assert.equal(await notice.evaluate(el=>!!el.closest('main')),false);assert.equal((await page.locator('.react-flow').boundingBox()).y,beforeCanvas.y);assert.equal((await chat().boundingBox()).y,beforeChat.y);
  await page.screenshot({path:resolve(output,'floating-error-no-displacement.png'),fullPage:true});simulatedChecks.errorFloatsWithoutDisplacingWorkspace=true;
  await page.getByRole('button',{name:'关闭提示',exact:true}).click();await expect(notice).toHaveCount(0);

  await page.setViewportSize({width:390,height:844});await preview(beforeApproval);
  const mobileConfirm=status().getByRole('button',{name:'已确认，继续',exact:true});
  await mobileConfirm.scrollIntoViewIfNeeded();await expect(mobileConfirm).toBeInViewport();await expect(mobileConfirm).toBeEnabled();
  await page.screenshot({path:resolve(output,'mobile-script-confirmation.png'),fullPage:true});
  await preview(audio);await page.getByRole('button',{name:'查看制作状态',exact:true}).click();await status().getByRole('button',{name:'查看并试听配音',exact:true}).click();
  await mobileConfirm.scrollIntoViewIfNeeded();await expect(mobileConfirm).toBeInViewport();await expect(mobileConfirm).toBeEnabled();
  const bounds=await status().boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=391);await page.screenshot({path:resolve(output,'mobile-chat-review.png'),fullPage:true});simulatedChecks.mobileReviewReachable=true;
  await context.unroute(route);
  const unchanged=await getState(existing);assert.deepEqual(unchanged.run,original.run);assert.deepEqual(unchanged.project,original.project);checks.userProjectUnchanged=true;
  assert.equal(chatRequests,2);assert.deepEqual(errors,[]);
  const report={url:base+'/',checks,simulatedChecks,pageErrors:errors,chatRequests,realApprovalRequests:confirmBodies.length,paidProviderRequests:0};writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
 }catch(error){if(page)await page.screenshot({path:resolve(output,'failure.png'),fullPage:true}).catch(()=>{});throw error}
 finally{
  if(browser)await browser.close();if(id){const r=await api.delete('/api/v1/projects/'+id,{headers});assert.ok(r.ok(),'Disposable fixture cleanup failed')}await api.dispose();
 }
})().catch(e=>{console.error(e.message);process.exitCode=1});
