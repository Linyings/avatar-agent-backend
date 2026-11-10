// Read-only check against the deployed catalog and real browser media decoding.
const assert = require('node:assert/strict');
const {resolve} = require('node:path');
const {expect} = require('../../frontend/node_modules/@playwright/test');

module.exports = async function verifyExplainerStyles({page, base, output}) {
  const catalog = await page.evaluate(async url => {
    const response = await fetch(url, {headers: {Authorization: `Bearer ${localStorage.getItem('auth_access_token')}`}});
    if (!response.ok) throw new Error(`Catalog status ${response.status}`);
    return response.json();
  }, base + '/api/v1/avatar-mvp/catalog');
  const payload = catalog.data ?? catalog;
  const choices = payload.styles.filter(s => s.published && !s.legacy && !s.archived);
  assert.equal(choices.length, 6, 'Expected exactly six current styles');
  assert.ok(choices.every(s => s.id.startsWith('explain-') && s.card_preview && s.preview));
  assert.ok(payload.styles.filter(s => s.id.startsWith('mg-')).every(s => s.archived));
  const dialog = page.getByRole('dialog', {name: '选择动效风格'});
  await expect(dialog.getByRole('button', {name: /^预览/})).toHaveCount(6);
  const decoded = [];
  for (const style of choices) {
    const card = dialog.getByRole('button', {name: '预览' + style.name});
    await card.scrollIntoViewIfNeeded();
    await expect.poll(() => card.locator('img').evaluate(img => img.naturalWidth), {timeout: 30000}).toBeGreaterThan(0);
    const video = card.locator('video');
    await expect.poll(() => video.evaluate(v => v.readyState), {timeout: 30000}).toBeGreaterThanOrEqual(2);
    const info = await video.evaluate(async v => {
      await v.play();
      const start = v.currentTime;
      await new Promise(resolve => setTimeout(resolve, 300));
      return {width: v.videoWidth, height: v.videoHeight, duration: v.duration, advanced: v.currentTime !== start};
    });
    assert.ok(info.width > 0 && info.advanced && info.duration <= 12.1, `Invalid preview ${style.id}`);
    decoded.push({id: style.id, ...info});
    await card.click();
    await expect(dialog.getByText(style.description, {exact: true})).toBeVisible();
  }
  const selected = choices[5];
  await dialog.getByRole('button', {name: '放大预览'}).click();
  const full = page.getByRole('dialog', {name: selected.name});
  await expect.poll(() => full.locator('video').evaluate(v => v.readyState), {timeout: 30000}).toBeGreaterThanOrEqual(2);
  await page.keyboard.press('Escape');
  await expect(full).toHaveCount(0);
  await expect(dialog).toBeVisible();
  await page.screenshot({path: resolve(output, 'six-styles-desktop.png'), fullPage: true});
  await dialog.getByRole('button', {name: '使用此风格'}).click();
  await expect(page.getByRole('button', {name: '动效风格：' + selected.name})).toBeVisible();
  await page.setViewportSize({width: 390, height: 844});
  await page.getByRole('button', {name: /^动效风格：/}).click();
  await expect(dialog.getByRole('button', {name: /^预览/})).toHaveCount(6);
  await page.screenshot({path: resolve(output, 'six-styles-mobile.png'), fullPage: true});
  await dialog.getByRole('button', {name: '取消', exact: true}).click();
  await page.setViewportSize({width: 1440, height: 1000});
  return {count: choices.length, decoded, selection: 'passed', detail: 'passed', mobile: 'passed'};
};
