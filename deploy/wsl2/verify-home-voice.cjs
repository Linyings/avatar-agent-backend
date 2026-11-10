// Verify the home composer voice model + voice picker on the real WSL deployment.
// Creates temporary drafts (no runs, no model calls) and deletes them afterwards.
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
 const report={url:base+'/',errors:[],modelTasksStarted:0,drafts:[]}
 let headers,page
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
  report.models=catalog.voice_models.map(m=>({id:m.id,voices:m.voices.length,samples:m.voices.filter(v=>v.sample).length}))
  assert.ok(catalog.voice_models.length>=10,'All RunningHub TTS models must be listed')

  const trigger=page.getByRole('button',{name:/^配音音色：/})
  await expect(trigger).toHaveAccessibleName('配音音色：睿智女声 · Wise Woman')
  const style=page.getByRole('button',{name:/^动效风格：/}),ratio=page.getByRole('combobox',{name:'画幅'})
  const [sb,rb,vb]=await Promise.all([style.boundingBox(),ratio.boundingBox(),trigger.boundingBox()])
  assert.ok(Math.abs(vb.y-sb.y)<8&&vb.x>rb.x,'Voice picker must sit beside the style and ratio controls')

  await trigger.click()
  const tabs=page.getByRole('tab')
  await expect(tabs).toHaveCount(catalog.voice_models.length)
  await page.screenshot({path:resolve(output,'voice-models.png')})

  // Switch model → list shows only that model's voices; preview plays; select.
  for(const target of [{model:'千问 Qwen3-TTS Flash',voice:'阿闻 · 新闻主播男声',id:'Neil',modelId:'alibaba/qwen3-tts-flash'},{model:'可灵 Kling TTS',voice:'东北老铁',id:'dongbeilaotie_speech02',modelId:'kling-lip-sync/tts'}]){
   if(!(await page.getByRole('listbox',{name:'可选音色'}).isVisible()))await trigger.click()
   await page.getByRole('tab',{name:target.model}).click()
   const list=page.getByRole('listbox',{name:'可选音色'})
   const want=catalog.voice_models.find(m=>m.id===target.modelId).voices.length
   await expect(list.getByRole('option')).toHaveCount(want)
   const sample=page.waitForResponse(r=>r.url().includes('/catalog/voices/'+encodeURIComponent(target.id)+'/sample'))
   await page.getByRole('button',{name:'试听'+target.voice,exact:true}).click()
   const response=await sample
   assert.equal(response.status(),200,target.model+' preview')
   await expect(page.getByRole('button',{name:'停止试听'+target.voice})).toBeVisible()
   await page.screenshot({path:resolve(output,target.modelId.replace(/\W+/g,'-')+'.png')})
   await page.getByRole('button',{name:'停止试听'+target.voice}).click()
   await list.getByRole('option',{name:target.voice}).click()
   await expect(trigger).toHaveAccessibleName('配音音色：'+target.model+' · '+target.voice)

   const created=page.waitForResponse(r=>r.request().method()==='POST'&&/\/api\/v1\/avatar-mvp\/projects$/.test(r.url()))
   await page.getByRole('textbox',{name:'描述视频创意'}).fill('音色模型验收（临时草稿，不生成）')
   await page.getByRole('button',{name:'发送创意'}).click()
   const body=unwrap(await (await created).json())
   report.drafts.push(body.id)
   const snapshot=unwrap(await (await context.request.get(base+'/api/v1/agent/project/'+body.id,{headers})).json())
   assert.equal(snapshot.project.input.voice_model,target.modelId)
   assert.equal(snapshot.project.input.voice,target.id)
   assert.equal(snapshot.run,null,'No run may start from the home composer')
   assert.ok(snapshot.voices.every(v=>v.model===target.modelId),'Workspace voices must follow the project model')
   report[target.modelId]={voice:snapshot.project.input.voice,model:snapshot.project.input.voice_model,preview:response.headers()['content-type']}
   await page.goto(base+'/',{waitUntil:'domcontentloaded'})
   await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible()
  }
  assert.deepEqual(report.errors,[])
  assert.equal(report.modelTasksStarted,0)
 }finally{
  for(const id of report.drafts){
   const deleted=await page.context().request.delete(base+'/api/v1/projects/'+id,{headers})
   report.temporaryDraftsDeleted=(report.temporaryDraftsDeleted??true)&&deleted.ok()
  }
  await browser.close()
 }
 writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n')
 console.log(JSON.stringify(report,null,2))
})().catch(e=>{console.error(e.message);process.exitCode=1})
