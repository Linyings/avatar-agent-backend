// Verify the home composer voice picker on the real WSL deployment.
// Creates one temporary draft (no runs, no model calls) and deletes it afterwards.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;

(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','-u','root','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let account={};
 for(const line of raw.split(/\r?\n/)){
  const i=line.indexOf(': ');if(i<0)continue
  const key=line.slice(0,i),value=line.slice(i+2)
  if(key==='Email'){if(account.email&&account.password)accounts.push(account);account={email:value}}
  else if(key==='Password')account.password=value
 }
 if(account.email&&account.password)accounts.push(account)
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','-u','root','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0]
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`
 const output=resolve(__dirname,'../../backend/data/wsl-deployment/home-voice',new URL(base).hostname)
 mkdirSync(output,{recursive:true})
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe',args:['--autoplay-policy=no-user-gesture-required']})
 const report={url:base+'/',errors:[],modelTasksStarted:0}
 let headers,draft,page
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}})
  page=await context.newPage()
  page.on('pageerror',e=>report.errors.push(e.message))
  page.on('request',req=>{if(req.method()==='POST'&&/\/api\/v1\/(?:agent\/(?:chat|confirm|pre-production\/start)|avatar-mvp\/(?:runs|tasks)|avatar-mvp\/projects\/[^/]+\/runs)/.test(req.url()))report.modelTasksStarted++})
  for(const candidate of accounts){
   const r=await context.request.post(base+'/api/v1/auth/login',{data:candidate})
   if(r.status()!==200)continue
   headers={Authorization:'Bearer '+unwrap(await r.json()).tokens.access_token}
   await page.goto(base+'/login',{waitUntil:'domcontentloaded'})
   await page.locator('input[name="email"]').fill(candidate.email)
   await page.locator('input[name="password"]').fill(candidate.password)
   await page.locator('button[type="submit"]').click()
   break
  }
  assert.ok(headers,'No valid saved deployment account')
  await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible()
  const catalog=unwrap(await (await context.request.get(base+'/api/v1/avatar-mvp/catalog',{headers})).json())
  report.catalogVoices=catalog.voices.length
  report.catalogSamples=catalog.voices.filter(v=>v.sample).length
  assert.equal(report.catalogVoices,17,'All 17 MiniMax system voices must be listed')
  assert.equal(report.catalogSamples,17,'Every system voice must have a preview')

  // Composer row: style, ratio and voice sit together under the prompt box.
  const trigger=page.getByRole('button',{name:/^配音音色：/})
  await expect(trigger).toBeVisible()
  await expect(trigger).toHaveAccessibleName('配音音色：睿智女声 · Wise Woman')
  const style=page.getByRole('button',{name:/^动效风格：/}),ratio=page.getByRole('combobox',{name:'画幅'})
  const [sb,rb,vb]=await Promise.all([style.boundingBox(),ratio.boundingBox(),trigger.boundingBox()])
  report.composerRow={style:sb,ratio:rb,voice:vb}
  assert.ok(Math.abs(vb.y-sb.y)<8&&vb.x>rb.x,'Voice picker must sit beside the style and ratio controls')
  await page.screenshot({path:resolve(output,'home-composer.png')})

  await trigger.click()
  const list=page.getByRole('listbox',{name:'可选音色'})
  await expect(list).toBeVisible()
  await expect(list.getByRole('option')).toHaveCount(17)
  await page.screenshot({path:resolve(output,'voice-list.png')})

  // Preview: fetched with auth, actually decoded and played by the browser.
  const sample=page.waitForResponse(r=>r.url().includes('/catalog/voices/Calm_Woman/sample'))
  await page.getByRole('button',{name:'试听沉稳女声 · Calm Woman'}).click()
  const response=await sample
  report.preview={status:response.status(),type:response.headers()['content-type']}
  assert.equal(response.status(),200)
  await expect(page.getByRole('button',{name:'停止试听沉稳女声 · Calm Woman'})).toBeVisible()
  await page.getByRole('button',{name:'停止试听沉稳女声 · Calm Woman'}).click()
  await expect(page.getByRole('button',{name:'试听沉稳女声 · Calm Woman'})).toBeVisible()

  await list.getByRole('option',{name:/沉稳女声/}).click()
  await expect(list).toBeHidden()
  await expect(trigger).toHaveAccessibleName('配音音色：沉稳女声 · Calm Woman')

  const created=page.waitForResponse(r=>r.request().method()==='POST'&&/\/api\/v1\/avatar-mvp\/projects$/.test(r.url()))
  await page.getByRole('textbox',{name:'描述视频创意'}).fill('音色选择验收（临时草稿，不生成）')
  await page.getByRole('button',{name:'发送创意'}).click()
  const body=unwrap(await (await created).json())
  draft=body.id
  report.createdVoice=body.input.voice
  assert.equal(body.input.voice,'Calm_Woman','Draft must keep the chosen voice')
  const snapshot=unwrap(await (await context.request.get(base+'/api/v1/agent/project/'+draft,{headers})).json())
  report.savedVoice=snapshot.project.input.voice
  assert.equal(snapshot.project.input.voice,'Calm_Woman')
  assert.equal(snapshot.run,null,'No run may start from the home composer')
  assert.deepEqual(report.errors,[])
  assert.equal(report.modelTasksStarted,0)
 }finally{
  if(draft&&headers){
   const deleted=await page.context().request.delete(base+'/api/v1/projects/'+draft,{headers})
   report.temporaryDraftDeleted=deleted.ok()
  }
  await browser.close()
 }
 writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n')
 console.log(JSON.stringify(report,null,2))
})().catch(e=>{console.error(e.message);process.exitCode=1})
