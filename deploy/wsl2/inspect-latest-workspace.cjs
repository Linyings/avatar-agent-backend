// Read-only diagnosis. Credentials stay in memory; do not print tokens or config.
const {execFileSync}=require('node:child_process');
const {request}=require('../../frontend/node_modules/@playwright/test');
const unwrap=x=>x.data??x;
(async()=>{
 const raw=execFileSync('wsl.exe',['-d','Ubuntu-24.04','--','cat','/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'],{encoding:'utf8'});
 const accounts=[];let entry={};
 for(const line of raw.split(/\r?\n/)){const split=line.indexOf(': ');if(split<0)continue;const key=line.slice(0,split),value=line.slice(split+2);if(key==='Email'){if(entry.email&&entry.password)accounts.push(entry);entry={email:value}}if(key==='Password')entry.password=value}
 if(entry.email&&entry.password)accounts.push(entry);
 const api=await request.newContext({baseURL:'http://localhost:8080'});
 try{
  let headers;
  for(const account of accounts){const r=await api.post('/api/v1/auth/login',{data:account});if(r.status()!==200)continue;headers={Authorization:'Bearer '+unwrap(await r.json()).tokens.access_token};break}
  if(!headers)throw Error('Saved local login unavailable');
  const r=await api.get('/api/v1/projects/?limit=8&offset=0&sort_by=created_at&sort_order=desc',{headers});if(!r.ok())throw Error('Project listing failed');
  const payload=unwrap(await r.json());const projects=payload.projects??payload.items??payload;
  console.log(JSON.stringify({projects:projects.map(p=>({id:p.id,name:p.name,status:p.status,created:p.created_at}))},null,2));
  const id=process.argv[2]||projects[0]?.id;if(!id)return;
  const snapshot=unwrap(await (await api.get('/api/v1/agent/project/'+id,{headers})).json());
  const run=snapshot.run,input=snapshot.project?.input;
  console.log(JSON.stringify({id,audio_timing:snapshot.audio_timing,input:{use_avatar:input?.use_avatar,photo:input?.photo,topic:input?.topic,assets:input?.assets?.map(a=>({id:a.id,name:a.name,kind:a.kind,excluded:a.excluded}))},proposal:snapshot.proposal,
   run:run?{id:run.id,status:run.status,stage:run.stage,target:run.target_node,confirmation:run.confirmation,error:run.error,updated:run.updated_at,duration:run.duration,segments:run.segments?.map(s=>({id:s.id,chapter:s.chapter,start:s.start,duration:s.duration,audio:s.audio,characters:s.text?.length})),steps:run.steps?.map(s=>({id:s.id,status:s.status,error:s.error,seconds:s.seconds})),needs:run.needs}:null},null,2));
  const history=unwrap(await (await api.get('/api/v1/agent/history?projectId='+id,{headers})).json());
  console.log(JSON.stringify({history:(history.messages??[]).slice(-18).map(m=>({role:m.role,tool:m.toolName,content:m.content?.slice(0,2500),calls:m.toolCalls}))},null,2));
 }finally{await api.dispose()}
})().catch(e=>{console.error(e.message);process.exitCode=1});
