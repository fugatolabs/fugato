const assert = require('node:assert/strict');
const { chromium } = require('playwright');

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const base = process.env.BASE_URL || 'http://localhost:3000';
  const title = `Browser test ${Date.now()}`;
  try {
    await page.goto(base);
    await assert.doesNotReject(() => page.getByRole('heading', { name: 'Things to do' }).waitFor());
    await page.getByLabel('New task').fill(title);
    await page.getByRole('button', { name: 'Add task' }).click();
    await page.getByText(title).waitFor();
    await page.reload();
    await page.getByText(title).waitFor();
    await page.getByRole('checkbox', { name: `Complete ${title}` }).check();
    await page.getByText(title).locator('..').locator('.completed').waitFor();
    await page.getByRole('button', { name: `Delete ${title}` }).click();
    await page.getByText(title).waitFor({ state: 'detached' });
    assert.deepEqual(errors, []);
    console.log('Browser create, reload, complete, and delete: PASS');
  } finally {
    await context.close();
    await browser.close();
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
