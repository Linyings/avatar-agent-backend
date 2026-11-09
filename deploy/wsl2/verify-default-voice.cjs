// Verify the deployed default and optionally repair one project draft.
// No workflow runs, confirmations, or provider generation requests are started.
const {execFileSync}=require('node:child_process');
const {createHash,randomUUID}=require('node:crypto');
const {mkdirSync,writeFileSync}=require('node:fs');
const {resolve}=require('node:path');
const assert=require('node:assert/strict');
const {request}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
const digest=x=>createHash('sha256').update(JSON.stringify(x)).digest('hex');

(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let account={};
 for(const line of raw.split(/\r?\n/)){
  const split=line.indexOf(': ');if(split<0)continue;
  const key=line.slice(0,split),value=line.slice(split+2);
  if(key==='Email'){if(account.email&&account.password)accounts.push(account);account={email:value}}
  if(key==='Password')account.password=value;
 }
 if(account.email&&account.password)accounts.push(account);
 const api=await request.newContext({baseURL:'http://localhost:8080'});
 let headers,temporaryProject;
 const report={defaultVoice:'Wise_Woman',model:'minimax/speech-2.8-hd',modelTasksStarted:0};
 try{
  for(const candidate of accounts){
   const response=await api.post('/api/v1/auth/login',{data:candidate});
   if(response.status()!==200)continue;
   headers={Authorization:'Bearer '+unwrap(await response.json()).tokens.access_token};break;
  }
  assert.ok(headers,'Saved local login unavailable');
  const get=async path=>{const r=await api.get(path,{headers});assert.equal(r.status(),200,path+' status');return unwrap(await r.json())};
  const catalog=await get('/api/v1/avatar-mvp/catalog');
  assert.equal(catalog.default_voice,report.defaultVoice);
  assert.ok(catalog.voices.some(v=>v.id===report.defaultVoice&&v.model===report.model));
  assert.ok(!catalog.voices.some(v=>v.id.endsWith('_bigtts')));
  report.catalog=catalog.voices.map(v=>({id:v.id,name:v.name,model:v.model}));

  const option=process.argv.indexOf('--repair-project');
  if(option!==-1){
   const id=process.argv[option+1];assert.match(id??'',/^[0-9a-f-]{36}$/i);
   const before=await get('/api/v1/agent/project/'+id);
   const previousVoice=before.project.input.voice;
   const runHash=digest(before.run);
   if(previousVoice!==report.defaultVoice){
    const response=await api.post('/api/v1/agent/packages/avatar.configure/invoke',{headers,data:{projectId:id,requestId:'default-voice-'+randomUUID(),input:{revision:before.project.input.revision,voice:report.defaultVoice}}});
    assert.equal(response.status(),200,'Repair project draft');
   }
   const after=await get('/api/v1/agent/project/'+id);
   assert.equal(after.project.input.voice,report.defaultVoice);
   assert.equal(digest(after.run),runHash,'Existing run and supplier orders must stay unchanged');
   const expectedInput={...before.project.input,voice:report.defaultVoice,revision:before.project.input.revision+(previousVoice===report.defaultVoice?0:1)};
   assert.deepEqual(after.project.input,expectedInput,'Only voice and draft revision may change');
   report.draftRepair={projectId:id,previousVoice,voice:after.project.input.voice,revision:after.project.input.revision,existingRunUnchanged:true,runHash,status:after.run?.status};
  }

  const created=await api.post('/api/v1/avatar-mvp/projects',{headers,data:{title:'默认音色验收（临时）',script:'这是默认音色配置检查，不执行生成。'}});
  assert.equal(created.status(),200,'Create draft without explicit voice');
  const project=unwrap(await created.json());temporaryProject=project.id;
  assert.equal(project.input.voice,report.defaultVoice);
  const snapshot=await get('/api/v1/agent/project/'+temporaryProject);
  assert.equal(snapshot.project.input.voice,report.defaultVoice);
  assert.equal(snapshot.run,null);
  report.newProjectDefault='passed';
 }finally{
  if(temporaryProject){
   const deleted=await api.delete('/api/v1/projects/'+temporaryProject,{headers});
   report.temporaryProjectDeleted=deleted.ok();
   assert.ok(deleted.ok(),'Temporary draft cleanup failed');
  }
  await api.dispose();
 }
 const out=resolve(__dirname,'../../docs/reviews/2026-11-07');mkdirSync(out,{recursive:true});
 writeFileSync(resolve(out,'default-voice-deployment.json'),JSON.stringify(report,null,2)+'\n');
 console.log(JSON.stringify(report,null,2));
})().catch(e=>{console.error(e.message);process.exitCode=1});
