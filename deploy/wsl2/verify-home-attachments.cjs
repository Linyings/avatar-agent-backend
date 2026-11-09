// Real WSL project persistence and media uploads. Only the Agent chat reply is
// mocked so this acceptance test never submits paid generation work.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync,readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=value=>value.data??value;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/home-attachments');
mkdirSync(output,{recursive:true});
const wsl=args=>execFileSync('wsl.exe',['-d','Ubuntu-24.04','--',...args],{maxBuffer:8*1024*1024});

function fixture(name,color,video=false){
 const mediaArgs=video?['-t','1','-c:v','libx264','-pix_fmt','yuv420p','-movflags','frag_keyframe+empty_moov','-f','mp4']
  :['-frames:v','1','-vcodec','png','-f','image2pipe'];
 const bytes=wsl(['docker','compose','--project-directory','/opt/aicat/avatar-mvp','exec','-T','api',
  'ffmpeg','-v','error','-f','lavfi','-i',`color=c=${color}:s=256x256:r=15`,...mediaArgs,'pipe:1']);
 const path=resolve(output,name);writeFileSync(path,bytes);return path;
}

(async()=>{
 const ip=wsl(['hostname','-I']).toString().trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const accounts=[];let entry={};
 for(const line of wsl(['cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt']).toString().split(/\r?\n/)){
  const split=line.indexOf(': ');if(split<0)continue;
  const key=line.slice(0,split),value=line.slice(split+2);
  if(key==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:value}}
  if(key==='Password')entry.password=value;
 }
 if(entry.email&&entry.password)accounts.push(entry);
 const api=await request.newContext({baseURL:base});
 let account,headers,browser,page,id;const checks={},errors=[],uploadStatuses=[];
 let projectCreates=0,mockedChatRequests=0;
 try{
  for(const candidate of accounts){
   const response=await api.post('/api/v1/auth/login',{data:candidate});
   if(response.status()===401)continue;assert.equal(response.status(),200,'Local login failed');
   account=candidate;headers={Authorization:'Bearer '+unwrap(await response.json()).tokens.access_token};break;
  }
  assert.ok(account,'No valid saved deployment account');
  const referenceImage=fixture('reference-image.png','0x597b9b');
  const portrait=fixture('primary-portrait.png','0xb29385');
  const videoOne=fixture('reference-one.mp4','0x82977e',true);
  const videoTwo=fixture('reference-two.mp4','0x957b99',true);
  const badVideo=resolve(output,'broken-video.mp4');writeFileSync(badVideo,'deliberately invalid video');
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1440,height:1000}});
  // Only this response is synthetic. All create, upload, read and download calls use WSL.
  await context.route('**/api/v1/agent/chat',async route=>{
   mockedChatRequests++;
   await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({status:'ok',reply:'素材验收完成，本次不启动生成任务。',turnId:'attachment-test'})});
  });
  page=await context.newPage();page.on('pageerror',error=>errors.push(error.message));
  page.on('request',req=>{if(req.method()==='POST'&&new URL(req.url()).pathname==='/api/v1/avatar-mvp/projects')projectCreates++});
  page.on('response',res=>{if(res.request().method()==='POST'&&/\/avatar-mvp\/projects\/[^/]+\/assets$/.test(new URL(res.url()).pathname))uploadStatuses.push(res.status())});
  await page.goto(base+'/login');
  await page.locator('input[name=email]').fill(account.email);
  await page.locator('input[name=password]').fill(account.password);
  await page.locator('button[type=submit]').click();
  const add=page.getByRole('button',{name:'添加参考素材',exact:true});
  await expect(add).toBeVisible();
  const prompt=page.getByRole('textbox',{name:'描述视频创意'});
  const dropBounds=await add.boundingBox(),textBounds=await prompt.boundingBox();
  assert.ok(dropBounds.x+dropBounds.width<textBounds.x,'Attachment box should sit left of the prompt');
  assert.equal(await add.evaluate(el=>getComputedStyle(el).borderTopStyle),'dashed');
  checks.leftDashedAttachmentBox=true;
  await page.screenshot({path:resolve(output,'home-empty-desktop.png'),fullPage:true});

  async function choose(label,paths){
   await add.click();
   const [chooser]=await Promise.all([page.waitForEvent('filechooser'),page.getByRole('menuitem',{name:label,exact:true}).click()]);
   assert.equal(chooser.isMultiple(),true);await chooser.setFiles(paths);
  }
  await choose('上传人物图片',[referenceImage,portrait]);
  await expect(page.getByRole('button',{name:'移除reference-image.png',exact:true})).toBeVisible();
  await choose('上传人物图片',[referenceImage]);
  await expect(page.getByRole('button',{name:'移除reference-image.png',exact:true})).toHaveCount(1);
  await page.getByRole('button',{name:'移除primary-portrait.png',exact:true}).click();
  await choose('上传人物图片',[portrait]);
  await page.getByRole('button',{name:'设为主人物primary-portrait.png',exact:true}).click();
  checks.multiImageAndRepeatSelection=true;
  await choose('上传参考视频',[videoOne,badVideo]);
  await expect(page.getByRole('button',{name:'移除reference-one.mp4',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'移除broken-video.mp4',exact:true})).toBeVisible();
  checks.multiVideoSelection=true;
  await add.click();await page.getByRole('menuitem',{name:'添加网页链接',exact:true}).click();
  const links=page.getByRole('textbox',{name:'网页链接',exact:true});
  await links.fill('javascript:alert(1)');await page.getByRole('button',{name:'添加链接',exact:true}).click();
  await expect(page.getByText('请输入完整的 http:// 或 https:// 网页链接')).toBeVisible();
  await links.fill('https://example.com/a\nhttps://example.com/b\nhttps://example.com/a\nhttps://example.com/remove');
  await page.getByRole('button',{name:'添加链接',exact:true}).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.getByRole('button',{name:'移除https://example.com/remove',exact:true}).click();
  await expect(page.getByRole('button',{name:'移除https://example.com/a',exact:true})).toHaveCount(1);
  checks.linkValidationDeduplicationAndRemoval=true;
  await prompt.fill('首页多素材验收：使用上传的图片、视频和网页地址，先整理制作思路。');
  await page.screenshot({path:resolve(output,'home-selected-desktop.png'),fullPage:true});
  await page.setViewportSize({width:390,height:844});
  await expect(add).toBeVisible();await expect(prompt).toBeVisible();
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),'Mobile page overflows horizontally');
  await page.screenshot({path:resolve(output,'home-selected-mobile.png'),fullPage:true});
  checks.mobile390NoOverflow=true;
  await page.setViewportSize({width:1440,height:1000});

  const created=page.waitForResponse(res=>res.request().method()==='POST'&&new URL(res.url()).pathname==='/api/v1/avatar-mvp/projects');
  await page.getByRole('button',{name:'发送创意',exact:true}).click();
  const response=await created;assert.equal(response.status(),200);id=unwrap(await response.json()).id;
  assert.ok(id,'Created project id missing');
  await expect(page.getByRole('alert')).toContainText('可移除失败文件',{timeout:60000});
  async function snapshot(){const response=await api.get('/api/v1/agent/project/'+id,{headers});assert.equal(response.status(),200);return unwrap(await response.json())}
  let state=await snapshot();assert.equal(state.project.input.assets.length,3);
  assert.deepEqual(state.project.input.reference_links,['https://example.com/a','https://example.com/b']);
  await expect(page.getByRole('button',{name:'移除reference-image.png',exact:true})).toBeDisabled();
  await page.screenshot({path:resolve(output,'upload-failure-recovery.png'),fullPage:true});
  await page.getByRole('button',{name:'移除broken-video.mp4',exact:true}).click();
  await choose('上传参考视频',[videoTwo]);
  await page.getByRole('button',{name:'发送创意',exact:true}).click();
  await expect(page).toHaveURL(base+'/workspace/'+id,{timeout:60000});
  await expect(page.getByRole('complementary',{name:'Agent 对话'})).toBeVisible();
  await expect.poll(()=>mockedChatRequests).toBe(1);
  assert.equal(projectCreates,1);assert.deepEqual(uploadStatuses,[200,200,200,400,200]);
  checks.failedVideoReplacementWithoutReupload=true;
  state=await snapshot();assert.equal(state.run,null,'Acceptance must not start generation');
  const assets=state.project.input.assets;
  assert.deepEqual(assets.map(asset=>[asset.name,asset.kind]),[
   ['reference-image.png','image'],['primary-portrait.png','photo'],['reference-one.mp4','video'],['reference-two.mp4','video'],
  ]);
  for(const asset of assets){
   const response=await api.get(`/api/v1/avatar-mvp/projects/${id}/assets/${asset.id}`,{headers});
   assert.equal(response.status(),200);assert.deepEqual(await response.body(),readFileSync(resolve(output,asset.name)));
  }
  checks.realUploadsAndDownloadBytes=true;
  await page.getByRole('button',{name:'素材与产物',exact:true}).click();
  await expect(page.getByRole('heading',{name:'primary-portrait.png',exact:true})).toBeVisible();
  await expect(page.getByRole('link',{name:'https://example.com/a',exact:true})).toHaveAttribute('href','https://example.com/a');
  await page.reload();
  await page.getByRole('button',{name:'素材与产物',exact:true}).click();
  await expect(page.getByRole('heading',{name:'reference-two.mp4',exact:true})).toBeVisible();
  await expect(page.getByRole('link',{name:'https://example.com/b',exact:true})).toBeVisible();
  await expect.poll(async()=>page.locator('img[alt="primary-portrait.png"]').evaluate(el=>el.complete&&el.naturalWidth>0)).toBe(true);
  assert.deepEqual((await snapshot()).project.input.reference_links,['https://example.com/a','https://example.com/b']);
  await page.screenshot({path:resolve(output,'workspace-persisted-assets.png'),fullPage:true});
  checks.assetsAndLinksPersistAfterRefresh=true;
  assert.deepEqual(errors,[]);
 }catch(error){
  if(page)await page.screenshot({path:resolve(output,'failure.png'),fullPage:true}).catch(()=>{});
  throw error;
 }finally{
  if(browser)await browser.close();
  if(id){const response=await api.delete('/api/v1/projects/'+id,{headers});assert.ok(response.ok(),'Temporary project cleanup failed');checks.temporaryProjectRemoved=true}
  await api.dispose();
 }
 const report={url:base+'/',checks,pageErrors:errors,projectCreates,uploadStatuses,mockedChatRequests,
  scope:'Real create/upload/download/persistence; only Agent chat response mocked; no paid generation',screenshots:output};
 writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2)+'\n');
 console.log(JSON.stringify(report,null,2));
})().catch(error=>{console.error(error.message);process.exitCode=1});
