// Manual integration check: NODE_PATH=<Playwright node_modules> node this-file.
// Optional CHROME_EXECUTABLE and WEBUI_SCREENSHOT_DIR configure local tooling.
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {mkdir, writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {createFixtureServer} from './task_owned_visual_host.mjs';
const require = createRequire(import.meta.url), {chromium} = require('playwright');
const output = resolve(process.env.WEBUI_SCREENSHOT_DIR || '/private/tmp/webui-task-owned-responsive');
await mkdir(output, {recursive: true});
const browser = await chromium.launch({headless: true, ...(process.env.CHROME_EXECUTABLE ? {executablePath: process.env.CHROME_EXECUTABLE} : {})});
const results = [];
try {
  for (const width of [320, 560, 850, 1440]) for (const colorScheme of ['light', 'dark']) {
    const {server, metrics} = createFixtureServer();
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const url = `http://127.0.0.1:${server.address().port}`;
    const context = await browser.newContext({viewport: {width, height: 1000}, colorScheme, locale: 'zh-CN'});
    const errors = [], blocked = [];
    await context.route('**/*', route => route.request().url().startsWith(url + '/') ? route.continue() : (blocked.push(route.request().url()), route.abort()));
    await context.addInitScript(() => {
      localStorage.setItem('devbridge.console.navigation', 'projects');
      localStorage.setItem('devbridge.console.projectTab', 'tasks');
      window.fixtureRequestIds = 0;
      const original = crypto.randomUUID.bind(crypto);
      crypto.randomUUID = () => {window.fixtureRequestIds++; return original();};
    });
    const page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    const checkOverflow = async (selector, stage) => {
      await page.locator(selector).evaluate(node => Promise.all(node.getAnimations({subtree: true}).map(animation => animation.finished.catch(() => {}))));
      const size = await page.locator(selector).evaluate(node => {const rect = node.getBoundingClientRect(); return {left: rect.left, right: rect.right, scroll: node.scrollWidth, client: node.clientWidth, viewport: innerWidth};});
      assert.ok(size.left >= -1 && size.right <= width + 1, `${width}/${colorScheme} ${stage} bounds ${JSON.stringify(size)}`);
      assert.ok(size.scroll <= size.client + 1, `${width}/${colorScheme} ${stage} horizontal overflow ${JSON.stringify(size)}`);
    };
    try {
      await page.goto(url);
      await page.locator('#page [data-task-detail="task-1"]').waitFor();
      assert.equal(await page.locator('[data-project-tab="executions"]').count(), 0);
      assert.equal(await page.locator('.primary-nav [data-product-nav]').count(), 6);
      for (const nav of await page.locator('.primary-nav [data-product-nav]').all()) {
        if (await nav.isVisible()) assert.ok(await nav.evaluate(node => [...node.querySelectorAll('span')].some(label => label.textContent.trim() && label.getBoundingClientRect().width > 5 && label.getBoundingClientRect().height > 5)), 'Visible primary navigation must retain its label or icon');
      }
      await checkOverflow('body', 'Task list');
      await page.screenshot({path: `${output}/${width}-${colorScheme}-tasks.png`, fullPage: true});
      for (const taskId of ['task-1', 'task-2', 'task-3']) {
        await page.locator(`#page [data-task-detail="${taskId}"]`).click();
        await page.locator(`.task-dashboard[data-task-id="${taskId}"]`).waitFor();
        assert.equal(await page.locator('[data-review-history][open]').count(), 0);
        assert.equal(await page.locator('[data-execution-history][open]').count(), 0);
        await checkOverflow('#detail', `${taskId} detail`);
        await checkOverflow('#detail-body', `${taskId} detail content`);
        if (taskId === 'task-1') await page.screenshot({path: `${output}/${width}-${colorScheme}-task-detail.png`, fullPage: true});
        if (taskId === 'task-3') assert.equal(await page.locator('#detail [data-task-action="APPROVE"]').count(), 1);
        if (taskId === 'task-2') assert.equal(await page.locator('#detail [data-task-action="ACCEPT"]').count(), 1);
        await page.locator('#detail-close').click();
      }
      await page.locator('[data-project-tab="featureAreas"]').first().click();
      await checkOverflow('body', 'Feature list');
      await page.locator('#page [data-topic-detail="area"]').first().click();
      await page.locator('#detail .feature-menu > summary').click();
      const retire = page.locator('#detail [data-close-feature-area="area"]');
      await retire.click();
      await page.locator('#retire-dialog[open]').waitFor();
      assert.equal(metrics.mutations.length, 0);
      assert.equal(await page.evaluate(() => window.fixtureRequestIds), 0);
      await checkOverflow('#detail', 'Feature detail');
      await checkOverflow('#detail-body', 'Feature detail content');
      await checkOverflow('#retire-dialog', 'Retire dialog');
      await page.screenshot({path: `${output}/${width}-${colorScheme}-retire.png`, fullPage: true});
      for (let index = 0; index < 8; index++) {
        await page.keyboard.press('Tab');
        assert.equal(await page.evaluate(() => !!document.activeElement.closest('#retire-dialog')), true, 'Tab must remain in confirmation dialog');
      }
      await page.keyboard.press('Shift+Tab');
      assert.equal(await page.evaluate(() => !!document.activeElement.closest('#retire-dialog')), true, 'Shift+Tab must remain in confirmation dialog');
      await page.keyboard.press('Escape');
      assert.equal(await page.locator('#retire-dialog[open]').count(), 0);
      assert.equal(await retire.evaluate(node => document.activeElement === node), true, 'Escape returns focus to trigger');
      assert.equal(await page.locator('#detail-layer.hidden').count(), 0, 'Escape leaves Feature detail open');
      assert.equal(metrics.mutations.length, 0);
      await retire.click();
      await page.locator('#retire-cancel').click();
      assert.equal(metrics.mutations.length, 0);
      assert.equal(await page.evaluate(() => window.fixtureRequestIds), 0);
      await retire.click();
      await page.locator('#retire-confirm').click();
      await page.locator('#retire-dialog').waitFor({state: 'hidden'});
      assert.equal(metrics.mutations.length, 1);
      assert.equal(await page.evaluate(() => window.fixtureRequestIds), 1);
      assert.equal(await page.locator('#detail-layer.hidden').count(), 0, 'Successful retirement preserves Feature detail');
      assert.equal(await page.locator('#detail [data-close-feature-area]').count(), 0);
      assert.equal(await page.locator('#detail [data-task-detail="task-1"]').count() > 0, true);
      assert.deepEqual(errors, []);
      assert.deepEqual(blocked, []);
      results.push({width, colorScheme, status: 'PASS', taskDetails: 3, retireMutations: metrics.mutations.length});
      console.log(`PASS ${width} ${colorScheme}: Task list/details, menu, dialog focus/Escape/cancel/confirm`);
    } catch (error) {console.error('Browser errors:', errors);console.error('Visible body:', (await page.locator('body').innerText()).slice(0,4000));await page.screenshot({path: `${output}/${width}-${colorScheme}-failure.png`, fullPage: true});throw error;} finally {await context.close(); await new Promise(resolve => server.close(resolve));}
  }
} finally {await browser.close(); await writeFile(`${output}/results.json`, JSON.stringify(results, null, 2));}
