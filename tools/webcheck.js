// 前端结构自检：HTML 标签配平、锚点有效性、资源引用完整性。
const fs = require('fs');
const path = require('path');

const WEB = 'D:/c/wgcloud-go/internal/server/web';
const TAGS = ['html','head','body','div','section','nav','main','header','footer',
  'table','thead','tbody','tr','th','td','pre','ul','ol','li','p','span','code',
  'select','option','button','script','a','strong','h1','h2','h3','h4','form','label','input'];

function count(src, re) {
  const m = src.match(re);
  return m ? m.length : 0;
}

console.log('=== HTML 标签配平 ===');
for (const f of ['index.html', 'screen.html', 'docs.html']) {
  const s = fs.readFileSync(path.join(WEB, f), 'utf8');
  const bad = [];
  for (const t of TAGS) {
    // 开标签：<tag 后跟空白或 >
    const open = count(s, new RegExp('<' + t + '(?=[\\s/>])', 'gi'));
    const close = count(s, new RegExp('</' + t + '\\s*>', 'gi'));
    // 自闭合标签不计
    if (['input','br','hr','img','meta','link'].includes(t)) continue;
    if (open !== close) bad.push(t + ' ' + open + '/' + close);
  }
  console.log((bad.length ? 'WARN ' : 'OK   ') + f +
    (bad.length ? '  -> ' + bad.join(', ') : '  全部配对'));
}

console.log('');
console.log('=== docs.html 锚点有效性 ===');
{
  const s = fs.readFileSync(path.join(WEB, 'docs.html'), 'utf8');
  const anchors = [...s.matchAll(/href="#([\w-]+)"/g)].map(m => m[1]);
  const ids = new Set([...s.matchAll(/id="([\w-]+)"/g)].map(m => m[1]));
  const dead = [...new Set(anchors)].filter(a => !ids.has(a));
  console.log('  锚点 ' + anchors.length + ' 个，section id ' + ids.size + ' 个');
  console.log('  失效锚点: ' + (dead.length ? dead.join(', ') : '（无）'));
}

console.log('');
console.log('=== 页面引用的静态资源是否存在 ===');
{
  const files = ['index.html', 'screen.html', 'docs.html'];
  const referenced = new Set();
  for (const f of files) {
    const s = fs.readFileSync(path.join(WEB, f), 'utf8');
    for (const m of s.matchAll(/(?:href|src)="(\/assets\/[^"]+)"/g)) {
      referenced.add(m[1].replace('/assets/', ''));
    }
  }
  for (const r of [...referenced].sort()) {
    const p = path.join(WEB, 'assets', r);
    const ok = fs.existsSync(p);
    console.log('  ' + (ok ? 'OK  ' : 'MISS') + ' ' + r +
      (ok ? '  ' + (fs.statSync(p).size / 1024).toFixed(0) + 'KB' : ''));
  }
}

console.log('');
console.log('=== JS 语法 ===');
for (const f of ['app.js', 'screen.js']) {
  const p = path.join(WEB, 'assets', f);
  try { new Function(fs.readFileSync(p, 'utf8')); console.log('  OK  ' + f); }
  catch (e) { console.log('  ERR ' + f + '  ' + e.message); }
}
