// Real deployed Agent verification: one text-only planning turn in a disposable
// project. No named skill/tool is requested in the prompt; the model chooses.
const {execFileSync} = require('node:child_process');
const {mkdirSync, writeFileSync} = require('node:fs');
const {resolve} = require('node:path');
const {randomUUID} = require('node:crypto');
const assert = require('node:assert/strict');

async function main() {
  const base = process.env.AVATAR_BASE_URL || 'http://localhost:8080';
  const raw = execFileSync('wsl.exe', ['-d', 'Ubuntu-24.04', '-u', 'root', '--',
    'cat', '/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'], {encoding: 'utf8'});
  const accounts = [];
  let account = {};
  for (const line of raw.split(/\r?\n/)) {
    const i = line.indexOf(': ');
    if (i < 0) continue;
    const key = line.slice(0, i), value = line.slice(i + 2);
    if (key === 'Email') {
      if (account.email && account.password) accounts.push(account);
      account = {email: value};
    } else if (key === 'Password') account.password = value;
  }
  if (account.email && account.password) accounts.push(account);
  let token, project, reader, streamTask;
  const events = [];
  const controller = new AbortController();
  const unwrap = body => body.data ?? body;
  const request = async (path, data, method = data === undefined ? 'GET' : 'POST') => {
    const response = await fetch(base + path, {
      method,
      headers: {'Content-Type': 'application/json', ...(token ? {Authorization: `Bearer ${token}`} : {})},
      body: data === undefined ? undefined : JSON.stringify(data),
      signal: AbortSignal.timeout(240000),
    });
    assert.equal(response.status, 200, `${method} ${path}: HTTP ${response.status}`);
    return unwrap(await response.json());
  };
  try {
    for (const candidate of accounts) {
      const response = await fetch(base + '/api/v1/auth/login', {
        method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(candidate),
        signal: AbortSignal.timeout(20000),
      });
      if (response.status === 401) continue;
      assert.equal(response.status, 200);
      token = unwrap(await response.json()).tokens.access_token;
      break;
    }
    assert.ok(token, 'No saved account could log in');
    project = (await request('/api/v1/avatar-mvp/projects', {
      title: '默认分镜技能验收-' + Date.now(), topic: '只讨论讲解视频的分镜设计',
      script: '先确定目标，再准备材料，最后检查结果。', style: 'flat-vector', ratio: '9:16',
      use_avatar: true, use_animation: true,
    })).id;
    assert.ok(project);
    const before = await request('/api/v1/agent/project/' + project);
    const skill = before.skills.find(item => item.name === 'talking-head-storyboard');
    assert.ok(skill?.default_enabled && skill.allow_implicit_invocation, 'Default skill absent');
    const stream = await fetch(`${base}/api/v1/agent/stream?projectId=${project}`, {
      headers: {Authorization: `Bearer ${token}`}, signal: controller.signal,
    });
    assert.equal(stream.status, 200);
    reader = stream.body.getReader();
    streamTask = (async () => {
      const decoder = new TextDecoder();
      let buffer = '';
      try {
        while (true) {
          const {value, done} = await reader.read();
          if (done) return;
          buffer += decoder.decode(value, {stream: true});
          let newline;
          while ((newline = buffer.indexOf('\n')) >= 0) {
            const line = buffer.slice(0, newline).trim();
            buffer = buffer.slice(newline + 1);
            if (!line.startsWith('data:')) continue;
            const event = JSON.parse(line.slice(5).trim());
            if (event.type === 'tool') events.push({tool: event.tool, status: event.status});
          }
        }
      } catch (error) {
        if (!controller.signal.aborted) throw error;
      }
    })();
    const result = await request('/api/v1/agent/chat', {
      projectId: project, requestId: randomUUID(), turnId: randomUUID(),
      message: '只分析，不修改项目或参数，不提交提案，不启动任何制作。我想把这三句话设计成竖版口播讲解：先确定目标，再准备材料，最后检查结果。请说明应该怎样切分场景，人物全屏和小窗什么时候切换，面板里的箭头和高亮怎样配合台词，以及普通换场应保留哪些图层。给一份简短分镜建议即可。',
    });
    const after = await request('/api/v1/agent/project/' + project);
    const output = resolve(__dirname, '../../backend/data/wsl-deployment/default-skill');
    mkdirSync(output, {recursive: true});
    const selected = events.some(event => event.tool === 'read_skill' && event.status === 'done');
    const report = {base, defaultEnabled: true, modelSelectedSkill: selected,
      inputUnchanged: JSON.stringify(after.project.input) === JSON.stringify(before.project.input),
      productionStarted: after.run !== null, status: result.status, toolEvents: events, reply: result.reply};
    writeFileSync(resolve(output, 'result.json'), JSON.stringify(report, null, 2));
    console.log(JSON.stringify({...report, reply: undefined, replyLength: result.reply?.length ?? 0}, null, 2));
    assert.equal(result.status, 'ok');
    assert.ok(result.reply, 'Real model returned an empty final reply; diagnostic report saved');
    assert.equal(after.run, null, 'Planning-only turn started production');
    assert.equal(after.proposal, null, 'Planning-only turn created an edit proposal');
    assert.deepEqual(after.project.input, before.project.input, 'Read-only planning changed project input');
    assert.ok(selected,
      'The real model did not choose the relevant default skill');
    assert.ok(events.every(event => ['read_skill', 'avatar_inspect', 'evidence', 'advice', 'review'].includes(event.tool)),
      'Planning-only turn invoked a writing or production tool');
  } finally {
    controller.abort();
    if (reader) await reader.cancel().catch(() => {});
    if (streamTask) await streamTask;
    if (project && token) {
      const response = await fetch(base + '/api/v1/projects/' + project, {
        method: 'DELETE', headers: {Authorization: `Bearer ${token}`}, signal: AbortSignal.timeout(20000),
      });
      assert.ok(response.ok, 'Disposable verification project cleanup failed');
    }
  }
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
