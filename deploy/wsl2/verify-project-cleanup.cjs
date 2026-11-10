// Real UI deletion with isolated, non-billable fixture projects and local files.
const {execFileSync} = require('node:child_process');
const {randomUUID} = require('node:crypto');
const assert = require('node:assert/strict');
const {chromium, expect} = require('../../frontend/node_modules/@playwright/test');
const wsl = args => execFileSync('wsl.exe', ['-d','Ubuntu-24.04','-u','root','--',...args], {encoding:'utf8'}).trim();
const sql = statement => wsl(['docker','exec','aicat-avatar-postgres-1','psql','-XqAt','-v','ON_ERROR_STOP=1','-U','avatar','-d','avatar','-c',statement]);
const uuid = id => { assert.match(id, /^[0-9a-f-]{36}$/); return id; };

(async()=>{
 const base='http://localhost:8080';
 const raw=wsl(['cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt']);
 const accounts=[];let candidate={};
 for(const line of raw.split(/\r?\n/)){
  const split=line.indexOf(': ');if(split<0)continue;
  const key=line.slice(0,split),value=line.slice(split+2);
  if(key==='Email'){if(candidate.email&&candidate.password)accounts.push(candidate);candidate={email:value};}
  if(key==='Password')candidate.password=value;
 }
 if(candidate.email&&candidate.password)accounts.push(candidate);
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
 const context=await browser.newContext();const page=await context.newPage();
 page.on('console', async message=>{if(message.type()==='warning' && /[Cc]leanup|缓存/.test(message.text())){console.log('Browser cleanup warning:',message.text());for(const arg of message.args())console.log(await arg.evaluate(value=>value?.errors?.map(e=>({name:e.name,message:e.message}))??null));}});
 const projects=[];const run=randomUUID();let seeded=false;
 const api=async(method,path,body)=>page.evaluate(async({method,path,body})=>{
  const r=await fetch(path,{method,headers:{Authorization:'Bearer '+localStorage.getItem('auth_access_token'),'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});
  return {status:r.status,body:await r.json()};
 },{method,path,body});
 try{
  let account;
  for(const possible of accounts){const r=await context.request.post(base+'/api/v1/auth/login',{data:possible});if(r.ok()){account=possible;break;}}
  assert.ok(account,'Saved login unavailable');
  await page.goto(base+'/login');
  await page.locator('input[name="email"]').fill(account.email);
  await page.locator('input[name="password"]').fill(account.password);
  await page.locator('button[type="submit"]').click();
  await expect(page).toHaveURL(base+'/');
  const names=['cleanup-check-'+Date.now(),'cleanup-control-'+Date.now()];
  for(const name of names){
   const r=await api('POST','/api/v1/avatar-mvp/projects',{title:name,topic:'本地清理验证',style:'explain-podcast',ratio:'9:16'});
   assert.equal(r.status,200,JSON.stringify(r.body));
   const id=uuid((r.body.data??r.body).id);projects.push(id);
   const upload=await page.evaluate(async id=>{
    const form=new FormData();form.set('kind','text');form.set('file',new Blob(['cleanup test original'],{type:'text/plain'}),'original.txt');
    const response=await fetch('/api/v1/avatar-mvp/projects/'+id+'/assets',{method:'POST',headers:{Authorization:'Bearer '+localStorage.getItem('auth_access_token')},body:form});
    return {status:response.status,body:await response.json()};
   },id);
   assert.equal(upload.status,200,JSON.stringify(upload.body));
  }
  const [target,control]=projects;
  const state=JSON.stringify({id:run,project_id:target,status:'running'});
  sql(`INSERT INTO workflow_runs (id,user_id,project_id,client_type,input_hash,status,billing_status,client_instance_id,last_active_at,expires_at,created_at,updated_at)
       SELECT '${run}',owner_id,id,'web','cleanup-check','running','released','cleanup-check',now(),now()+interval '1 hour',now(),now() FROM projects WHERE id='${target}' AND name LIKE 'cleanup-check-%';
       INSERT INTO workflow_step_executions (id,workflow_run_id,step_id,input_hash,execution_key,status,result,created_at,updated_at)
       VALUES ('${randomUUID()}','${run}','avatar.workflow.v2','cleanup-check','${randomUUID()}','running','${state}',now(),now());`);
  seeded=true;
  const fixturePython=`import pathlib,sys,uuid
p,r=map(lambda x:str(uuid.UUID(x)),sys.argv[1:])
root=pathlib.Path('/app/data/avatar-mvp')
paths=[root/'workflow-v2/projects'/p/'cache/render.mp4',root/'workflow-v2/runs'/r/'output.mp4',root/'workflow-v2/runs'/r/'pieces/temp.bin',root/r/'photo']
for path in paths:
 path.parent.mkdir(parents=True,exist_ok=True)
 path.write_bytes(b'local cleanup fixture')
print('Fixture files ready')`;
  wsl(['docker','exec','aicat-avatar-api-1','/opt/avatar-venv/bin/python','-c',fixturePython,target,run]);
  const blocked=await api('DELETE','/api/v1/projects/'+target);
  assert.equal(blocked.status,409,'Running project was not protected');
  sql(`UPDATE workflow_runs SET status='succeeded' WHERE id='${run}'; UPDATE workflow_step_executions SET status='succeeded',result='${JSON.stringify({id:run,project_id:target,status:'succeeded'})}' WHERE workflow_run_id='${run}';`);
  await page.evaluate(async ids=>{
   const root=await navigator.storage.getDirectory();const projects=await root.getDirectoryHandle('projects',{create:true});
   for(const id of ids){const dir=await projects.getDirectoryHandle(id,{create:true});const file=await dir.getFileHandle('original',{create:true});const stream=await file.createWritable();await stream.write('local cache');await stream.close();}
   await new Promise((resolve,reject)=>{const request=indexedDB.open('narrato-local-audio',3);
    request.onupgradeneeded=()=>{for(const name of ['audio-blobs','preview-images']){const store=request.result.createObjectStore(name,{keyPath:'key'});store.createIndex('projectId','projectId');store.createIndex('updatedAt','updatedAt');}};
    request.onerror=()=>reject(request.error);request.onsuccess=()=>{const db=request.result,tx=db.transaction(['audio-blobs','preview-images'],'readwrite');for(const name of ['audio-blobs','preview-images'])for(const id of ids)tx.objectStore(name).put({key:`project:${id}:${name==='audio-blobs'?'narration:1':'preview-image'}`,projectId:id,blob:new Blob(['fixture']),updatedAt:new Date().toISOString()});tx.oncomplete=()=>{db.close();resolve();};tx.onerror=()=>reject(tx.error);};
   });
  },projects);
  await page.goto(base+'/projects');
  await page.getByRole('button',{name:'管理 '+names[0],exact:true}).click();
  await page.getByRole('menuitem',{name:'删除项目',exact:true}).click();
  await expect(page.getByRole('dialog')).toContainText('原始素材');
  const deleted=page.waitForResponse(r=>r.request().method()==='DELETE'&&r.url().endsWith('/'+target));
  await page.getByRole('button',{name:'确认删除',exact:true}).click();
  assert.equal((await deleted).status(),200);
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'打开项目 '+names[0],exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'打开项目 '+names[1],exact:true})).toBeVisible();
  const caches=await page.evaluate(async ids=>{
   const root=await navigator.storage.getDirectory(),projects=await root.getDirectoryHandle('projects');const opfs=[];
   for(const id of ids){try{await projects.getDirectoryHandle(id);opfs.push(true);}catch(e){if(e.name!=='NotFoundError')throw e;opfs.push(false);}}
   const indexed=await new Promise((resolve,reject)=>{const request=indexedDB.open('narrato-local-audio',3);request.onerror=()=>reject(request.error);request.onsuccess=()=>{const db=request.result,tx=db.transaction(['audio-blobs','preview-images']);const result={};for(const name of ['audio-blobs','preview-images']){const r=tx.objectStore(name).getAllKeys();r.onsuccess=()=>result[name]=r.result;}tx.oncomplete=()=>{db.close();resolve(result);};};});
   return {opfs,indexed};
  },projects);
  assert.deepEqual(caches.opfs,[false,true]);
  assert.deepEqual(caches.indexed['audio-blobs'],[`project:${control}:narration:1`]);
  assert.deepEqual(caches.indexed['preview-images'],[`project:${control}:preview-image`]);
  const verifyPython=`import pathlib,sys,uuid
p,r,c=map(lambda x:str(uuid.UUID(x)),sys.argv[1:])
root=pathlib.Path('/app/data/avatar-mvp')
assert not (root/'workflow-v2/projects'/p).exists()
assert not (root/'workflow-v2/runs'/r).exists()
assert not (root/r).exists()
assert any((root/'workflow-v2/projects'/c/'assets').iterdir())
print('Original, cache, run and legacy files verified')`;
  wsl(['docker','exec','aicat-avatar-api-1','/opt/avatar-venv/bin/python','-c',verifyPython,target,run,control]);
  assert.equal(sql(`SELECT count(*) FROM workflow_runs WHERE id='${run}'`),'0');
  assert.equal(sql(`SELECT count(*) FROM workflow_step_executions WHERE workflow_run_id='${run}'`),'0');
  assert.equal((await api('GET','/api/v1/avatar-mvp/projects/'+target)).status,404);
  assert.equal((await api('GET','/api/v1/avatar-mvp/projects/'+control)).status,200);
  console.log(JSON.stringify({ui_delete:true,originals_removed:true,process_files_removed:true,opfs_removed:true,indexeddb_removed:true,active_project_protected:true,other_project_preserved:true,paid_calls:0}));
 }finally{
  if(seeded)sql(`UPDATE workflow_runs SET status='succeeded' WHERE id='${run}'; UPDATE workflow_step_executions SET status='succeeded',result='{"status":"succeeded"}' WHERE workflow_run_id='${run}';`);
  for(const id of projects){const r=await api('DELETE','/api/v1/projects/'+id).catch(()=>null);if(r&&! [200,404].includes(r.status))console.error('Test project cleanup needs retry:',id);}
  await browser.close();
 }
})().catch(error=>{console.error(error.message);process.exitCode=1;});
