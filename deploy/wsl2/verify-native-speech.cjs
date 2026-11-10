// Optional deployment check: creates and removes its own draft, never starts a model task.
const assert = require('node:assert/strict');
const {resolve} = require('node:path');
const {expect} = require('../../frontend/node_modules/@playwright/test');

function testWave(seconds) {
  const rate=24000,count=Math.round(rate*seconds),buffer=Buffer.alloc(44+count*2);
  buffer.write('RIFF');buffer.writeUInt32LE(buffer.length-8,4);buffer.write('WAVEfmt ',8);
  buffer.writeUInt32LE(16,16);buffer.writeUInt16LE(1,20);buffer.writeUInt16LE(1,22);
  buffer.writeUInt32LE(rate,24);buffer.writeUInt32LE(rate*2,28);buffer.writeUInt16LE(2,32);buffer.writeUInt16LE(16,34);
  buffer.write('data',36);buffer.writeUInt32LE(count*2,40);
  for(let i=0;i<count;i++)buffer.writeInt16LE(Math.round(4000*Math.sin(2*Math.PI*440*i/rate)),44+i*2);
  return buffer;
}

module.exports = async function verifyNativeSpeech({page,context,base,output}) {
  const token=await page.evaluate(()=>localStorage.getItem('auth_access_token'));
  const headers={Authorization:`Bearer ${token}`};
  const unwrap=async response=>{assert.ok(response.ok(),`API status ${response.status()}`);const body=await response.json();return body.data??body;};
  const created=await unwrap(await context.request.post(base+'/api/v1/avatar-mvp/projects',{headers,data:{title:'验收临时草稿-Seedance原声',topic:'只检查配置，不生成视频',script:'这是一段原声口播。',use_avatar:true,ratio:'9:16'}}));
  const id=created.id;
  assert.ok(id);assert.equal(created.input.speech_mode,'native-video');
  try {
    await page.goto(`${base}/workspace/${id}`,{waitUntil:'domcontentloaded'});
    await page.getByRole('button',{name:'制作设置',exact:true}).click();
    await expect(page.getByRole('combobox',{name:'参考音色（可选）'})).toBeVisible();
    await expect(page.getByRole('combobox',{name:'配音音色'})).toHaveCount(0);
    await expect(page.getByRole('textbox',{name:'全片配音语速'})).toHaveCount(0);
    const fileChooser=page.waitForEvent('filechooser');
    await page.getByRole('button',{name:'上传参考音色（2–30 秒）',exact:true}).click();
    const uploaded=page.waitForResponse(r=>new URL(r.url()).pathname===`/api/v1/avatar-mvp/projects/${id}/assets`&&r.request().method()==='POST');
    await (await fileChooser).setFiles({name:'验收声音.wav',mimeType:'audio/wav',buffer:testWave(3)});
    const asset=await unwrap(await uploaded);
    assert.equal(asset.kind,'audio');assert.ok(Math.abs(asset.duration-3)<.05);
    await expect(page.getByRole('combobox',{name:'参考音色（可选）'})).toHaveValue('验收声音.wav');
    const saved=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/agent/packages/avatar.configure/invoke'&&r.request().method()==='POST');
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await unwrap(await saved);
    const state=await unwrap(await context.request.get(`${base}/api/v1/agent/project/${id}`,{headers}));
    assert.equal(state.project.input.voice_reference,asset.id);
    assert.ok(!state.run,'Configuration must not start production');
    assert.equal(state.packages.find(p=>p.node==='F04').ready,true,'Seedance backend is not configured');
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
    await page.screenshot({path:resolve(output,'native-speech-settings.png'),fullPage:true});
    const rejected=await context.request.post(`${base}/api/v1/avatar-mvp/projects/${id}/assets`,{headers,multipart:{kind:'audio',file:{name:'too-short.wav',mimeType:'audio/wav',buffer:testWave(1)}}});
    assert.equal(rejected.status(),400);
    const invalidRef=await context.request.post(`${base}/api/v1/agent/packages/avatar.configure/invoke`,{headers,data:{projectId:id,input:{voice_reference:'outside-project'}}});
    assert.ok(!invalidRef.ok(),'Foreign reference must be rejected');
    await unwrap(await context.request.post(`${base}/api/v1/agent/packages/avatar.configure/invoke`,{headers,data:{projectId:id,input:{voice_reference:''}}}));
    const finalState=await unwrap(await context.request.get(`${base}/api/v1/agent/project/${id}`,{headers}));
    assert.equal(finalState.project.input.voice_reference??'','');
    return {newProjectMode:'native-video',ttsControls:'hidden',audioUpload:'passed',referenceSave:'passed',durationValidation:'passed',referenceScope:'passed',defaultVoice:'passed',modelCalls:0};
  } finally {
    await page.goto(base+'/',{waitUntil:'domcontentloaded'});
    await unwrap(await context.request.delete(`${base}/api/v1/projects/${id}`,{headers}));
  }
};
