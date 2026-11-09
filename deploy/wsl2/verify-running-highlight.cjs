// Read-only deployment check. Status transitions are browser response fixtures;
// the production project and its running jobs are never changed.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=body=>body.data??body;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/running-highlight');

(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--exec','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let candidate={};
 for(const line of raw.split(/\r?\n/)){
  const i=line.indexOf(': ');if(i<0)continue;
  const key=line.slice(0,i),value=line.slice(i+2);
  if(key==='Email'){if(candidate.email&&candidate.password)accounts.push(candidate);candidate={email:value}}
  if(key==='Password')candidate.password=value;
 }
 if(candidate.email&&candidate.password)accounts.push(candidate);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--exec','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const api=await request.newContext({baseURL:base});
 let browser;
 try{
  let account,headers;
  for(const item of accounts){
   const res=await api.post('/api/v1/auth/login',{data:item});if(res.status()===401)continue;
   assert.equal(res.status(),200);account=item;headers={Authorization:'Bearer '+unwrap(await res.json()).tokens.access_token};break;
  }
  assert.ok(account,'No valid deployment account');
  const projectId=process.env.AVATAR_VERIFY_PROJECT_ID||'465168c3-7348-4914-b535-b59d9f0d7d16';
  const endpoint='/api/v1/agent/project/'+projectId;
  const response=await api.get(endpoint,{headers});assert.equal(response.status(),200);
  const realState=unwrap(await response.json());assert.ok(realState.run?.needs.length,'No video nodes available');
  mkdirSync(output,{recursive:true});
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1600,height:1000}});
  const page=await context.newPage();const errors=[],blockedWrites=[];
  page.on('pageerror',e=>errors.push(e.message));
  // Reject application writes after login, including accidental model requests.
  await page.route('**/api/**',async route=>{
   const req=route.request();
   if(!['GET','HEAD','OPTIONS'].includes(req.method())&&!/\/auth\/(login|refresh)/.test(req.url())){
    blockedWrites.push(new URL(req.url()).pathname);return route.abort();
   }
   await route.continue();
  });
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name=email]').fill(account.email);
  await page.locator('input[name=password]').fill(account.password);
  await page.locator('button[type=submit]').click();
  await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  async function ready(){
   await expect(page.getByTestId('workflow-canvas')).toBeVisible();
   await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled({timeout:20000});
  }
  async function settle(){
   // Let React Flow's 250ms viewport transition finish before measuring pixels.
   await page.waitForTimeout(350);
  }
  await page.goto(`${base}/workspace/${projectId}`,{waitUntil:'domcontentloaded'});await ready();
  await page.getByRole('button',{name:'查看全部节点',exact:true}).click();await settle();
  await page.screenshot({path:resolve(output,'live-overview.png'),fullPage:true});

  const need=realState.run.needs.find(n=>n.kind==='animation');assert.ok(need,'No motion node available');
  const node='F07/'+need.id;
  const fixture=structuredClone(realState);
  fixture.run.status='running';fixture.run.stage=node+'/scene-2';fixture.run.error=undefined;fixture.run.error_step_id=undefined;fixture.run.confirmation=undefined;
  fixture.run.steps=fixture.run.steps.filter(s=>!(s.id===node||s.id.startsWith(node+'/')||s.id.startsWith(node+'-')))
   .map(s=>({...s,status:s.status==='running'?'pending':s.status}));
  fixture.run.steps.push({id:node,status:'running',seconds:0},{id:node+'/scene-2',status:'running',seconds:0});
  let viewState=fixture;
  await page.route('**'+endpoint,route=>route.fulfill({json:{data:viewState}}));
  await page.reload({waitUntil:'domcontentloaded'});await ready();
  const card=page.getByTestId('workflow-node-'+node);
  await expect(card).toHaveAttribute('data-status','running');
  await expect(card.getByText(/正在执行/)).toBeVisible();
  await expect(page.getByTestId('workflow-node-F10')).not.toHaveAttribute('data-status','running');
  const location=page.getByRole('button',{name:/^定位正在执行的/});
  await expect(location).toHaveCount(1);await location.click();await settle();
  const metrics=()=>card.evaluate(el=>{
   const css=getComputedStyle(el),zoom=new DOMMatrixReadOnly(document.querySelector('.react-flow__viewport').style.transform).a;
   return {zoom,outlineColor:css.outlineColor,outlinePixels:parseFloat(css.outlineWidth)*zoom,haloAnimation:getComputedStyle(el,'::after').animationName};
  });
  const focused=await metrics();assert.ok(focused.outlinePixels>=2.9);
  await page.screenshot({path:resolve(output,'fixture-focused.png'),fullPage:true});
  await page.getByRole('button',{name:'查看全部节点',exact:true}).click();await settle();
  const overview=await metrics();assert.ok(overview.zoom<.5);assert.ok(overview.outlinePixels>=2.9,'Outline must remain visible when zoomed out');
  await expect(location).toBeVisible();
  await page.screenshot({path:resolve(output,'fixture-overview.png'),fullPage:true});
  // Select a different node without changing the execution highlight.
  const other=page.getByTestId('workflow-node-F06/'+realState.run.needs.find(n=>n.kind==='avatar').id);
  await other.locator('header').click({force:true});
  await expect(other).toHaveAttribute('data-selected','true');
  await expect(card).toHaveCSS('outline-color','rgb(255, 209, 102)');
  const selectedOther=await metrics();assert.ok(selectedOther.outlinePixels>=2.9);
  // All supported zoom levels, including the 12% overview minimum.
  const zoomOut=page.getByRole('button',{name:'缩小画布',exact:true});
  for(let i=0;i<12&&await zoomOut.isEnabled();i++){await zoomOut.click();await settle()}
  await settle();const minimum=await metrics();assert.ok(minimum.zoom<=.13);assert.ok(minimum.outlinePixels>=2.9);
  await page.emulateMedia({reducedMotion:'reduce'});
  await expect.poll(async()=>(await metrics()).haloAnimation).toBe('none');
  assert.ok((await metrics()).outlinePixels>=2.9);
  await page.screenshot({path:resolve(output,'fixture-minimum-reduced-motion.png'),fullPage:true});
  const stoppedStates=[];
  for(const status of ['paused','failed','awaiting_confirmation','succeeded']){
   viewState=structuredClone(fixture);viewState.run.status=status;
   await page.reload({waitUntil:'domcontentloaded'});await ready();
   await expect(page.getByRole('button',{name:/^定位正在执行的/})).toHaveCount(0);
   await expect(page.locator('[data-testid^="workflow-node-"][data-status="running"]')).toHaveCount(0);
   stoppedStates.push(status);
  }
  assert.deepEqual(errors,[]);assert.deepEqual(blockedWrites,[]);
  const report={base,projectId,liveStatus:realState.run.status,fixtureNode:node,focused,overview,selectedOther,minimum,
   reducedMotion:'passed',stoppedStates,pageErrors:errors,applicationWrites:blockedWrites.length,screenshots:output,
   note:'live-overview.png uses real server state; fixture-* screenshots and state-transition checks only override browser GET responses.'};
  writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
 }finally{await browser?.close();await api.dispose()}
})().catch(error=>{console.error(error.message);process.exitCode=1});
