// Read-only checks against the deployed WSL app. The final chat scenario uses
// intercepted history/replies so no user conversation or model task is created.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync,statSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=body=>body.data??body;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/delivery-chat');
const wsl=args=>execFileSync('wsl.exe',['-d','Ubuntu-24.04','-u','root','--',...args],{encoding:'utf8'});

(async()=>{
 mkdirSync(output,{recursive:true});
 const accounts=[];let item={};
 for(const line of wsl(['cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt']).split(/\r?\n/)){
  const i=line.indexOf(': ');if(i<0)continue;const key=line.slice(0,i),value=line.slice(i+2);
  if(key==='Email'){if(item.email&&item.password)accounts.push(item);item={email:value}}
  if(key==='Password')item.password=value;
 }
 if(item.email&&item.password)accounts.push(item);
 const ip=wsl(['hostname','-I']).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const api=await request.newContext({baseURL:base});
 const checks={},simulatedChecks={},errors=[];let browser,page,headers,account,modelRequests=0;
 try{
  for(const candidate of accounts){
   const response=await api.post('/api/v1/auth/login',{data:candidate});if(response.status()===401)continue;
   assert.equal(response.status(),200);headers={Authorization:'Bearer '+unwrap(await response.json()).tokens.access_token};account=candidate;break;
  }
  assert.ok(account,'No valid saved deployment account');
  const projectsResponse=await api.get('/api/v1/projects/',{headers,params:{limit:100}});assert.equal(projectsResponse.status(),200);
  const payload=unwrap(await projectsResponse.json()),projects=Array.isArray(payload)?payload:payload.projects??payload.items??[];
  let snapshot;
  const getState=async id=>{const response=await api.get('/api/v1/agent/project/'+id,{headers});assert.equal(response.status(),200);return unwrap(await response.json())};
  for(const p of projects){
   if(p.metadata?.inference_mode!=='avatar'&&p.workflow?.mode!=='avatar')continue;
   const state=await getState(p.id),ids=state.run?.artifacts?.map(a=>a.id)??[];
   if(state.run?.status==='succeeded'&&ids.includes('video')&&ids.includes('cover')&&ids.some(id=>id.startsWith('preview-'))){snapshot=state;break}
  }
  assert.ok(snapshot,'No existing finished project with video, cover and previews');
  const id=snapshot.project.id;
  const historyResponse=await api.get('/api/v1/agent/history',{headers,params:{projectId:id}});assert.equal(historyResponse.status(),200);
  const history=await historyResponse.json();
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1600,height:1000},reducedMotion:'reduce'});
  page=await context.newPage();page.on('pageerror',error=>errors.push(error.message));
  const modelPath=/\/api\/v1\/(?:agent\/(?:chat|confirm|packages\/.*\/invoke|pre-production\/start)|avatar-mvp\/(?:runs|tasks))/;
  // This also guards against accidental task submission from the application.
  await context.route('**/api/v1/**',route=>{
   const req=route.request();if(req.method()==='POST'&&modelPath.test(new URL(req.url()).pathname)){modelRequests++;return route.abort()}
   return route.continue();
  });
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name=email]').fill(account.email);await page.locator('input[name=password]').fill(account.password);
  await page.locator('button[type=submit]').click();await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  const open=async()=>{await page.goto(base+'/workspace/'+id,{waitUntil:'domcontentloaded'});await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled({timeout:20000});await expect(page.getByRole('log',{name:'对话消息'})).toBeVisible()};
  const focus=async node=>{
   const title=snapshot.packages.find(p=>p.node===node).title;
   await page.getByRole('combobox',{name:'定位制作步骤',exact:true}).fill(`${node} ${title}`);
   await page.getByRole('option',{name:`${node} ${title}`,exact:true}).click();
   await expect(page.getByTestId('workflow-node-'+node)).toBeVisible();
  };
  await open();await focus('F12');
  const delivery=page.getByTestId('workflow-node-F12');
  const finalNames=snapshot.run.artifacts.filter(a=>a.id==='video'||a.id==='cover').map(a=>a.name);
  assert.deepEqual(await delivery.locator('[data-kind]').evaluateAll(els=>els.map(el=>el.getAttribute('aria-label'))),finalNames);
  await expect(delivery.locator('video')).toHaveCount(1);
  await expect.poll(()=>delivery.locator('video').evaluate(video=>video.readyState),{timeout:30000}).toBeGreaterThanOrEqual(2);
  await delivery.locator('video').evaluate(video=>video.play());
  await expect.poll(()=>delivery.locator('video').evaluate(video=>video.currentTime)).toBeGreaterThan(.1);
  await delivery.locator('video').evaluate(video=>video.pause());checks.finalVideoPlays=true;
  await page.screenshot({path:resolve(output,'delivery-node.png'),fullPage:true});
  await page.getByRole('button',{name:'查看成片交付详情',exact:true}).click();
  const detail=page.getByRole('complementary',{name:'步骤详情'});
  await expect(detail.getByRole('button',{name:'下载',exact:true})).toHaveCount(2);
  assert.deepEqual(await detail.locator('h3').allTextContents(),finalNames);
  await expect.poll(()=>detail.locator('img').evaluate(img=>img.complete&&img.naturalWidth>0),{timeout:30000}).toBe(true);
  const pendingDownload=page.waitForEvent('download');await detail.getByRole('button',{name:'下载',exact:true}).first().click();
  const download=await pendingDownload;assert.equal(download.suggestedFilename(),finalNames[0]);assert.ok(statSync(await download.path()).size>0);
  checks.onlyVideoAndCoverInDelivery=true;checks.coverLoads=true;checks.finalDownload=true;
  await page.screenshot({path:resolve(output,'delivery-detail.png'),fullPage:true});
  await page.getByRole('button',{name:'返回画布',exact:true}).click();await focus('F11');
  const previews=snapshot.run.artifacts.filter(a=>a.id==='preview'||a.id.startsWith('preview-')).map(a=>a.name);
  assert.deepEqual(await page.getByTestId('workflow-node-F11').locator('[data-kind]').evaluateAll(els=>els.map(el=>el.getAttribute('aria-label'))),previews);
  await page.getByRole('button',{name:'查看有声预览详情',exact:true}).click();
  assert.deepEqual(await detail.locator('h3').allTextContents(),previews);checks.segmentPreviewsRemain=true;
  await page.getByRole('button',{name:'返回画布',exact:true}).click();
  const log=()=>page.getByRole('log',{name:'对话消息'}),status=()=>log().getByRole('region',{name:'制作状态',exact:true});
  await expect(status()).toContainText('视频制作已完成');
  const completedAt=Date.parse(snapshot.run.completed_at??snapshot.run.updated_at);
  const realMessages=history.messages.filter(m=>m.role==='user'||m.role==='assistant');
  const later=realMessages.findIndex(m=>Date.parse(m.createdAt)>completedAt);
  assert.equal(await log().locator('[data-chat-item]').evaluateAll(els=>els.findIndex(el=>el.dataset.chatItem==='notice')),later<0?realMessages.length:later);
  checks.realHistoryOrder=true;

  // UI-only continuation scenario: authentic run/media with controlled chat.
  const fixtureHistory=[{id:'fixture-before',role:'user',content:'验收：此前已请求制作视频。',createdAt:new Date(completedAt-60000).toISOString()},
   ...Array.from({length:18},(_,i)=>({id:'fixture-after-'+i,role:i%2?'assistant':'user',content:`验收对话 ${i+1}：现在讨论下一期的选题和拍摄风格，完成提示应留在此前的位置。`,createdAt:new Date(completedAt+60000*(i+1)).toISOString()}))];
  await context.route('**/api/v1/agent/history?*',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({messages:fixtureHistory})}));
  await context.route('**/api/v1/agent/chat',route=>{
   const body=route.request().postDataJSON();
   fixtureHistory.push({id:'fixture-new-user',role:'user',content:body.message,createdAt:new Date(completedAt+2000000).toISOString()},{id:'fixture-new-reply',role:'assistant',content:'验收回复：继续讨论新的拍摄风格。',createdAt:new Date(completedAt+2001000).toISOString()});
   return route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({reply:'验收回复：继续讨论新的拍摄风格。',status:'ok',turnId:body.turnId})});
  });
  await open();await expect(status()).toHaveCount(1);
  const noticeIndex=()=>log().locator('[data-chat-item]').evaluateAll(els=>els.findIndex(el=>el.dataset.chatItem==='notice'));
  assert.equal(await noticeIndex(),1);
  await log().evaluate(el=>{el.style.scrollBehavior='auto';el.scrollTop=0});
  await expect.poll(()=>log().evaluate(el=>el.scrollTop)).toBe(0);
  const top=(await status().boundingBox()).y;
  await log().evaluate(el=>{el.scrollTop=180});
  await expect.poll(async()=>top-(await status().boundingBox()).y).toBeGreaterThan(170);
  assert.equal(await status().evaluate(el=>getComputedStyle(el).position),'static');simulatedChecks.statusMovesWithScroll=true;
  await page.getByRole('textbox',{name:'向 Agent 发送指令'}).fill('验收：换个话题，聊聊拍摄风格。');
  await page.getByRole('button',{name:'发送消息',exact:true}).click();await expect(log().getByText('验收回复：继续讨论新的拍摄风格。',{exact:true})).toBeVisible();
  assert.equal(await noticeIndex(),1);await expect(status()).toHaveCount(1);simulatedChecks.newRepliesAfterCompletion=true;
  const logBox=await log().boundingBox(),statusBox=await status().boundingBox();assert.ok(statusBox.y+statusBox.height<logBox.y,'Completed status must scroll out of the latest conversation');
  await page.screenshot({path:resolve(output,'chat-later-messages.png'),fullPage:true});
  // A normal five-second snapshot refresh must not move the card or scroll the reader.
  await log().evaluate(el=>{el.scrollTop=90});
  await page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/agent/project/'+id&&r.status()===200,{timeout:12000});
  assert.equal(await noticeIndex(),1);assert.equal(await log().evaluate(el=>el.scrollTop),90);simulatedChecks.pollKeepsOrderAndScroll=true;
  await page.reload({waitUntil:'domcontentloaded'});await expect(status()).toHaveCount(1);assert.equal(await noticeIndex(),1);simulatedChecks.reloadRestoresOrder=true;
  await page.getByRole('button',{name:'查看制作状态',exact:true}).click();await expect(status()).toBeInViewport();
  await page.screenshot({path:resolve(output,'chat-completion-in-history.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});await page.getByRole('button',{name:'查看制作状态',exact:true}).click();await expect(status()).toBeInViewport();
  const mobile=await status().boundingBox();assert.ok(mobile.x>=0&&mobile.x+mobile.width<=391);simulatedChecks.mobileStatusReachable=true;
  await page.screenshot({path:resolve(output,'mobile-chat-status.png'),fullPage:true});
  assert.deepEqual((await getState(id)).run,snapshot.run);
  const historyAfter=await api.get('/api/v1/agent/history',{headers,params:{projectId:id}});assert.deepEqual(await historyAfter.json(),history);checks.realProjectAndHistoryUnchanged=true;
  assert.equal(modelRequests,0);assert.deepEqual(errors,[]);
  const report={checkedAt:new Date().toISOString(),base,projectId:id,checks,simulatedChecks,modelRequests,pageErrors:errors,screenshots:output};
  writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
 }catch(error){if(page)await page.screenshot({path:resolve(output,'failure.png'),fullPage:true}).catch(()=>{});throw error}
 finally{if(browser)await browser.close();await api.dispose()}
})().catch(error=>{console.error(error.message);process.exitCode=1});
