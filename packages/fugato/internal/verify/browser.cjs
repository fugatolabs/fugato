// Run this worker with `node -e <contents>` inside the selected runtime's
// browser service. The adapter supplies FUGATO_VERIFY_URL and FUGATO_TIMEOUT_MS.
// stdout contains one JSON object; diagnostics belong on stderr.
const { chromium } = require('playwright');

async function main() {
  const url = process.env.FUGATO_VERIFY_URL;
  const timeout = Number(process.env.FUGATO_TIMEOUT_MS || 30000);
  if (!url || !Number.isInteger(timeout) || timeout < 1 || timeout > 60000) {
    throw new Error('invalid browser verification input');
  }
  const issues = [];
  let status = 0;
  let screenshot = '';
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext();
    try {
      const page = await context.newPage();
      page.on('pageerror', error => issues.push({ kind: 'page', text: error.message }));
      page.on('requestfailed', request => issues.push({
        kind: 'request', url: request.url(), text: request.failure()?.errorText || 'request failed'
      }));
      page.on('response', response => {
        if (response.status() >= 400) {
          issues.push({ kind: 'http', url: response.url(), text: `HTTP ${response.status()}` });
        }
      });
      try {
        const response = await page.goto(url, { waitUntil: 'load', timeout });
        status = response?.status() || 0;
        await page.waitForTimeout(250);
      } catch (error) {
        issues.push({ kind: 'navigation', url, text: error.message });
      }
      try {
        screenshot = (await page.screenshot({ type: 'png', fullPage: true, timeout: 5000 })).toString('base64');
      } catch (error) {
        issues.push({ kind: 'screenshot', url, text: error.message });
      }
    } finally {
      await context.close();
    }
  } finally {
    await browser.close();
  }
  process.stdout.write(JSON.stringify({ status, issues, screenshot }) + '\n');
}

main().catch(error => {
  process.stdout.write(JSON.stringify({ status: 0, issues: [{ kind: 'browser', text: error.message }], screenshot: '' }) + '\n');
  process.exitCode = 1;
});
