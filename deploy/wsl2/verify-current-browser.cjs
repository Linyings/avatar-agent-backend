// Validate the real WSL deployment without starting model calls or creating tasks.
const {execFileSync} = require('node:child_process');
const {mkdirSync, writeFileSync} = require('node:fs');
const {resolve} = require('node:path');
const assert = require('node:assert/strict');
const {chromium, expect} = require('../../frontend/node_modules/@playwright/test');

(async () => {
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
  const ip = execFileSync('wsl.exe', ['-d', 'Ubuntu-24.04', '-u', 'root', '--', 'hostname', '-I'],
    {encoding: 'utf8'}).trim().split(/\s+/)[0];
  const base = process.env.AVATAR_BASE_URL || `http://${ip}:8080`;
  const output = resolve(__dirname, '../../backend/data/wsl-deployment/current', new URL(base).hostname);
  mkdirSync(output, {recursive: true});
  const browser = await chromium.launch({headless: true,
    executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  const errors = [];
  let modelTasksStarted = 0;
  let page;
  try {
    const context = await browser.newContext({viewport: {width: 1440, height: 1000}});
    page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', req => {
      if (req.method() === 'POST' && /\/api\/v1\/(?:agent\/(?:chat|confirm|pre-production\/start)|avatar-mvp\/(?:runs|tasks))/.test(req.url())) modelTasksStarted++;
    });
    const heading = page.getByRole('heading', {name: /你的专属AI视频Agent/});
    await page.goto(base + '/', {waitUntil: 'domcontentloaded'});
    await expect(heading).toBeVisible();

    let validAccount;
    for (const candidate of accounts) {
      const response = await context.request.post(base + '/api/v1/auth/login', {data: candidate});
      if (response.status() === 401) continue;
      assert.equal(response.status(), 200, 'Saved-account login check failed');
      validAccount = candidate;
      break;
    }
    assert.ok(validAccount, 'No valid saved deployment account');
    await page.goto(base + '/login', {waitUntil: 'domcontentloaded'});
    await page.locator('input[name="email"]').fill(validAccount.email);
    await page.locator('input[name="password"]').fill(validAccount.password);
    await page.locator('button[type="submit"]').click();
    await expect(heading).toBeVisible();
    await expect(page).toHaveURL(base + '/');
    await page.reload({waitUntil: 'domcontentloaded'});
    await expect(heading).toBeVisible();
    await page.screenshot({path: resolve(output, 'home.png'), fullPage: true});

    await page.getByRole('button', {name: /^动效风格：/}).click();
    const styles = page.getByRole('dialog', {name: '选择动效风格'});
    await expect(styles.getByText('演示仅供风格参考。AI 会结合你的口播文案和人物画面重新设计动效，人物仍由参考图生成。', {exact: true})).toBeVisible();
    await expect(styles.getByRole('complementary', {name: '风格预览'})).toHaveCount(0);
    await page.screenshot({path: resolve(output, 'content-motion-styles.png'), fullPage: true});
    await styles.getByRole('button', {name: '取消', exact: true}).click();

    for (const path of ['/avatar-mvp', '/avatar-mvp?task_id=old-task&legacy=1']) {
      await page.goto(base + path, {waitUntil: 'domcontentloaded'});
      await expect(page).toHaveURL(base + '/');
      await expect(heading).toBeVisible();
      await expect(page.getByText('从内容到完整视频', {exact: true})).toHaveCount(0);
    }

    const pendingProjects = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/projects/' && r.status() === 200);
    await page.goto(base + '/projects', {waitUntil: 'domcontentloaded'});
    const projectResponse = await (await pendingProjects).json();
    const payload = projectResponse.data ?? projectResponse;
    const projects = Array.isArray(payload) ? payload : payload.projects ?? payload.items ?? [];
    const avatarProjects = projects.filter(p => p.metadata?.inference_mode === 'avatar' || p.workflow?.mode === 'avatar');
    const project = process.env.AVATAR_VERIFY_PROJECT_ID
      ? avatarProjects.find(p => p.id === process.env.AVATAR_VERIFY_PROJECT_ID)
      : avatarProjects.find(p => p.status === 'completed') || avatarProjects[0];
    assert.ok(project, 'No existing avatar project available for read-only workspace verification');
    await expect(page.getByRole('heading', {name:'我的项目'})).toBeVisible();
    const history = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/agent/history' && r.status() === 200);
    const studioResponse = page.waitForResponse(r => new URL(r.url()).pathname === `/api/v1/agent/project/${project.id}` && r.status() === 200);
    await page.goto(`${base}/projects/${encodeURIComponent(project.id)}/avatar`, {waitUntil: 'domcontentloaded'});
    await expect(page).toHaveURL(`${base}/workspace/${project.id}`);
    await expect(page.getByRole('complementary', {name:'Agent 对话'})).toBeVisible();
    await history;
    const studioBody = await (await studioResponse).json();
    const legacyRun = (studioBody.data??studioBody).run;
    let errorNode;
    if (process.env.AVATAR_VERIFY_PROJECT_ID && legacyRun?.status === 'failed') {
      const failed = legacyRun.error_step_id || legacyRun.steps.find(s => s.status === 'failed' && s.error === legacyRun.error)?.id;
      assert.ok(failed, 'No failed step matches the displayed diagnostic');
      errorNode = failed.split('/').slice(0,2).join('/');
      await expect(page.locator('[data-execution-status] strong')).toContainText(errorNode);
      const selector = page.getByRole('combobox', {name:'定位制作步骤'});
      await selector.fill(errorNode);
      await page.getByRole('option').filter({hasText:errorNode}).first().click();
      const card = page.getByTestId('workflow-node-'+errorNode);
      await card.getByRole('button', {name:/查看.*详情/}).click();
      const detail = page.getByRole('complementary', {name:'步骤详情'});
      await expect(detail.getByText('当前视频使用的提示词', {exact:true})).toBeVisible();
      // The parent video and the failing scene can carry the same diagnostic.
      await expect(detail.getByText(legacyRun.error, {exact:true}).first()).toBeVisible();
    }
    const legacyUpgradeEntry = legacyRun && legacyRun.version !== 'avatar-workflow/3.0';
    if(legacyUpgradeEntry) await expect(page.getByRole('button', {name:'使用新版 11 步流程',exact:true})).toBeVisible();
    await page.screenshot({path: resolve(output, 'workspace.png'), fullPage: true});
    await page.getByRole('button', {name:'制作设置',exact:true}).click();
    await expect(page.getByRole('textbox', {name:'全片配音语速',exact:true})).toBeVisible();
    if (legacyRun?.segments?.length) {
      await page.getByText('单段语速微调', {exact:true}).click();
      const segment=legacyRun.segments[0];
      await expect(page.getByRole('textbox', {name:`${segment.id} · ${segment.chapter||'口播段落'}语速`,exact:true})).toBeVisible();
    }
    await page.screenshot({path: resolve(output, 'voice-speed-settings.png'), fullPage: true});
    await page.reload({waitUntil: 'domcontentloaded'});
    await expect(page.getByRole('complementary', {name:'Agent 对话'})).toBeVisible();
    assert.deepEqual(errors, []);
    assert.equal(modelTasksStarted, 0, 'Read-only verification must not submit model work');
    const report = {url: base + '/', login: 'passed', home: 'passed', refresh: 'passed',
      removedForm: 'absent', legacyRedirect: 'passed', projectList: 'passed', workspace: 'passed',
      agentHistory: 'passed', voiceSpeedSettings:'passed', errorNode, legacyUpgradeEntry:legacyUpgradeEntry?'visible':'not-applicable', contentMotionStyleReference: 'passed', pageErrors: errors, modelTasksStarted, screenshots: output};
    writeFileSync(resolve(output, 'verification.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report, null, 2));
  } catch (error) {
    console.error(JSON.stringify({url: page?.url(), pageErrors: errors}));
    throw error;
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
