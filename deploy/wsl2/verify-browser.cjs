// Runs against the real WSL HTTP service; no API mocks or stored credentials.
const {execFileSync} = require('node:child_process');
const {mkdirSync} = require('node:fs');
const {resolve} = require('node:path');
const assert = require('node:assert/strict');
const {chromium} = require('../../frontend/node_modules/@playwright/test');

(async () => {
  // Match verify.py/live-test.py: a credential file may contain appended accounts.
  // Always use the first account so task ownership is consistent across checks.
  const access = {};
  for (const line of execFileSync('wsl.exe', ['-d', 'Ubuntu-24.04', '-u', 'root', '--',
    'cat', '/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt'], {encoding: 'utf8'}).trim().split(/\r?\n/)) {
    const i = line.indexOf(': ');
    if (i < 0) continue;
    const key = line.slice(0, i);
    if (!(key in access)) access[key] = line.slice(i + 2);
  }
  const ip = execFileSync('wsl.exe', ['-d', 'Ubuntu-24.04', '-u', 'root', '--', 'hostname', '-I'],
    {encoding: 'utf8'}).trim().split(/\s+/)[0];
  const base = `http://${ip}:8080`;
  const taskId = process.env.AVATAR_TEST_TASK_ID;
  const output = resolve(__dirname, '../../backend/data/wsl-deployment');
  mkdirSync(output, {recursive: true});
  const browser = await chromium.launch({headless: true,
    executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe'});
  let page;
  const errors = [];
  let taskVerification;
  try {
    const context = await browser.newContext({viewport: {width: 1440, height: 1000}});
    page = await context.newPage();
    page.on('pageerror', err => errors.push(err.message));
    await page.goto(`${base}/avatar-mvp`);
    await page.locator('input[name="email"]').fill(access.Email);
    await page.locator('input[name="password"]').fill(access.Password);
    const configuration = page.waitForResponse(response => response.url().endsWith('/avatar-mvp/config') && response.status() === 200);
    await page.locator('button[type="submit"]').click();
    await page.waitForURL('**/avatar-mvp');
    await page.getByRole('heading', {name: '让你的表达，成为视频'}).waitFor();
    const config = await (await configuration).json();
    if (!config.configured) await page.getByText(config.message, {exact: true}).waitFor();
    await page.getByText(`视频模型：${config.provider} / ${config.model}`, {exact: true}).waitFor();
    if (config.voice) await page.getByText(`${config.voice} 固定音色`, {exact: true}).waitFor();
    if (taskId) {
      const taskPath = `/avatar-mvp/tasks/${encodeURIComponent(taskId)}`;
      const pendingTask = page.waitForResponse(r => r.url().endsWith(taskPath) && r.status() === 200);
      await page.goto(`${base}/avatar-mvp?task_id=${encodeURIComponent(taskId)}`);
      const task = await (await pendingTask).json();
      assert.equal(task.id, taskId);
      assert.equal(task.shots.length, 3);
      if (task.error) await page.getByText(task.error, {exact: true}).first().waitFor();
      if (['failed', 'interrupted'].includes(task.status)) {
        await page.getByRole('button', {name: '重试失败步骤'}).waitFor();
      }
      if (task.status === 'succeeded') {
        const video = page.getByLabel('数字人口播成片');
        await video.waitFor();
        await page.waitForFunction(() => document.querySelector('video')?.readyState >= 1);
        const downloadPending = page.waitForEvent('download');
        await page.getByRole('link', {name: '下载 MP4'}).click();
        const download = await downloadPending;
        await download.saveAs(resolve(output, 'verified-download.mp4'));
        assert.equal(await download.failure(), null);
      }
      const restoredTask = page.waitForResponse(r => r.url().endsWith(taskPath) && r.status() === 200);
      await page.reload();
      assert.equal((await (await restoredTask).json()).id, taskId);
      assert.equal(new URL(page.url()).searchParams.get('task_id'), taskId);
      taskVerification = {id: taskId, status: task.status, stage: task.stage,
        refreshRestored: true, retryClicked: false, videoDownloaded: task.status === 'succeeded'};
    }
    assert.equal(await page.evaluate(() => isSecureContext), false, 'Must test plain HTTP IP origin');
    await page.screenshot({path: resolve(output, 'desktop.png'), fullPage: true});
    await page.reload();
    await page.getByRole('heading', {name: '让你的表达，成为视频'}).waitFor();
    await page.setViewportSize({width: 390, height: 844});
    await page.waitForFunction(() => document.querySelector('nav').getBoundingClientRect().right <=
      document.querySelector('h1').getBoundingClientRect().left, null, {timeout: 3000});
    await page.screenshot({path: resolve(output, 'mobile.png'), fullPage: true});
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({url: `${base}/avatar-mvp`, login: 'passed', refresh: 'passed',
      httpIP: 'passed', pageErrors: errors, task: taskVerification, screenshots: output}, null, 2));
  } catch (err) {
    if (page) {
      await page.screenshot({path: resolve(output, 'failure.png'), fullPage: true});
      console.error(JSON.stringify({url: page.url(), pageErrors: errors,
        visibleText: (await page.locator('body').innerText()).slice(0, 2400)}, null, 2));
    }
    throw err;
  } finally {
    await browser.close();
  }
})().catch(err => {console.error(err.message); process.exitCode = 1;});
