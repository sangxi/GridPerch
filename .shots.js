// 只重截用户点名的三张：screen-1 / screen-2 / tasks，全部来自远程平台。
const { chromium } = require('playwright-core');
const fs = require('fs');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REMOTE = 'http://192.168.40.111:9999';
const OUT = 'D:\\c\\gridperch\\docs\\screenshots';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function makeAlias(name) {
  if (/数据库|DB$|SQL|mysql|oracle/i.test(name)) return 'db';
  if (/ERP/i.test(name)) return 'erp';
  if (/OA/i.test(name)) return 'oa';
  if (/FTP|SFTP/i.test(name)) return 'ftp';
  if (/ETL|同步/i.test(name)) return 'etl';
  if (/站点|门户|WEB/i.test(name)) return 'web';
  if (/测试/i.test(name)) return 'test';
  return 'srv';
}

async function buildPairs(page) {
  const names = await page.evaluate(async () => {
    try {
      const r = await fetch('/api/hosts');
      const d = await r.json();
      return Array.isArray(d) ? d.map((h) => h.hostname) : [];
    } catch (e) {
      return [];
    }
  });
  const counters = {};
  // 大屏标题栏中间留给标题的宽度很窄（两侧是状态 pill 和时间/模板控件），
  // 换成「GridPerch 运维监控中心」会被 CSS 省略号切成「GridPerc…」，所以只放品牌名。
  const pairs = [['昆药集团医药商业有限公司', 'GridPerch']];
  for (const n of names) {
    if (!n) continue;
    const p = makeAlias(n);
    counters[p] = (counters[p] || 0) + 1;
    pairs.push([n, p + '-' + String(counters[p]).padStart(2, '0')]);
  }
  return pairs;
}

async function mask(page, pairs) {
  await page.evaluate((rules) => {
    const rs = rules.filter((p) => p[0]).sort((a, b) => b[0].length - a[0].length);
    const fix = (s) => {
      for (const [from, to] of rs) if (s.indexOf(from) >= 0) s = s.split(from).join(to);
      return s.replace(/\b(\d{1,3}\.\d{1,3})\.\d{1,3}\.\d{1,3}\b/g, '$1.*.*');
    };
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, null);
    const nodes = [];
    while (w.nextNode()) nodes.push(w.currentNode);
    for (const n of nodes) {
      const v = n.nodeValue;
      if (!v || !v.trim()) continue;
      const nv = fix(v);
      if (nv !== v) n.nodeValue = nv;
    }
    for (const el of document.querySelectorAll('[title],[placeholder],[alt]')) {
      for (const a of ['title', 'placeholder', 'alt']) {
        const v = el.getAttribute(a);
        if (v) {
          const nv = fix(v);
          if (nv !== v) el.setAttribute(a, nv);
        }
      }
    }
  }, pairs);
}

async function stageInfo(page) {
  return page.evaluate(() => {
    const s = document.getElementById('stage');
    return {
      stage: s ? s.innerHTML.length : -1,
      canvas: document.querySelectorAll('canvas').length,
      text: (document.body.innerText || '').length,
      net: (document.getElementById('netState') || {}).textContent || '',
      name: (document.getElementById('layoutName') || {}).textContent || '',
    };
  });
}

(async () => {
  const browser = await chromium.launch({ executablePath: CHROME, headless: true });
  const ctx = await browser.newContext({
    viewport: { width: 1280, height: 720 },
    deviceScaleFactor: 1,
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
  });
  const page = await ctx.newPage();

  await page.goto(REMOTE, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1500);
  if (await page.$('#loginForm')) {
    await page.fill('#username', 'admin');
    await page.fill('#password', 'admin');
    await page.click('#loginForm button[type=submit]');
    await page.waitForTimeout(3000);
  }
  const pairs = await buildPairs(page);
  console.log('别名', pairs.length - 1);

  for (const [n, file] of [[1, 'screen-1.png'], [7, 'screen-2.png']]) {
    await page.goto(REMOTE + '/screen/' + n, { waitUntil: 'load' });
    await page.waitForTimeout(4000);
    // 等 stage 真的写进内容，最多 20 秒
    let ok = false;
    for (let i = 0; i < 40; i++) {
      const s = await stageInfo(page);
      if (s.stage > 500 && s.canvas > 0) {
        ok = true;
        break;
      }
      await sleep(500);
    }
    console.log('tpl' + n, ok ? 'ready' : 'TIMEOUT', JSON.stringify(await stageInfo(page)));
    await sleep(1500); // 让 ECharts 动画跑完
    await mask(page, pairs);
    await sleep(500);
    await page.screenshot({ path: path.join(OUT, file) });
    console.log('  ok', file);
  }

  // ---- 监控项管理：用远程（用户要求），远程没建监控项就是空态 ----
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(REMOTE + '/#/tasks', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(5000);
  await mask(page, pairs);
  await sleep(400);
  await page.screenshot({ path: path.join(OUT, 'tasks.png') });
  console.log('  ok tasks.png');

  await browser.close();
})();
