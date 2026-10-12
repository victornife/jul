// Browser checks reuse the Console's locked Playwright dependency in CI.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const site = path.join(root, 'dist/site');
const preview = path.join(root, 'dist/site-preview');
const { chromium } = createRequire(path.join(root, 'internal/admin/ui/package.json'))('@playwright/test');
const types = { '.html': 'text/html; charset=utf-8', '.css': 'text/css', '.png': 'image/png' };

async function main() {
  fs.mkdirSync(preview, { recursive: true });
  const server = http.createServer((request, response) => {
    try {
      const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
      if (!pathname.startsWith('/jul/')) { response.writeHead(404).end(); return; }
      let file = path.resolve(site, pathname.slice('/jul/'.length));
      if (file !== site && !file.startsWith(site + path.sep)) { response.writeHead(404).end(); return; }
      if (fs.existsSync(file) && fs.statSync(file).isDirectory()) file = path.join(file, 'index.html');
      if (!fs.existsSync(file)) { response.writeHead(404).end(); return; }
      response.writeHead(200, { 'Content-Type': types[path.extname(file)] || 'application/octet-stream' });
      response.end(fs.readFileSync(file));
    } catch { response.writeHead(400).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  const reports = [];
  try {
    browser = await chromium.launch({ headless: true });
    for (const [device, viewport] of [['desktop', { width: 1440, height: 1000 }], ['mobile', { width: 390, height: 844 }]]) {
      const context = await browser.newContext({ viewport });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
      for (const route of ['', 'vision/', 'guide/', 'get-started/', 'docs/']) {
        const response = await page.goto(`http://127.0.0.1:${server.address().port}/jul/${route}`, { waitUntil: 'networkidle' });
        assert.equal(response.status(), 200, `${device} ${route}: page must load`);
        const rendered = await page.evaluate(() => ({
          title: document.title,
          width: innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
          headings: document.querySelectorAll('h1').length,
          tables: document.querySelectorAll('table').length,
          loadedImages: Array.from(document.images).every(image => image.complete && image.naturalWidth > 0),
        }));
        assert.equal(rendered.headings, 1, `${device} ${route}: one page title`);
        assert.equal(rendered.loadedImages, true, `${device} ${route}: images must load`);
        assert.ok(rendered.scrollWidth <= rendered.width + 1, `${device} ${route}: horizontal page overflow`);
        reports.push({ device, route: route || '/', ...rendered });
        await page.screenshot({ path: path.join(preview, `${device}-${route.replace('/', '') || 'home'}.png`) });
      }
      await page.goto(`http://127.0.0.1:${server.address().port}/jul/`);
      await page.keyboard.press('Tab');
      assert.equal(await page.locator(':focus').textContent(), 'Skip to content');
      await page.keyboard.press('Enter');
      assert.equal(await page.locator(':focus').getAttribute('id'), 'content');
      await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'Get started', exact: true }).click();
      assert.ok(page.url().endsWith('/jul/get-started/index.html'), 'Get started must navigate within the project site');
      assert.deepEqual(errors, [], `${device}: unexpected browser errors`);
      await context.close();
    }
    fs.writeFileSync(path.join(preview, 'checks.json'), JSON.stringify(reports, null, 2) + '\n');
    console.log(`PASS: ${reports.length} desktop/mobile page checks, keyboard access and project-subpath navigation.`);
  } finally {
    if (browser) await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
