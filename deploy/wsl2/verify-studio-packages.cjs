// Browser acceptance against the actual WSL deployment. Only the Agent's
// read/configure tools and a supplied-script F02 run are checked. No paid media is generated.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,expect}=require('../../frontend/node_modules/@playwright/test');

(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let account={};for(const line of raw.split(/\r?\n/)){const i=line.indexOf(': ');if(i<0)continue;const k=line.slice(0,i),v=line.slice(i+2);if(k==='Email'){if(account.email&&account.password)accounts.push(account);account={email:v}}else if(k==='Password')account.password=v}if(account.email&&account.password)accounts.push(account);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||'http://localhost:8080';
 const out=resolve(__dirname,'../../backend/data/wsl-deployment/studio-packages',new URL(base).hostname);mkdirSync(out,{recursive:true});
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
 const context=await browser.newContext({viewport:{width:1440,height:1000}});
 const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
 let ownProject,token;const checks={};let productionCalls=0;const toolCalls=[];
 page.on('request',r=>{if(r.method()==='POST'&&/\/runs$|\/tasks$/.test(new URL(r.url()).pathname))productionCalls++});
 page.on('response',async r=>{if(r.url().includes('/agent/chat')&&r.request().method()==='POST'){checks.chatStatus=r.status()}});
 try{
  let login,validAccount;
  for(const a of accounts){const res=await context.request.post(base+'/api/v1/auth/login',{data:a});if(res.status()===401)continue;assert.equal(res.status(),200);const b=await res.json();login=b.data??b;validAccount=a;break}
  assert.ok(login?.tokens);token=login.tokens.access_token;
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name="email"]').fill(validAccount.email);
  await page.locator('input[name="password"]').fill(validAccount.password);
  await page.locator('button[type="submit"]').click();
  await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  await page.screenshot({path:resolve(out,'home.png'),fullPage:true});checks.home=true;
  await page.getByRole('button',{name:'选择插件',exact:true}).click();
  await expect(page.getByRole('heading',{name:'数字人生成',exact:true})).toBeVisible();
  await expect(page.getByRole('heading',{name:'成片交付',exact:true})).toBeVisible();
  await page.keyboard.press('Escape');checks.packageLibrary=true;
  const prompt='这是 UI 与 package 接入验收。请必须调用 avatar_inspect 检查项目，再用 avatar_configure 将项目标题设为“原型与插件验收”。不要执行任何制作节点，不要生成配音或视频。完成后简短回复。';
  await page.getByRole('textbox',{name:'描述视频创意'}).fill(prompt);
  const chat=page.waitForResponse(r=>r.url().includes('/agent/chat')&&r.request().method()==='POST',{timeout:240000});
  await page.getByRole('button',{name:'发送创意',exact:true}).click();
  await expect(page).toHaveURL(/\/workspace\//,{timeout:30000});
  ownProject=new URL(page.url()).pathname.split('/').pop();
  await expect(page.getByRole('complementary',{name:'Agent 对话'})).toBeVisible();
  const response=await chat;assert.equal(response.status(),200);const result=await response.json();assert.equal(result.status,'ok');
  await expect(page.getByRole('button',{name:'发送消息',exact:true})).toBeVisible({timeout:20000});
  checks.agentReply=!!result.reply;
  const auth={Authorization:'Bearer '+token};
  let snapshot=await (await context.request.get(base+'/api/v1/agent/project/'+ownProject,{headers:auth})).json();snapshot=snapshot.data??snapshot;
  assert.equal(snapshot.packages.length,19);assert.equal(snapshot.project.input.title,'原型与插件验收');assert.equal(snapshot.run,null);
  checks.toolConfigure=true;
  await expect(page.getByText('制作需求 · 已返回',{exact:true})).toBeVisible({timeout:15000});
  await expect(page.getByText('已连接',{exact:true})).toBeVisible();
  await expect(page.locator('strong').filter({hasText:/^原型与插件验收$/})).toBeVisible();
  for(const locator of await page.locator('[data-status="done"]').all()){const text=await locator.innerText();if(text.includes('已返回'))toolCalls.push(text)}
  assert.ok(toolCalls.length>=2,'Missing completed tool events in the UI');checks.toolEvents=toolCalls;
  await page.waitForTimeout(250);
  await page.screenshot({path:resolve(out,'workspace.png'),fullPage:true});
  await page.getByRole('button',{name:'制作设置',exact:true}).click();
  await expect(page.getByLabel('项目名称',{exact:true})).toBeVisible();
  await page.getByLabel('项目名称',{exact:true}).fill('原型与插件验收');
  await page.getByLabel('制作要求',{exact:true}).fill('回归测试草稿；不运行付费制作。');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  await page.reload({waitUntil:'domcontentloaded'});
  await page.getByRole('button',{name:'制作设置',exact:true}).click();
  await expect(page.getByLabel('制作要求',{exact:true})).toHaveValue('回归测试草稿；不运行付费制作。');checks.savedDraft=true;
  const fixture=await context.newPage();await fixture.setViewportSize({width:128,height:128});await fixture.setContent('<html><body style="margin:0;background:#243a58;width:128px;height:128px;display:grid;place-items:center;color:white;font:16px sans-serif">UI TEST</body></html>');
  const png=await fixture.screenshot();await fixture.close();
  await page.locator('input[type=file]').first().setInputFiles({name:'studio-ui-test.png',mimeType:'image/png',buffer:png});
  await expect.poll(async()=>{let r=await(await context.request.get(base+'/api/v1/agent/project/'+ownProject,{headers:auth})).json();return(r.data??r).project.input.assets.length},{timeout:30000}).toBe(1);checks.upload=true;
  await page.getByRole('button',{name:'素材与产物',exact:true}).click();await expect(page.getByRole('heading',{name:'studio-ui-test.png'})).toBeVisible();await expect(page.getByAltText('studio-ui-test.png')).toBeVisible();checks.protectedMedia=true;
  for(const name of ['分镜故事板','时间轴','AI 画布流']){await page.getByRole('button',{name,exact:true}).click()}
  await page.getByRole('button',{name:/F02 · 口播文案/}).click();await expect(page.getByRole('button',{name:'执行到此步骤'})).toBeVisible();checks.nodeInspector=true;
  // Supplied narration is a local F02 operation. It must stop before F03/paid calls.
  const configured=await context.request.post(base+'/api/v1/agent/packages/avatar.configure/invoke',{headers:auth,data:{projectId:ownProject,input:{script:'这是本地插件流程验收。',use_avatar:false},requestId:'studio-local-script'}});
  assert.equal(configured.status(),200);
  await page.getByRole('button',{name:'执行到此步骤'}).click();
  await expect.poll(async()=>{let r=await(await context.request.get(base+'/api/v1/agent/project/'+ownProject,{headers:auth})).json();return(r.data??r).run?.status},{timeout:30000}).toBe('paused');
  let localRun=await(await context.request.get(base+'/api/v1/agent/project/'+ownProject,{headers:auth})).json();localRun=(localRun.data??localRun).run;
  assert.equal(localRun.script,'这是本地插件流程验收。');assert.equal(localRun.target_node,'F02');
  assert.equal(localRun.steps.some(s=>s.id==='F03'||s.id.startsWith('F04')),false);
  checks.packageLocalExecution=true;checks.targetBoundary=true;
  await page.goto(base+'/projects',{waitUntil:'domcontentloaded'});await expect(page.getByRole('heading',{name:'我的项目'})).toBeVisible();
  await page.getByRole('textbox',{name:'搜索项目'}).fill('原型与插件验收');await expect(page.getByTestId('studio-project')).toHaveCount(1);
  await page.getByRole('button',{name:'切换项目视图'}).click();await expect(page.getByRole('button',{name:'打开项目 原型与插件验收'})).toBeVisible();checks.projectSearchAndViews=true;
  await page.getByRole('textbox',{name:'搜索项目'}).fill('');await page.getByRole('button',{name:'切换项目视图'}).click();
  await page.waitForTimeout(250);
  await page.screenshot({path:resolve(out,'projects.png'),fullPage:true});
  await page.getByRole('textbox',{name:'搜索项目'}).fill('原型与插件验收');
  await page.getByRole('button',{name:'管理 原型与插件验收',exact:true}).click();
  await page.getByRole('menuitem',{name:'重命名',exact:true}).click();
  await page.getByRole('textbox',{name:'项目名称',exact:true}).fill('原型与插件验收-重命名');
  await page.getByRole('button',{name:'保存',exact:true}).click();
  await expect(page.getByRole('heading',{name:'原型与插件验收-重命名',exact:true})).toBeVisible();
  await page.reload({waitUntil:'domcontentloaded'});
  await expect(page.getByRole('heading',{name:'原型与插件验收-重命名',exact:true})).toBeVisible();checks.rename=true;
  await page.getByRole('button',{name:'管理 原型与插件验收-重命名',exact:true}).click();
  await page.getByRole('menuitem',{name:'归档项目',exact:true}).click();
  await page.getByRole('button',{name:'已归档',exact:true}).click();
  await expect(page.getByRole('heading',{name:'原型与插件验收-重命名',exact:true})).toBeVisible();checks.archive=true;
  await page.setViewportSize({width:390,height:844});await page.goto(base+'/',{waitUntil:'domcontentloaded'});await expect(page.getByRole('button',{name:'发送创意',exact:true})).toBeVisible();
  const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>window.innerWidth+1);assert.equal(overflow,false);
  await page.screenshot({path:resolve(out,'home-mobile.png'),fullPage:true});checks.mobile=true;
  await page.goto(base+'/avatar-mvp',{waitUntil:'domcontentloaded'});await expect(page).toHaveURL(base+'/');checks.legacyRedirect=true;
  assert.deepEqual(errors,[]);assert.equal(productionCalls,0);
  const report={base,wslIP:ip,checks,pageErrors:errors,productionCalls,screenshots:out};writeFileSync(resolve(out,'verification.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report,null,2));
 }catch(e){await page.screenshot({path:resolve(out,'failure.png'),fullPage:true}).catch(()=>{});console.error(JSON.stringify({error:e.message,url:page.url(),pageErrors:errors,ownProject}));throw e}
 finally{if(ownProject&&token){const res=await context.request.delete(base+'/api/v1/projects/'+ownProject,{headers:{Authorization:'Bearer '+token}});console.log('Owned verification project cleanup: '+res.status())}await browser.close()}
})().catch(e=>{console.error(e.message);process.exitCode=1});
