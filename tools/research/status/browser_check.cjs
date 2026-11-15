'use strict';
// 使用已安装浏览器检查本地离线文档，不启动服务、不访问市场或改浏览器常用配置。
const { chromium } = require('playwright');
const fs = require('node:fs/promises');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const assert = require('node:assert/strict');

async function main() {
  const [document, output] = process.argv.slice(2);
  if (!document || !output) throw new Error('用法：node browser_check.cjs <data-status.html> <全新输出目录>');
  await fs.mkdir(output);
  const data = JSON.parse(await fs.readFile(path.join(path.dirname(document), 'data-status.json'), 'utf8'));
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.STATUS_BROWSER_EXECUTABLE || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe'
  });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1080 } });
  const errors = [], remote = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => { if (/^https?:/.test(request.url())) remote.push(request.url()); });
  try {
    await page.goto(pathToFileURL(path.resolve(document)).href);
    assert.equal(await page.locator('#products tr').count(), data.summary.products);
    await page.screenshot({ path: path.join(output, 'desktop.png') });
    await page.selectOption('#exchange', 'DCE');
    assert.equal(await page.locator('#products tr').count(), data.products.filter(p => p.exchange === 'DCE').length);
    const soybean = page.locator('[data-product="DCE.m"]');
    assert.match(await soybean.locator('td').nth(1).innerText(), /^0 \/ /);
    assert.match(await soybean.locator('td').nth(3).innerText(), /主目录未对照/);
    assert.match(await soybean.locator('td').nth(5).innerText(), /份有限样本/);
    await page.click('#reset');
    await page.fill('#search', '螺纹钢');
    assert.equal(await page.locator('#products tr').count(), 1);
    assert.equal(await page.locator('#products tr').getAttribute('data-product'), 'SHFE.rb');
    await page.locator('.product-title').click();
    assert.equal(await page.locator('#detail').isVisible(), true);
    assert.match(await page.locator('#detail-body').innerText(), /主EDB无此列/);
    assert.match(await page.locator('#detail-body').innerText(), /官方历史合约全集/);
    await page.click('#close-detail');
    await page.click('#reset');
    await page.selectOption('#state', 'difference');
    assert.equal(await page.locator('#products tr').count(), data.products.filter(p => p.distinct_fields > 0).length);
    await page.click('#reset');
    await page.selectOption('#exchange', 'CFFEX');
    assert.equal(await page.locator('#products tr').count(), 8);
    for (const row of await page.locator('#products tr').all()) {
      assert.match(await row.innerText(), /明文参考/);
      assert.match(await row.innerText(), /本目录仅研究用途/);
    }
    await page.fill('#search', 'NOT_A_REAL_PRODUCT');
    assert.equal(await page.locator('#products tr').count(), 0);
    assert.equal(await page.locator('#empty').isVisible(), true);
    await page.click('#reset');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.selectOption('#exchange', 'DCE');
    await page.locator('.workspace').evaluate(node => node.scrollIntoView({block:'start'}));
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), true);
    await page.screenshot({ path: path.join(output, 'mobile.png') });
    assert.deepEqual(errors, []);
    assert.deepEqual(remote, []);
    await fs.writeFile(path.join(output, 'browser-review.json'), JSON.stringify({
      status: 'passed', products: data.summary.products, checks: [
        'all-products', 'exchange-filter', 'supplementary-not-no-data', 'chinese-search',
        'detail-boundaries', 'difference-filter', 'cffex-unauthenticated', 'no-match', 'mobile-width'
      ], page_errors: errors, remote_requests: remote, price_or_admission_changed: false
    }, null, 2) + '\n');
    process.stdout.write('离线文档桌面/移动端及九项交互检查通过，无远端请求。\n');
  } finally { await browser.close(); }
}
main().catch(error => { process.stderr.write(String(error.stack || error) + '\n'); process.exitCode = 1; });
