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
const types = { '.html': 'text/html; charset=utf-8', '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml', '.webp': 'image/webp' };

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
      for (const route of ['', 'vision/', 'examples/', 'guide/', 'get-started/', 'docs/']) {
        const response = await page.goto(`http://127.0.0.1:${server.address().port}/jul/${route}`, { waitUntil: 'networkidle' });
        assert.equal(response.status(), 200, `${device} ${route}: page must load`);
        const rendered = await page.evaluate(() => ({
          title: document.title,
          width: innerWidth,
          scrollWidth: document.documentElement.scrollWidth,
          headings: document.querySelectorAll('h1').length,
          tables: document.querySelectorAll('table').length,
          loadedImages: Array.from(document.images).every(image => image.complete && image.naturalWidth > 0),
          navigation: Array.from(document.querySelectorAll('nav[aria-label="Main navigation"] a'), link => link.textContent),
        }));
        assert.equal(rendered.headings, 1, `${device} ${route}: one page title`);
        assert.equal(rendered.loadedImages, true, `${device} ${route}: images must load`);
        assert.deepEqual(rendered.navigation, ['Our vision', 'Examples', 'Practical guide', 'Documentation', 'Get started', 'GitHub ↗'], `${device} ${route}: preserve every existing menu link`);
        assert.ok(rendered.scrollWidth <= rendered.width + 1, `${device} ${route}: horizontal page overflow`);
        reports.push({ device, route: route || '/', ...rendered });
        if (route === 'examples/') {
          const examples = await page.evaluate(() => ({
            cards: Array.from(document.querySelectorAll('.scenario'), card => card.querySelectorAll('img').length),
            diagrams: Array.from(document.querySelectorAll('.diagram-picture'), picture => {
              const bounds = picture.getBoundingClientRect();
              return { offset: Math.abs(bounds.x + bounds.width / 2 - innerWidth / 2), source: picture.querySelector('img').currentSrc };
            }),
          }));
          assert.deepEqual(examples.cards, [1, 1, 1, 1, 1], 'every example card has its approved picture');
          assert.equal(examples.diagrams.length, 2, 'both explanatory diagrams render');
          for (const diagram of examples.diagrams) {
            assert.ok(diagram.offset <= 1, 'diagram is centered');
            assert.equal(diagram.source.includes('-mobile.svg'), device === 'mobile', 'diagram matches the device');
          }
          await page.locator('.scenario-grid').screenshot({ path: path.join(preview, `${device}-examples-cards.png`) });
        }
        await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }));
        await page.screenshot({ path: path.join(preview, `${device}-${route.replace('/', '') || 'home'}.png`) });
      }
      await page.goto(`http://127.0.0.1:${server.address().port}/jul/`);
      await page.keyboard.press('Tab');
      assert.equal(await page.locator(':focus').textContent(), 'Skip to content');
      await page.keyboard.press('Enter');
      assert.equal(await page.locator(':focus').getAttribute('id'), 'content');
      await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'Get started', exact: true }).click();
      assert.ok(page.url().endsWith('/jul/get-started/index.html'), 'Get started must navigate within the project site');
      await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'Examples', exact: true }).click();
      assert.ok(page.url().endsWith('/jul/examples/index.html'), 'Examples must navigate within the project site');
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
