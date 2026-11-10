// Run the source service against real IndexedDB before a frontend rebuild.
const {readFileSync}=require('node:fs');
const {resolve}=require('node:path');
const {transpileModule,ModuleKind,ScriptTarget}=require('../../frontend/node_modules/typescript');
const {chromium}=require('../../frontend/node_modules/@playwright/test');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:'C:/Program Files/Google/Chrome/Application/chrome.exe'});
 try{
  const page=await (await browser.newContext()).newPage();
  await page.goto('http://localhost:8080/');
  const source=readFileSync(resolve(__dirname,'../../frontend/src/shared/services/localAudioStorageService.ts'),'utf8');
  const compiled=transpileModule(source,{compilerOptions:{module:ModuleKind.ESNext,target:ScriptTarget.ES2022}}).outputText;
  await page.addScriptTag({type:'module',content:compiled+'\nwindow.cleanupService=localAudioStorageService;'});
  const result=await page.evaluate(async()=>{
   const service=window.cleanupService;
   const audioKeys=[],previewKeys=[];
   for(const projectId of ['delete-me','keep-me']){
    audioKeys.push(await service.saveNarrationAudio({projectId,itemId:1,blob:new Blob(['audio'])}));
    previewKeys.push(await service.saveProjectPreviewImage({projectId,blob:new Blob(['preview'])}));
   }
   await service.deleteProjectCachedData('delete-me');
   return {audio:await Promise.all(audioKeys.map(async k=>!!(await service.getAudioBlob(k)))),preview:await Promise.all(previewKeys.map(async k=>!!(await service.getProjectPreviewBlob(k))))};
  });
  assert.deepEqual(result,{audio:[false,true],preview:[false,true]});
  console.log(JSON.stringify({real_indexeddb_project_cleanup:'passed',other_project:'preserved'}));
 }finally{await browser.close();}
})().catch(error=>{console.error(error.message);process.exitCode=1;});
