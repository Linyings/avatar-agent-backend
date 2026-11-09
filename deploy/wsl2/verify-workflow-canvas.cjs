// Real WSL browser acceptance. Only newly created fixture projects are deleted.
// Existing projects are read-only; no model or generation requests are made.
const {execFileSync}=require('node:child_process');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {chromium,request,expect}=require('../../frontend/node_modules/@playwright/test');
const unwrap=b=>b.data??b;
const output=resolve(__dirname,'../../backend/data/wsl-deployment/workflow-canvas');
mkdirSync(output,{recursive:true});
async function connect(){
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let item={};
 for(const line of raw.split(/\r?\n/)){
  const i=line.indexOf(': ');if(i<0)continue;const key=line.slice(0,i),value=line.slice(i+2);
  if(key==='Email'){if(item.email&&item.password)accounts.push(item);item={email:value}}
  if(key==='Password')item.password=value;
 }
 if(item.email&&item.password)accounts.push(item);
 const ip=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','hostname','-I'],{encoding:'utf8'}).trim().split(/\s+/)[0];
 const base=process.env.AVATAR_BASE_URL||`http://${ip}:8080`;
 const api=await request.newContext({baseURL:base});
 for(const account of accounts){
  const response=await api.post('/api/v1/auth/login',{data:account});if(response.status()===401)continue;
  assert.equal(response.status(),200);const token=unwrap(await response.json()).tokens.access_token;
  return {base,api,account,headers:{Authorization:'Bearer '+token}};
 }
 throw new Error('No valid deployment login');
}
async function mediaProjects(api,headers){
 const response=await api.get('/api/v1/projects/',{headers,params:{limit:100}});assert.equal(response.status(),200);
 const body=unwrap(await response.json()),projects=Array.isArray(body)?body:body.projects??body.items??[];
 const states=[];
 for(const p of projects){
  if(p.metadata?.inference_mode!=='avatar'&&p.workflow?.mode!=='avatar')continue;
  const res=await api.get('/api/v1/agent/project/'+p.id,{headers});if(res.status()!==200)continue;
  const state=unwrap(await res.json());if(state.run?.segments?.some(s=>s.audio))states.push(state);
 }
 return states;
}
(async()=>{
 const {base,api,account,headers}=await connect();const created=[];let browser,page;let passed=false;const checks={},errors=[],flowErrors=[];let modelRequests=0;
 try{
  const existing=await mediaProjects(api,headers);
  if(process.argv[2]==='inventory'){
   console.log(JSON.stringify(existing.map(s=>({id:s.project.id,status:s.run.status,audio:s.run.segments.filter(x=>x.audio).length,artifacts:s.run.artifacts.length}))));return;
  }
  const getState=async id=>unwrap(await(await api.get('/api/v1/agent/project/'+id,{headers})).json());
  async function create(title){
   const res=await api.post('/api/v1/avatar-mvp/projects',{headers,data:{title,topic:'工作流画布交互验收',script:'这是用于验证节点展示的口播文案。\n'+Array.from({length:16},(_,i)=>`第 ${i+1} 段：文案、图片、播放器和真实完成进度应直接显示在节点内部。`).join('\n'),style:'flat-vector',use_avatar:false}});
   assert.equal(res.status(),200);const id=unwrap(await res.json()).id;created.push(id);return id;
  }
  const id=await create('画布验收-'+Date.now());
  browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const context=await browser.newContext({viewport:{width:1600,height:1000}});
  const pngPage=await context.newPage();await pngPage.setViewportSize({width:480,height:270});
  await pngPage.setContent('<body style="margin:0;display:grid;place-items:center;height:100vh;background:linear-gradient(140deg,#334c83,#14746d);color:white;font:24px sans-serif">CANVAS · 素材预览</body>');
  const png=await pngPage.screenshot();await pngPage.close();
  const uploaded=await api.post(`/api/v1/avatar-mvp/projects/${id}/assets`,{headers,multipart:{kind:'image',file:{name:'canvas-preview.png',mimeType:'image/png',buffer:png}}});assert.equal(uploaded.status(),200);
  const snapshot=await getState(id);
  const nodes=snapshot.packages.filter(p=>p.node),ids=new Set(nodes.map(p=>p.node));
  const expectedEdges=nodes.flatMap(p=>p.dependencies.filter(d=>ids.has(d)).map(d=>`${d}->${p.node}`)).sort();
  page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  page.on('console',msg=>{if(msg.text().includes('[React Flow]'))flowErrors.push(msg.text())});
  page.on('request',req=>{if(req.method()==='POST'&&/\/api\/v1\/(?:agent\/(?:chat|confirm|pre-production\/start|packages\/.*\/invoke)|avatar-mvp\/(?:runs|tasks))/.test(req.url()))modelRequests++});
  await page.goto(base+'/login',{waitUntil:'domcontentloaded'});
  await page.locator('input[name=email]').fill(account.email);await page.locator('input[name=password]').fill(account.password);await page.locator('button[type=submit]').click();
  await expect(page.getByRole('heading',{name:/你的专属AI视频Agent/})).toBeVisible();
  async function open(project){
   await page.goto(base+'/workspace/'+project,{waitUntil:'domcontentloaded'});
   await expect(page.locator('.react-flow__node')).toHaveCount(nodes.length);
   await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled({timeout:20000});
   await expect(page.locator('.react-flow__edge')).toHaveCount(expectedEdges.length);
  }
  async function focus(node){
   const p=nodes.find(p=>p.node===node),input=page.getByRole('combobox',{name:'定位制作步骤',exact:true});
   await input.fill(`${node} ${p.title}`);await page.getByRole('option',{name:`${node} ${p.title}`,exact:true}).click();
   await expect.poll(()=>page.getByTestId('workflow-node-'+node).evaluate(el=>{
    const r=el.getBoundingClientRect(),p=el.closest('[data-testid=workflow-canvas]').getBoundingClientRect();return r.left>=p.left&&r.right<=p.right&&r.top>=p.top&&r.bottom<=p.bottom&&Math.abs(r.x+r.width/2-p.x-p.width/2)<2&&Math.abs(r.y+r.height/2-p.y-p.height/2)<2;
   })).toBe(true);
  }
  const transform=node=>page.locator(`.react-flow__node[data-id="${node}"]`).evaluate(el=>el.style.transform);
  const viewport=()=>page.locator('.react-flow__viewport').evaluate(el=>el.style.transform);
  const savedLayout=()=>page.evaluate(project=>{
   const key=Object.keys(localStorage).find(k=>k.startsWith('studio-canvas:v1:')&&k.endsWith(':'+project));return key?JSON.parse(localStorage.getItem(key)):null;
  },id);
  await open(id);
  assert.deepEqual(await page.locator('.react-flow__edge').evaluateAll(es=>es.map(e=>e.dataset.id).sort()),expectedEdges);checks.dependencies=true;
  await focus('F01');const image=page.getByTestId('workflow-node-F01').getByAltText('canvas-preview.png');await expect(image).toBeVisible();
  await expect.poll(()=>image.evaluate(img=>img.complete&&img.naturalWidth>0)).toBe(true);checks.realImage=true;
  await page.screenshot({path:resolve(output,'input-node.png'),fullPage:true});
  const before=await transform('F01'),header=await page.getByTestId('workflow-node-F01').locator('header').boundingBox();
  await page.mouse.move(header.x+100,header.y+header.height/2);await page.mouse.down();await page.mouse.move(header.x+180,header.y+header.height/2+60,{steps:12});await page.mouse.up();
  await expect.poll(()=>transform('F01')).not.toBe(before);const dragged=await transform('F01');
  await expect.poll(async()=>(await savedLayout())?.positions?.F01?.x).toBeGreaterThan(40);checks.nodeDrag=true;
  const edited=await api.post(`/api/v1/agent/packages/avatar.configure/invoke`,{headers,data:{projectId:id,input:{revision:snapshot.project.input.revision,topic:'后台刷新后仍保留节点位置'},requestId:'canvas-refresh-'+Date.now()}});assert.equal(edited.status(),200);
  await expect(page.getByTestId('workflow-node-F01')).toContainText('后台刷新后仍保留节点位置',{timeout:10000});
  assert.equal(await transform('F01'),dragged);checks.pollKeepsLayout=true;
  const stored=await savedLayout();await page.reload({waitUntil:'domcontentloaded'});
  await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled({timeout:20000});
  await expect.poll(()=>transform('F01')).toBe(dragged);assert.deepEqual((await savedLayout()).positions.F01,stored.positions.F01);checks.refreshKeepsLayout=true;
  const originalView=await viewport();await page.getByRole('button',{name:'放大画布',exact:true}).click();await expect.poll(viewport).not.toBe(originalView);checks.zoom=true;
  const blank=await page.getByTestId('workflow-canvas').evaluate(el=>{
   const r=el.getBoundingClientRect();for(let y=r.bottom-160;y>r.top+100;y-=60)for(let x=r.left+60;x<r.right-60;x+=80){if(document.elementFromPoint(x,y)?.classList.contains('react-flow__pane'))return{x,y}}return null;
  });assert.ok(blank,'No empty canvas position');const view=await viewport();
  await page.mouse.move(blank.x,blank.y);await page.mouse.down();await page.mouse.move(blank.x+55,blank.y+35,{steps:8});await page.mouse.up();await expect.poll(viewport).not.toBe(view);checks.pan=true;
  await focus('F02');await expect(page.getByTestId('workflow-node-F02')).toContainText('这是用于验证节点展示的口播文案');
  const copy=page.getByLabel('口播文案内容',{exact:true}),copyView=await viewport();await copy.hover();await page.mouse.wheel(0,180);
  await expect.poll(()=>copy.evaluate(el=>el.scrollTop)).toBeGreaterThan(0);assert.equal(await viewport(),copyView);checks.textScrollDoesNotZoom=true;
  await page.getByRole('button',{name:'查看口播文案详情',exact:true}).click();await expect(page.getByRole('complementary',{name:'步骤详情'})).toBeVisible();await expect(page.getByTestId('workflow-canvas')).toBeVisible();
  await page.getByRole('button',{name:'返回画布'}).click();checks.nodeDetails=true;
  await page.getByRole('button',{name:'自动排列',exact:true}).click();await expect(page.getByRole('button',{name:'自动排列',exact:true})).toBeEnabled();
  await expect.poll(()=>transform('F01')).not.toBe(dragged);checks.autoLayout=true;
  await page.getByRole('button',{name:'展开大画布'}).click();await expect(page.getByTestId('workflow-canvas')).toHaveAttribute('data-expanded','true');
  await page.getByRole('button',{name:'查看全部节点',exact:true}).click();await page.screenshot({path:resolve(output,'workflow-overview.png'),fullPage:true});
  await page.keyboard.press('Escape');await expect(page.getByTestId('workflow-canvas')).toHaveAttribute('data-expanded','false');checks.expand=true;
  const second=await create('画布隔离验收-'+Date.now());await open(second);
  await expect.poll(()=>transform('F01')).not.toBe(dragged);checks.projectIsolation=true;
  await page.setViewportSize({width:390,height:844});await focus('F01');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true);
  assert.equal(await page.getByTestId('workflow-node-F01').locator('header').evaluate(el=>{const r=el.getBoundingClientRect();return el.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2))}),true,'Mobile toolbar must not cover the node drag handle');checks.mobile=true;
  await page.screenshot({path:resolve(output,'mobile.png'),fullPage:true});await page.setViewportSize({width:1600,height:1000});
  assert.ok(existing.length,'No existing project with audio available for real playback verification');
  const mediaState=existing[0];await open(mediaState.project.id);await focus('F04');
  const audio=page.getByTestId('workflow-node-F04').locator('audio').first();await expect(audio).toBeVisible();
  await expect.poll(()=>audio.evaluate(a=>a.readyState),{timeout:20000}).toBeGreaterThanOrEqual(2);
  const audioPosition=await transform('F04');await audio.click({position:{x:18,y:15}});await expect.poll(()=>audio.evaluate(a=>a.currentTime)).toBeGreaterThan(.1);await audio.evaluate(a=>a.pause());
  assert.equal(await transform('F04'),audioPosition);checks.realAudioPlayback=true;
  const total=mediaState.run.segments.length,complete=mediaState.run.segments.filter(s=>mediaState.run.steps.some(x=>x.id==='F04/'+s.id&&x.status==='succeeded')).length;
  await expect(page.getByTestId('workflow-node-F04').getByRole('progressbar')).toHaveAttribute('aria-valuenow',String(Math.round(complete/total*100)));checks.realProgress=true;
  await page.screenshot({path:resolve(output,'audio-node.png'),fullPage:true});
  assert.equal(modelRequests,0);assert.deepEqual(errors,[]);assert.deepEqual(flowErrors,[]);passed=true;
 }catch(e){if(page)await page.screenshot({path:resolve(output,'failure.png'),fullPage:true}).catch(()=>{});throw e}
 finally{
  if(browser)await browser.close();
  const cleanup=[];for(const id of created){const res=await api.delete('/api/v1/projects/'+id,{headers});cleanup.push({id,status:res.status()})}
  await api.dispose();
  if(process.argv[2]!=='inventory'){
   const report={checkedAt:new Date().toISOString(),base,passed,checks,errors,flowErrors,modelRequests,cleanup};
   writeFileSync(resolve(output,'verification.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report));
  }
 }
})().catch(e=>{console.error(e.message);process.exitCode=1});
