// Real catalog/media and project persistence; only Agent chat is stubbed to
// prevent this UI acceptance test from starting paid production.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=v=>v.data??v;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/motion-style-cards');
mkdirSync(output,{recursive:true});
const wsl=args=>execFileSync('wsl.exe',['-d','Ubuntu-24.04','-u','root','--',...args],{encoding:'utf8'});

(async()=>{
 const base=process.env.AVATAR_BASE_URL||`http://${wsl(['hostname','-I']).trim().split(/\s+/)[0]}:8080`;
 const accounts=[];let entry={};
 for(const line of wsl(['cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt']).split(/\r?\n/)){
  const i=line.indexOf(': ');if(i<0)continue;
  const key=line.slice(0,i),value=line.slice(i+2);
  if(key==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:value}}
  if(key==='Password')entry.password=value;
 }
 if(entry.email&&entry.password)accounts.push(entry);
 const api=await request.newContext({baseURL:base});
 let account,headers,browser,page,id;const checks={},errors=[],mediaRequests=[];let mockedChats=0;
 try{
  for(const candidate of accounts){
   const res=await api.post('/api/v1/auth/login',{data:candidate});if(res.status()===401)continue;
   assert.equal(res.status(),200);account=candidate;headers={Authorization:'Bearer '+unwrap(await res.json()).tokens.access_token};break;
  }
  assert.ok(account,'Saved local account unavailable');
  const catalogResponse=await api.get('/api/v1/avatar-mvp/catalog',{headers});assert.equal(catalogResponse.status(),200);
  const available=unwrap(await catalogResponse.json()).styles.filter(s=>s.published&&!s.legacy);
  const chosen=available.find(s=>s.id==='mg-isometric');assert.ok(chosen,'Expected real isometric style');
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  await context.route('**/api/v1/agent/chat',route=>{mockedChats++;return route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({status:'ok',reply:'风格验收：暂不启动制作。',turnId:'style-acceptance'})})});
  page=await context.newPage();page.on('pageerror',error=>errors.push(error.message));
  page.on('request',req=>{const pathname=new URL(req.url()).pathname;if(pathname.includes('/avatar-mvp/catalog/'))mediaRequests.push(pathname)});
  await page.goto(base+'/login');
  await page.locator('input[name=email]').fill(account.email);await page.locator('input[name=password]').fill(account.password);
  await page.locator('button[type=submit]').click();
  const styleButton=page.getByRole('button',{name:'动效风格：默认风格',exact:true});await expect(styleButton).toBeVisible();
  await expect(page.getByLabel('制作类型',{exact:true})).toHaveCount(0);
  await styleButton.click();
  const dialog=page.getByRole('dialog',{name:'选择动效风格'});await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('complementary',{name:'风格预览'})).toHaveCount(0);
  await expect(dialog.getByRole('button',{name:/^预览/})).toHaveCount(available.length);
  await expect.poll(()=>dialog.locator('img').evaluateAll(images=>images.length>0&&images.every(img=>img.complete&&img.naturalWidth>0)),{timeout:60000}).toBe(true);
  checks.realCatalogAndPosters=available.length;
  await dialog.getByRole('button',{name:'预览'+chosen.name,exact:true}).click();
  await expect(dialog.getByRole('button',{name:'预览'+chosen.name,exact:true})).toHaveAttribute('aria-pressed','true');
  const video=dialog.getByRole('button',{name:'预览'+chosen.name,exact:true}).locator('video');await expect(video).toBeVisible();
  await expect.poll(()=>video.evaluate(el=>el.readyState>=2&&el.videoWidth>0),{timeout:60000}).toBe(true);
  assert.equal(await video.evaluate(el=>el.muted&&el.loop&&el.playsInline&&el.videoWidth<=480&&el.videoHeight<=480),true);
  await expect.poll(()=>video.evaluate(el=>el.currentTime)).toBeGreaterThan(0.15);
  const initialVideos=await dialog.locator('video').count();assert.ok(initialVideos>1&&initialVideos<available.length,'Only visible cards should load video');
  checks.initialVideoCount=initialVideos;checks.inlineMutedAutoplay=true;
  await dialog.getByRole('button',{name:'暂停演示',exact:true}).click();await expect(dialog.locator('video')).toHaveCount(0);
  await dialog.getByRole('button',{name:'播放演示',exact:true}).click();await expect(video).toBeVisible();
  await expect.poll(()=>dialog.locator('video').evaluateAll(videos=>videos.length>0&&videos.every(el=>el.currentTime>.1&&!el.paused)),{timeout:30000}).toBe(true);
  checks.pauseAndResume=true;
  await page.screenshot({path:resolve(output,'style-picker-desktop.png'),fullPage:true});
  const played=[];
  for(const item of available){
   const card=dialog.getByRole('button',{name:'预览'+item.name,exact:true});await card.scrollIntoViewIfNeeded();
   await expect(card.locator('video')).toBeVisible({timeout:30000});
   await expect.poll(()=>card.locator('video').evaluate(el=>el.currentTime>.1&&!el.paused&&el.videoWidth<=480&&el.videoHeight<=480),{timeout:30000}).toBe(true);
   played.push(item.id);
  }
  checks.allCompressedDemosPlayed=played.length;
  await dialog.locator('[aria-label="可用动效风格"]').evaluate(el=>{el.scrollTop=el.scrollHeight});
  await expect(dialog.getByRole('button',{name:'预览'+available[0].name,exact:true}).locator('video')).toHaveCount(0);
  checks.offscreenVideoUnloaded=true;
  await dialog.getByRole('textbox',{name:'搜索动效风格'}).fill('等轴');
  await expect(dialog.getByRole('button',{name:/^预览/})).toHaveCount(1);
  await expect(dialog.locator('video')).toHaveCount(1);
  await dialog.getByRole('textbox',{name:'搜索动效风格'}).fill('不存在的风格');
  await expect(dialog.getByText('没有找到匹配的风格，试试其他关键词。')).toBeVisible();
  await expect(dialog.locator('video')).toHaveCount(0);
  await dialog.getByRole('button',{name:'取消',exact:true}).click();await expect(dialog).toHaveCount(0);
  await expect(styleButton).toBeVisible();checks.cancelAndSearch=true;
  await styleButton.click();await dialog.getByRole('button',{name:'预览'+chosen.name,exact:true}).click();
  await dialog.getByRole('button',{name:'使用此风格',exact:true}).click();await expect(dialog).toHaveCount(0);
  const selected=page.getByRole('button',{name:'动效风格：'+chosen.name,exact:true});await expect(selected).toBeVisible();
  await page.getByRole('button',{name:'知识科普口播',exact:true}).click();await expect(selected).toBeVisible();
  await page.screenshot({path:resolve(output,'home-style-selected.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});await selected.click();
  await expect(dialog.getByRole('button',{name:'预览'+chosen.name,exact:true})).toHaveAttribute('aria-pressed','true');
  assert.ok(await dialog.evaluate(el=>el.scrollWidth<=el.clientWidth),'Mobile style picker overflow');
  await expect(dialog.getByRole('button',{name:'预览'+chosen.name,exact:true}).locator('video')).toBeVisible();
  await expect.poll(()=>dialog.getByRole('button',{name:'预览'+chosen.name,exact:true}).locator('video').evaluate(el=>el.currentTime>.1&&!el.paused)).toBe(true);
  await page.screenshot({path:resolve(output,'style-picker-mobile.png'),fullPage:true});
  await dialog.getByRole('button',{name:'使用此风格',exact:true}).click();
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Mobile home overflow');
  await page.screenshot({path:resolve(output,'home-style-mobile.png'),fullPage:true});checks.mobile390NoOverflow=true;
  await page.setViewportSize({width:1440,height:1000});
  await page.getByRole('textbox',{name:'描述视频创意'}).fill('风格融合验收：科普口播项目，先保存需求，不开始生成。');
  const created=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/avatar-mvp/projects');
  await page.getByRole('button',{name:'发送创意',exact:true}).click();
  const response=await created;assert.equal(response.status(),200);id=unwrap(await response.json()).id;assert.ok(id);
  await expect(page).toHaveURL(base+'/workspace/'+id,{timeout:30000});
  await expect(page.getByRole('complementary',{name:'Agent 对话'})).toBeVisible();
  async function snapshot(){const r=await api.get('/api/v1/agent/project/'+id,{headers});assert.equal(r.status(),200);return unwrap(await r.json())}
  function verifyState(s){
   assert.equal(s.run,null);assert.equal(s.project.input.style,chosen.id);
   assert.equal(s.project.input.use_avatar,true);assert.equal(s.project.input.use_animation,true);
   assert.match(s.project.input.direction,/参考图/);assert.match(s.project.input.direction,/分层融合/);
   assert.ok(s.workflow_plan.nodes.includes('F06'));assert.ok(s.workflow_plan.nodes.includes('F07'));
  }
  verifyState(await snapshot());await page.reload();verifyState(await snapshot());
  await expect(page.getByRole('complementary',{name:'Agent 对话'})).toBeVisible();
  await page.screenshot({path:resolve(output,'workspace-fusion-plan.png'),fullPage:true});
  await page.getByRole('button',{name:'制作设置',exact:true}).click();
  await expect(page.getByRole('combobox',{name:'动效风格',exact:true})).toHaveValue(chosen.name);
  await expect(page.getByRole('checkbox',{name:'使用数字人出镜',exact:true})).toBeChecked();
  await expect(page.getByRole('checkbox',{name:'加入动效画面',exact:true})).toBeChecked();
  checks.fusionProjectAndStylePersist=true;checks.avatarAndAnimationBranches=true;
  await page.goto(base+'/?mode=animation');await expect(page.getByRole('dialog',{name:'选择动效风格'})).toBeVisible();checks.legacyAnimationLinkOpensStyles=true;
  await page.getByRole('dialog').getByRole('button',{name:'取消',exact:true}).click();
  for(let i=0;i<2;i++){
   await page.getByRole('link',{name:'动效风格',exact:true}).click();
   await expect(page.getByRole('dialog',{name:'选择动效风格'})).toBeVisible();
   await page.getByRole('dialog').getByRole('button',{name:'取消',exact:true}).click();
  }
  checks.sidebarReopensStyles=true;
  assert.ok(mediaRequests.length>0&&mediaRequests.every(path=>path.endsWith('/card-preview')||path.endsWith('/card-poster')),'The gallery fetched full-size original media');
  checks.noOriginalVideoDownloads=true;
  assert.deepEqual(errors,[]);
 }catch(error){if(page)await page.screenshot({path:resolve(output,'failure.png'),fullPage:true}).catch(()=>{});throw error}
 finally{
  if(browser)await browser.close();
  if(id){const res=await api.delete('/api/v1/projects/'+id,{headers});assert.ok(res.ok(),'Temporary project cleanup failed');checks.temporaryProjectRemoved=true}
  await api.dispose();
 }
 const report={url:base+'/',checks,pageErrors:errors,mockedChats,scope:'Real compressed card autoplay, viewport loading, pause/resume, catalog, create and persistence; only Agent chat stubbed, no model generation',screenshots:output};
 writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
})().catch(error=>{console.error(error.message);process.exitCode=1});
