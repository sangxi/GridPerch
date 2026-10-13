/* OpsMonitor 控制台：全部功能页面的渲染与交互，零第三方依赖。 */
'use strict';

const COLORS = ['#2563eb', '#16a34a', '#f59e0b', '#dc2626', '#7c3aed'];
const REFRESH_SEC = 30;
// 是否在控制台展示「授权系统地址」及跳转授权站的链接。
// 授权站遭受 DDoS 期间对外不暴露其域名，故默认关闭；
// 需要排障时把这里改成 true 重新构建即可（其余功能不受影响）。
const SHOW_HUB_URL = false;

/* ---------------- 导航结构（左侧模块栏） ---------------- */
const NAV_GROUPS = [
  { label: '监控', items: [['dashboard', '仪表盘'], ['hosts', '主机'], ['catalogs', '主机目录'], ['tasks', '监控项'], ['alerts', '告警']] },
  { label: '运维', items: [['tools', '工具箱'], ['commands', '指令下发'], ['assets', '资产台账'], ['devices', '设备账号'], ['inspections', '巡检报告']] },
  { label: '系统', items: [['releases', '在线升级'], ['license', '授权购买'], ['members', '成员权限'], ['tokens', '开放接口'], ['audit', '审计日志'], ['settings', '系统设置']] },
];
const ROUTE_TITLES = {
  dashboard: '仪表盘', hosts: '主机监控', catalogs: '主机目录', onboard: '添加主机', tasks: '监控项管理', alerts: '告警中心',
  tools: '工具箱', commands: '指令下发', assets: '资产台账', devices: '设备账号', inspections: '巡检报告',
  releases: '采集端版本发布', license: '授权管理',
  members: '成员账号与权限', tokens: '数据开放接口', audit: '审计日志', settings: '系统设置',
};

/* ---------------- 全局状态 ---------------- */
const state = {
  user: null,
  menus: [],
  route: 'dashboard',
  timer: null,
  lastAlertCount: -1,
  public: {},
  cache: {},
};

/* ---------------- 基础工具 ---------------- */
const $ = (sel) => document.querySelector(sel);
const app = () => $('#app');
const esc = (s) => String(s === undefined || s === null ? '' : s)
  .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

function toast(msg, bad) {
  let el = $('.toast');
  if (!el) {
    el = document.createElement('div');
    el.className = 'toast';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.className = 'toast show' + (bad ? ' bad' : '');
  clearTimeout(el._t);
  el._t = setTimeout(() => { el.className = 'toast' + (bad ? ' bad' : ''); }, 2600);
}

async function request(url, options) {
  const res = await fetch(url, Object.assign({ credentials: 'same-origin' }, options || {}));
  const text = await res.text();
  // 先解析后端统一返回体：401 也可能带 msg（如「用户名或密码错误」），
  // 此前这里把 401 一律改写成「未登录」，登录失败的具体原因就被丢掉了。
  let parsed;
  let parsedOK = false;
  try { parsed = JSON.parse(text); parsedOK = true; } catch (e) { /* 非 JSON 响应 */ }
  const serverMsg = parsedOK && parsed && parsed.msg ? parsed.msg : '';
  if (res.status === 401) {
    // 登录接口自身的 401 属于「账号或密码错误」这类正常校验失败：
    // 页面本来就在登录页，没必要再弹一次登录框，更不该吞掉后端返回的原因，
    // 否则用户点「登录」后看不到任何反馈 —— 表现为「登录没反应」。
    if (url.indexOf('/api/login') === 0) throw new Error(serverMsg || '用户名或密码错误');
    showLogin(true);
    throw new Error('未登录');
  }
  if (!res.ok) throw new Error(serverMsg || `HTTP ${res.status}`);
  return parsedOK ? parsed : text;
}
const get = (u) => request(u);
// asList 保证列表类接口拿到的永远是数组：
// 后端空结果集若返回 null（历史行为 / 异常兜底），这里统一兜成 []，
// 避免 .map/.find 抛 "Cannot read properties of null"。
const asList = (v) => (Array.isArray(v) ? v : []);
const getList = (u) => request(u).then(asList);
function post(u, body) {
  return request(u, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
}
function put(u, body) {
  return request(u, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
}
const del = (u) => request(u, { method: 'DELETE' });

function fmtNum(v, digits) {
  const n = Number(v);
  if (!isFinite(n)) return '-';
  return n.toFixed(digits === undefined ? 1 : digits);
}

/* ---------------- SVG 折线图 ---------------- */
function lineChart(series, opts) {
  opts = opts || {};
  const width = opts.width || 640;
  const height = opts.height || 190;
  const pad = { l: 42, r: 12, t: 12, b: 26 };
  const all = [];
  series.forEach((s) => s.points.forEach((p) => all.push(Number(p.value))));
  if (all.length === 0) return '<div class="empty">暂无数据</div>';
  let max = Math.max.apply(null, all);
  let min = Math.min.apply(null, all);
  if (opts.pct) { min = 0; max = 100; }
  else if (max === min) { max = max + 1; min = Math.max(0, min - 1); }
  const innerW = width - pad.l - pad.r;
  const innerH = height - pad.t - pad.b;
  const maxPoints = Math.max.apply(null, series.map((s) => s.points.length).concat([1]));
  const x = (i) => pad.l + (innerW * i) / Math.max(1, maxPoints - 1);
  const y = (v) => pad.t + innerH - ((v - min) / (max - min)) * innerH;

  let svg = `<svg class="chart" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none">`;
  for (let g = 0; g <= 4; g++) {
    const val = min + ((max - min) * g) / 4;
    const yy = y(val);
    svg += `<line x1="${pad.l}" y1="${yy}" x2="${width - pad.r}" y2="${yy}" stroke="#eef1f6"/>`;
    svg += `<text x="6" y="${yy + 4}" font-size="10" fill="#9aa3b2">${fmtNum(val, val < 10 ? 2 : 0)}</text>`;
  }
  series.forEach((s, idx) => {
    const color = COLORS[idx % COLORS.length];
    const pts = s.points.map((p, i) => `${x(i)},${y(Number(p.value))}`).join(' ');
    if (opts.area !== false) {
      svg += `<polygon points="${pad.l},${pad.t + innerH} ${pts} ${x(s.points.length - 1)},${pad.t + innerH}" fill="${color}" opacity="0.08"/>`;
    }
    svg += `<polyline points="${pts}" fill="none" stroke="${color}" stroke-width="2" stroke-linejoin="round"/>`;
  });
  svg += '</svg>';
  const legend = series.map((s, i) =>
    `<span class="chart-legend"><i class="legend-color" style="background:${COLORS[i % COLORS.length]}"></i>${esc(s.name)}${s.unit ? ' (' + esc(s.unit) + ')' : ''}</span>`).join('');
  const tf = opts.trimTime ? (t) => String(t).slice(5, 16) : (t) => String(t);
  const last = series.length && series[0].points.length ? series[0].points[series[0].points.length - 1] : null;
  const times = series.length && series[0].points.length
    ? `<div class="legend-line"><span>${esc(tf(series[0].points[0].time))}</span><span>${esc(tf(last.time))}</span></div>` : '';
  return `<div class="panel-body">${svg}<div class="chart-footer">${legend}${times}</div></div>`;
}

/* ---------------- 仪表盘图表基础件 ---------------- */

// 线性插值的两点式折线——单序列 sparkline，用于卡片内嵌趋势。
function sparkline(points, color, w, h) {
  const vals = (points || []).map((p) => Number(p.value)).filter((v) => isFinite(v));
  if (vals.length < 2) return '';
  const max = Math.max.apply(null, vals), min = Math.min.apply(null, vals);
  const span = max - min || 1;
  const step = w / (vals.length - 1);
  const pts = vals.map((v, i) => `${(i * step).toFixed(1)},${(h - ((v - min) / span) * (h - 2) - 1).toFixed(1)}`).join(' ');
  return `<svg class="spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none"><polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.6" stroke-linejoin="round"/></svg>`;
}

// ringSvg 画环形占比图：segments 为 [{label,value,color}]，中心可写数值与说明。
function ringSvg(segments, opts) {
  opts = opts || {};
  const size = opts.size || 150;
  const stroke = opts.stroke || 15;
  const cx = size / 2, cy = size / 2;
  const r = (size - stroke) / 2;
  const circ = 2 * Math.PI * r;
  const total = segments.reduce((a, s) => a + Math.max(0, Number(s.value) || 0), 0);
  let arcs = '';
  if (total > 0) {
    let off = 0;
    arcs = segments.map((s) => {
      const v = Math.max(0, Number(s.value) || 0);
      if (!v) return '';
      const len = (v / total) * circ;
      const seg = `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="${s.color}" stroke-width="${stroke}"
        stroke-dasharray="${len.toFixed(2)} ${(circ - len).toFixed(2)}" stroke-dashoffset="${(-off).toFixed(2)}"
        transform="rotate(-90 ${cx} ${cy})"/>`;
      off += len;
      return seg;
    }).join('');
  }
  const valueSize = opts.valueSize || 22;
  return `<svg class="ring" viewBox="0 0 ${size} ${size}" width="${size}" height="${size}" role="img">
    <circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="#eef1f7" stroke-width="${stroke}"/>
    ${arcs}
    <text x="${cx}" y="${cy - 1}" text-anchor="middle" font-size="${valueSize}" font-weight="700" fill="#1f2937">${esc(opts.valueText || '')}</text>
    <text x="${cx}" y="${cy + 16}" text-anchor="middle" font-size="11" fill="#6b7280">${esc(opts.labelText || '')}</text>
  </svg>`;
}

// ringCard 把一个环形图与右侧图例组合成一块。
function ringCard(title, segments, centerText, centerLabel, valueSize) {
  const legend = segments.map((s) =>
    `<li><i style="background:${s.color}"></i><span class="lg-label">${esc(s.label)}</span><span class="lg-val">${esc(String(s.value))}</span></li>`).join('');
  return `<div class="ring-cell">
    ${ringSvg(segments, { valueText: centerText, labelText: centerLabel, valueSize: valueSize })}
    <div class="ring-side"><div class="ring-title">${esc(title)}</div><ul class="ring-legend">${legend}</ul></div>
  </div>`;
}

// hbarRow 横向进度条，用于资源占用排行。
function hbarRow(name, display, value, max, color) {
  const pct = max > 0 ? Math.min(100, (Math.max(0, value) / max) * 100) : 0;
  return `<div class="hbar">
    <div class="hbar-head"><span class="hbar-name" title="${esc(name)}">${esc(name)}</span><span class="hbar-val">${esc(display)}</span></div>
    <div class="hbar-track"><i style="width:${pct.toFixed(1)}%;background:${color}"></i></div>
  </div>`;
}

// gaugeTone 依据百分制分数返回配色与文案。
function gaugeTone(score) {
  if (score >= 90) return { color: '#16a34a', text: '运行良好', cls: 'ok' };
  if (score >= 75) return { color: '#2563eb', text: '基本正常', cls: '' };
  if (score >= 60) return { color: '#f59e0b', text: '需要关注', cls: 'warn' };
  return { color: '#dc2626', text: '存在风险', cls: 'danger' };
}

const ICONS = {
  host: '<rect x="2.5" y="3.5" width="19" height="7" rx="2"/><rect x="2.5" y="13.5" width="19" height="7" rx="2"/><path d="M6 7h.01M6 17h.01"/>',
  pulse: '<path d="M2 12h4l2.5-6 3.5 12 2.5-6H22"/>',
  bell: '<path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/>',
  cpu: '<rect x="6.5" y="6.5" width="11" height="11" rx="2"/><path d="M10 2.5v4M14 2.5v4M10 17.5v4M14 17.5v4M2.5 10h4M2.5 14h4M17.5 10h4M17.5 14h4"/>',
  mem: '<rect x="2.5" y="7" width="19" height="10" rx="2"/><path d="M6 11h.01M10 11h.01M6 14h8"/>',
  task: '<path d="M9 11.5l2 2 4-4.5"/><rect x="3.5" y="4.5" width="17" height="15" rx="2.5"/>',
  shield: '<path d="M12 21.5s7.5-3.6 7.5-9.5V5.5L12 2.8 4.5 5.5V12c0 5.9 7.5 9.5 7.5 9.5z"/>',
  box: '<path d="M21 8.4v7.2a2 2 0 0 1-1 1.7l-7 4a2 2 0 0 1-2 0l-7-4a2 2 0 0 1-1-1.7V8.4a2 2 0 0 1 1-1.7l7-4a2 2 0 0 1 2 0l7 4a2 2 0 0 1 1 1.7z"/><path d="M3.3 7.4 12 12.3l8.7-4.9M12 12.3V21"/>',
  key: '<circle cx="8" cy="14" r="4"/><path d="M11 11l8-8M17 5l2 2M15 7l2 2"/>',
  upload: '<path d="M12 16V4M7.5 8.5 12 4l4.5 4.5"/><path d="M4 16v2.5A1.5 1.5 0 0 0 5.5 20h13a1.5 1.5 0 0 0 1.5-1.5V16"/>',
};

// icon 输出统一风格的线性图标。
function icon(name) {
  return `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"
    stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] || ''}</svg>`;
}

// kpiCard 仪表盘顶部指标卡：图标 + 主数值 + 说明 + 可选进度条 / 迷你趋势。
function kpiCard(o) {
  const bar = o.progress === undefined ? '' :
    `<div class="kpi-progress"><i style="width:${Math.min(100, Math.max(0, o.progress)).toFixed(1)}%;background:${o.barColor || 'var(--primary)'}"></i></div>`;
  const spark = o.spark && o.spark.length > 1
    ? `<div class="kpi-spark">${sparkline(o.spark.slice(-60), o.sparkColor || 'var(--primary)', 200, 30)}</div>` : '';
  return `<div class="kpi ${o.tone || ''}">
    <div class="kpi-top"><span class="kpi-icon">${icon(o.icon)}</span><span class="kpi-label">${esc(o.label)}</span></div>
    <div class="kpi-value">${esc(o.value)}${o.unit ? `<span class="kpi-unit">${esc(o.unit)}</span>` : ''}</div>
    <div class="kpi-sub">${o.sub || ''}</div>${bar}${spark}
  </div>`;
}

// deltaTag 显示与上一周期相比的升降，format 决定数值呈现。
function deltaTag(delta, unit, invert) {
  if (!isFinite(delta) || Math.abs(delta) < 0.05) return '<span class="delta flat">持平</span>';
  const up = delta > 0;
  const bad = invert ? !up : up;
  return `<span class="delta ${bad ? 'up' : 'down'}">${up ? '▲' : '▼'} ${fmtNum(Math.abs(delta))}${esc(unit || '')}</span>`;
}

function panel(title, bodyHtml, actions) {
  return `<section class="panel"><div class="panel-head"><h3>${esc(title)}</h3>${actions || ''}</div>${bodyHtml}</section>`;
}

/* ---------------- 登录与初始化 ---------------- */
function showLogin(show) {
  $('#loginMask').style.display = show ? 'flex' : 'none';
}

async function loadPublicSettings() {
  try {
    state.public = await get('/api/settings/public');
  } catch (e) { state.public = {}; }
  const p = state.public || {};
  const name = p.systemName || 'OpsMonitor';
  $('#systemName').textContent = name;
  $('#loginTitle').textContent = name;
  document.title = p.systemName ? p.systemName + ' 运维监控' : 'OpsMonitor 运维监控';
  if (p.logoData) {
    const img = $('#brandLogo');
    img.src = p.logoData;
    img.hidden = false;
    $('#brandLogoDefault').style.display = 'none';
  }
  if (p.hideFooter === 'true') $('#pageFooter').style.display = 'none';
  if (p.footerText) $('#footerText').textContent = p.footerText;
}

async function bootstrap() {
  try {
    const prof = await get('/api/profile');
    state.user = prof;
    state.menus = prof.menus || [];
    state.license = prof.license || {};
    showLogin(false);
    $('#userBadge').textContent = `${prof.username}（${prof.role === 'admin' ? '管理员' : prof.role === 'operator' ? '运维' : '只读'}）`;
    renderVersionBadge();
    renderNav();
    router();
  } catch (e) {
    showLogin(true);
  }
}

function renderNav() {
  const nav = $('#nav');
  let html = '';
  NAV_GROUPS.forEach((g) => {
    const items = g.items.filter((it) => state.menus.indexOf(it[0]) >= 0);
    if (!items.length) return;
    html += `<div class="nav-group"><span class="group-label">${esc(g.label)}</span>`;
    items.forEach((it) => {
      html += `<a href="#/${it[0]}" data-route="${it[0]}">${esc(it[1])}</a>`;
    });
    html += '</div>';
  });
  nav.innerHTML = html;
  markActiveNav();
}

/* 窄屏下侧栏为抽屉，点击菜单后自动收起 */
function toggleSidebar(open) {
  const sb = $('#sidebar');
  const mask = $('#sidebarMask');
  if (!sb) return;
  const on = open === undefined ? !sb.classList.contains('open') : open;
  sb.classList.toggle('open', on);
  if (mask) mask.classList.toggle('show', on);
}

// renderVersionBadge 在侧栏底部显示服务端版本与当前授权档位。
function renderVersionBadge() {
  const el = $('#verBadge');
  if (!el) return;
  const lic = state.license || {};
  const ver = state.user && state.user.serverVersion ? 'v' + state.user.serverVersion : '';
  const edition = lic.edition || '';
  el.className = 'ver-badge' + (lic.valid ? ' ok' : '');
  const tip = lic.valid
    ? (lic.notAfter ? `授权有效期至 ${lic.notAfter}` : '已授权，永久有效')
    : '当前为社区版，主机数与账号数受配额限制；管理员可在「授权管理」导入授权码';
  el.innerHTML = `${esc(ver)}<span class="ver-sep">·</span>${esc(edition)} <span class="ver-tip" title="${esc(tip)}">?</span>`;
}

// 部分页面是主模块的子页（不单列在侧栏），高亮时归到它的父级菜单。
const NAV_ALIAS = { onboard: 'hosts' };

function markActiveNav() {
  const cur = NAV_ALIAS[state.route] || state.route;
  document.querySelectorAll('.nav a').forEach((a) => {
    a.classList.toggle('active', a.dataset.route === cur);
  });
}

/* ---------------- 路由 ---------------- */
function router() {
  const route = (location.hash || '#/dashboard').replace('#/', '') || 'dashboard';
  if (!ROUTE_TITLES[route]) { location.hash = '#/dashboard'; return; }
  const bar = $('#pageTitleBar');
  if (bar) bar.textContent = ROUTE_TITLES[route];
  if (route !== 'dashboard' && state.menus.indexOf(route) < 0) {
    app().innerHTML = panel('无访问权限', '<div class="empty">当前账号未被授权访问该模块，请联系管理员调整菜单权限。</div>');
    markActiveNav();
    return;
  }
  state.route = route;
  markActiveNav();
  if (window.innerWidth <= 900) toggleSidebar(false);
  render();
}

async function render() {
  const renderers = {
    dashboard: renderDashboard, hosts: renderHosts, catalogs: renderCatalogs, onboard: renderOnboard, tasks: renderTasks, alerts: renderAlerts,
    tools: renderTools, commands: renderCommands, assets: renderAssets, devices: renderDevices, inspections: renderInspections,
    releases: renderUpgrade, license: renderLicenseBuy,
    members: renderMembers, tokens: renderTokens, audit: renderAudit, settings: renderSettings,
  };
  const fn = renderers[state.route] || renderDashboard;
  app().innerHTML = '<section class="panel"><div class="panel-body"><div class="empty">加载中…</div></div></section>';
  try {
    await fn();
  } catch (e) {
    app().innerHTML = panel('加载失败', `<div class="empty">${esc(e.message)}</div>`);
  }
  const now = new Date().toLocaleTimeString('zh-CN', { hour12: false });
  $('#refreshTip').textContent = '更新于 ' + now;
}

/* ---------------- 告警声音提醒 ---------------- */
function maybePlayAlert(count) {
  const mode = (state.public || {}).alertSound || 'off';
  if (mode === 'off') return;
  if (state.lastAlertCount >= 0 && count > state.lastAlertCount) {
    try {
      const ctx = new (window.AudioContext || window.webkitAudioContext)();
      const conf = { soft: [660, 0.12, 0.06], normal: [880, 0.25, 0.12], urgent: [1180, 0.5, 0.18] }[mode] || [880, 0.25, 0.12];
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = 'sine';
      osc.frequency.value = conf[0];
      gain.gain.value = conf[2];
      osc.connect(gain).connect(ctx.destination);
      const t = ctx.currentTime;
      osc.start(t);
      gain.gain.exponentialRampToValueAtTime(0.001, t + conf[1]);
      osc.stop(t + conf[1] + 0.02);
    } catch (e) { /* 浏览器未授权音频 */ }
  }
  state.lastAlertCount = count;
}

/* ---------------- 通用表格渲染 ---------------- */
function dataTable(headers, rows, emptyText) {
  if (!rows.length) return `<div class="empty">${esc(emptyText || '暂无数据')}</div>`;
  let html = '<div class="table-scroll"><table class="data"><thead><tr>';
  headers.forEach((h) => { html += `<th>${esc(h)}</th>`; });
  html += '</tr></thead><tbody>';
  rows.forEach((cells) => {
    html += '<tr>';
    cells.forEach((c) => { html += `<td>${c}</td>`; });
    html += '</tr>';
  });
  return html + '</tbody></table></div>';
}

/* ================= 仪表盘 ================= */
async function renderDashboard() {
  const [dash, summary, events, tasks, states, trend] = await Promise.all([
    get('/api/dashboard'), get('/api/monitor/summary'),
    getList('/api/monitor/events?limit=10'), getList('/api/tasks'),
    getList('/api/monitor/states'),
    getList('/api/metrics/overview?minutes=180').catch(() => []),
  ]);
  maybePlayAlert(dash.alertActive);

  const hosts = asList(dash.hosts);
  const alerts = asList(dash.alerts);
  const total = dash.hostTotal || 0;
  const online = dash.hostOnline || 0;
  const offline = dash.hostOffline || 0;
  const alertActive = dash.alertActive || 0;
  const badItems = summary.badItems || 0;
  const items = summary.items || 0;
  const taskTotal = summary.taskTotal || 0;
  const taskBad = summary.taskBad || 0;
  const onlineRate = total > 0 ? (online / total) * 100 : 0;

  // 健康度评分：在线率 50 分 + 告警 30 分 + 监控项异常 20 分。
  // 一台主机都没接入时不给出分数——无数据不能说成「健康」。
  const noHost = total === 0;
  let score = 100;
  score -= total > 0 ? (offline / total) * 50 : 0;
  score -= Math.min(30, alertActive * 5);
  score -= Math.min(20, badItems * 2);
  score = Math.max(0, Math.round(score));
  const tone = noHost ? { color: '#94a3b8', text: '尚未接入主机', cls: '' } : gaugeTone(score);

  // 摘要一句话
  const concerns = [];
  if (offline > 0) concerns.push(`${offline} 台主机离线`);
  if (alertActive > 0) concerns.push(`${alertActive} 条未处理告警`);
  if (badItems > 0) concerns.push(`${badItems} 个监控点异常`);
  const heroTitle = noHost ? '尚未接入任何主机'
    : tone.text + (concerns.length ? '' : '，一切正常');
  const heroSub = noHost
    ? '前往「添加主机」生成采集端接入命令，采集端首次上报后这里会展示主机、告警与监控项的整体运行概览。'
    : (concerns.length ? concerns.join('、') + '，建议尽快处理。' : '所有已接入主机在线，未发现未处理告警或异常监控点。');

  let html = '';

  // ---- 顶部概览横幅 ----
  html += `<section class="hero">
    <div class="hero-glow"></div>
    <div class="hero-left">
      <div class="hero-eyebrow">系统运行概览</div>
      <h2 class="hero-title">${esc(heroTitle)}</h2>
      <p class="hero-sub">${esc(heroSub)}</p>
      <div class="hero-facts">
        <div class="fact"><span class="fact-num">${online}</span><span class="fact-label">在线主机</span></div>
        <div class="fact"><span class="fact-num ${offline > 0 ? 'danger' : ''}">${offline}</span><span class="fact-label">离线主机</span></div>
        <div class="fact"><span class="fact-num ${alertActive > 0 ? 'danger' : ''}">${alertActive}</span><span class="fact-label">活动告警</span></div>
        <div class="fact"><span class="fact-num ${badItems > 0 ? 'warn' : ''}">${badItems}</span><span class="fact-label">异常监控点</span></div>
        <div class="fact"><span class="fact-num">${taskTotal}</span><span class="fact-label">启用监控项</span></div>
      </div>
      ${noHost ? `<a class="btn primary small hero-cta" href="#/onboard">添加第一台主机</a>` : ''}
    </div>
    <div class="hero-ring">
      ${ringSvg([{ label: '健康度', value: noHost ? 0 : score, color: tone.color }],
        { size: 138, stroke: 13, valueText: noHost ? '—' : String(score), labelText: '健康度', valueSize: 34 })}
    </div>
  </section>`;

  // ---- KPI 指标卡 ----
  const cpuSeries = (trend || []).find((s) => /CPU/.test(s.name)) || { points: [] };
  const memSeries = (trend || []).find((s) => /内存/.test(s.name)) || { points: [] };
  const cpuDelta = cpuSeries.points.length > 1
    ? cpuSeries.points[cpuSeries.points.length - 1].value - cpuSeries.points[0].value : NaN;
  html += `<div class="kpi-grid">
    ${kpiCard({
      icon: 'host', label: '接入主机', value: total, unit: ' 台',
      sub: `<span class="tag ok">在线 ${online}</span> <span class="tag ${offline ? 'danger' : 'off'}">离线 ${offline}</span>`,
    })}
    ${kpiCard({
      icon: 'pulse', label: '在线率', value: fmtNum(onlineRate, 1), unit: '%',
      tone: onlineRate >= 99 ? 'ok' : onlineRate >= 90 ? '' : 'danger',
      sub: `共 ${total} 台主机纳入监控`, progress: onlineRate,
      barColor: onlineRate >= 99 ? 'var(--ok)' : onlineRate >= 90 ? 'var(--primary)' : 'var(--danger)',
    })}
    ${kpiCard({
      icon: 'bell', label: '未处理告警', value: alertActive, unit: ' 条',
      tone: alertActive > 0 ? 'danger' : 'ok',
      sub: alertActive > 0 ? `<a class="link" href="#/alerts">前往告警中心处理</a>` : '暂无待处理告警',
    })}
    ${kpiCard({
      icon: 'cpu', label: '平均 CPU', value: fmtNum(dash.avgCpu), unit: '%',
      tone: dash.avgCpu >= 85 ? 'danger' : dash.avgCpu >= 70 ? 'warn' : '',
      sub: `在线主机 ${online} 台均值`, spark: cpuSeries.points, sparkColor: '#2563eb',
    })}
    ${kpiCard({
      icon: 'mem', label: '平均内存', value: fmtNum(dash.avgMem), unit: '%',
      tone: dash.avgMem >= 85 ? 'danger' : dash.avgMem >= 70 ? 'warn' : '',
      sub: `在线主机 ${online} 台均值`, spark: memSeries.points, sparkColor: '#7c3aed',
    })}
    ${kpiCard({
      icon: 'task', label: '监控项', value: taskTotal, unit: ' 项',
      tone: taskBad > 0 ? 'warn' : 'ok',
      sub: `采集点 ${items} 个 · 异常项 ${taskBad} 项`,
    })}
  </div>`;

  // ---- 资源趋势 + 运行状态分布 ----
  const trendBody = (trend && trend.some((s) => s.points.length))
    ? lineChart(trend, { trimTime: true, height: 220, pct: true })
    : '<div class="empty">近 3 小时暂无采样数据，采集端上报后此处将显示平均资源曲线</div>';
  html += `<div class="grid-2">
    ${panel('资源趋势（近 3 小时平均）', trendBody,
      `<span class="panel-note">CPU 较窗口起点 ${deltaTag(cpuDelta, ' 个百分点')}</span>`)}
    <section class="panel"><div class="panel-head"><h3>运行状态分布</h3></div>
      <div class="ring-grid">
        ${ringCard('主机在线状态',
          [{ label: '在线', value: online, color: '#16a34a' }, { label: '离线', value: offline, color: '#dc2626' }],
          total > 0 ? fmtNum(onlineRate, 1) + '%' : '—', '在线率')}
        ${ringCard('监控点健康度',
          [{ label: '正常', value: Math.max(0, items - badItems), color: '#2563eb' }, { label: '异常', value: badItems, color: '#f59e0b' }],
          items > 0 ? fmtNum(items > 0 ? ((items - badItems) / items) * 100 : 0, 1) + '%' : '—', '正常占比')}
      </div>
    </section>
  </div>`;

  // ---- 资源占用排行 + 监控项类型分布 ----
  const byCpu = hosts.filter((h) => h.state === '1').slice().sort((a, b) => b.cpuPer - a.cpuPer).slice(0, 5);
  const byMem = hosts.filter((h) => h.state === '1').slice().sort((a, b) => b.memPer - a.memPer).slice(0, 5);
  const cpuMax = Math.max(60, ...byCpu.map((h) => h.cpuPer));
  const memMax = Math.max(60, ...byMem.map((h) => h.memPer));
  const rankBody = (byCpu.length || byMem.length)
    ? `<div class="rank-cols">
        <div><div class="rank-title">CPU 占用 Top 5</div>${byCpu.map((h) => hbarRow(h.hostname, fmtNum(h.cpuPer) + '%', h.cpuPer, cpuMax, h.cpuPer >= 85 ? '#dc2626' : h.cpuPer >= 70 ? '#f59e0b' : '#2563eb')).join('') || '<div class="empty">暂无在线主机</div>'}</div>
        <div><div class="rank-title">内存占用 Top 5</div>${byMem.map((h) => hbarRow(h.hostname, fmtNum(h.memPer) + '%', h.memPer, memMax, h.memPer >= 85 ? '#dc2626' : h.memPer >= 70 ? '#f59e0b' : '#7c3aed')).join('') || '<div class="empty">暂无在线主机</div>'}</div>
      </div>`
    : '<div class="empty">暂无在线主机，无法生成资源排行</div>';
  html += `<div class="grid-2">
    ${panel('主机资源占用排行', rankBody, '<a class="btn small ghost" href="#/hosts">全部主机</a>')}
    ${panel('监控项类型分布', monitorTypePanel(tasks), '<a class="btn small" href="#/tasks">管理监控项</a>')}
  </div>`;

  // ---- 主机概览 ----
  const hostRows = hosts.slice(0, 12).map((h) => [
    `<a class="link" href="javascript:openHost('${esc(h.hostname)}')">${esc(h.hostname)}</a>`,
    `<span class="mono">${esc(h.ip)}</span>`, esc(h.version),
    h.state === '1' ? '<span class="tag ok"><i class="dot"></i>在线</span>' : '<span class="tag danger"><i class="dot"></i>离线</span>',
    barHtml(h.cpuPer), barHtml(h.memPer), esc(h.cpuCoreNum) + ' 核', esc(h.uptimeDesc || h.lastSeen),
  ]);
  html += panel('主机概览', dataTable(['主机', 'IP', '操作系统', '状态', 'CPU', '内存', '核数', '运行时长 / 最后上报'], hostRows, '尚未有主机接入，可在「添加主机」页面生成采集端接入命令'),
    `<span class="panel-note">共 ${total} 台，显示前 ${Math.min(12, hosts.length)} 台</span> <a class="btn small ghost" href="/api/export/hosts?format=xlsx">导出 Excel</a> <a class="btn small" href="#/hosts">全部主机</a>`);

  // ---- 告警与监控项变化 ----
  const alertRows = alerts.slice(0, 8).map((a) => [
    esc(a.hostname), esc(typeCN(a.type)), levelTag(a.level),
    `<span class="ellipsis" title="${esc(a.content)}">${esc(a.content)}</span>`,
    `<span class="mono">${esc(a.createTime)}</span>`,
    `<button class="btn small" data-resolve="${esc(a.id)}">标记恢复</button>`,
  ]);
  const evRows = (events || []).map((e) => [
    esc(e.name), esc(typeCN(e.type)), esc(e.hostname),
    e.status === '2' ? '<span class="tag danger"><i class="dot"></i>异常</span>' : '<span class="tag ok"><i class="dot"></i>正常</span>',
    `<span class="ellipsis mono" title="${esc(e.msg)}">${esc(e.msg)}</span>`, `<span class="mono">${esc(e.checkTime)}</span>`,
  ]);
  html += `<div class="grid-2">
    ${panel('最近告警', dataTable(['主机', '类型', '级别', '内容', '时间', '操作'], alertRows, '暂无告警，系统运行平稳'), '<a class="btn small" href="#/alerts">告警中心</a>')}
    ${panel('监控项状态变化', dataTable(['监控项', '类型', '目标/主机', '状态', '说明', '时间'], evRows, '暂无状态变化记录'))}
  </div>`;

  // ---- 异常监控点清单（有则展示） ----
  const badStates = (states || []).filter((s) => s.status === '2').slice(0, 10);
  if (badStates.length) {
    const rows = badStates.map((s) => [
      esc(s.name), esc(typeCN(s.type)), esc(s.hostname),
      `<span class="tag danger">异常</span>`,
      `<span class="ellipsis mono" title="${esc(s.msg)}">${esc(s.msg)}</span>`,
      `<span class="mono">${esc(s.checkTime)}</span>`,
    ]);
    html += panel(`当前异常监控点（${badStates.length}）`, dataTable(['监控项', '类型', '目标/主机', '状态', '详情', '检测时间'], rows, '暂无异常监控点'), '<a class="btn small" href="#/alerts">查看告警</a>');
  }

  app().innerHTML = html;
  bindDashboardActions();
}

// monitorTypePanel 按启用状态统计各监控项类型，输出带占比条的列表。
function monitorTypePanel(tasks) {
  const enabled = (tasks || []).filter((t) => t.enabled);
  const typeHint = typeChipCloud();
  if (!enabled.length) {
    return `<div class="panel-body"><div class="empty">尚未配置监控项</div>
      <div class="hint-box" style="margin-top:12px">支持以下 ${Object.keys(TYPE_CN).length} 类监控：${typeHint}
        <div style="margin-top:6px">服务端探测类（网络设备 SNMP/PING、数据库、中间件、K8S）无需在被监控端安装采集端。</div>
      </div></div>`;
  }
  const counts = {};
  enabled.forEach((t) => { counts[t.type] = (counts[t.type] || 0) + 1; });
  const list = Object.keys(counts).map((k) => ({ k, n: counts[k] })).sort((a, b) => b.n - a.n);
  const max = list[0].n;
  const bars = list.map((it) => hbarRow(typeCN(it.k), it.n + ' 项', it.n, max, '#2563eb')).join('');
  return `<div class="panel-body">${bars}
    <div class="hint-box" style="margin-top:14px">共 <b>${enabled.length}</b> 个启用监控项，覆盖 <b>${list.length}</b> 种类型。服务端探测类（网络设备 SNMP/PING、数据库、中间件、K8S）无需在被监控端安装采集端。</div>
  </div>`;
}

// typeChipCloud 输出全部监控项类型的中文标签，供空状态与帮助说明使用。
function typeChipCloud() {
  return `<span class="chip-cloud">${Object.keys(TYPE_CN)
    .map((k) => `<span class="tag off">${esc(TYPE_CN[k])}</span>`).join('')}</span>`;
}

function barHtml(v) {
  const n = Number(v) || 0;
  const cls = n >= 90 ? 'danger' : n >= 75 ? 'warn' : '';
  return `<div class="bar ${cls}"><i style="width:${Math.min(100, n)}%"></i></div><span class="mono">${fmtNum(n)}%</span>`;
}
function levelTag(level) {
  const map = { critical: ['danger', '严重'], warning: ['warn', '警告'], info: ['', '提示'] };
  const it = map[level] || ['', level];
  return `<span class="tag ${it[0]}">${it[1]}</span>`;
}
function bindDashboardActions() {
  document.querySelectorAll('[data-resolve]').forEach((btn) => {
    btn.onclick = async () => {
      try { await post(`/api/alerts/${btn.dataset.resolve}/resolve`, {}); toast('已标记为恢复'); render(); }
      catch (e) { toast('操作失败：' + e.message, true); }
    };
  });
}

/* ================= 主机监控 ================= */
async function renderHosts() {
  const hosts = await getList('/api/hosts');
  window.__hosts = hosts;
  // 目录用于按业务/机房/环境归类，名称可以是中文
  let catalogs = [];
  try { catalogs = asList((await get('/api/host-catalogs')).list); } catch (e) { catalogs = []; }
  window.__catalogs = catalogs;
  // 主机改名 / 删除会级联改动历史数据，只给管理员开放
  const isAdmin = !!(state.user && state.user.role === 'admin');
  const hostActions = (h) => `<span class="row-acts">
      <button class="btn small" data-cmd="${esc(h.hostname)}">下发指令</button>
      ${state.user && state.user.role === 'viewer' ? '' : `<button class="btn small ghost" data-hcfg="${esc(h.hostname)}">采集端配置</button>`}
      ${state.user && state.user.role === 'viewer' ? '' : `<button class="btn small ghost" data-hcat="${esc(h.hostname)}">归类</button>`}
      ${isAdmin ? `<button class="btn small ghost" data-hren="${esc(h.hostname)}">重命名</button>` : ''}
      ${isAdmin ? `<button class="btn small ghost danger" data-hdel="${esc(h.hostname)}">删除</button>` : ''}
    </span>`;
  // 双击主机名进入四方格详情，链接本身保留单击跳转抽屉的可发现性
  const hostCell = (h) => `<a href="javascript:openHost('${esc(h.hostname)}')" data-dbl="${esc(h.hostname)}" title="双击查看四方格详情">${esc(h.hostname)}</a>`;
  const catCell = (h) => h.catalogId
    ? `<span class="tag cat">${esc(h.catalogName)}</span>`
    : '<span class="muted">未分组</span>';
  const hostRow = (h) => [
    hostCell(h),
    catCell(h),
    esc(h.ip), esc(h.version),
    h.state === '1' ? '<span class="tag ok">在线</span>' : '<span class="tag danger">离线</span>',
    barHtml(h.cpuPer), barHtml(h.memPer), esc(h.cpuXh), esc(h.lastSeen),
    hostActions(h),
  ];
  const HEAD = ['主机', '目录', 'IP', '操作系统', '状态', 'CPU', '内存', 'CPU 型号', '最后上报', '操作'];
  const catOptions = ['<option value="">全部目录</option>', '<option value="__none__">未分组</option>']
    .concat(catalogs.map((c) => `<option value="${esc(c.id)}">${esc(c.name)}（${c.hostCount || 0}）</option>`)).join('');
  const rows = hosts.map(hostRow);
  let html = `<div class="toolbar">
    <input type="search" id="hostSearch" placeholder="搜索主机名 / IP / 系统 / 目录">
    <select id="hostCatalogFilter" class="sel">${catOptions}</select>
    <span class="spacer"></span>
    ${state.menus.indexOf('catalogs') >= 0 ? '<a class="btn ghost small" href="#/catalogs">主机目录</a>' : ''}
    ${state.menus.indexOf('onboard') >= 0 ? '<a class="btn primary small" href="#/onboard">+ 添加主机</a>' : ''}
    <a class="btn ghost small" href="/api/export/hosts?format=csv">导出 CSV</a>
    <a class="btn ghost small" href="/api/export/hosts?format=xlsx">导出 Excel</a>
  </div>`;
  html += `<div class="hint-line">提示：<b>双击主机名</b>进入四方格详情（CPU / 内存 / 历史告警 / 网络速率）。`
    + (isAdmin ? '管理员可对主机行执行<b>重命名</b>（历史数据一并迁移）与<b>删除</b>。' : '') + '</div>';
  html += panel('主机列表', dataTable(HEAD, rows, '尚未有主机接入'), '');
  app().innerHTML = html;

  const applyFilter = () => {
    const kw = ($('#hostSearch').value || '').toLowerCase();
    const cat = $('#hostCatalogFilter').value;
    const filtered = hosts.filter((h) => {
      const hitKw = (h.hostname + h.ip + h.version + (h.catalogName || '')).toLowerCase().includes(kw);
      let hitCat = true;
      if (cat === '__none__') hitCat = !h.catalogId;
      else if (cat) hitCat = h.catalogId === cat;
      return hitKw && hitCat;
    });
    const el = document.querySelector('.panel');
    if (!el) return;
    el.innerHTML = '<div class="panel-head"><h3>主机列表</h3></div>'
      + dataTable(HEAD, filtered.map(hostRow), '无匹配主机');
    bindHostActions();
    bindHostDblClick();
  };
  $('#hostSearch').oninput = applyFilter;
  $('#hostCatalogFilter').onchange = applyFilter;
  bindHostActions();
  bindHostDblClick();
}

// 双击主机名 → 打开四方格详情页（告警在前、网速在后）
function bindHostDblClick() {
  document.querySelectorAll('[data-dbl]').forEach((el) => {
    el.ondblclick = (ev) => {
      ev.preventDefault();
      openHostGrid(el.dataset.dbl);
    };
  });
}
function bindHostActions() {
  document.querySelectorAll('[data-cmd]').forEach((btn) => {
    btn.onclick = () => openCommandBox(btn.dataset.cmd);
  });
  document.querySelectorAll('[data-hcfg]').forEach((btn) => {
    btn.onclick = () => openHostAgentConfig(btn.dataset.hcfg);
  });
  document.querySelectorAll('[data-hcat]').forEach((btn) => {
    btn.onclick = () => openHostCatalogAssign(btn.dataset.hcat);
  });
  document.querySelectorAll('[data-hcatq]').forEach((btn) => {
    btn.onclick = () => openHostCatalogQuick(btn.dataset.hcatq);
  });
  document.querySelectorAll('[data-hren]').forEach((btn) => {
    btn.onclick = () => openHostRename(btn.dataset.hren);
  });
  document.querySelectorAll('[data-hdel]').forEach((btn) => {
    btn.onclick = () => confirmHostDelete(btn.dataset.hdel);
  });
}

/* ================= 主机重命名 / 删除（管理员） ================= */

// 重命名主机。主机名由采集端 `-name` 决定并随每次上报携带，平台改不了远端服务参数，
// 所以平台侧把该主机名下的全部数据搬到新名字下，并登记「旧名 → 新名」映射：
// 仍在用旧名上报的采集端会被自动归位，无需重装或改配置。
async function openHostRename(hostname) {
  const m = ensureModal();
  m.show(`<div class="modal-head"><h3>重命名主机</h3></div>
  <div class="modal-body">
    <div class="muted" style="margin-bottom:8px">当前主机名：<b>${esc(hostname)}</b></div>
    <div class="form-row"><label>新主机名</label>
      <input id="hrName" value="${esc(hostname)}" maxlength="64" placeholder="例如：测试主机-01"></div>
    <div class="warn-box" style="margin-top:10px">
      该主机的指标、告警、系统事件、目录归属、采集端配置，以及监控项里的「作用主机」都会迁移到新名字。<br>
      采集端仍会按自身配置的旧名上报，平台自动归到新名字下，<b>无需重装采集端</b>。
    </div>
    <div class="modal-msg" id="hrMsg"></div>
  </div>
  <div class="modal-foot"><button class="btn ghost" id="hrCancel">取消</button>
    <button class="btn primary" id="hrSave">保存</button></div>`);
  $('#hrCancel').onclick = () => m.close();
  $('#hrSave').onclick = async () => {
    try {
      const r = await put(`/api/hosts/${encodeURIComponent(hostname)}/rename`, { name: $('#hrName').value });
      m.close();
      toast(r.msg || '已重命名');
      render();
    } catch (e) {
      $('#hrMsg').textContent = e.message;
      $('#hrMsg').className = 'modal-msg err';
    }
  };
}

// 删除主机及其全部历史数据。采集端若仍在运行会在下次上报时被重新发现，
// 所以确认框里把这一点明确写出来，避免用户以为「删了还会自己回来」是 bug。
async function confirmHostDelete(hostname) {
  const m = ensureModal();
  m.show(`<div class="modal-head"><h3>删除主机</h3></div>
  <div class="modal-body">
    <div class="warn-box danger">
      将删除主机 <b>${esc(hostname)}</b> 及其全部历史数据（指标、告警、系统事件、采集端配置、目录归属），
      此操作<b>不可撤销</b>。<br><br>
      若该主机的采集端仍在运行，它会在下次上报时被<b>重新发现</b>。
      要让它彻底不再出现，请先在目标主机上停用或卸载采集端。
    </div>
    <div class="modal-msg" id="hdMsg"></div>
  </div>
  <div class="modal-foot"><button class="btn ghost" id="hdCancel">取消</button>
    <button class="btn ghost danger" id="hdOk">确认删除</button></div>`);
  $('#hdCancel').onclick = () => m.close();
  $('#hdOk').onclick = async () => {
    try {
      const r = await del(`/api/hosts/${encodeURIComponent(hostname)}`);
      m.close();
      toast(r.msg || '已删除');
      render();
    } catch (e) {
      $('#hdMsg').textContent = e.message;
      $('#hdMsg').className = 'modal-msg err';
    }
  };
}

/* ================= 主机目录（名称支持中文） ================= */
async function renderCatalogs() {
  const [cats, hosts] = await Promise.all([asList((await get('/api/host-catalogs')).list), getList('/api/hosts')]);
  const canEdit = !(state.user && state.user.role === 'viewer');
  const rows = cats.map((c) => [
    esc(c.name),
    `<span class="muted">${esc(c.remark || '')}</span>`,
    String(c.hostCount || 0),
    String(c.sort || 0),
    esc(c.updateTime || c.createTime || ''),
    canEdit ? `<span class="row-acts">
      <button class="btn small" data-cedit="${esc(c.id)}">编辑</button>
      <button class="btn small ghost" data-cdel="${esc(c.id)}">删除</button>
    </span>` : '',
  ]);
  const ungrouped = hosts.filter((h) => !h.catalogId);
  let html = `<div class="toolbar">
    <span class="muted">按业务、机房或环境把主机归类，目录名可以是中文。</span>
    <span class="spacer"></span>
    ${canEdit ? '<button class="btn primary small" id="catNew">+ 新建目录</button>' : ''}
    <a class="btn ghost small" href="#/hosts">返回主机列表</a>
  </div>`;
  html += panel('目录列表', dataTable(['目录名称', '备注', '主机数', '排序', '更新时间', '操作'], rows, '还没有目录，点击右上角「新建目录」'), '');
  html += panel('未分组主机', dataTable(['主机', 'IP', '状态'],
    ungrouped.map((h) => [esc(h.hostname), esc(h.ip),
      h.state === '1' ? '<span class="tag ok">在线</span>' : '<span class="tag danger">离线</span>']),
    '所有主机都已归类'), '');
  app().innerHTML = html;
  const nb = $('#catNew');
  if (nb) nb.onclick = () => openCatalogForm(null, hosts);
  document.querySelectorAll('[data-cedit]').forEach((b) => {
    b.onclick = () => {
      const c = cats.find((x) => x.id === b.dataset.cedit);
      if (c) openCatalogForm(c, hosts);
    };
  });
  document.querySelectorAll('[data-cdel]').forEach((b) => {
    b.onclick = async () => {
      const c = cats.find((x) => x.id === b.dataset.cdel);
      if (!c) return;
      if (!confirm(`删除目录「${c.name}」？其下 ${c.hostCount || 0} 台主机会移到「未分组」，主机本身不会被删除。`)) return;
      try {
        const r = await del('/api/host-catalogs/' + encodeURIComponent(c.id));
        toast(r.msg || '已删除');
        render();
      } catch (e) { toast(e.message, true); }
    };
  });
}

// 目录编辑弹窗：名称/备注/排序，以及把未分组主机批量移入
function openCatalogForm(cat, hosts) {
  const m = ensureModal();
  const isNew = !cat;
  const ungrouped = hosts.filter((h) => !h.catalogId);
  m.show(`<div class="modal-head"><h3>${isNew ? '新建主机目录' : '编辑目录'}</h3></div>
  <div class="modal-body">
    <div class="form-row"><label>目录名称</label>
      <input type="text" id="catName" maxlength="40" placeholder="例如：生产环境 / 核心机房 / 财务部" value="${esc(cat ? cat.name : '')}"></div>
    <div class="form-row"><label>备注</label>
      <input type="text" id="catRemark" maxlength="80" placeholder="选填" value="${esc(cat ? cat.remark : '')}"></div>
    <div class="form-row"><label>排序</label>
      <input type="number" id="catSort" value="${cat ? (cat.sort || 0) : 0}" placeholder="数字越小越靠前"></div>
    ${isNew && ungrouped.length ? `<div class="form-row"><label>同时移入未分组主机</label>
      <div class="check-list" id="catMembers">${ungrouped.map((h) =>
        `<label class="chk"><input type="checkbox" value="${esc(h.hostname)}"> ${esc(h.hostname)} <span class="muted">${esc(h.ip)}</span></label>`).join('')}</div></div>` : ''}
    <div class="modal-msg" id="catMsg"></div>
  </div>
  <div class="modal-foot">
    <button class="btn ghost" id="catCancel">取消</button>
    <button class="btn primary" id="catSave">保存</button>
  </div>`);
  $('#catCancel').onclick = () => m.close();
  $('#catSave').onclick = async () => {
    const name = ($('#catName').value || '').trim();
    if (!name) { $('#catMsg').textContent = '目录名称不能为空'; $('#catMsg').className = 'modal-msg err'; return; }
    const picked = Array.from(document.querySelectorAll('#catMembers input:checked')).map((i) => i.value);
    const body = {
      name, remark: ($('#catRemark').value || '').trim(),
      sort: Number($('#catSort').value || 0), hosts: picked,
    };
    try {
      const r = isNew ? await post('/api/host-catalogs', body)
        : await put('/api/host-catalogs/' + encodeURIComponent(cat.id), body);
      toast(r.msg || '已保存');
      render();
    } catch (e) {
      $('#catMsg').textContent = e.message;
      $('#catMsg').className = 'modal-msg err';
    }
  };
}

// 从主机列表「归类」按钮进入：选目录
function openHostCatalogAssign(hostname) {
  const cats = window.__catalogs || [];
  const host = (window.__hosts || []).find((h) => h.hostname === hostname);
  const m = ensureModal();
  m.show(`<div class="modal-head"><h3>归类 · ${esc(hostname)}</h3></div>
  <div class="modal-body">
    <div class="form-row"><label>所属目录</label>
      <select id="hcaSel" class="sel">
        <option value="">未分组</option>
        ${cats.map((c) => `<option value="${esc(c.id)}"${host && host.catalogId === c.id ? ' selected' : ''}>${esc(c.name)}</option>`).join('')}
      </select></div>
    <div class="modal-msg" id="hcaMsg"></div>
  </div>
  <div class="modal-foot"><button class="btn ghost" id="hcaCancel">取消</button>
    <button class="btn primary" id="hcaSave">保存</button></div>`);
  $('#hcaCancel').onclick = () => m.close();
  $('#hcaSave').onclick = async () => {
    try {
      const r = await post(`/api/hosts/${encodeURIComponent(hostname)}/catalog`,
        { catalogId: $('#hcaSel').value });
      toast(r.msg || '已保存');
      render();
    } catch (e) {
      $('#hcaMsg').textContent = e.message;
      $('#hcaMsg').className = 'modal-msg err';
    }
  };
}

// 目录页里对单台主机快速改归属
async function openHostCatalogQuick(hostname) {
  const cats = window.__catalogs || [];
  const hosts = await getList('/api/hosts');
  const host = hosts.find((h) => h.hostname === hostname);
  if (!host) return toast('主机不存在', true);
  const m = ensureModal();
  m.show(`<div class="modal-head"><h3>归类 · ${esc(hostname)}</h3></div>
  <div class="modal-body"><div class="form-row"><label>所属目录</label>
    <select id="hcqSel" class="sel"><option value="">未分组</option>
      ${cats.map((c) => `<option value="${esc(c.id)}"${host.catalogId === c.id ? ' selected' : ''}>${esc(c.name)}</option>`).join('')}
    </select></div>
    <div class="modal-msg" id="hcqMsg"></div></div>
  <div class="modal-foot"><button class="btn ghost" id="hcqCancel">取消</button>
    <button class="btn primary" id="hcqSave">保存</button></div>`);
  $('#hcqCancel').onclick = () => m.close();
  $('#hcqSave').onclick = async () => {
    try {
      const r = await post(`/api/hosts/${encodeURIComponent(hostname)}/catalog`, { catalogId: $('#hcqSel').value });
      toast(r.msg || '已保存');
      render();
    } catch (e) {
      $('#hcqMsg').textContent = e.message;
      $('#hcqMsg').className = 'modal-msg err';
    }
  };
}

/* ---------------- 复制到剪贴板（含非安全上下文兜底） ---------------- */
function copyText(text) {
  const done = () => toast('已复制到剪贴板');
  const fallback = () => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); done(); } catch (e) { toast('复制失败，请手动选中复制', true); }
    document.body.removeChild(ta);
  };
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(done).catch(fallback);
  } else {
    fallback();
  }
}

/* ================= 添加主机（采集端接入） ================= */
// 采集端产物名由 build.sh 决定：opsmonitor-agent-<goos>-<goarch>[.exe]
const AGENT_OS = [
  {
    key: 'linux', label: 'Linux', goos: 'linux', exe: '', binDir: '/usr/local/bin',
    note: '适配主流发行版：CentOS / RHEL / Rocky / AlmaLinux / Ubuntu / Debian / openEuler / Anolis / 麒麟 / 统信',
  },
  {
    key: 'darwin', label: 'macOS', goos: 'darwin', exe: '', binDir: '/usr/local/bin',
    note: '适配 macOS 11 及以上；Intel 机器选 AMD64，Apple 芯片（M 系列）选 ARM64',
  },
  {
    key: 'windows', label: 'Windows', goos: 'windows', exe: '.exe', binDir: 'C:\\OpsMonitor',
    note: '适配 Windows 7 / Server 2008 R2 及以上，注册服务需以管理员身份运行 PowerShell',
  },
];
const AGENT_ARCH = [
  { key: 'amd64', label: 'x86_64 / AMD64' },
  { key: 'arm64', label: 'ARM64 / aarch64' },
];

// defaultAgentName 按目标系统给一个主机名示例，用户可自行修改。
function defaultAgentName(os) {
  if (os === 'windows') return 'win-01';
  if (os === 'darwin') return 'mac-01';
  return 'web-01';
}

// isUnreachableURL 判断地址是否落在「别的机器访问不到」的网段 —— 这种地址给其它主机用必然连不上。
// 覆盖：回环（127/8、::1、localhost）、私有内网（10/8、172.16/12、192.168/16）、
// 运营商级 NAT（100.64/10）、链路本地（169.254/16、fe80::/10）、IPv6 唯一本地（fc00::/7）、以及 0.0.0.0。
// 云主机尤其容易踩：网卡上只有内网 IP（如 10.0.4.7），公网 IP 是 NAT 映射，反推出来就是不可达地址。
function isUnreachableURL(u) {
  const m = String(u || '').trim().match(/^https?:\/\/([^/?#]+)/i);
  if (!m) return false;
  let host = m[1].split('@').pop();        // 去掉 http://user:pass@ 里的 userinfo
  host = host.replace(/:\d+$/, '');        // 去掉端口
  host = host.replace(/^\[|\]$/g, '').toLowerCase(); // 去掉 IPv6 的方括号
  if (!host) return false;
  if (host === 'localhost' || host.endsWith('.localhost') ||
      host === '0.0.0.0' || host === '::' || host === '::1') return true;
  const v4 = host.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  if (v4) {
    const a = +v4[1], b = +v4[2];
    return a === 0 || a === 127 || a === 10 ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168) ||
      (a === 100 && b >= 64 && b <= 127) ||
      (a === 169 && b === 254);
  }
  // IPv6：唯一本地 fc00::/7、链路本地 fe80::/10
  return /^f[cd][0-9a-f]{2}:/.test(host) || /^fe[89ab][0-9a-f]:/.test(host);
}

// renderAddrWarn 在生成命令前提示「地址别的机器连不上」，避免命令拷到目标主机执行才发现。
// 两种典型来源：① 在服务端本机打开控制台 → 反推出 127.0.0.1；
// ② 云主机 / 反代没透传 Host → 反推出内网 IP（如 10.0.4.7），跨网段的主机同样连不上。
function renderAddrWarn() {
  const el = document.getElementById('obAddrWarn');
  if (!el) return;
  const cur = (state.onboard && state.onboard.server) || '';
  if (!isUnreachableURL(cur)) { el.innerHTML = ''; return; }
  const loop = /^https?:\/\/(127\.|localhost\b|\[::1\])/i.test(cur.trim());
  const why = loop
    ? `命令里的 <code>${esc(cur)}</code> 只在服务端本机执行才有效。`
    : `命令里的 <code>${esc(cur)}</code> 是<strong>内网 / 私有网段地址</strong>，公网或跨网段的其它主机访问不到` +
      `（云主机常见：网卡上只有内网 IP，公网 IP 是 NAT 映射）。`;
  el.innerHTML = `<div class="warn-box">
    <strong>当前的服务端地址，装到其它主机上会连不上。</strong>
    ${why}
    请把上方「服务端地址」改成<strong>目标主机能访问到的地址</strong>（同一内网填内网 IP；公网填
    <code>http://公网IP:9999</code> 或域名）。
    想一劳永逸：给服务端设置 <code>-public-url http://可达地址:9999</code>（或环境变量
    <code>OPSM_PUBLIC_URL</code>），之后生成的命令都按它来；反向代理场景还需透传
    <code>Host</code> / <code>X-Forwarded-Host</code>。
  </div>`;
}

// renderReleaseWarn 在生成命令前先查平台有没有对应平台的采集端可下发。
//
// 安装脚本的第一步就是拿 os/arch 去 /agent/version 问版本，平台没发布就当场中止；
// 这里把同一件事提前到页面上说清楚，省得命令拷到目标主机执行才发现。
function renderReleaseWarn() {
  const el = document.getElementById('obRelWarn');
  if (!el) return;
  const p = state.onboard || {};
  const list = p.platforms || [];
  const cn = (k) => RELEASE_OS_CN[k] || k;

  if (!list.length) {
    el.innerHTML = `<div class="warn-box danger">
      <strong>平台还没有发布任何采集端程序，下面这些命令现在执行会直接失败。</strong>
      请先到 <a href="#/releases">版本发布</a> 页上传采集端二进制；
      在服务端源码目录执行 <code>./release.sh</code> 可以一次性构建并上传全部平台的采集端。
    </div>`;
    return;
  }

  const hit = list.find((r) => r.os === p.os && r.arch === p.arch);
  if (hit) { el.innerHTML = ''; return; }

  const others = list.map((r) => `${cn(r.os)} / ${r.arch}`).join('、');
  el.innerHTML = `<div class="warn-box danger">
    <strong>平台尚未发布 ${esc(cn(p.os))} / ${esc(p.arch)} 的采集端，命令会停在「查询服务端发布的最新版本」这一步。</strong>
    当前已发布的平台：${esc(others)}。
    请到 <a href="#/releases">版本发布</a> 页上传 ${esc(cn(p.os))} / ${esc(p.arch)} 的采集端程序，
    或在上方改选已发布的系统与架构。
  </div>`;
}

async function renderOnboard() {
  let info = { token: '', tokenHash: '', server: location.origin, interval: 60 };
  try {
    info = Object.assign(info, (await get('/api/agent/install')) || {});
  } catch (e) {
    toast('获取接入密钥失败：' + e.message, true);
  }
  const p = state.onboard = {
    os: 'linux', arch: 'amd64', name: defaultAgentName('linux'), mode: 'auto',
    server: info.server || location.origin,
    token: info.token || '',
    tokenHash: info.tokenHash || '',
    interval: info.interval || 60,
    platforms: info.platforms || [],
  };

  let html = `<div class="toolbar">
    <a class="btn ghost small" href="#/hosts">← 返回主机列表</a>
    <span class="spacer"></span>
  </div>`;

  html += `<div class="hint-box">
    采集端程序<strong>由平台下发</strong>，目标主机上不需要事先准备任何文件，命令里也不需要填一堆参数。
    一键安装命令会做四件事：查询平台上的最新版本 → 从本平台下载二进制并校验完整性 →
    拉取平台下发的运行配置 → 注册为开机自启的系统服务。
    目标主机只要能访问服务端地址 <code>${esc(p.server)}</code> 即可，<strong>无需连接外网</strong>。
  </div>`;

  html += '<div id="obRelWarn"></div>';
  html += '<div id="obAddrWarn"></div>';

  html += panel('1. 选择目标主机系统', `<div class="panel-body">
    <div class="seg" id="obOs">${AGENT_OS.map((o) => `<button type="button" data-os="${o.key}">${esc(o.label)}</button>`).join('')}</div>
    <div class="seg-note" id="obOsNote"></div>
    <span class="seg-label">CPU 架构</span>
    <div class="seg" id="obArch">${AGENT_ARCH.map((a) => `<button type="button" data-arch="${a.key}">${esc(a.label)}</button>`).join('')}</div>
  </div>`);

  html += panel('2. 确认接入参数', `<div class="panel-body">
    <div class="form-grid">
      <label>主机名称（示例，可自行更改）
        <input id="obName" value="${esc(p.name)}" placeholder="例如：web-01">
      </label>
      <label>服务端地址
        <input id="obServer" value="${esc(p.server)}" placeholder="http://服务端IP:9999">
      </label>
      <label class="full">通信密钥（token，两端必须一致）
        <span class="dev-pick" style="margin-top:5px">
          <input id="obToken" value="${esc(p.token)}" placeholder="opsmonitor">
          <button class="btn small" type="button" id="obCopyToken">复制</button>
        </span>
      </label>
    </div>
    <div class="seg-note">密钥取自服务端启动参数 <code>-token</code>（默认 <code>opsmonitor</code>，部署后请立即修改）。
      页面生成的命令里只会带上它的 <strong>MD5 指纹</strong>（<code>-token-hash</code>），
      目标主机的命令行历史与 systemd 单元文件里都不会留下口令明文。只读账号无权查看该密钥。</div>
    <div class="seg-note">上报间隔、执行指令开关、跳过磁盘、更新策略等运行配置<strong>不由命令携带</strong>，
      而是采集端启动后从平台拉取，在<a href="#/settings">系统设置 → 采集端配置</a>里统一调整，改完自动生效、不用重装。</div>
  </div>`);

  html += panel('3. 在目标主机执行', `<div class="panel-body">
    <div class="seg" id="obMode">
      <button type="button" data-mode="auto">一键安装（推荐）</button>
      <button type="button" data-mode="manual">手动部署</button>
    </div>
    <div id="obCmds"></div>
  </div>`);

  html += panel('4. 接入后如何确认 / 后续如何升级', `<div class="panel-body">
    <div class="kv">
      <div><span class="k">成功标志</span>采集端日志打印「已应用服务端配置」与「已上报 CPU x% / 内存 x% / 磁盘分区 n 个」，「主机监控」中出现该主机且状态为在线。</div>
      <div><span class="k">一直不出现</span>多为通信密钥不一致（服务端日志会打印「拒绝上报：token 校验失败」）或网络不通（防火墙未放行服务端端口）。</div>
      <div><span class="k">改配置</span>到「系统设置 → 采集端配置」调整，采集端在刷新周期内自动应用；也可在「主机监控」里对单台主机单独覆盖。想固定用本地参数启动可加 <code>-remote-config off</code>。</div>
      <div><span class="k">自动升级</span>采集端按平台配置的检查间隔向平台查询版本，发现新版本会自行下载校验、替换二进制并重启，全程无需登录目标主机。可在「采集端配置」里改成只提示不升级，或关闭检查。</div>
      <div><span class="k">离线判定</span>默认超过 180 秒未上报标记为离线，可用服务端 <code>-offline</code> 参数调整。</div>
      <div><span class="k">不需要了</span>Linux/macOS：<code>systemctl disable --now opsmonitor-agent</code>（macOS：<code>launchctl unload</code>）；Windows：删除计划任务 <code>opsmonitor-agent</code>。主机记录会保留，历史数据按服务端保留天数自动清理。</div>
    </div>
  </div>`);

  app().innerHTML = html;

  const syncSeg = () => {
    document.querySelectorAll('#obOs button').forEach((b) => b.classList.toggle('active', b.dataset.os === p.os));
    document.querySelectorAll('#obArch button').forEach((b) => b.classList.toggle('active', b.dataset.arch === p.arch));
    document.querySelectorAll('#obMode button').forEach((b) => b.classList.toggle('active', b.dataset.mode === p.mode));
    $('#obOsNote').textContent = (AGENT_OS.find((o) => o.key === p.os) || {}).note || '';
  };
  const refresh = () => {
    $('#obCmds').innerHTML = onboardCmds(p);
    bindCmdCopy();
    renderAddrWarn();
    renderReleaseWarn();
  };

  document.querySelectorAll('#obOs button').forEach((b) => {
    b.onclick = () => {
      // 切换系统时，若主机名还是上一个系统的默认示例，就跟着换成新示例
      if (p.name === defaultAgentName(p.os)) p.name = defaultAgentName(b.dataset.os);
      p.os = b.dataset.os;
      $('#obName').value = p.name;
      syncSeg(); refresh();
    };
  });
  document.querySelectorAll('#obArch button').forEach((b) => {
    b.onclick = () => { p.arch = b.dataset.arch; syncSeg(); refresh(); };
  });
  document.querySelectorAll('#obMode button').forEach((b) => {
    b.onclick = () => { p.mode = b.dataset.mode; syncSeg(); refresh(); };
  });
  [['#obName', 'name'], ['#obServer', 'server'], ['#obToken', 'token']].forEach((pair) => {
    $(pair[0]).oninput = () => { p[pair[1]] = $(pair[0]).value; refresh(); };
  });
  $('#obCopyToken').onclick = () => copyText(p.token);
  syncSeg();
  refresh();
}

function bindCmdCopy() {
  document.querySelectorAll('[data-copy]').forEach((b) => {
    b.onclick = () => {
      const pre = document.getElementById(b.dataset.copy);
      if (pre) copyText(pre.textContent);
    };
  });
}

function cmdBlock(id, title, desc, code) {
  return `<div class="cmd-block">
    <div class="cmd-head">
      <span class="cmd-title">${esc(title)}</span>
      <span class="cmd-desc">${esc(desc || '')}</span>
      <span class="spacer"></span>
      <button class="btn small ghost" type="button" data-copy="${esc(id)}">复制</button>
    </div>
    <pre id="${esc(id)}">${esc(code)}</pre>
  </div>`;
}

// onboardCmds 按所选系统、模式与参数实时生成接入命令。
// 主机名按主机名规范处理（空白替换为 -），避免命令里出现需要转义的引号。
function onboardCmds(p) {
  const os = AGENT_OS.find((o) => o.key === p.os) || AGENT_OS[0];
  const name = ((p.name || '').trim() || defaultAgentName(p.os)).replace(/\s+/g, '-');
  const server = ((p.server || '').trim() || location.origin).replace(/\/+$/, '');
  const hash = (p.tokenHash || '').trim();
  const q = `wgToken=${encodeURIComponent(hash)}&name=${encodeURIComponent(name)}`;

  if (p.mode === 'manual') {
    return manualCmds(os, p, { name, server, hash });
  }

  // ---- 一键安装：脚本从平台拉取，目标主机无需外部网络 ----
  if (os.key === 'windows') {
    return [
      cmdBlock('ob-a1', '一键安装（管理员 PowerShell）', '下载平台上的采集端、校验完整性、拉取配置并注册开机自启',
        `Invoke-Expression (Invoke-WebRequest -UseBasicParsing '${server}/install.ps1?${q}').Content`),
      cmdBlock('ob-a2', '分步执行（想先看脚本内容时用）', '先把脚本存到磁盘、确认无误再执行；命令里不含任何变量，复制粘贴不会被破坏',
        `New-Item -ItemType Directory -Force C:\\OpsMonitor | Out-Null\n` +
        `Invoke-WebRequest -UseBasicParsing '${server}/install.ps1?${q}' -OutFile C:\\OpsMonitor\\opsm-install.ps1\n` +
        `powershell -ExecutionPolicy Bypass -File C:\\OpsMonitor\\opsm-install.ps1`),
      cmdBlock('ob-a3', '确认已接入 / 后续操作', '在目标主机上验证能查到版本与平台下发的配置',
        `Invoke-RestMethod '${server}/agent/version?os=windows&arch=${p.arch}&wgToken=${esc(hash)}&current=0.0.0' | Format-List\n` +
        `Invoke-RestMethod '${server}/agent/config?wgToken=${esc(hash)}&hostname=${esc(name)}' | Format-List\n\n` +
        `# 手动重启采集端：\n` +
        `Start-ScheduledTask -TaskName opsmonitor-agent\n\n` +
        `# 查看计划任务状态（应为 Running）：\n` +
        `Get-ScheduledTask -TaskName opsmonitor-agent | Select-Object State`),
    ].join('');
  }

  return [
    cmdBlock('ob-a1', '一键安装', '下载平台上的采集端、校验完整性、拉取配置并注册系统服务',
      `curl -fsSL "${server}/install.sh?${q}" | sudo bash\n\n` +
      `# 目标主机没有 curl 时用 wget：\n` +
      `# wget -qO- "${server}/install.sh?${q}" | sudo bash\n\n` +
      `# 服务端使用自签 HTTPS 证书时追加 -k：\n` +
      `# curl -kfsSL "${server}/install.sh?${q}" | sudo bash`),
    cmdBlock('ob-a2', '确认已接入', '在目标主机上验证能查到版本与平台下发的配置',
      `curl -fsSL "${server}/agent/version?os=${os.goos}&arch=${p.arch}&wgToken=${esc(hash)}&current=0.0.0"\n` +
      `curl -fsSL "${server}/agent/config?wgToken=${esc(hash)}&hostname=${esc(name)}"`),
    cmdBlock('ob-a3', '改配置与卸载', '运行配置由平台下发，改完自动生效，无需重装',
      `# 改配置：控制台「系统设置 → 采集端配置」调整上报间隔、执行指令开关、\n` +
      `#         跳过磁盘、更新策略；采集端在刷新周期内自动应用，不用重启。\n` +
      `# 单台主机想用不同配置：「主机监控」中该主机行的「采集端配置」按钮。\n\n` +
      (os.key === 'darwin'
        ? `# 卸载：\nsudo launchctl unload /Library/LaunchDaemons/opsmonitor-agent.plist\nsudo rm -f /Library/LaunchDaemons/opsmonitor-agent.plist /usr/local/bin/opsmonitor-agent`
        : `# 卸载：\nsudo systemctl disable --now opsmonitor-agent\nsudo rm -f /etc/systemd/system/opsmonitor-agent.service /usr/local/bin/opsmonitor-agent`)),
  ].join('');
}

// manualCmds 手动部署路径：仍然从平台下载，但不自动注册服务，适合不方便跑脚本的场景。
function manualCmds(os, p, c) {
  const { name, server, hash } = c;
  const home = `${os.binDir}/opsmonitor-agent${os.exe}`;
  const args = `-server ${server} -token-hash ${hash} -name ${name}`;
  const dl = `${server}/agent/download?os=${os.goos}&arch=${p.arch}&wgToken=${esc(hash)}`;

  if (os.key === 'windows') {
    return [
      cmdBlock('ob-m1', '从平台下载程序', '在管理员 PowerShell 中执行',
        `New-Item -ItemType Directory -Force ${os.binDir} | Out-Null\n` +
        `Invoke-WebRequest '${dl}' -OutFile '${os.binDir}\\opsmonitor-agent.exe'`),
      cmdBlock('ob-m2', '前台试运行', '确认能连通服务端后再注册为服务',
        `${os.binDir}\\opsmonitor-agent.exe ${args}\n\n` +
        `# 看到「已上报 CPU ...」即接入成功，控制台出现该主机后按 Ctrl+C 停止`),
      cmdBlock('ob-m3', '注册开机自启', '开机自启、异常自动拉起（需管理员权限）',
        `$a = "${args}"\n` +
        `$action = New-ScheduledTaskAction -Execute '${os.binDir}\\opsmonitor-agent.exe' -Argument $a\n` +
        `$trigger = New-ScheduledTaskTrigger -AtStartup\n` +
        `$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest\n` +
        `Register-ScheduledTask -TaskName opsmonitor-agent -Action $action -Trigger $trigger -Principal $principal -Force\n` +
        `Start-ScheduledTask -TaskName opsmonitor-agent\n\n` +
        `# 卸载：Unregister-ScheduledTask -TaskName opsmonitor-agent -Confirm:$false`),
    ].join('');
  }

  const svc = os.key === 'darwin'
    ? cmdBlock('ob-m3', '注册为后台服务（launchd）', '开机自启、退出后自动拉起',
      `sudo install -m 0755 /tmp/opsmonitor-agent ${home}\n\n` +
      `sudo tee /Library/LaunchDaemons/opsmonitor-agent.plist >/dev/null <<'EOF'\n` +
      `<?xml version="1.0" encoding="UTF-8"?>\n` +
      `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n` +
      `<plist version="1.0">\n` +
      `<dict>\n` +
      `  <key>Label</key><string>opsmonitor-agent</string>\n` +
      `  <key>ProgramArguments</key>\n` +
      `  <array>\n` +
      `    <string>${home}</string>\n` +
      `    <string>-server</string><string>${server}</string>\n` +
      `    <string>-token-hash</string><string>${hash}</string>\n` +
      `    <string>-name</string><string>${name}</string>\n` +
      `  </array>\n` +
      `  <key>RunAtLoad</key><true/>\n` +
      `  <key>KeepAlive</key><true/>\n` +
      `</dict>\n` +
      `</plist>\n` +
      `EOF\n\n` +
      `sudo chown root:wheel /Library/LaunchDaemons/opsmonitor-agent.plist\n` +
      `sudo launchctl load -w /Library/LaunchDaemons/opsmonitor-agent.plist`)
    : cmdBlock('ob-m3', '注册为系统服务（systemd）', '开机自启、异常自动拉起',
      `sudo install -m 0755 /tmp/opsmonitor-agent ${home}\n\n` +
      `sudo tee /etc/systemd/system/opsmonitor-agent.service >/dev/null <<'EOF'\n` +
      `[Unit]\n` +
      `Description=OpsMonitor 采集端\n` +
      `After=network-online.target\n` +
      `Wants=network-online.target\n\n` +
      `[Service]\n` +
      `Type=simple\n` +
      `ExecStart=${home} ${args}\n` +
      `Restart=always\n` +
      `RestartSec=5\n\n` +
      `[Install]\n` +
      `WantedBy=multi-user.target\n` +
      `EOF\n\n` +
      `sudo systemctl daemon-reload\n` +
      `sudo systemctl enable --now opsmonitor-agent\n` +
      `systemctl status opsmonitor-agent --no-pager\n\n` +
      `# 无 systemd 的发行版（Alpine、容器等）改用：\n` +
      `# nohup ${home} ${args} >/var/log/opsmonitor-agent.log 2>&1 &`);

  return [
    cmdBlock('ob-m1', '从平台下载程序', '目标主机直接向平台取二进制，不需要 scp 拷贝',
      `curl -fsSL "${dl}" -o /tmp/opsmonitor-agent\n` +
      `chmod +x /tmp/opsmonitor-agent`),
    cmdBlock('ob-m2', '前台试运行', '确认能连通服务端后再转为常驻',
      `/tmp/opsmonitor-agent ${args}\n\n` +
      `# 看到「已上报 CPU ...」即接入成功，控制台出现该主机后按 Ctrl+C 停止`),
    svc,
  ].join('');
}

/* ================= 采集端版本发布 ================= */
const RELEASE_OS = [
  { key: 'linux', label: 'Linux', exe: '' },
  { key: 'windows', label: 'Windows', exe: '.exe' },
  { key: 'darwin', label: 'macOS', exe: '' },
];
const RELEASE_ARCH = [
  { key: 'amd64', label: 'x86_64 / AMD64' },
  { key: 'arm64', label: 'ARM64 / aarch64' },
  { key: 'arm', label: 'ARM 32 位' },
  { key: '386', label: 'x86 32 位' },
];
const RELEASE_OS_CN = { linux: 'Linux', windows: 'Windows', darwin: 'macOS' };

function fmtSize(n) {
  const v = Number(n) || 0;
  if (v < 1024) return v + ' B';
  if (v < 1024 * 1024) return (v / 1024).toFixed(1) + ' KB';
  return (v / 1024 / 1024).toFixed(2) + ' MB';
}
const shortHash = (h) => (h ? h.slice(0, 10) + '…' : '—');

/* ============================================================
   授权系统（更新与授权中心）驱动的两个页面
   ------------------------------------------------------------
   平台不再自己管理版本与授权：发布物与授权码都在授权系统后台维护，
   平台按间隔（默认 5 分钟）从授权系统拉取。这两个页面只做展示与手动触发。
   ============================================================ */

/* ---------------- 在线升级 ---------------- */
async function renderUpgrade() {
  const data = await get('/api/hub/status');
  const hub = data.hub || {};
  const latest = hub.serverLatest || null;
  const cached = hub.agentCached || {};
  const cachedKeys = Object.keys(cached);

  let html = `<div class="hint-box">
    版本与更新由<strong>授权系统统一管理</strong>。平台会按间隔自动从授权系统拉取：
    采集端发布物下载后缓存到本机，内网主机从这里升级；平台自身的新版本也在这里提示。
    目标主机<strong>不需要能访问公网</strong>。
  </div>`;

  if (!hub.configured) {
    html += panel('当前为完全离线模式', `<div class="panel-body">
      <div class="alert-box info">平台未配置授权系统地址（<code>-hub</code> 留空），因此不会与授权中心通信。
        采集端安装与升级改走<strong>本地上传发布物</strong>的方式，功能不受影响，只是不再自动获取授权与更新。</div>
      <div class="kv" style="margin-top:12px">
        <div><span class="k">启用方式</span><span class="mono">去掉 -hub 参数即可（地址已内置，不在界面显示）</span></div>
        <div><span class="k">拉取令牌</span><span class="mono">已内置，无需配置；改用自建授权中心时才需 -hub-token</span></div>
        <div><span class="k">拉取间隔</span><span class="mono">-hub-interval 5（分钟）</span></div>
        <div><span class="k">自动更新</span><span class="mono">-hub-auto-update download（默认）</span></div>
      </div>
      <div class="seg-note" style="margin-top:10px">改为默认授权中心后重启服务端即可恢复自动同步；
        若仍在授权购买页手工导入授权码，可点下方按钮从本地发布物列表手动升级。</div>
    </div>`);
    app().innerHTML = html;
    renderUpgradeOfflineActions();
    return;
  }

  const syncTone = hub.lastError ? 'warn' : 'ok';
  const syncText = hub.lastError ? '同步失败' : '同步正常';
  const hasNew = !!(latest && latest.hasUpdate);

  const autoMode = { check: '只提示，不自动下载', download: '自动下载并就位', off: '已关闭' };
  html += `<div class="kpi-grid">
    ${kpiCard({ icon: 'pulse', label: '当前平台版本', value: data.server || '—', unit: '', sub: '构建于 ' + esc(data.build || '') })}
    ${kpiCard({ icon: 'download', label: '授权系统最新平台版本', value: latest ? latest.version : '—', unit: '',
      tone: hasNew ? 'warn' : 'ok', sub: latest ? (hasNew ? '有新版本可升级' : '已是最新') : '授权系统暂无该平台版本' })}
    ${kpiCard({ icon: 'host', label: '已缓存采集端', value: cachedKeys.length, unit: ' 个平台', sub: '从授权系统拉取并落地本机' })}
    ${kpiCard({ icon: 'shield', label: '授权系统连接', value: syncText, unit: '', tone: syncTone,
      sub: hub.lastCheck ? '最近检查 ' + esc(hub.lastCheck) : '尚未检查' })}
  </div>`;

  // 令牌来源只展示「来自哪里」，任何情况下都不回显令牌内容，
  // 避免把凭证通过接口响应或页面源码交出去。
  const tokenSrc = hub.tokenSource === 'builtin'
    ? '内置（随程序分发，界面与日志均不回显）'
    : hub.tokenSource === 'custom'
      ? '自定义（-hub-token 或 OPSM_HUB_TOKEN）'
      : '未设置';

  html += panel('授权系统连接', `<div class="panel-body">
    <div class="kv">
      ${SHOW_HUB_URL ? `<div><span class="k">授权系统地址</span><span class="mono">${esc(hub.url || '')}</span></div>` : ''}
      <div><span class="k">本机机器码</span><span class="mono">${esc(data.machine || hub.machine || '')}</span></div>
      <div><span class="k">拉取间隔</span>${hub.intervalSec ? Math.round(hub.intervalSec / 60) + ' 分钟' : '—'}</div>
      <div><span class="k">拉取令牌来源</span>${esc(tokenSrc)}</div>
      <div><span class="k">自动更新策略</span>${esc(autoMode[hub.autoUpdate] || hub.autoUpdate || '—')}</div>
      <div><span class="k">最近同步</span>${esc(hub.lastCheck || '尚未同步')}</div>
    </div>
  </div>`);

  if (hub.lastError) {
    html += panel('同步异常', `<div class="panel-body"><div class="alert-box warn">${esc(hub.lastError)}</div>
      <div class="kv" style="margin-top:10px">
        ${SHOW_HUB_URL ? `<div><span class="k">授权系统</span><span class="mono">${esc(hub.url || '')}</span></div>` : ''}
        <div><span class="k">连续失败</span>${hub.serverFail || 0} 次</div>
        ${hub.nextRetrySec ? `<div><span class="k">已退避</span>本轮后再等待 ${Math.ceil(hub.nextRetrySec / 60)} 分钟重试</div>` : ''}
      </div>
      <div class="seg-note" style="margin-top:10px">请检查平台能否访问授权中心${SHOW_HUB_URL ? `（<code>${esc(hub.url || '')}</code>）` : ''}，
        以及授权中心侧配置的令牌是否与本机一致（令牌内容不会在任何界面或日志中显示）。
        失败会按 1→2→4→8→… 分钟自动退避，最长 30 分钟一次；授权端恢复后自动回到常规间隔。</div>
      <div class="row-acts" style="margin-top:10px"><button class="btn" id="upRetry">立即重试同步</button></div></div>`);
  }

  // 服务端自身升级
  let upBody = '';
  if (!latest) {
    upBody = `<div class="alert-box info">授权系统尚未发布当前平台对应的版本，暂时无需升级。</div>`;
  } else {
    upBody = `<div class="kv">
      <div><span class="k">可升级到</span><b>${esc(latest.version)}</b></div>
      <div><span class="k">平台</span><span class="mono">${esc(latest.os + '/' + latest.arch)}</span></div>
      <div><span class="k">文件大小</span>${(latest.size / 1048576).toFixed(1)} MB</div>
      <div><span class="k">发布时间</span>${esc(latest.publishAt || '')}</div>
      <div><span class="k">SHA256</span><span class="mono">${esc((latest.sha256 || '').slice(0, 16))}…</span></div>
      <div><span class="k">更新说明</span>${esc(latest.notes || '—')}</div>
      <div><span class="k">自动更新策略</span>${esc(autoMode[hub.autoUpdate] || hub.autoUpdate || '—')}</div>
    </div>`;
    if (hub.serverUpgraded) {
      upBody += `<div class="alert-box warn" style="margin-top:12px">
        版本 <b>${esc(hub.serverUpgraded)}</b> 已下载并就位，<strong>正在自动重启以生效</strong>。
        重启由 systemd / Docker 的重启策略自动完成，手工启动的会自行拉起新进程；若新版本启动失败会自动回滚到 .bak。<br>
        若提示长时间不消失，说明当前环境没有可用的重启机制（如容器未配置 restart 策略），请手工重启服务。</div>`;
      upBody += `<div class="row-acts" style="margin-top:10px">
        <button class="btn primary" id="upRestartHint">知道了</button></div>`;
    } else if (hasNew) {
      upBody += `<div class="row-acts" style="margin-top:12px">
        <button class="btn primary" id="upPull">拉取并升级到 ${esc(latest.version)}</button>
        <span class="panel-note">下载并替换本机程序，替换完成后<strong>自动重启生效</strong>，旧版本会备份为 .bak</span>
      </div>`;
      if (hub.autoUpdate === 'download') {
        upBody += `<div class="seg-note" style="margin-top:8px">当前策略为自动下载，通常无需手动点击——下次同步（约
          ${esc(hub.intervalSec ? Math.round(hub.intervalSec / 60) : 5)} 分钟内）会自动就位。</div>`;
      }
    } else {
      upBody += `<div class="seg-note" style="margin-top:12px">当前已是最新版本，无需升级。</div>`;
    }
    if (hub.serverNote) {
      upBody += `<div class="seg-note" style="margin-top:8px">${esc(hub.serverNote)}</div>`;
    }
  }
  html += panel('平台升级', `<div class="panel-body">${upBody}</div>`);

  // 采集端发布物缓存
  let cacheBody = '';
  if (!cachedKeys.length) {
    cacheBody = `<div class="empty">还没有从授权系统缓存到采集端发布物。授权系统后台发布对应平台后，平台会在下次同步时自动下载。</div>`;
  } else {
    cacheBody = `<div class="table-wrap"><table class="table"><thead><tr><th>平台</th><th>已缓存版本</th></tr></thead><tbody>`
      + cachedKeys.sort().map((k) => `<tr><td class="mono">${esc(k)}</td><td><b>${esc(cached[k])}</b></td></tr>`).join('')
      + '</tbody></table></div>';
    cacheBody += `<div class="seg-note" style="margin-top:10px">这些版本已落在本机 <code>releases/</code> 目录，内网主机安装与自动更新都从这里取，无需访问公网。</div>`;
  }
  if (hub.agentError) {
    cacheBody += `<div class="alert-box warn" style="margin-top:10px">
      以下平台从授权系统缓存采集端失败：<span class="mono">${esc(hub.agentError)}</span><br>
      常见原因：授权端下载接口拒绝取包（令牌未随下载地址下发，或下载鉴权方式与 check 不一致），
      或本机到授权系统的下载链路不通/被网关中断。授权端未发布的平台不会出现在这里。
    </div>`;
  }
  html += panel('采集端发布物（已缓存）', `<div class="panel-body">${cacheBody}</div>`);

  html += `<div class="toolbar" style="margin-top:4px">
    <button class="btn ghost" id="upCheck">立即检查更新</button>
    <span class="spacer"></span>
    <span class="panel-note">平台每 ${Math.max(1, Math.round((data.hub && data.hub.intervalSec) || 300) / 60)} 分钟自动同步一次</span>
  </div>`;

  app().innerHTML = html;

  $('#upCheck').onclick = async () => {
    try {
      const r = await post('/api/hub/check', {});
      toast(r.msg || '已与授权系统同步');
      render();
    } catch (e) { toast(e.message, true); }
  };
  const retry = $('#upRetry');
  if (retry) retry.onclick = () => $('#upCheck').click();
  const pull = $('#upPull');
  if (pull) {
    pull.onclick = async () => {
      if (!confirm('确认从授权系统拉取并安装新版本？替换完成后平台会自动重启以生效。')) return;
      pull.disabled = true;
      pull.textContent = '下载中…';
      try {
        const r = await post('/api/hub/pull', {});
        toast(r.msg || '已就位，正在自动重启生效');
        pull.textContent = '已就位，正在自动重启';
      } catch (e) {
        toast(e.message, true);
        pull.disabled = false;
        pull.textContent = '重试';
      }
    };
  }
  const hint = $('#upRestartHint');
  if (hint) hint.onclick = () => render();
}

// 离线模式（未配置授权系统）下的按钮绑定：此时只能从本地上传发布物。
function renderUpgradeOfflineActions() {
  const btn = $('#upCheck');
  if (!btn) return;
  btn.onclick = () => {
    toast('当前为离线模式，如需自动同步请去掉 -hub 参数后重启（地址已内置，不在界面显示）', true);
  };
}

/* ---------------- 授权购买 ---------------- */
async function renderLicenseBuy() {
  const data = await get('/api/hub/status');
  const hub = data.hub || {};
  const st = data.license || {};
  const editions = asList(data.editions);
  const free = data.free || { MaxHosts: 10, MaxUsers: 3 };

  const expireText = !st.notAfter ? '永久有效'
    : st.valid ? `有效期至 ${st.notAfter}（剩余 ${st.daysLeft} 天）` : `已于 ${st.notAfter} 到期`;

  let html = `<div class="hint-box">
    授权由<strong>授权中心统一签发</strong>：在授权中心后台选择客户与档位即可生成授权码，
    平台凭本机机器码自动拉取并导入，无需人工粘贴。
    授权码用<strong>Ed25519 私钥签名</strong>，平台只内置公钥、<strong>不具备签发能力</strong>；
    未导入授权码时平台以<strong>社区版</strong>运行（${free.MaxHosts || 10} 台主机 / ${free.MaxUsers || 3} 个账号）。
  </div>`;

  html += `<div class="kpi-grid">
    ${kpiCard({ icon: 'key', label: '当前版本', value: st.edition || '社区版', unit: '',
      tone: st.valid ? 'ok' : 'warn', sub: st.customer ? esc(st.customer) : (st.valid ? '已授权' : '未导入授权码') })}
    ${kpiCard({ icon: 'host', label: '主机上限', value: st.maxHosts > 0 ? st.maxHosts : '不限', unit: st.maxHosts > 0 ? ' 台' : '', sub: '按授权档位' })}
    ${kpiCard({ icon: 'shield', label: '账号上限', value: st.maxUsers > 0 ? st.maxUsers : '不限', unit: st.maxUsers > 0 ? ' 个' : '', sub: '按授权档位' })}
    ${kpiCard({ icon: 'pulse', label: '授权状态', value: st.valid ? '已授权' : '未授权', tone: st.valid ? 'ok' : 'warn', sub: esc(expireText) })}
  </div>`;

  if (!hub.configured) {
    html += panel('授权来源', `<div class="panel-body">
      <div class="alert-box info">当前为完全离线模式（<code>-hub</code> 留空），平台不会与授权中心通信。
        授权码仍可在本页下方<strong>手动粘贴导入</strong>，功能与在线模式一致。</div>
      <div class="kv" style="margin-top:12px">
        <div><span class="k">启用在线授权</span><span class="mono">去掉 -hub 参数即可（地址已内置）</span></div>
        <div><span class="k">或显式指定</span><span class="mono">-hub 你的授权中心地址</span></div>
        <div><span class="k">拉取令牌</span><span class="mono">已内置，无需配置（界面与日志均不回显）</span></div>
      </div>
    </div>`);
  } else {
    const stateCN = { active: '已获取授权', pending: '等待授权系统签发', none: '授权系统尚未收到本机申请' }[hub.licenseState] || '未同步';
    html += panel('授权系统授权同步', `<div class="panel-body">
      <div class="kv">
        ${SHOW_HUB_URL ? `<div><span class="k">授权系统地址</span><span class="mono">${esc(hub.url || '')}</span></div>` : ''}
        <div><span class="k">同步状态</span>${esc(stateCN)}</div>
        <div><span class="k">本机机器码</span><span class="mono">${esc(data.machine || '')}</span>
          <button class="btn small ghost" id="licCopyMachine">复制</button></div>
        <div><span class="k">最近拉取</span>${esc(hub.licensePulled || hub.lastCheck || '—')}</div>
      </div>
      ${hub.licenseMsg ? `<div class="seg-note" style="margin-top:10px">${esc(hub.licenseMsg)}</div>` : ''}
      <div class="row-acts" style="margin-top:12px">
        <button class="btn primary" id="licPull">立即从授权系统拉取授权</button>
        <span class="panel-note">签发后平台最长 ${Math.max(1, Math.round(((data.hub && data.hub.intervalSec) || 300) / 60))} 分钟自动拉取一次</span>
      </div>
    </div>`);
  }

  if (!st.valid && st.reason) {
    html += panel('授权提示', `<div class="panel-body"><div class="alert-box warn">${esc(st.reason)}</div></div>`);
  }

  // 档位对照（与授权系统一致）
  let planRows = `<div class="table-wrap"><table class="table"><thead><tr>
    <th>档位</th><th>主机上限</th><th>账号上限</th><th>价格</th></tr></thead><tbody>
    <tr><td><b>社区版</b></td><td>${free.MaxHosts || 10}</td><td>${free.MaxUsers || 3}</td><td><span class="badge ok">永久免费</span></td></tr>`;
  editions.forEach((e) => {
    planRows += `<tr><td><b>${esc(e.name)}</b></td><td>${e.maxHosts || '不限'}</td><td>${e.maxUsers || '不限'}</td><td>按需报价</td></tr>`;
  });
  planRows += '</tbody></table></div>';

  html += panel('版本档位（来自授权系统）', `<div class="panel-body">
    ${planRows}
    <div class="seg-note" style="margin-top:10px">商业版价格按主机规模、部署方式与支持年限确定，不做公开标价。</div>
    ${SHOW_HUB_URL ? `<div class="row-acts" style="margin-top:12px">
      <a class="btn primary" href="${esc(hub.url || '')}/" target="_blank" rel="noopener">前往授权系统查看详情</a>
      <a class="btn ghost" href="${esc(hub.url || '')}/tools.html#request" target="_blank" rel="noopener">生成授权申请单</a>
      <a class="btn ghost" href="${esc(hub.url || '')}/tools.html#verify" target="_blank" rel="noopener">校验授权码</a>
    </div>` : ''}
  </div>`);

  html += panel('手动导入授权码', `<div class="panel-body">
    <div class="form-grid">
      <label class="full">授权码（粘贴完整内容，可含换行）
        <textarea id="licKey" rows="3" placeholder="OPSM2-xxxxxxxx.xxxxxxxx"></textarea>
      </label>
    </div>
    <div class="toolbar" style="margin-top:12px">
      <button class="btn ghost" id="licImport">导入并校验</button>
      <span class="spacer"></span>
      <span class="panel-note">仅在无法访问授权系统时使用；授权系统下发的授权会自动覆盖这里的内容</span>
    </div>
  </div>`);

  app().innerHTML = html;

  $('#licCopyMachine').onclick = () => copyText(data.machine || '');
  $('#licImport').onclick = async () => {
    const key = ($('#licKey').value || '').trim();
    if (!key) { toast('请先粘贴授权码', true); return; }
    try {
      const r = await post('/api/license', { key });
      toast('导入成功：' + (r.summary || ''));
      render();
    } catch (e) { toast('导入失败：' + e.message, true); }
  };
  const lp = $('#licPull');
  if (lp) {
    lp.onclick = async () => {
      try {
        const r = await post('/api/hub/check', {});
        toast(r.msg || '已同步');
        render();
      } catch (e) { toast(e.message, true); }
    };
  }
}
async function openHost(host) {
  const drawer = ensureDrawer();
  drawer.body.innerHTML = '<div class="empty">加载中…</div>';
  drawer.el.classList.add('show');
  drawer.mask.classList.add('show');
  try {
    const [disks, procs, alerts] = await Promise.all([
      getList(`/api/hosts/${encodeURIComponent(host)}/disks`),
      getList(`/api/hosts/${encodeURIComponent(host)}/processes?limit=30`),
      getList(`/api/alerts?limit=50`),
    ]);
    let html = `<div class="drawer-head"><h3>${esc(host)}</h3><span class="spacer"></span>
      <button class="btn small" id="drawerRefresh">刷新</button>
      <button class="btn small ghost" id="drawerClose">关闭</button>
      <button class="btn small primary" id="drawerCmd">下发指令</button></div>`;
    html += '<div class="tabs" id="hostTabs">';
    ['cpu', 'mem', 'load', 'net', 'diskio'].forEach((t, i) => {
      html += `<button data-metric="${t}" class="${i === 0 ? 'active' : ''}">${esc(METRIC_TABS[t])}</button>`;
    });
    html += '</div><div id="hostCharts"><div class="empty">加载中…</div></div>';

    const diskRows = disks.map((d) => [esc(d.fileSystem), esc(d.size), esc(d.used), esc(d.avail), barHtml(d.usePer)]);
    html += panel('磁盘分区', dataTable(['挂载点', '容量', '已用', '可用', '使用率'], diskRows, '无磁盘数据'));

    const procRows = procs.map((p) => [String(p.pid), esc(p.name), fmtNum(p.cpuPer) + '%', fmtNum(p.memPer, 2) + '%', `<span class="mono">${esc(p.cmdline)}</span>`]);
    html += panel('进程占用 TOP', dataTable(['PID', '进程名', 'CPU', '内存', '命令行'], procRows, '暂无进程数据（需新版本 agent 采集）'));

    const hostAlerts = alerts.filter((a) => a.hostname === host).slice(0, 10);
    html += panel('近期告警', dataTable(['类型', '级别', '内容', '时间'],
      hostAlerts.map((a) => [esc(typeCN(a.type)), levelTag(a.level), esc(a.content), esc(a.createTime)]), '无告警记录'));

    drawer.body.innerHTML = html;
    $('#drawerClose').onclick = closeDrawer;
    $('#drawerRefresh').onclick = () => openHost(host);
    $('#drawerCmd').onclick = () => openCommandBox(host);
    document.querySelectorAll('#hostTabs button').forEach((b) => {
      b.onclick = () => {
        document.querySelectorAll('#hostTabs button').forEach((x) => x.classList.remove('active'));
        b.classList.add('active');
        loadHostCharts(host, b.dataset.metric);
      };
    });
    loadHostCharts(host, 'cpu');
  } catch (e) {
    drawer.body.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
  }
}

const METRIC_TABS = { cpu: 'CPU', mem: '内存', load: '负载', net: '网络速率', diskio: '磁盘 IO' };

/* ================= 主机四方格详情（双击主机名进入） =================
   四格顺序按要求：告警在前、网速在后 —— 即
   ┌─────────────┬─────────────┐
   │  CPU        │  内存        │
   ├─────────────┼─────────────┤
   │  历史告警    │  网络速率    │
   └─────────────┴─────────────┘
   ================================================================ */
const GRID_RANGE = [15, 60, 180, 720];

async function openHostGrid(host) {
  const info = (window.__hosts || []).find((h) => h.hostname === host);
  state.gridHost = host;
  const catName = info && info.catalogName ? info.catalogName : '未分组';
  const minutes = state.gridMinutes || 60;
  state.route = 'hosts';
  app().innerHTML = `
    <div class="grid-page">
      <div class="grid-head">
        <button class="btn small ghost" id="gridBack">← 返回主机列表</button>
        <h3>${esc(host)}</h3>
        <span class="tag cat">${esc(catName)}</span>
        ${info ? `<span class="muted">${esc(info.ip)} · ${esc(info.version || '未知系统')}</span>` : ''}
        <span id="gridState"></span>
        <span class="spacer"></span>
        <div class="seg" id="gridRange">${GRID_RANGE.map((m) =>
          `<button data-min="${m}" class="${m === minutes ? 'active' : ''}">${m >= 60 ? (m / 60) + ' 小时' : m + ' 分钟'}</button>`).join('')}</div>
        <button class="btn small" id="gridRefresh">刷新</button>
        <button class="btn small ghost" id="gridCmd">下发指令</button>
        <button class="btn small ghost" id="gridDrawer">详细面板</button>
      </div>
      <div class="grid-four" id="gridFour">
        <section class="grid-cell"><div class="grid-cell-head"><h4>CPU 使用率</h4><span id="gCpuNow" class="grid-now"></span></div><div class="grid-body" id="gCpu"></div></section>
        <section class="grid-cell"><div class="grid-cell-head"><h4>内存使用率</h4><span id="gMemNow" class="grid-now"></span></div><div class="grid-body" id="gMem"></div></section>
        <section class="grid-cell"><div class="grid-cell-head"><h4>历史告警</h4><span id="gAlertCnt" class="grid-now"></span></div><div class="grid-body scroll" id="gAlert"></div></section>
        <section class="grid-cell"><div class="grid-cell-head"><h4>网络速率</h4><span id="gNetNow" class="grid-now"></span></div><div class="grid-body" id="gNet"></div></section>
      </div>
    </div>`;
  $('#gridBack').onclick = () => render();
  $('#gridRefresh').onclick = () => openHostGrid(host);
  $('#gridCmd').onclick = () => openCommandBox(host);
  $('#gridDrawer').onclick = () => openHost(host);
  document.querySelectorAll('#gridRange button').forEach((b) => {
    b.onclick = () => { state.gridMinutes = Number(b.dataset.min); openHostGrid(host); };
  });
  await loadGridData(host, minutes);
  clearInterval(state.gridTimer);
  state.gridTimer = setInterval(() => loadGridData(host, state.gridMinutes || 60), 30000);
}

async function loadGridData(host, minutes) {
  try {
    const [cpu, mem, net, alerts] = await Promise.all([
      getList(`/api/metrics?host=${encodeURIComponent(host)}&type=cpu&minutes=${minutes}`),
      getList(`/api/metrics?host=${encodeURIComponent(host)}&type=mem&minutes=${minutes}`),
      getList(`/api/metrics?host=${encodeURIComponent(host)}&type=net&minutes=${minutes}`),
      getList(`/api/alerts?host=${encodeURIComponent(host)}&limit=30`),
    ]);
    // 各格独立 try，某格失败不影响其他三格
    fillGridCell('gCpu', cpu, { pct: true });
    fillGridCell('gMem', mem, { pct: true });
    fillGridCell('gNet', net, {});
    const last = (arr) => (arr && arr.length && arr[0].points.length ? Number(arr[0].points[arr[0].points.length - 1].value) : null);
    const cpuNow = last(cpu);
    const memNow = last(mem);
    $('#gCpuNow').textContent = cpuNow === null ? '' : fmtNum(cpuNow, 1) + ' %';
    $('#gMemNow').textContent = memNow === null ? '' : fmtNum(memNow, 1) + ' %';
    const netNow = net.map((s) => {
      const v = last([s]);
      return `${s.name} ${v === null ? '-' : fmtNum(v, 1) + ' KB/s'}`;
    }).join(' · ');
    $('#gNetNow').textContent = netNow;
    renderGridAlerts(alerts);
  } catch (e) {
    toast(e.message, true);
  }
}

function fillGridCell(id, series, opts) {
  const el = document.getElementById(id);
  if (!el) return;
  try {
    el.innerHTML = lineChart(asList(series), Object.assign({ width: 520, height: 176, trimTime: true }, opts || {}));
  } catch (e) {
    el.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
  }
}

function renderGridAlerts(alerts) {
  const box = document.getElementById('gAlert');
  if (!box) return;
  const list = asList(alerts);
  const active = list.filter((a) => a.state !== '0');
  $('#gAlertCnt').textContent = list.length ? `${active.length} 条未恢复 / 共 ${list.length} 条` : '';
  if (!list.length) { box.innerHTML = '<div class="empty">该主机暂无告警记录</div>'; return; }
  box.innerHTML = '<div class="alert-list">' + list.map((a) => `
    <div class="alert-row ${a.state === '0' ? 'resolved' : ''}">
      <div class="alert-row-top">${levelTag(a.level)}<b>${esc(typeCN(a.type))}</b>
        ${a.type === 'syslog' ? '<span class="tag cat">系统事件</span>' : ''}
        <span class="muted">${esc(a.createTime)}</span></div>
      <div class="alert-row-body">${esc(a.content)}</div>
      ${a.state === '0' ? '' : `<button class="btn tiny" data-resolve="${esc(a.id)}">标记恢复</button>`}
    </div>`).join('') + '</div>';
  box.querySelectorAll('[data-resolve]').forEach((b) => {
    b.onclick = async () => {
      try {
        await post(`/api/alerts/${encodeURIComponent(b.dataset.resolve)}/resolve`, {});
        toast('已标记恢复');
        openHostGrid(state.gridHost);
      } catch (e) { toast(e.message, true); }
    };
  });
}

async function loadHostCharts(host, type) {
  const box = $('#hostCharts');
  if (!box) return;
  box.innerHTML = '<div class="empty">加载中…</div>';
  try {
    const minutes = Number(($('#rangeSel') || {}).value || 60);
    const series = await getList(`/api/metrics?host=${encodeURIComponent(host)}&type=${type}&minutes=${minutes}`);
    box.innerHTML = panel(METRIC_TABS[type] + '趋势', lineChart(series));
  } catch (e) {
    box.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
  }
}

function ensureDrawer() {
  let el = $('.drawer');
  let mask = $('.drawer-mask');
  if (!el) {
    mask = document.createElement('div');
    mask.className = 'drawer-mask';
    mask.onclick = closeDrawer;
    el = document.createElement('div');
    el.className = 'drawer';
    document.body.appendChild(mask);
    document.body.appendChild(el);
  }
  return { el, mask: mask, body: el };
}
function closeDrawer() {
  const d = ensureDrawer();
  d.el.classList.remove('show');
  d.mask.classList.remove('show');
}

/* ================= 指令下发（抽屉内表单） ================= */
async function openCommandBox(host, preset) {
  const d = ensureDrawer();
  d.el.classList.add('show');
  d.mask.classList.add('show');
  const hosts = window.__hosts || await getList('/api/hosts');
  let html = `<div class="drawer-head"><h3>下发指令</h3><span class="spacer"></span><button class="btn small ghost" id="cmdClose">关闭</button></div>`;
  html += `<div class="hint-box">指令会推送到目标主机的采集端执行，采集端需以默认配置启动（<span class="mono">-allow-command</span> 未关闭）。结果在「指令下发」模块查看。</div>`;
  html += `<div class="form-grid">
    <label>目标主机<select id="cmdHost">${hosts.map((h) => `<option ${h.hostname === host ? 'selected' : ''}>${esc(h.hostname)}</option>`).join('')}</select></label>
    <label>超时时间（秒）<input type="number" id="cmdTimeout" value="30" min="5" max="600"></label>
    <label class="full">指令内容<textarea id="cmdText" rows="4" placeholder="例如：systemctl restart nginx">${esc(preset || '')}</textarea></label>
  </div>
  <div class="modal-actions"><button class="btn primary" id="cmdSend">下发执行</button></div>
  <h3 style="margin-top:20px">最近下发记录</h3><div id="cmdHistory"><div class="empty">加载中…</div></div>`;
  d.body.innerHTML = html;
  $('#cmdClose').onclick = closeDrawer;
  $('#cmdSend').onclick = async () => {
    try {
      await post('/api/commands', {
        host: $('#cmdHost').value, cmd: $('#cmdText').value, timeoutSec: Number($('#cmdTimeout').value) || 30,
      });
      toast('指令已下发，等待采集端回包');
      loadCmdHistory($('#cmdHost').value);
    } catch (e) { toast('下发失败：' + e.message, true); }
  };
  loadCmdHistory(host);
}

async function loadCmdHistory(host) {
  const list = await getList(`/api/commands?host=${encodeURIComponent(host)}&limit=10`);
  const rows = list.map((c) => [
    esc(c.createTime), esc(c.cmd), commandStatusTag(c.status), String(c.exitCode),
    `<pre class="output">${esc((c.output || c.error || '无输出').slice(0, 1200))}</pre>`,
  ]);
  const target = $('#cmdHistory');
  if (target) target.innerHTML = dataTable(['时间', '指令', '状态', '退出码', '输出'], rows, '暂无下发记录');
}
function commandStatusTag(s) {
  const map = { pending: ['warn', '待执行'], running: ['', '执行中'], done: ['ok', '成功'], failed: ['danger', '失败'], timeout: ['danger', '超时'] };
  const it = map[s] || ['', s];
  return `<span class="tag ${it[0]}">${it[1]}</span>`;
}

/* ================= 监控项管理 ================= */
const TYPE_CN = {
  process: '进程监控', port: '端口监控', http: 'HTTP 检测', ping: 'PING 检测', docker: 'Docker 容器',
  log: '日志监控', file: '文件防篡改', nginxlog: 'Nginx 日志分析', script: '自定义脚本',
  tcp: 'TCP 端口', httpapi: 'HTTP 接口', icmp: 'ICMP 探测', snmp: 'SNMP 网络设备', mysql: 'MySQL',
  redis: 'Redis', kafka: 'Kafka', rabbitmq: 'RabbitMQ', elasticsearch: 'Elasticsearch',
  ftp: 'FTP', sftp: 'SFTP', ssh: 'SSH', ipmi: 'IPMI', k8s: 'Kubernetes', login: '主机登录',
};
// 主机资源阈值告警的类型名。单独一张表，避免混进监控项类型下拉。
const RES_ALERT_CN = {
  cpu: 'CPU 使用率', mem: '内存使用率', load: '系统负载', disk: '磁盘使用率',
  net: '网络流量', diskio: '磁盘 IO', syslog: '系统事件告警',
};
// typeCN 统一把告警 / 监控项的类型码翻译成中文。
const typeCN = (t) => TYPE_CN[t] || RES_ALERT_CN[t] || t || '—';
// ALERT_TYPE_CN 告警筛选用的完整类型表（监控项类型 + 主机资源阈值）。
const ALERT_TYPE_CN = Object.assign({}, RES_ALERT_CN, TYPE_CN);
const TARGET_TPL = {
  process: { name: 'nginx' },
  port: { host: '127.0.0.1', port: 80 },
  http: { url: 'http://127.0.0.1/health', expectCode: 200, keyword: '' },
  ping: { host: '10.0.0.1', count: 3 },
  docker: { container: '' },
  log: { path: '/var/log/app.log', keywords: 'ERROR,FATAL', maxLines: 200 },
  file: { path: '/etc/passwd', maxFiles: 200 },
  nginxlog: { path: '/var/log/nginx/access.log', errorRate: 10 },
  script: { cmd: 'systemctl is-active nginx' },
  tcp: { host: '10.0.0.1', port: 22 },
  httpapi: { url: 'http://api.example.com/health', expectCode: 200, method: 'GET', keyword: '' },
  icmp: { host: '10.0.0.1', count: 3 },
  snmp: { host: '10.0.0.1', port: 161, community: 'public', version: '2c', oids: 'sysUpTime,sysDescr', targets: '' },
  mysql: { host: '10.0.0.1', port: 3306, username: 'root', password: '', db: '', sql: 'SELECT VERSION()' },
  redis: { host: '10.0.0.1', port: 6379, password: '' },
  kafka: { host: '10.0.0.1', port: 9092 },
  rabbitmq: { host: '10.0.0.1', port: 15672, username: 'guest', password: 'guest', https: false },
  elasticsearch: { host: '10.0.0.1', port: 9200, https: false },
  ftp: { host: '10.0.0.1', port: 21, username: 'ftp', password: '' },
  sftp: { host: '10.0.0.1', port: 22, username: 'root', password: '' },
  ssh: { host: '10.0.0.1', port: 22, username: 'root', password: '', cmd: 'uptime' },
  ipmi: { host: '10.0.0.1', port: 623 },
  k8s: { host: '10.0.0.1', port: 6443, token: '', skipTls: true },
};
const TARGET_HINT = {
  file: '首次执行会建立文件指纹基线，之后内容变化即告警；目录会递归校验（可用 maxFiles 限制文件数）。',
  log: '增量扫描日志文件，命中关键字即告警；配置自动恢复命令可实现日志异常自动处理。',
  process: '进程不存在即告警；开启自动恢复并填写启动命令，可实现进程下线自动拉起。',
  snmp: 'targets 字段可批量填入多个设备 IP（逗号分隔），由服务端统一轮询多台网络设备；oids 支持 sysUpTime/sysDescr/ifInOctets 等别名。',
  k8s: 'token 需具备读取 nodes 的权限；skipTls 为 true 时忽略自签证书校验。',
  nginxlog: '按增量解析 Nginx access log，统计请求量与 5xx 错误率，超过 errorRate 阈值即告警。',
};

async function renderTasks() {
  const [tasks, states] = await Promise.all([getList('/api/tasks'), getList('/api/monitor/states')]);
  const stateOf = {};
  states.forEach((s) => { stateOf[s.taskId + '|' + s.hostname] = s; });
  const rows = tasks.map((t) => {
    const enabled = t.enabled ? '<span class="tag ok">启用</span>' : '<span class="tag off">停用</span>';
    return [
      esc(t.name), esc(typeCN(t.type)),
      t.runner === 'server' ? '<span class="tag">服务端探测</span>' : '<span class="tag">采集端执行</span>',
      esc(t.hosts === '*' ? '全部主机' : t.hosts),
      `${t.intervalSec}s / ${t.timeoutSec}s`,
      t.autoHeal ? '<span class="tag ok">已开启自动处理</span>' : '<span class="tag off">未开启</span>',
      enabled,
      `<button class="btn small" data-edit="${esc(t.id)}">编辑</button>
       <button class="btn small ghost" data-del="${esc(t.id)}">删除</button>`,
    ];
  });
  const stateRows = states.map((s) => [
    esc(s.name), esc(typeCN(s.type)), esc(s.hostname),
    s.status === '2' ? '<span class="tag danger">异常</span>' : '<span class="tag ok">正常</span>',
    `<span class="mono">${esc(s.msg)}</span>`, esc(s.checkTime),
  ]);

  let html = `<div class="toolbar">
    <select id="rangeSel"><option value="60">最近 1 小时</option><option value="360">最近 6 小时</option><option value="1440">最近 24 小时</option></select>
    <span class="spacer"></span>
    <button class="btn primary" id="newTask">+ 新建监控项</button>
    <a class="btn ghost small" href="/api/export/tasks?format=csv">导出 CSV</a>
    <a class="btn ghost small" href="/api/export/monitors?format=xlsx">导出状态 Excel</a>
  </div>`;
  html += panel('监控项配置', dataTable(['名称', '类型', '执行端', '作用主机', '间隔/超时', '自动处理', '状态', '操作'], rows,
    '暂无监控项，点击右上角新建'), '');
  html += panel('最新检查结果', dataTable(['监控项', '类型', '目标/主机', '状态', '说明', '检查时间'], stateRows, '暂无检查结果'));
  app().innerHTML = html;
  $('#newTask').onclick = () => openTaskEditor(null);
  document.querySelectorAll('[data-edit]').forEach((b) => { b.onclick = () => openTaskEditor(b.dataset.edit); });
  document.querySelectorAll('[data-del]').forEach((b) => {
    b.onclick = async () => {
      if (!confirm('确认删除该监控项？相关状态与流水会一并清理。')) return;
      try { await del(`/api/tasks/${b.dataset.del}`); toast('已删除'); render(); } catch (e) { toast('删除失败：' + e.message, true); }
    };
  });
}

async function openTaskEditor(id) {
  const hosts = window.__hosts || await getList('/api/hosts');
  const devices = await getList('/api/device-accounts');
  const task = id ? await get(`/api/tasks/${id}`) : {
    type: 'process', name: '', target: JSON.stringify(TARGET_TPL.process, null, 2),
    intervalSec: 30, timeoutSec: 5, enabled: true, hosts: '*', alertContent: '',
    autoHeal: false, healCmd: '', runner: 'agent',
  };
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>${id ? '编辑监控项' : '新建监控项'}</h3></div>
    <div class="modal-body">
      <div class="form-grid">
        <label>名称<input type="text" id="tName" value="${esc(task.name)}" placeholder="例如：核心交换机 SNMP"></label>
        <label>类型<select id="tType"></select></label>
        <label>作用主机<select id="tHosts"><option value="*">全部主机</option>${hosts.map((h) => `<option value="${esc(h.hostname)}" ${task.hosts === h.hostname ? 'selected' : ''}>${esc(h.hostname)}</option>`).join('')}</select></label>
        <label>执行端<select id="tRunner"><option value="agent">采集端执行</option><option value="server">服务端探测</option></select></label>
        <label>采集间隔（秒）<input type="number" id="tInterval" value="${task.intervalSec}" min="5"></label>
        <label>超时时间（秒）<input type="number" id="tTimeout" value="${task.timeoutSec}" min="1"></label>
        <label class="full">引用设备账号
          <span class="dev-pick">
            <select id="tDevice"><option value="">不引用，手动填写地址与口令</option>${devices.map((d) => `<option value="${esc(d.id)}">${esc(d.name)}（${esc(PROTOCOL_CN[d.protocol] || d.protocol)} ${esc(d.host || '未填地址')}）</option>`).join('')}</select>
            <button class="btn small" id="tDeviceApply" type="button">写入 deviceId</button>
            <button class="btn small ghost" id="tDeviceClear" type="button">移除</button>
          </span>
        </label>
        <label class="full">目标配置（JSON）<textarea id="tTarget" rows="8">${esc(task.target)}</textarea></label>
        <label class="full">告警说明<input type="text" id="tAlert" value="${esc(task.alertContent || '')}" placeholder="可选，出现告警时的补充说明"></label>
        <label class="full"><input type="checkbox" id="tAutoHeal" ${task.autoHeal ? 'checked' : ''}> 异常时自动执行处理命令（进程自愈 / 日志联动 / 权限还原）</label>
        <label class="full">自动处理命令<textarea id="tHeal" rows="2" placeholder="例如：systemctl restart nginx">${esc(task.healCmd || '')}</textarea></label>
        <label class="full"><input type="checkbox" id="tEnabled" ${task.enabled !== false ? 'checked' : ''}> 启用该监控项</label>
      </div>
      <div class="hint-box" id="tHint"></div>
    </div>
    <div class="modal-actions">
      <button class="btn" id="tCancel">取消</button>
      <button class="btn primary" id="tSave">保存</button>
    </div>`);
  $('#tType').innerHTML = Object.keys(TYPE_CN)
    .filter((k) => TARGET_TPL[k])
    .map((k) => `<option value="${k}">${esc(TYPE_CN[k])}（${k}）</option>`).join('');
  $('#tType').value = task.type;
  $('#tRunner').value = task.runner === 'server' ? 'server' : 'agent';
  // 回显已引用的设备账号
  try {
    const cur = JSON.parse($('#tTarget').value);
    if (cur && cur.deviceId) $('#tDevice').value = cur.deviceId;
  } catch (e) { /* 非法 JSON 时忽略回显 */ }
  const syncHint = () => {
    $('#tHint').textContent = TARGET_HINT[$('#tType').value] || 'target 为 JSON 配置，字段随类型不同，可参考模板修改。';
  };
  const syncRunner = () => {
    const isServer = SERVER_TYPES.indexOf($('#tType').value) >= 0;
    $('#tRunner').value = isServer ? 'server' : 'agent';
  };
  $('#tType').onchange = () => {
    // 切换类型时自动套用该类型的配置模板
    $('#tTarget').value = JSON.stringify(TARGET_TPL[$('#tType').value] || {}, null, 2);
    $('#tDevice').value = '';
    syncHint(); syncRunner();
  };
  // 把选中的设备账号 id 写进 target，服务端探测即可直接复用其地址与口令
  const editTarget = (fn) => {
    let obj;
    try { obj = JSON.parse($('#tTarget').value) || {}; } catch (e) { toast('当前目标配置不是合法 JSON，无法写入', true); return; }
    fn(obj);
    $('#tTarget').value = JSON.stringify(obj, null, 2);
  };
  $('#tDeviceApply').onclick = () => {
    const v = $('#tDevice').value;
    if (!v) { toast('请先选择设备账号', true); return; }
    editTarget((o) => { o.deviceId = v; });
    if (SERVER_TYPES.indexOf($('#tType').value) >= 0) $('#tRunner').value = 'server';
    toast('已写入 deviceId');
  };
  $('#tDeviceClear').onclick = () => {
    $('#tDevice').value = '';
    editTarget((o) => { delete o.deviceId; });
    toast('已移除引用');
  };
  syncHint();
  $('#tCancel').onclick = modal.close;
  $('#tSave').onclick = async () => {
    const payload = {
      id: id || '', type: $('#tType').value, name: $('#tName').value,
      target: $('#tTarget').value, intervalSec: Number($('#tInterval').value) || 30,
      timeoutSec: Number($('#tTimeout').value) || 5, hosts: $('#tHosts').value,
      alertContent: $('#tAlert').value, autoHeal: $('#tAutoHeal').checked,
      healCmd: $('#tHeal').value, enabled: $('#tEnabled').checked, runner: $('#tRunner').value,
    };
    if (!payload.name) { toast('请填写监控项名称', true); return; }
    try {
      JSON.parse(payload.target);
    } catch (e) { toast('目标配置不是合法 JSON：' + e.message, true); return; }
    try {
      if (id) await put(`/api/tasks/${id}`, payload); else await post('/api/tasks', payload);
      toast('已保存'); modal.close(); render();
    } catch (e) { toast('保存失败：' + e.message, true); }
  };
}
const SERVER_TYPES = ['tcp', 'httpapi', 'icmp', 'snmp', 'mysql', 'redis', 'kafka', 'rabbitmq', 'elasticsearch', 'ftp', 'sftp', 'ssh', 'ipmi', 'k8s'];

/* ================= 告警中心 ================= */
async function renderAlerts() {
  const [alerts, logins] = await Promise.all([getList('/api/alerts?limit=200'), getList('/api/login-events?limit=50')]);
  const active = alerts.filter((a) => a.state === '1');
  let html = `<div class="toolbar">
    <select id="aType"><option value="">全部类型</option>${Object.keys(ALERT_TYPE_CN).map((k) => `<option value="${k}">${esc(ALERT_TYPE_CN[k])}</option>`).join('')}</select>
    <select id="aState"><option value="">全部状态</option><option value="1">未恢复</option><option value="0">已恢复</option></select>
    <input type="search" id="aKeyword" placeholder="按主机或内容搜索">
    <span class="spacer"></span>
    <a class="btn ghost small" href="/api/export/alerts?format=csv">导出 CSV</a>
    <a class="btn ghost small" href="/api/export/alerts?format=xlsx">导出 Excel</a>
  </div>`;
  html += panel('告警记录', '<div id="alertBox"></div>', '');
  const loginRows = logins.map((e) => [esc(e.hostname), esc(e.diff), esc(e.users), esc(e.createTime)]);
  html += panel('主机登录提醒', dataTable(['主机', '新增登录用户', '当前在线用户', '时间'], loginRows,
    '暂无登录变化记录（由采集端上报登录用户变化自动生成）'));
  app().innerHTML = html;
  const draw = () => {
    const kw = ($('#aKeyword').value || '').toLowerCase();
    const list = alerts.filter((a) =>
      (!$('#aType').value || a.type === $('#aType').value) &&
      (!$('#aState').value || a.state === $('#aState').value) &&
      (!kw || (a.hostname + a.content).toLowerCase().includes(kw)));
    const rows = list.map((a) => [
      esc(a.hostname), esc(typeCN(a.type)), levelTag(a.level), esc(a.content), esc(a.createTime),
      a.state === '1' ? `<button class="btn small" data-resolve="${esc(a.id)}">标记恢复</button>` : '<span class="tag ok">已恢复</span>',
    ]);
    $('#alertBox').innerHTML = dataTable(['主机', '类型', '级别', '内容', '时间', '操作'], rows, '无匹配告警');
    bindDashboardActions();
  };
  ['#aType', '#aState'].forEach((s) => { $(s).onchange = draw; });
  $('#aKeyword').oninput = draw;
  draw();
}

/* ================= 指令下发模块 ================= */
async function renderCommands() {
  const [cmds, hosts] = await Promise.all([getList('/api/commands?limit=200'), getList('/api/hosts')]);
  window.__hosts = hosts;
  let html = `<div class="toolbar">
    <select id="cmdHostFilter"><option value="">全部主机</option>${hosts.map((h) => `<option>${esc(h.hostname)}</option>`).join('')}</select>
    <input type="text" id="cmdQuick" placeholder="输入一次性命令，回车快速下发">
    <span class="spacer"></span>
    <button class="btn primary" id="cmdOpen">+ 下发指令</button>
    <a class="btn ghost small" href="/api/export/commands?format=csv">导出 CSV</a>
    <a class="btn ghost small" href="/api/export/commands?format=xlsx">导出 Excel</a>
  </div>`;
  const rows = cmds.map((c) => [
    esc(c.createTime), esc(c.host), `<span class="mono">${esc(c.cmd)}</span>`, esc(c.user || '-'),
    commandStatusTag(c.status), String(c.exitCode), esc(c.finishTime || '-'),
    `<button class="btn small" data-view="${esc(c.id)}">查看输出</button>
     ${c.status === 'pending' ? `<button class="btn small ghost" data-cancel="${esc(c.id)}">撤销</button>` : ''}`,
  ]);
  html += panel('指令执行历史', dataTable(['下发时间', '主机', '指令', '执行人', '状态', '退出码', '完成时间', '操作'], rows, '暂无下发记录'));
  app().innerHTML = html;
  $('#cmdOpen').onclick = () => openCommandBox(hosts.length ? hosts[0].hostname : '');
  $('#cmdQuick').onkeydown = async (ev) => {
    if (ev.key !== 'Enter') return;
    const host = $('#cmdHostFilter').value || (hosts[0] || {}).hostname;
    if (!host) { toast('请先接入主机', true); return; }
    try { await post('/api/commands', { host, cmd: $('#cmdQuick').value, timeoutSec: 30 }); toast('已下发'); $('#cmdQuick').value = ''; render(); }
    catch (e) { toast('下发失败：' + e.message, true); }
  };
  document.querySelectorAll('[data-view]').forEach((b) => {
    b.onclick = () => {
      const c = cmds.find((x) => x.id === b.dataset.view);
      openOutputModal(c);
    };
  });
  document.querySelectorAll('[data-cancel]').forEach((b) => {
    b.onclick = async () => {
      try { await del(`/api/commands/${b.dataset.cancel}`); toast('已撤销'); render(); }
      catch (e) { toast('撤销失败：' + e.message, true); }
    };
  });
}

function openOutputModal(c) {
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>指令执行详情</h3></div>
    <div class="modal-body">
      <div class="kv"><span>目标主机</span><b>${esc(c.host)}</b></div>
      <div class="kv"><span>指令内容</span><span class="mono">${esc(c.cmd)}</span></div>
      <div class="kv"><span>状态</span>${commandStatusTag(c.status)}</div>
      <div class="kv"><span>退出码</span>${esc(c.exitCode)}</div>
      <div class="kv"><span>错误信息</span>${esc(c.error || '-')}</div>
      <div style="margin-top:10px">输出内容：</div>
      <pre class="output">${esc(c.output || '(无输出)')}</pre>
    </div>
    <div class="modal-actions"><button class="btn" id="outClose">关闭</button></div>`);
  $('#outClose').onclick = modal.close;
}

/* ================= 工具箱 ================= */
// 两类工具，界面上用标签区分「在哪里执行」：
//   [服务端探测] 由 OpsMonitor 服务端直接发包，与目标主机操作系统无关；
//   [主机本地执行] 指令下发到目标主机采集端在本地运行，受操作系统限制（按主机上报的系统过滤）。
const TOOL_OS_CN = { linux: 'Linux', windows: 'Windows', darwin: 'macOS', freebsd: 'FreeBSD' };

// 工具箱彩虹配色：按工具在各自列表中的**固定下标**取色，这样按系统过滤后
// 每张卡片的颜色不会跳来跳去（同一个工具永远是同一个色）。
const TOOL_COLORS = [
  '#2563eb', '#0d9488', '#16a34a', '#ca8a04', '#ea580c', '#dc2626',
  '#db2777', '#7c3aed', '#0891b2', '#4f46e5', '#059669', '#b45309',
];
function toolColor(i) { return TOOL_COLORS[i % TOOL_COLORS.length]; }

const TOOL_SERVER = [
  {
    key: 'icmp', name: 'Ping', desc: 'ICMP 可达性、丢包率与平均时延',
    params: [{ k: 'host', label: '目标地址', def: '', fromHost: true }, { k: 'count', label: '探测次数', def: '3' }],
  },
  {
    key: 'tcp', name: 'TCP 端口检测', desc: '检测目标端口能否连通，并给出响应耗时',
    params: [{ k: 'host', label: '目标地址', def: '', fromHost: true }, { k: 'port', label: '端口', def: '22' }],
  },
  {
    key: 'dns', name: 'DNS 查询', desc: '解析域名记录，可指定 DNS 服务器',
    params: [
      { k: 'name', label: '域名', def: 'www.baidu.com' },
      { k: 'type', label: '记录类型', def: 'A', options: ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SRV'] },
      { k: 'server', label: 'DNS 服务器（可选）', def: '' },
    ],
  },
  {
    key: 'httpapi', name: 'HTTP/HTTPS 探测', desc: '检查状态码与响应时间，可校验响应关键字',
    params: [
      { k: 'url', label: 'URL', def: 'http://' },
      { k: 'expectCode', label: '期望状态码', def: '200' },
      { k: 'keyword', label: '响应关键字（可选）', def: '' },
    ],
  },
];

// 本机执行类工具：cmds 按操作系统给出不同命令，缺哪个系统即代表该系统不支持该工具。
const TOOL_LOCAL = [
  { key: 'ping', name: 'ping 连通测试', desc: '在目标主机上 ping 指定地址',
    params: [{ k: 'target', label: '目标地址', def: '114.114.114.114' }],
    cmds: { linux: 'ping -c 4 {target}', darwin: 'ping -c 4 {target}', windows: 'ping -n 4 {target}' } },
  { key: 'ports', name: '查看监听端口', desc: '列出本机正在监听的端口与占用进程',
    cmds: { linux: 'ss -tlnp 2>/dev/null || netstat -tlnp', darwin: 'lsof -nP -iTCP -sTCP:LISTEN', windows: 'powershell -NoProfile -Command netstat -ano ^| findstr LISTENING' } },
  { key: 'conn', name: '网络连接统计', desc: '汇总本机 TCP 连接状态',
    cmds: { linux: 'ss -s', darwin: 'netstat -an | grep -c ESTABLISHED', windows: 'powershell -NoProfile -Command netstat -ano ^| findstr ESTABLISHED' } },
  { key: 'traceroute', name: '路由追踪', desc: '查看到目标地址经过的路由',
    params: [{ k: 'target', label: '目标地址', def: '114.114.114.114' }],
    cmds: { linux: 'traceroute -m 15 {target} 2>/dev/null || tracepath -m 15 {target}', darwin: 'traceroute -m 15 {target}', windows: 'tracert -h 15 -w 1000 {target}' } },
  { key: 'route', name: '查看路由表', desc: '列出本机路由条目',
    cmds: { linux: 'ip route 2>/dev/null || netstat -rn', darwin: 'netstat -rn', windows: 'route print -4' } },
  { key: 'dns', name: 'DNS 解析', desc: '在目标主机上解析域名',
    params: [{ k: 'target', label: '域名', def: 'www.baidu.com' }],
    cmds: { linux: 'nslookup {target} 2>/dev/null || getent hosts {target}', darwin: 'nslookup {target}', windows: 'nslookup {target}' } },
  { key: 'disk', name: '磁盘使用', desc: '查看磁盘分区与使用率',
    cmds: { linux: 'df -h', darwin: 'df -h',
      windows: 'powershell -NoProfile -Command Get-CimInstance Win32_LogicalDisk ^| Select-Object DeviceID,VolumeName,Size,FreeSpace' } },
  { key: 'mem', name: '内存使用', desc: '查看内存占用情况',
    cmds: { linux: 'free -m', darwin: 'vm_stat',
      windows: "powershell -NoProfile -Command '物理内存(MB) 总:{0} 可用:{1}' -f [math]::Round((Get-CimInstance Win32_OperatingSystem).TotalVisibleMemorySize/1KB), [math]::Round((Get-CimInstance Win32_OperatingSystem).FreePhysicalMemory/1KB)" } },
  { key: 'proc', name: '进程列表', desc: '按资源占用列出进程',
    cmds: { linux: 'ps aux --sort=-%cpu | head -n 16', darwin: 'ps aux | sort -nrk 3 | head -n 16', windows: 'powershell -NoProfile -Command Get-Process ^| Sort-Object WS -Descending ^| Select-Object -First 16 Id,ProcessName,CPU,WS' } },
  { key: 'uptime', name: '运行时长与负载', desc: '查看开机时长与系统负载',
    cmds: { linux: 'uptime', darwin: 'uptime', windows: 'net statistics workstation' } },
  { key: 'custom', name: '自定义命令', desc: '手工输入命令在目标主机执行（等同指令下发，请自行确认风险）', custom: true },
];

async function renderTools() {
  const hosts = await getList('/api/hosts');
  window.__hosts = hosts;
  if (!hosts.length) {
    app().innerHTML = panel('工具箱', '<div class="empty">还没有接入任何主机。请先到「添加主机」接入采集端，再回来使用工具箱。</div>');
    return;
  }
  const sorted = hosts.slice().sort((a, b) => String(a.hostname).localeCompare(String(b.hostname)));
  let html = `<div class="hint-box">
    工具箱把诊断能力分成两类，每张卡片都标了<strong>在哪里执行</strong>：
    <span class="tag">[服务端探测]</span> 由 OpsMonitor 服务端直接发包，<strong>与目标主机操作系统无关</strong>；
    <span class="tag">[主机本地执行]</span> 把命令下发到目标主机采集端在本地运行，<strong>受操作系统限制</strong>，
    已按主机上报的系统自动过滤。
  </div>`;
  html += `<div class="toolbar">
    <span class="panel-note">目标主机</span>
    <select id="toolHost">${sorted.map((h) => `<option value="${esc(h.hostname)}">${esc(h.hostname)}${h.ip ? ' · ' + esc(h.ip) : ''}</option>`).join('')}</select>
    <span id="toolOs"></span>
    <span class="spacer"></span>
    <button class="btn ghost small" id="toolReload">刷新</button>
  </div>`;
  html += '<div id="toolBody"></div>';
  app().innerHTML = html;
  $('#toolReload').onclick = () => render();
  $('#toolHost').onchange = () => renderToolBody();
  renderToolBody();
}

// currentToolHost 返回当前下拉选中的主机记录。
function currentToolHost() {
  const sel = $('#toolHost');
  const name = sel ? sel.value : '';
  return (window.__hosts || []).find((h) => h.hostname === name) || null;
}

// renderToolBody 按选中主机识别到的系统，重新渲染工具列表。
function renderToolBody() {
  const h = currentToolHost();
  const os = h ? (h.os || '') : '';
  const known = !!TOOL_OS_CN[os];
  const osLabel = known ? TOOL_OS_CN[os] : '未知';
  if ($('#toolOs')) {
    const tip = h && h.version ? `（采集端上报：${esc(h.version)}）` : '';
    $('#toolOs').innerHTML = `自动识别系统：<span class="tag ${known ? 'ok' : 'warn'}">${esc(osLabel)}</span> <span class="panel-note">${tip}</span>`;
  }
  const serverCards = TOOL_SERVER.map((t, i) => toolCard(t, 'server', i)).join('');
  // 先按原列表下标取色，再按系统过滤：保证颜色只跟工具绑定，不跟筛选结果绑定。
  // 下半部分从服务端那组的色位之后接着排，两排不会都从「蓝-青-绿-黄」开头。
  const localCards = TOOL_LOCAL.map((t, i) => [t, i])
    .filter(([t]) => (known ? (t.custom || !!(t.cmds && t.cmds[os])) : t.custom))
    .map(([t, i]) => toolCard(t, 'local', i + TOOL_SERVER.length)).join('');
  let localPanel;
  if (!known) {
    localPanel = panel('主机本地执行 · 受目标系统限制', `<div class="panel-body">
      <div class="alert-box warn">该主机尚未上报操作系统信息（老版本采集端，或还没完成一次上报），
        无法判断可用的本地命令。把采集端升级到 1.1.0+ 并等一次上报后即可自动识别；
        期间仍可使用上方的服务端探测。</div>
      <div class="tool-grid">${localCards}</div></div>`);
  } else {
    localPanel = panel(`主机本地执行 · 受目标系统限制（已按 ${osLabel} 过滤）`,
      `<div class="panel-body"><div class="tool-grid">${localCards}</div></div>`);
  }
  $('#toolBody').innerHTML = panel('服务端探测 · 与目标系统无关',
    `<div class="panel-body"><div class="tool-grid">${serverCards}</div></div>`) + localPanel;
  document.querySelectorAll('[data-tool]').forEach((el) => {
    el.onclick = () => {
      const parts = el.dataset.tool.split(':');
      openToolRunner(parts[0], parts[1]);
    };
  });
}

function toolCard(t, mode, idx) {
  const tag = mode === 'server'
    ? '<span class="tag">[服务端探测]</span>'
    : '<span class="tag">[主机本地执行]</span>';
  return `<div class="tool-card" style="--tc:${toolColor(idx || 0)}" data-tool="${esc(mode + ':' + t.key)}">
    <div class="tool-title"><span class="tool-dot"></span>${tag}<b>${esc(t.name)}</b></div>
    <div class="tool-desc">${esc(t.desc || '')}</div>
  </div>`;
}

// openToolRunner 打开工具的参数表单并执行。
function openToolRunner(mode, key) {
  const list = mode === 'server' ? TOOL_SERVER : TOOL_LOCAL;
  const t = list.find((x) => x.key === key);
  const h = currentToolHost();
  if (!t || !h) { toast('请先选择目标主机', true); return; }
  const os = h.os || '';
  const isCustom = !!t.custom;
  const fields = (t.params || []).map((p) => {
    const dv = p.fromHost && h.ip ? h.ip : (p.def || '');
    if (p.options) {
      return `<label>${esc(p.label)}<select data-p="${esc(p.k)}">${p.options.map((o) => `<option ${o === dv ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select></label>`;
    }
    return `<label>${esc(p.label)}<input data-p="${esc(p.k)}" value="${esc(dv)}"></label>`;
  }).join('');
  const tpl = mode === 'local' && !isCustom ? (t.cmds[os] || '') : '';
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>${mode === 'server' ? '[服务端探测] ' : '[主机本地执行] '}${esc(t.name)}</h3></div>
    <div class="modal-body">
      <div class="kv"><span>目标主机</span><b>${esc(h.hostname)}</b>
        <span class="panel-note">${esc(h.ip || '')} · ${esc(TOOL_OS_CN[os] || '未知系统')}</span></div>
      <div class="seg-note" style="margin:8px 0">${esc(t.desc || '')}</div>
      ${isCustom
        ? '<label>命令内容<textarea id="toolCmd" rows="3" placeholder="例如：systemctl status nginx"></textarea></label>'
        : `<div class="form-grid" id="toolParams">${fields}</div>`}
      ${tpl ? `<div style="margin-top:8px">将在目标主机执行：<span class="mono" id="toolPreview">${esc(tpl)}</span></div>` : ''}
      <div id="toolResult" style="margin-top:10px"></div>
    </div>
    <div class="modal-actions">
      <button class="btn primary" id="toolRun">执行</button>
      <button class="btn" id="toolClose">关闭</button>
    </div>`);
  $('#toolClose').onclick = modal.close;

  const refreshPreview = () => {
    const pv = $('#toolPreview');
    if (pv && tpl) pv.textContent = buildLocalCmd(tpl, collectToolParams());
  };
  document.querySelectorAll('#toolParams [data-p]').forEach((el) => {
    el.oninput = refreshPreview;
    el.onchange = refreshPreview;
  });
  refreshPreview(); // 打开即按默认参数渲染命令预览，避免显示未替换的 {占位符}

  $('#toolRun').onclick = async () => {
    const box = $('#toolResult');
    const btn = $('#toolRun');
    btn.disabled = true;
    box.innerHTML = '<div class="empty">执行中…</div>';
    try {
      if (mode === 'server') {
        const r = await post('/api/tools/probe', { type: t.key, params: collectToolParams(), timeoutSec: 10 });
        box.innerHTML = toolResultBox(!!r.ok, r.msg || '探测完成', '');
      } else {
        const cmd = (isCustom ? $('#toolCmd').value : buildLocalCmd(t.cmds[os] || '', collectToolParams())).trim();
        if (!cmd) { box.innerHTML = toolResultBox(false, '命令为空，未下发', ''); return; }
        const r = await post('/api/commands', { host: h.hostname, cmd, timeoutSec: 30 });
        box.innerHTML = `<div class="alert-box info">已下发到 <b>${esc(h.hostname)}</b>，等待采集端执行…
          <div class="mono" style="margin-top:6px">${esc(cmd)}</div></div>`;
        const c = await waitCommandResult(r.id);
        const okRun = !c.error && Number(c.exitCode) === 0;
        const title = c.status === 'timeout' ? '等待回包超时' : `执行完成 · 退出码 ${c.exitCode}`;
        const detail = [c.output, c.error ? '错误：' + c.error : ''].filter(Boolean).join('\n');
        box.innerHTML = `<div class="kv"><span>实际执行</span><span class="mono">${esc(c.cmd || cmd)}</span></div>`
          + toolResultBox(okRun, title, detail || '(无输出)');
      }
    } catch (e) {
      box.innerHTML = toolResultBox(false, '执行失败', e.message);
    } finally {
      btn.disabled = false;
    }
  };
}

// collectToolParams 收集弹窗里的参数输入。
function collectToolParams() {
  const out = {};
  document.querySelectorAll('#toolParams [data-p]').forEach((el) => { out[el.dataset.p] = el.value; });
  return out;
}

// buildLocalCmd 用参数填充命令模板里的 {占位符}；空值保留原样，便于一眼看出缺哪个参数。
function buildLocalCmd(tpl, params) {
  return String(tpl || '').replace(/\{(\w+)\}/g, (m, k) => {
    const v = params[k];
    return v === undefined || v === '' ? m : v;
  });
}

// waitCommandResult 轮询单条指令的执行结果。采集端每 5 秒拉取一次指令，因此要等一会儿。
async function waitCommandResult(id, tries) {
  const max = tries || 40; // 40 × 1.5s ≈ 60s
  for (let i = 0; i < max; i++) {
    let c = null;
    try { c = await get('/api/commands/' + encodeURIComponent(id)); } catch (e) { /* 单次失败继续等 */ }
    if (c && c.status && c.status !== 'pending' && c.status !== 'running') return c;
    await new Promise((res) => setTimeout(res, 1500));
  }
  return { status: 'timeout', exitCode: -1, output: '',
    error: '采集端未在 60 秒内回包。请确认主机在线、采集端为 1.1.0+，且未在平台侧关闭指令执行。' };
}

function toolResultBox(okRun, title, detail) {
  const body = detail ? `<pre class="output" style="margin-top:8px">${esc(detail)}</pre>` : '';
  return `<div class="alert-box ${okRun ? 'info' : 'warn'}"><b>${esc(title)}</b>${body}</div>`;
}

/* ================= 资产台账 ================= */
async function renderAssets() {
  const [list, stats] = await Promise.all([getList('/api/assets'), get('/api/assets/stats')]);
  let html = `<div class="toolbar">
    <input type="search" id="asKeyword" placeholder="搜索名称 / IP / 序列号 / 责任人">
    <select id="asCategory"><option value="">全部类别</option>${ASSET_CATEGORIES.map((c) => `<option>${c}</option>`).join('')}</select>
    <select id="asStatus"><option value="">全部状态</option>${Object.keys(ASSET_STATUS).map((k) => `<option value="${k}">${ASSET_STATUS[k]}</option>`).join('')}</select>
    <span class="spacer"></span>
    <button class="btn" id="asBatch">批量导入</button>
    <button class="btn primary" id="asNew">+ 新增资产</button>
    <a class="btn ghost small" href="/api/assets/export?format=csv">导出 CSV</a>
    <a class="btn ghost small" href="/api/assets/export?format=xlsx">导出 Excel</a>
  </div>`;
  html += '<div class="stat-row">' +
    Object.keys(ASSET_STATUS).map((k) => `<div class="mini-stat"><div class="label">${ASSET_STATUS[k]}</div><div class="value">${stats[k] || 0}</div></div>`).join('') +
    `<div class="mini-stat"><div class="label">资产总数</div><div class="value">${list.length}</div></div></div>`;
  html += panel('资产台账', '<div id="assetBox"></div>');
  app().innerHTML = html;
  const draw = () => {
    const kw = ($('#asKeyword').value || '').toLowerCase();
    const cat = $('#asCategory').value, st = $('#asStatus').value;
    const filtered = list.filter((a) =>
      (!kw || (`${a.name}${a.ip}${a.sn}${a.owner}${a.location}`.toLowerCase().includes(kw))) &&
      (!cat || a.category === cat) && (!st || a.status === st));
    const rows = filtered.map((a) => [
      esc(a.name), esc(a.category || '-'), esc(a.type || '-'), esc(a.ip || '-'), esc(a.location || '-'),
      esc(a.owner || '-'), esc(a.expireDate || '-'), statusTag(ASSET_STATUS[a.status] || a.status),
      `<button class="btn small" data-edit="${esc(a.id)}">编辑</button>
       <button class="btn small ghost" data-del="${esc(a.id)}">删除</button>`,
    ]);
    $('#assetBox').innerHTML = dataTable(['名称', '类别', '型号', 'IP', '位置', '责任人', '维保到期', '状态', '操作'], rows, '暂无资产记录');
    document.querySelectorAll('[data-edit]').forEach((b) => { b.onclick = () => openAssetEditor(list.find((x) => x.id === b.dataset.edit)); });
    document.querySelectorAll('[data-del]').forEach((b) => {
      b.onclick = async () => {
        if (!confirm('确认删除该资产？关联的设备账号也会一并删除。')) return;
        try { await del(`/api/assets/${b.dataset.del}`); toast('已删除'); render(); } catch (e) { toast('删除失败：' + e.message, true); }
      };
    });
  };
  ['#asKeyword', '#asCategory', '#asStatus'].forEach((s) => { $(s).oninput = draw; $(s).onchange = draw; });
  draw();
  $('#asNew').onclick = () => openAssetEditor(null);
  $('#asBatch').onclick = openAssetBatch;
}
const ASSET_CATEGORIES = ['服务器', '网络设备', '存储设备', '安全设备', '终端设备', '其他'];
const ASSET_STATUS = { inuse: '在用', idle: '闲置', repair: '维修中', scrap: '已报废', storage: '库存' };
function statusTag(text) {
  const tone = text === '在用' || text === '正常' ? 'ok' : text === '维修中' ? 'warn' : text === '已报废' ? 'danger' : '';
  return `<span class="tag ${tone}">${esc(text)}</span>`;
}
function openAssetEditor(asset) {
  const a = asset || {};
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>${asset ? '编辑资产' : '新增资产'}</h3></div>
    <div class="modal-body">
      <div class="form-grid">
        <label>资产名称<input id="fName" value="${esc(a.name || '')}"></label>
        <label>类别<select id="fCategory">${ASSET_CATEGORIES.map((c) => `<option ${a.category === c ? 'selected' : ''}>${c}</option>`).join('')}</select></label>
        <label>型号<input id="fType" value="${esc(a.type || '')}"></label>
        <label>状态<select id="fStatus">${Object.keys(ASSET_STATUS).map((k) => `<option value="${k}" ${(a.status || 'inuse') === k ? 'selected' : ''}>${ASSET_STATUS[k]}</option>`).join('')}</select></label>
        <label>IP 地址<input id="fIp" value="${esc(a.ip || '')}"></label>
        <label>MAC 地址<input id="fMac" value="${esc(a.mac || '')}"></label>
        <label>厂商<input id="fVendor" value="${esc(a.vendor || '')}"></label>
        <label>序列号<input id="fSn" value="${esc(a.sn || '')}"></label>
        <label>操作系统<input id="fOs" value="${esc(a.os || '')}"></label>
        <label>配置说明<input id="fSpec" value="${esc(a.spec || '')}"></label>
        <label>所在位置<input id="fLocation" value="${esc(a.location || '')}"></label>
        <label>责任人<input id="fOwner" value="${esc(a.owner || '')}"></label>
        <label>使用部门<input id="fDepartment" value="${esc(a.department || '')}"></label>
        <label>购入日期<input id="fBuyDate" value="${esc(a.buyDate || '')}" placeholder="2026-01-01"></label>
        <label>维保到期<input id="fExpireDate" value="${esc(a.expireDate || '')}" placeholder="2029-01-01"></label>
        <label>关联监控主机<input id="fBind" value="${esc(a.monitorBind || '')}" placeholder="填写 opsmonitor-agent 的主机名"></label>
        <label class="full">备注<textarea id="fRemark" rows="2">${esc(a.remark || '')}</textarea></label>
      </div>
    </div>
    <div class="modal-actions"><button class="btn" id="fCancel">取消</button><button class="btn primary" id="fSave">保存</button></div>`);
  $('#fCancel').onclick = modal.close;
  $('#fSave').onclick = async () => {
    const payload = {
      id: a.id || '', name: $('#fName').value, category: $('#fCategory').value, type: $('#fType').value,
      status: $('#fStatus').value, ip: $('#fIp').value, mac: $('#fMac').value, vendor: $('#fVendor').value,
      sn: $('#fSn').value, os: $('#fOs').value, spec: $('#fSpec').value, location: $('#fLocation').value,
      owner: $('#fOwner').value, department: $('#fDepartment').value, buyDate: $('#fBuyDate').value,
      expireDate: $('#fExpireDate').value, monitorBind: $('#fBind').value, remark: $('#fRemark').value,
    };
    if (!payload.name) { toast('请填写资产名称', true); return; }
    try {
      if (a.id) await put(`/api/assets/${a.id}`, payload); else await post('/api/assets', payload);
      toast('已保存'); modal.close(); render();
    } catch (e) { toast('保存失败：' + e.message, true); }
  };
}
function openAssetBatch() {
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>批量导入资产</h3></div>
    <div class="modal-body">
      <div class="hint-box">每行一条记录，字段顺序：<span class="mono">名称,类别,型号,IP,责任人,位置,序列号,状态,备注</span>；也可直接粘贴 JSON 数组。</div>
      <textarea id="batchText" rows="12" placeholder="web-01,服务器,PowerEdge R750,10.0.0.11,张三,IDC-A,SN123456,inuse,业务域名前端"></textarea>
    </div>
    <div class="modal-actions"><button class="btn" id="bCancel">取消</button><button class="btn primary" id="bSave">导入</button></div>`);
  $('#bCancel').onclick = modal.close;
  $('#bSave').onclick = async () => {
    const raw = $('#batchText').value.trim();
    let list = [];
    try {
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) list = parsed;
    } catch (e) {
      raw.split('\n').forEach((line) => {
        if (!line.trim()) return;
        const [name, category, type, ip, owner, location, sn, status, remark] = line.split(',');
        list.push({ name: (name || '').trim(), category: (category || '').trim(), type: (type || '').trim(),
          ip: (ip || '').trim(), owner: (owner || '').trim(), location: (location || '').trim(),
          sn: (sn || '').trim(), status: (status || 'inuse').trim(), remark: (remark || '').trim() });
      });
    }
    if (!list.length) { toast('未解析到任何记录', true); return; }
    try {
      const res = await post('/api/assets/batch', list);
      toast(`成功导入 ${res.count} 条`); modal.close(); render();
    } catch (e) { toast('导入失败：' + e.message, true); }
  };
}

/* ================= 设备账号 ================= */
async function renderDevices() {
  const [accounts, assets] = await Promise.all([getList('/api/device-accounts'), getList('/api/assets')]);
  const assetName = (id) => { const a = assets.find((x) => x.id === id); return a ? a.name : '-'; };
  const rows = accounts.map((a) => [
    esc(a.name), esc(assetName(a.assetId)), esc(PROTOCOL_CN[a.protocol] || a.protocol),
    esc(a.host || '-'), String(a.port || '-'), esc(a.username || '-'),
    a.hasSecret ? '<span class="tag ok">已保存</span>' : '<span class="tag off">未设置</span>',
    esc(a.remark || '-'),
    `<button class="btn small" data-test="${esc(a.id)}">检测</button>
     <button class="btn small ghost" data-edit="${esc(a.id)}">编辑</button>
     <button class="btn small ghost" data-del="${esc(a.id)}">删除</button>`,
  ]);
  let html = `<div class="toolbar">
    <span class="spacer"></span>
    <button class="btn primary" id="devNew">+ 新增账号</button>
  </div>
  <div class="hint-box">账号口令使用服务端密钥加密存储，列表页与接口均不返回明文。点击「检测」会用保存的凭据对设备发起一次真实连通性测试（SSH/SFTP/FTP/MySQL/Redis/SNMP/IPMI/Web 走协议校验，RDP 仅判断端口可达）。<br>
  在监控项的 target 中填写 <code>{"deviceId":"账号 ID"}</code>，即可让服务端探测直接复用这里的地址与口令；target 里显式写的字段优先级更高。</div>
  <div class="test-result" id="devTestBox" hidden></div>`;
  html += panel('设备账号', dataTable(['名称', '关联资产', '协议', '地址', '端口', '用户名', '口令', '备注', '操作'], rows, '暂无设备账号'));
  app().innerHTML = html;
  $('#devNew').onclick = () => openDeviceEditor(null, accounts, assets);
  document.querySelectorAll('[data-edit]').forEach((b) => {
    b.onclick = () => openDeviceEditor(accounts.find((x) => x.id === b.dataset.edit), accounts, assets);
  });
  document.querySelectorAll('[data-test]').forEach((b) => {
    b.onclick = async () => {
      const box = $('#devTestBox');
      const acc = accounts.find((x) => x.id === b.dataset.test) || {};
      box.hidden = false;
      box.className = 'test-result running';
      box.textContent = `正在检测「${acc.name || b.dataset.test}」…`;
      b.disabled = true;
      try {
        const r = await post(`/api/device-accounts/${b.dataset.test}/test`, {});
        box.className = 'test-result ' + (r.ok ? 'ok' : 'bad');
        box.textContent = `${acc.name || ''}（${PROTOCOL_CN[acc.protocol] || acc.protocol} ${acc.host || ''}:${acc.port || ''}）${r.ok ? '连通' : '不通'}：${r.msg}`;
        toast(r.ok ? '检测通过' : '检测未通过', !r.ok);
      } catch (e) {
        box.className = 'test-result bad';
        box.textContent = `${acc.name || ''} 检测失败：${e.message}`;
        toast('检测失败：' + e.message, true);
      } finally { b.disabled = false; }
    };
  });
  document.querySelectorAll('[data-del]').forEach((b) => {
    b.onclick = async () => {
      if (!confirm('确认删除该设备账号？')) return;
      try { await del(`/api/device-accounts/${b.dataset.del}`); toast('已删除'); render(); } catch (e) { toast('删除失败：' + e.message, true); }
    };
  });
}
const PROTOCOL_CN = { ssh: 'SSH', sftp: 'SFTP', ftp: 'FTP', rdp: '远程桌面', mysql: 'MySQL', redis: 'Redis', snmp: 'SNMP', ipmi: 'IPMI', web: 'Web 控制台' };
function openDeviceEditor(item, accounts, assets) {
  const a = item || { protocol: 'ssh', port: 22 };
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>${item ? '编辑设备账号' : '新增设备账号'}</h3></div>
    <div class="modal-body">
      <div class="form-grid">
        <label>名称<input id="dName" value="${esc(a.name || '')}" placeholder="例如：核心交换机 SSH"></label>
        <label>协议<select id="dProtocol">${Object.keys(PROTOCOL_CN).map((k) => `<option value="${k}" ${a.protocol === k ? 'selected' : ''}>${PROTOCOL_CN[k]}</option>`).join('')}</select></label>
        <label>关联资产<select id="dAsset"><option value="">未关联</option>${assets.map((x) => `<option value="${x.id}" ${a.assetId === x.id ? 'selected' : ''}>${esc(x.name)}</option>`).join('')}</select></label>
        <label>地址<input id="dHost" value="${esc(a.host || '')}"></label>
        <label>端口<input type="number" id="dPort" value="${a.port || 22}"></label>
        <label>用户名<input id="dUsername" value="${esc(a.username || '')}"></label>
        <label class="full">口令<input type="password" id="dSecret" placeholder="${item && item.hasSecret ? '留空表示保持原口令不变' : ''}"></label>
        <label class="full">备注<textarea id="dRemark" rows="2">${esc(a.remark || '')}</textarea></label>
      </div>
    </div>
    <div class="modal-actions"><button class="btn" id="dCancel">取消</button><button class="btn primary" id="dSave">保存</button></div>`);
  $('#dCancel').onclick = modal.close;
  $('#dSave').onclick = async () => {
    const payload = {
      id: a.id || '', name: $('#dName').value, protocol: $('#dProtocol').value, assetId: $('#dAsset').value,
      host: $('#dHost').value, port: Number($('#dPort').value) || 22, username: $('#dUsername').value,
      secret: $('#dSecret').value, remark: $('#dRemark').value,
    };
    if (!payload.name) { toast('请填写账号名称', true); return; }
    try {
      if (a.id) await put(`/api/device-accounts/${a.id}`, payload); else await post('/api/device-accounts', payload);
      toast('已保存'); modal.close(); render();
    } catch (e) { toast('保存失败：' + e.message, true); }
  };
}

/* ================= 巡检报告 ================= */
async function renderInspections() {
  const list = await getList('/api/inspections');
  const rows = list.map((r) => [
    `<a href="javascript:openInspection('${esc(r.id)}')">${esc(r.title)}</a>`,
    esc(r.scope || '-'), esc(r.author || '-'),
    `<span class="score-badge ${scoreClass(r.score)}">${r.score}</span>`,
    esc(r.summary || '-'), esc(r.createTime),
    `<a class="btn small ghost" href="/api/inspections/${esc(r.id)}/export?format=xlsx">导出</a>
     <button class="btn small ghost" data-del="${esc(r.id)}">删除</button>`,
  ]);
  let html = `<div class="toolbar">
    <span class="spacer"></span>
    <button class="btn primary" id="insNew">生成巡检报告</button>
  </div>
  <div class="hint-box">巡检会自动汇总主机在线率、监控项健康度、未处理告警、资产台账完整度、指令执行成功率，给出评分与整改建议，可导出 Excel 归档。</div>`;
  html += panel('巡检报告', dataTable(['标题', '巡检范围', '执行人', '评分', '结论摘要', '生成时间', '操作'], rows, '暂无巡检报告，点击右上角生成'));
  app().innerHTML = html;
  $('#insNew').onclick = async () => {
    try {
      const res = await post('/api/inspections', {});
      toast(`巡检完成，评分 ${res.score}`);
      openInspection(res.id);
    } catch (e) { toast('生成失败：' + e.message, true); }
  };
  document.querySelectorAll('[data-del]').forEach((b) => {
    b.onclick = async () => {
      if (!confirm('确认删除该巡检报告？')) return;
      try { await del(`/api/inspections/${b.dataset.del}`); toast('已删除'); render(); } catch (e) { toast(e.message, true); }
    };
  });
}
function scoreClass(score) { return score >= 90 ? 'score-good' : score >= 70 ? 'score-mid' : 'score-bad'; }
async function openInspection(id) {
  const r = await get(`/api/inspections/${id}`);
  let rep = { items: [], suggestions: [] };
  try { rep = JSON.parse(r.content); } catch (e) { /* 忽略解析失败 */ }
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>${esc(r.title)}</h3></div>
    <div class="modal-body">
      <div class="stat-row">
        <div class="mini-stat"><div class="label">综合评分</div><div class="value"><span class="score-badge ${scoreClass(r.score)}">${r.score}</span></div></div>
        <div class="mini-stat"><div class="label">巡检范围</div><div class="value" style="font-size:15px">${esc(r.scope || '全部主机')}</div></div>
        <div class="mini-stat"><div class="label">执行人</div><div class="value" style="font-size:15px">${esc(r.author || '-')}</div></div>
        <div class="mini-stat"><div class="label">生成时间</div><div class="value" style="font-size:15px">${esc(r.createTime)}</div></div>
      </div>
      <p>${esc(rep.summary || r.summary || '')}</p>
      <h4>检查明细</h4>
      ${dataTable(['检查项', '结果', '说明', '影响'], rep.items.map((i) => [
        esc(i.name), i.status === 'ok' ? '<span class="tag ok">正常</span>' : '<span class="tag danger">异常</span>',
        esc(i.detail), esc(i.impact)]), '无明细')}
      <h4 style="margin-top:16px">整改建议</h4>
      <ol>${(rep.suggestions || []).map((s) => `<li>${esc(s)}</li>`).join('') || '<li>无</li>'}</ol>
    </div>
    <div class="modal-actions">
      <a class="btn" href="/api/inspections/${esc(id)}/export?format=xlsx">导出 Excel</a>
      <button class="btn primary" id="insClose">关闭</button>
    </div>`);
  $('#insClose').onclick = modal.close;
}

/* ================= 成员权限 ================= */
async function renderMembers() {
  const list = await getList('/api/members');
  const rows = list.map((m) => [
    esc(m.username), esc(m.nickname || '-'), roleTag(m.role), esc(m.email || '-'), esc(m.phone || '-'),
    `<span class="mono">${esc((MenusCN(m.menus) || (m.role === 'admin' ? '全部' : '-')))}</span>`,
    m.enabled ? '<span class="tag ok">启用</span>' : '<span class="tag off">停用</span>',
    esc(m.lastLogin || '-'), esc(m.lastLoginIp || '-'), String(m.loginCount || 0),
    `<button class="btn small" data-edit="${esc(m.username)}">编辑</button>
     ${m.role === 'admin' ? '' : `<button class="btn small ghost" data-del="${esc(m.username)}">删除</button>`}`,
  ]);
  let html = `<div class="toolbar"><span class="spacer"></span><button class="btn primary" id="memNew">+ 新增成员</button></div>
  <div class="hint-box">成员权限按角色 + 菜单两级控制：管理员拥有全部权限；运维可下发指令与维护台账；只读仅能查看。可在此基础上逐账号勾选可见菜单。<br>
  每个账号的<strong>登录次数、最近登录时间与来源 IP</strong> 会随登录自动累加，完整的登录与操作流水见「审计日志」。</div>`;
  html += panel('成员账号', dataTable(['账号', '姓名', '角色', '邮箱', '手机', '可见菜单', '状态', '最近登录', '来源 IP', '登录次数', '操作'], rows, '无成员数据'));
  app().innerHTML = html;
  $('#memNew').onclick = () => openMemberEditor(null, list);
  document.querySelectorAll('[data-edit]').forEach((b) => {
    b.onclick = () => openMemberEditor(list.find((x) => x.username === b.dataset.edit), list);
  });
  document.querySelectorAll('[data-del]').forEach((b) => {
    b.onclick = async () => {
      if (!confirm('确认删除该成员账号？')) return;
      try { await del(`/api/members/${b.dataset.del}`); toast('已删除'); render(); } catch (e) { toast(e.message, true); }
    };
  });
}
const MENU_CN = { dashboard: '仪表盘', hosts: '主机', onboard: '添加主机', tasks: '监控项', alerts: '告警', tools: '工具箱', commands: '指令下发', assets: '资产台账', devices: '设备账号', inspections: '巡检报告', screens: '大屏', releases: '版本发布', license: '授权管理', members: '成员权限', tokens: '开放接口', audit: '审计日志', settings: '系统设置' };
function MenusCN(menus) {
  if (!menus) return '';
  return menus.split(',').filter(Boolean).map((m) => MENU_CN[m] || m).join('、');
}
function roleTag(role) {
  const map = { admin: ['ok', '管理员'], operator: ['', '运维'], viewer: ['off', '只读'] };
  const it = map[role] || ['', role];
  return `<span class="tag ${it[0]}">${it[1]}</span>`;
}
function openMemberEditor(member, list) {
  const m = member || { role: 'viewer', menus: '', enabled: true };
  const modal = ensureModal();
  modal.show(`
    <div class="modal-head"><h3>${member ? '编辑成员' : '新增成员'}</h3></div>
    <div class="modal-body">
      <div class="form-grid">
        <label>登录账号<input id="mUser" value="${esc(m.username || '')}" ${member ? 'disabled' : ''}></label>
        <label>姓名<input id="mNick" value="${esc(m.nickname || '')}"></label>
        <label>角色<select id="mRole">${[['admin', '管理员'], ['operator', '运维'], ['viewer', '只读']].map((r) => `<option value="${r[0]}" ${m.role === r[0] ? 'selected' : ''}>${r[1]}</option>`).join('')}</select></label>
        <label>状态<select id="mEnabled"><option value="1" ${m.enabled !== false ? 'selected' : ''}>启用</option><option value="0" ${m.enabled === false ? 'selected' : ''}>停用</option></select></label>
        <label>邮箱<input id="mEmail" value="${esc(m.email || '')}"></label>
        <label>手机<input id="mPhone" value="${esc(m.phone || '')}"></label>
        <label class="full">密码<input type="password" id="mPass" placeholder="${member ? '留空表示不修改密码' : '初始登录密码'}"></label>
        <label class="full">可见菜单（管理员默认全部可见）</label>
        <div class="check-grid full">${Object.keys(MENU_CN).map((k) => `<label><input type="checkbox" class="mMenu" value="${k}" ${(m.menus || '').split(',').includes(k) ? 'checked' : ''}>${MENU_CN[k]}</label>`).join('')}</div>
      </div>
    </div>
    <div class="modal-actions"><button class="btn" id="mCancel">取消</button><button class="btn primary" id="mSave">保存</button></div>`);
  $('#mCancel').onclick = modal.close;
  $('#mSave').onclick = async () => {
    const payload = {
      username: m.username || $('#mUser').value, nickname: $('#mNick').value, role: $('#mRole').value,
      email: $('#mEmail').value, phone: $('#mPhone').value, enabled: $('#mEnabled').value === '1',
      menus: Array.from(document.querySelectorAll('.mMenu:checked')).map((x) => x.value).join(','),
      password: $('#mPass').value,
    };
    if (!payload.username) { toast('请填写登录账号', true); return; }
    try {
      if (member) await put(`/api/members/${member.username}`, payload); else await post('/api/members', payload);
      toast('已保存'); modal.close(); render();
    } catch (e) { toast('保存失败：' + e.message, true); }
  };
}

/* ================= 开放接口 ================= */
async function renderTokens() {
  const list = await getList('/api/tokens');
  const rows = list.map((t) => [
    esc(t.name), `<span class="mono">${esc(t.token)}</span>`, esc(t.perms || '-'),
    t.enabled ? '<span class="tag ok">启用</span>' : '<span class="tag off">停用</span>',
    esc(t.lastUsed || '未使用'), esc(t.createTime),
    `<button class="btn small ghost" data-del="${esc(t.id)}">删除</button>`,
  ]);
  let html = `<div class="toolbar"><span class="spacer"></span><button class="btn primary" id="tkNew">+ 创建凭证</button></div>
  <div class="hint-box">开放接口用于对接第三方平台或自研看板，示例：
    <span class="mono">curl -H "X-API-Key: wgk_xxx" http://服务端地址/openapi/v1/hosts</span>；
    可用端点：/openapi/v1/hosts、/alerts、/metrics?host=xx&type=cpu、/monitors、/assets。</div>`;
  html += panel('API 凭证', dataTable(['名称', '密钥', '授权范围', '状态', '最近使用', '创建时间', '操作'], rows, '尚未创建任何凭证'));
  app().innerHTML = html;
  $('#tkNew').onclick = async () => {
    try {
      const res = await post('/api/tokens', { name: '外部系统接入' });
      toast('已创建：' + res.token);
      render();
    } catch (e) { toast(e.message, true); }
  };
  document.querySelectorAll('[data-del]').forEach((b) => {
    b.onclick = async () => {
      if (!confirm('确认删除该凭证？调用方将立即失效。')) return;
      try { await del(`/api/tokens/${b.dataset.del}`); toast('已删除'); render(); } catch (e) { toast(e.message, true); }
    };
  });
}

/* ================= 审计日志 ================= */
async function renderAudit() {
  let stats = {};
  try { stats = await get('/api/audit/stats'); } catch (e) { stats = {}; }
  const cats = stats.categories || {};
  const users = stats.users || [];
  const mini = (label, value, tone) =>
    `<div class="mini-stat"><div class="label">${esc(label)}</div><div class="value" ${tone === 'danger' ? 'style="color:var(--danger)"' : ''}>${esc(String(value))}</div></div>`;

  app().innerHTML = `
    <div class="stat-row">
      ${mini('累计记录', stats.total || 0)}
      ${mini('今日操作', stats.today || 0)}
      ${mini('今日登录', stats.authToday || 0)}
      ${mini('失败操作', stats.failed || 0, (stats.failed || 0) > 0 ? 'danger' : '')}
    </div>
    <div class="tabs" id="auditTabs">
      <button data-tab="operate" class="active">操作审计</button>
      <button data-tab="auth">用户登录</button>
      <button data-tab="host">主机登录</button>
    </div>
    <div id="auditBody"></div>`;

  const filters = { username: '', category: '', result: '', keyword: '', from: '', to: '' };
  const query = (extra) => {
    const q = new URLSearchParams();
    Object.keys(filters).forEach((k) => { if (filters[k]) q.set(k, filters[k]); });
    Object.keys(extra || {}).forEach((k) => q.set(k, extra[k]));
    return q.toString();
  };

  const draw = async () => {
    const tab = document.querySelector('#auditTabs button.active').dataset.tab;
    const body = $('#auditBody');
    if (tab === 'host') { await drawHostLogins(body); return; }
    const scope = tab === 'auth' ? 'auth' : 'operate';
    const rowOf = (e) => [
      esc(e.createTime), esc(e.username), esc(roleCN(e.role)),
      `<span class="tag ${e.result === 'fail' ? 'danger' : 'ok'}">${e.result === 'fail' ? '失败' : '成功'}</span>`,
      esc(cats[e.category] || e.category || '-'), esc(e.action), esc(e.target || '-'),
      esc(String(e.durationMs)) + ' ms', esc(e.ip || '-'), `<span class="mono">${esc(e.detail || '-')}</span>`,
    ];
    const headers = ['时间', '账号', '角色', '结果', '归类', '动作', '对象', '耗时', '来源 IP', '明细'];
    let list = [];
    try { list = await getList('/api/audit?' + query({ scope: scope, limit: '500' })); } catch (e) { /* 提示由空态承担 */ }

    body.innerHTML = `<div class="toolbar">
        <select id="auUser"><option value="">全部账号</option>${users.map((u) =>
          `<option value="${esc(u)}" ${filters.username === u ? 'selected' : ''}>${esc(u)}</option>`).join('')}</select>
        ${tab === 'operate' ? `<select id="auCat"><option value="">全部归类</option>${Object.keys(cats).map((k) =>
          `<option value="${esc(k)}" ${filters.category === k ? 'selected' : ''}>${esc(cats[k])}</option>`).join('')}</select>` : ''}
        <select id="auResult">
          <option value="">全部结果</option>
          <option value="ok" ${filters.result === 'ok' ? 'selected' : ''}>成功</option>
          <option value="fail" ${filters.result === 'fail' ? 'selected' : ''}>失败</option>
        </select>
        <input type="search" id="auKw" placeholder="按动作 / 对象 / 明细搜索" value="${esc(filters.keyword)}" style="min-width:200px">
        <input type="datetime-local" id="auFrom" value="${esc(filters.from)}" title="开始时间">
        <span class="mono">至</span>
        <input type="datetime-local" id="auTo" value="${esc(filters.to)}" title="结束时间">
        <button class="btn small" id="auSearch">查询</button>
        <button class="btn small ghost" id="auReset">重置</button>
      </div>
      <div class="hint-box">审计日志记录每一次写操作的<strong>操作人、来源 IP、动作、对象与结果</strong>，
        包含登录成功与失败；口令类字段不会写入明细。当前筛选结果 <strong>${list.length}</strong> 条${list.length >= 500 ? '（最多显示 500 条，导出可获取更多）' : ''}。</div>` +
      panel(tab === 'auth' ? '用户登录记录' : '操作审计', dataTable(headers, list.map(rowOf),
        tab === 'auth' ? '暂无用户登录记录' : '暂无操作记录'),
        `<a class="btn small ghost" href="/api/audit/export?${query({ scope: scope, format: 'csv' })}">导出 CSV</a>
         <a class="btn small ghost" href="/api/audit/export?${query({ scope: scope, format: 'xlsx' })}">导出 Excel</a>
         <button class="btn small ghost" id="auClear">清空日志</button>`);

    const readFilters = () => {
      filters.username = $('#auUser') ? $('#auUser').value : '';
      filters.category = $('#auCat') ? $('#auCat').value : '';
      filters.result = $('#auResult').value;
      filters.keyword = $('#auKw').value.trim();
      filters.from = ($('#auFrom').value || '').replace('T', ' ');
      filters.to = ($('#auTo').value || '').replace('T', ' ');
    };
    $('#auSearch').onclick = () => { readFilters(); draw(); };
    $('#auKw').onkeydown = (ev) => { if (ev.key === 'Enter') { readFilters(); draw(); } };
    $('#auReset').onclick = () => {
      Object.keys(filters).forEach((k) => { filters[k] = ''; });
      draw();
    };
    $('#auClear').onclick = async () => {
      if (!confirm('确认清空全部审计日志？该操作不可恢复（清空动作本身会留痕）。')) return;
      try { await del('/api/audit'); toast('审计日志已清空'); draw(); } catch (e) { toast('清空失败：' + e.message, true); }
    };
  };

  const drawHostLogins = async (body) => {
    let list = [];
    try { list = await getList('/api/login-events?limit=100'); } catch (e) { /* 空态 */ }
    const rows = list.map((e) => [
      esc(e.createTime), esc(e.hostname), esc(e.users || '-'),
      `<span class="tag warn">${esc(e.diff || '-')}</span>`,
    ]);
    body.innerHTML = `<div class="hint-box">主机登录提醒来自采集端上报的登录用户变化：同一主机出现新的登录用户时记录一条。
      该功能需要采集端运行，并可在「系统设置 → 告警与提醒」中关闭。</div>` +
      panel('主机登录事件', dataTable(['时间', '主机', '当前在线用户', '新增用户'], rows, '暂无主机登录事件'));
  };

  document.querySelectorAll('#auditTabs button').forEach((b) => {
    b.onclick = () => {
      document.querySelectorAll('#auditTabs button').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      draw();
    };
  });
  await draw();
}

function roleCN(role) {
  return { admin: '管理员', operator: '运维', viewer: '只读', '-': '-' }[role] || role || '-';
}

/* ================= 系统设置 ================= */
async function renderSettings() {
  const cfg = await get('/api/settings');
  window.__settings = cfg;
  const val = (k, def) => cfg[k] === undefined ? (def || '') : cfg[k];
  let html = `<div class="tabs" id="setTabs">
    <button data-tab="brand" class="active">界面定制</button>
    <button data-tab="agent">采集端配置</button>
    <button data-tab="alert">告警与提醒</button>
    <button data-tab="mail">邮件推送</button>
    <button data-tab="webhook">机器人推送</button>
  </div><div id="setBody"></div>`;
  app().innerHTML = html;
  const draw = () => {
    const tab = $('.tabs button.active').dataset.tab;
    let body = '';
    if (tab === 'brand') {
      body = `<div class="form-grid">
        <label>系统名称<input id="sName" value="${esc(val('systemName', 'OpsMonitor'))}"></label>
        <label>自定义 Logo（上传图片）<input type="file" id="sLogo" accept="image/*"></label>
        <label class="full">Logo 预览<div id="logoPreview" style="margin-top:6px">${cfg.logoData ? `<img src="${esc(cfg.logoData)}" style="height:36px">` : '<span class="mono">未设置</span>'}</div></label>
        <label class="full"><input type="checkbox" id="sHideFooter" ${val('hideFooter') === 'true' ? 'checked' : ''}> 隐藏页底版权内容</label>
        <label class="full">页底自定义文字<input id="sFooter" value="${esc(val('footerText'))}"></label>
      </div>`;
    } else if (tab === 'agent') {
      body = `<div class="hint-box">
        这些是<strong>平台统一下发</strong>给采集端的运行配置。采集端启动后自动拉取，
        在下次刷新周期内生效 —— 改完不需要登录目标主机，也不用重新安装采集端。
        如需固定用本地启动参数，可在采集端加 <code>-remote-config off</code>。
      </div>
      <div class="form-grid">
        <label>指标上报间隔（秒）
          <input type="number" id="agInterval" min="5" max="3600" value="${esc(val('agentInterval', '60'))}">
        </label>
        <label>版本检查间隔（分钟）
          <input type="number" id="agUpdateInterval" min="1" max="1440" value="${esc(val('agentUpdateInterval', '30'))}">
        </label>
        <label>配置刷新间隔（秒）
          <input type="number" id="agRefresh" min="30" max="86400" value="${esc(val('agentRefreshSec', '300'))}">
        </label>
        <label>更新策略<select id="agUpdateMode">
          ${[['auto', '自动升级（推荐）'], ['check', '只提示不升级'], ['off', '关闭版本检查']].map((m) =>
            `<option value="${m[0]}" ${val('agentUpdateMode', 'auto') === m[0] ? 'selected' : ''}>${m[1]}</option>`).join('')}
        </select></label>
        <label class="full">跳过采集的磁盘挂载点（多个以逗号分隔）
          <input id="agSkipDisk" value="${esc(val('agentSkipDisk'))}" placeholder="例如：/run,/boot/efi,/var/lib/docker">
        </label>
        <label class="full"><input type="checkbox" id="agAllowCommand" ${val('agentAllowCommand') !== 'false' ? 'checked' : ''}>
          允许平台向采集端下发并执行指令</label>
      </div>
      <div class="seg-note">关闭「执行指令」后，采集端仍正常上报指标，但会拒绝执行「指令下发」里的命令。
        「跳过采集的磁盘」用于屏蔽容器、临时挂载等不需要告警的分区。</div>
      <div id="agOverrideBox"></div>`;
    } else if (tab === 'alert') {
      body = `<div class="form-grid">
        <label>告警标题前缀<input id="sPrefix" value="${esc(val('alertPrefix', '[OpsMonitor]'))}" placeholder="[OpsMonitor]"></label>
        <label>邮件告警签名<input id="sSign" value="${esc(val('alertSignature'))}" placeholder="例如：运维值班中心"></label>
        <label>告警提示音<select id="sSound">
          ${[['off', '关闭'], ['soft', '轻柔'], ['normal', '标准'], ['urgent', '紧急']].map((s) => `<option value="${s[0]}" ${val('alertSound', 'off') === s[0] ? 'selected' : ''}>${s[1]}</option>`).join('')}
        </select></label>
        <label>主机登录提醒<input type="checkbox" id="sLoginAlert" ${val('loginAlert') === 'true' ? 'checked' : ''}></label>
      </div>
      <div class="hint-box">告警提示音在浏览器检测到新增未处理告警时播放；主机登录提醒依赖采集端上报登录用户信息。</div>`;
    } else if (tab === 'webhook') {
      body = `<div class="hint-box">
        把告警推送到钉钉、企业微信、飞书等群机器人，无需邮件服务器。
        支持同时配置多个机器人，每个可单独订阅告警级别；机器人地址本身即凭据，<strong>只有管理员可见可改</strong>。
      </div>
      <div id="hookBox"><div class="empty">加载中…</div></div>`;
    } else {
      const levels = (val('mailLevels') || 'critical,warning,info').split(',').map((s) => s.trim());
      const secure = val('mailTLS') === 'true' ? 'tls' : (val('mailStartTLS') === 'true' ? 'starttls' : 'none');
      const pwSet = cfg.mailPasswordSet === 'true';
      const lastTime = val('mailLastTime'), lastErr = val('mailLastError');
      body = `<div class="form-grid">
        <label>启用邮件推送<input type="checkbox" id="sMailEnabled" ${val('mailEnabled') === 'true' ? 'checked' : ''}></label>
        <label>SMTP 服务器<input id="sHost" value="${esc(val('mailHost'))}" placeholder="smtp.example.com"></label>
        <label>端口<input type="number" id="sPort" value="${esc(val('mailPort', '25'))}" min="1" max="65535"></label>
        <label>加密方式<select id="sSecure">
          <option value="none" ${secure === 'none' ? 'selected' : ''}>不加密（25 端口）</option>
          <option value="starttls" ${secure === 'starttls' ? 'selected' : ''}>STARTTLS（587 端口）</option>
          <option value="tls" ${secure === 'tls' ? 'selected' : ''}>隐式 TLS / SSL（465 端口）</option>
        </select></label>
        <label>邮箱账号<input id="sUser" value="${esc(val('mailUser'))}" placeholder="用于 SMTP 认证的账号"></label>
        <label>邮箱密码 / 授权码<input type="password" id="sPass" value="" placeholder="${pwSet ? '已保存，留空表示不修改' : '未设置，请输入'}"></label>
        <label>发件人<input id="sFrom" value="${esc(val('mailFrom'))}" placeholder="告警中心 &lt;ops@example.com&gt;"></label>
        <label>聚合发送间隔（秒）<input type="number" id="sInterval" value="${esc(val('mailIntervalSec', '60'))}" min="15"></label>
        <label class="full">收件人（多个以逗号分隔）<input id="sTo" value="${esc(val('mailTo'))}" placeholder="ops@example.com,oncall@example.com"></label>
        <label>免打扰开始<input type="time" id="sQuietFrom" value="${esc(val('mailQuietFrom'))}"></label>
        <label>免打扰结束<input type="time" id="sQuietTo" value="${esc(val('mailQuietTo'))}"></label>
        <label class="full">发送的告警级别
          <span class="check-grid" style="margin-top:6px">
            ${[['critical', '严重'], ['warning', '警告'], ['info', '提示']].map((lv) =>
              `<label><input type="checkbox" class="sLevel" value="${lv[0]}" ${levels.indexOf(lv[0]) >= 0 ? 'checked' : ''}> ${lv[1]}</label>`).join('')}
          </span>
        </label>
        <label class="full"><input type="checkbox" id="sRecover" ${val('mailRecover') === 'true' ? 'checked' : ''}> 监控项恢复正常时也发送通知邮件</label>
      </div>
      <div class="hint-box">邮件标题前缀与签名在「告警与提醒」中设置。免打扰时段内产生的告警不会丢失，时段结束后会补发。
        ${lastTime ? `最近一次发送：${esc(lastTime)}${lastErr ? ` · <span style="color:#b91c1c">失败：${esc(lastErr)}</span>` : ' · 成功'}` : '尚未发送过邮件'}。</div>
      <div id="mailTestBox"></div>
      <div class="modal-actions">
        <button class="btn" id="sTest">发送测试邮件</button>
        <button class="btn primary" id="sSave">保存设置</button>
      </div>
      <div id="mailLogBox"></div>`;
    }
    if (tab !== 'mail' && tab !== 'webhook') body += `<div class="modal-actions"><button class="btn primary" id="sSave">保存设置</button></div>`;
    $('#setBody').innerHTML = body;
    if ($('#sSave')) $('#sSave').onclick = saveSettings;
    if (tab === 'agent') loadAgentConfig();
    if (tab === 'webhook') loadWebhooks();
    if ($('#sLogo')) {
      $('#sLogo').onchange = (ev) => {
        const file = ev.target.files[0];
        if (!file) return;
        const reader = new FileReader();
        reader.onload = () => {
          window.__logo = reader.result;
          $('#logoPreview').innerHTML = `<img src="${esc(reader.result)}" style="height:36px">`;
        };
        reader.readAsDataURL(file);
      };
    }
    if ($('#sTest')) {
      loadMailLogs();
      $('#sTest').onclick = async () => {
        const box = $('#mailTestBox');
        const btn = $('#sTest');
        btn.disabled = true;
        box.innerHTML = '<div class="test-result running">正在发送测试邮件…</div>';
        const saved = await saveSettings();
        if (!saved) { box.innerHTML = '<div class="test-result bad">请先补全并保存邮件配置</div>'; btn.disabled = false; return; }
        try {
          const r = await post('/api/test-mail', {});
          box.innerHTML = `<div class="test-result ok">测试邮件已发送：${esc(r.msg || '')}</div>`;
          toast('测试邮件已发送');
        } catch (e) {
          box.innerHTML = `<div class="test-result bad">发送失败：${esc(e.message)}</div>`;
          toast('发送失败：' + e.message, true);
        } finally { btn.disabled = false; loadMailLogs(); }
      };
    }
  };
  document.querySelectorAll('#setTabs button').forEach((b) => {
    b.onclick = () => {
      document.querySelectorAll('#setTabs button').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      draw();
    };
  });
  draw();

  async function saveSettings() {
    const patch = {};
    const set = (k, el) => { if (el) patch[k] = el.type === 'checkbox' ? String(el.checked) : el.value; };
    const tab = $('.tabs button.active').dataset.tab;
    if (tab === 'brand') {
      set('systemName', $('#sName')); set('hideFooter', $('#sHideFooter')); set('footerText', $('#sFooter'));
      if (window.__logo) patch.logoData = window.__logo;
    } else if (tab === 'agent') {
      patch.agentInterval = $('#agInterval').value.trim();
      patch.agentUpdateInterval = $('#agUpdateInterval').value.trim();
      patch.agentRefreshSec = $('#agRefresh').value.trim();
      patch.agentSkipDisk = $('#agSkipDisk').value.trim();
      patch.agentUpdateMode = $('#agUpdateMode').value;
      patch.agentAllowCommand = String($('#agAllowCommand').checked);
      if (!(Number(patch.agentInterval) >= 5)) { toast('上报间隔至少 5 秒', true); return false; }
      if (!(Number(patch.agentUpdateInterval) >= 1)) { toast('版本检查间隔至少 1 分钟', true); return false; }
      if (!(Number(patch.agentRefreshSec) >= 30)) { toast('配置刷新间隔至少 30 秒', true); return false; }
    } else if (tab === 'alert') {
      set('alertPrefix', $('#sPrefix')); set('alertSignature', $('#sSign')); set('alertSound', $('#sSound'));
      set('loginAlert', $('#sLoginAlert'));
    } else {
      set('mailEnabled', $('#sMailEnabled')); set('mailHost', $('#sHost')); set('mailPort', $('#sPort'));
      set('mailUser', $('#sUser')); set('mailPassword', $('#sPass')); set('mailFrom', $('#sFrom'));
      set('mailTo', $('#sTo')); set('mailIntervalSec', $('#sInterval'));
      set('mailQuietFrom', $('#sQuietFrom')); set('mailQuietTo', $('#sQuietTo'));
      set('mailRecover', $('#sRecover'));
      const sec = $('#sSecure') ? $('#sSecure').value : 'none';
      patch.mailTLS = String(sec === 'tls');
      patch.mailStartTLS = String(sec === 'starttls');
      const lv = Array.prototype.slice.call(document.querySelectorAll('.sLevel:checked')).map((x) => x.value);
      patch.mailLevels = lv.join(',');
      if (!$('#sHost').value.trim() || !$('#sFrom').value.trim() || !$('#sTo').value.trim()) {
        toast('SMTP 服务器、发件人与收件人为必填项', true);
        return false;
      }
    }
    try {
      await put('/api/settings', patch);
      toast('设置已保存');
      await loadPublicSettings();
      return true;
    } catch (e) { toast('保存失败：' + e.message, true); return false; }
  }

  // 邮件发送记录：确认告警邮件到底发出去没有
  async function loadMailLogs() {
    const box = $('#mailLogBox');
    if (!box) return;
    let logs = [];
    try { logs = await getList('/api/mail/logs?limit=30'); } catch (e) { /* 接口异常不阻塞设置页 */ }
    const rows = logs.map((l) => [
      esc(l.createTime), l.kind === 'test' ? '测试邮件' : '告警推送', esc(l.subject || '-'),
      esc(l.recipients || '-'), String(l.itemCount || 0),
      l.status === 'ok' ? '<span class="tag ok">成功</span>' : '<span class="tag danger">失败</span>',
      esc(l.error || '-'),
    ]);
    box.innerHTML = `<div style="margin-top:16px"></div>` + panel('邮件发送记录',
      dataTable(['时间', '类型', '标题', '收件人', '条数', '结果', '失败原因'], rows, '暂无发送记录'),
      `<button class="btn small ghost" id="mailLogClear">清空记录</button>`);
    const clr = $('#mailLogClear');
    if (clr) {
      clr.onclick = async () => {
        if (!confirm('确认清空全部邮件发送记录？')) return;
        try { await del('/api/mail/logs'); toast('已清空'); loadMailLogs(); } catch (e) { toast('清空失败：' + e.message, true); }
      };
    }
  }
}

/* ---------------- 采集端配置：主机级覆盖 ---------------- */

// openHostAgentConfig 弹出单台主机的采集端配置，留空即沿用全局默认。
async function openHostAgentConfig(hostname) {
  const m = ensureModal();
  m.show('<div class="empty">加载中…</div>');
  let data = null;
  try {
    data = await get(`/api/hosts/${encodeURIComponent(hostname)}/agent-config`);
  } catch (e) {
    m.show(`<div class="modal-head"><h3>采集端配置 · ${esc(hostname)}</h3></div>
      <div class="modal-body"><div class="test-result bad">读取失败：${esc(e.message)}</div></div>
      <div class="modal-actions"><button class="btn" data-close>关闭</button></div>`);
    bindModalClose();
    return;
  }
  const g = data.global || {};
  const o = data.override || {};
  const e = data.effective || {};
  const inherit = (v) => (v === undefined || v === null || v === '' ? '' : String(v));
  const eInterval = o.interval ? o.interval : '';
  const eAllow = inherit(o.allowCommand);
  const eSkip = inherit(o.skipDisk);
  const eMode = inherit(o.updateMode);
  const eUpdate = o.updateInterval ? o.updateInterval : '';

  m.show(`<div class="modal-head"><h3>采集端配置 · ${esc(hostname)}</h3>
      <button class="btn ghost" data-close aria-label="关闭">✕</button></div>
    <div class="modal-body">
      <div class="seg-note">留空表示沿用全局默认值。当前生效：上报间隔 <strong>${esc(e.interval)}</strong> 秒 ·
        执行指令 <strong>${e.allowCommand ? '开启' : '关闭'}</strong> ·
        更新策略 <strong>${esc(e.updateMode)}</strong> ·
        跳过磁盘 <strong>${esc((e.skipDisk || []).join(',') || '无')}</strong>（来源：${esc(e.source === 'override' ? '主机级配置' : '全局默认')}）</div>
      <div class="form-grid">
        <label>上报间隔（秒，全局 ${esc(g.interval)}）
          <input type="number" id="hcInterval" min="5" max="3600" value="${esc(eInterval)}" placeholder="继承全局">
        </label>
        <label>版本检查间隔（分钟，全局 ${esc(g.updateInterval)}）
          <input type="number" id="hcUpdateInterval" min="1" max="1440" value="${esc(eUpdate)}" placeholder="继承全局">
        </label>
        <label>执行指令<select id="hcAllow">
          <option value="" ${eAllow === '' ? 'selected' : ''}>继承全局</option>
          <option value="true" ${eAllow === 'true' ? 'selected' : ''}>开启</option>
          <option value="false" ${eAllow === 'false' ? 'selected' : ''}>关闭</option>
        </select></label>
        <label>更新策略<select id="hcMode">
          <option value="" ${eMode === '' ? 'selected' : ''}>继承全局</option>
          ${[['auto', '自动升级'], ['check', '只提示不升级'], ['off', '关闭版本检查']].map((x) =>
            `<option value="${x[0]}" ${eMode === x[0] ? 'selected' : ''}>${x[1]}</option>`).join('')}
        </select></label>
        <label class="full">跳过采集的磁盘挂载点
          <input id="hcSkipDisk" value="${esc(eSkip)}" placeholder="留空继承全局（当前全局：${esc(g.skipDisk || '无')}）">
        </label>
      </div>
      <div id="hcResult"></div>
    </div>
    <div class="modal-actions">
      <button class="btn ghost" id="hcReset">恢复全局默认</button>
      <span class="spacer"></span>
      <button class="btn" data-close>取消</button>
      <button class="btn primary" id="hcSave">保存</button>
    </div>`);
  bindModalClose();

  $('#hcSave').onclick = async () => {
    const body = {
      interval: Number($('#hcInterval').value) || 0,
      allowCommand: $('#hcAllow').value,
      skipDisk: $('#hcSkipDisk').value.trim(),
      updateMode: $('#hcMode').value,
      updateInterval: Number($('#hcUpdateInterval').value) || 0,
    };
    try {
      await put(`/api/hosts/${encodeURIComponent(hostname)}/agent-config`, body);
      toast('已保存，该主机将在下个刷新周期内生效');
      m.close();
      if ($('#agOverrideBox')) loadAgentConfig();
    } catch (err) { $('#hcResult').innerHTML = `<div class="test-result bad" style="margin-top:12px">${esc(err.message)}</div>`; }
  };
  $('#hcReset').onclick = async () => {
    if (!confirm(`确认让「${hostname}」恢复为全局默认配置？`)) return;
    try {
      await del(`/api/hosts/${encodeURIComponent(hostname)}/agent-config`);
      toast('已恢复为全局默认配置');
      m.close();
      if ($('#agOverrideBox')) loadAgentConfig();
    } catch (err) { toast('操作失败：' + err.message, true); }
  };
}

async function loadAgentConfig() {
  const box = $('#agOverrideBox');
  if (!box) return;
  let data = { overrides: [] };
  try { data = await get('/api/agent-config'); } catch (e) {
    box.innerHTML = `<div class="seg-note">读取主机覆盖配置失败：${esc(e.message)}</div>`;
    return;
  }
  const dash = '<span class="mono">继承</span>';
  const rows = (data.overrides || []).map((o) => [
    esc(o.hostname),
    o.interval ? esc(String(o.interval)) : dash,
    o.allowCommand === 'true' ? '开启' : (o.allowCommand === 'false' ? '关闭' : dash),
    o.skipDisk ? esc(o.skipDisk) : dash,
    o.updateMode ? esc(o.updateMode) : dash,
    `<button class="btn small ghost" data-cfg="${esc(o.hostname)}">编辑</button>`,
  ]);
  box.innerHTML = `<div style="margin-top:16px"></div>` + panel('已单独配置的主机',
    dataTable(['主机', '上报间隔', '执行指令', '跳过磁盘', '更新策略', ''], rows, '暂无主机单独覆盖，全部沿用上面的全局配置'),
    '<span class="cmd-desc">也可在「主机监控」里点主机行的「采集端配置」</span>');
  box.querySelectorAll('[data-cfg]').forEach((b) => { b.onclick = () => openHostAgentConfig(b.dataset.cfg); });
}

/* ---------------- Webhook 机器人 ---------------- */

// bindModalClose 绑定弹窗内所有 data-close 元素。
function bindModalClose() {
  const m = ensureModal();
  document.querySelectorAll('.modal-mask [data-close]').forEach((b) => { b.onclick = () => m.close(); });
}

function shortUrl(u) {
  return u && u.length > 56 ? u.slice(0, 53) + '…' : (u || '');
}

async function loadWebhooks() {
  const box = $('#hookBox');
  if (!box) return;
  let data = { list: [], kinds: [] };
  try { data = await get('/api/webhooks'); } catch (e) {
    box.innerHTML = `<div class="empty">读取机器人配置失败：${esc(e.message)}</div>`;
    return;
  }
  window.__hookKinds = data.kinds || [];
  const list = data.list || [];
  const kindCN = (k) => ((data.kinds || []).find((x) => x.key === k) || {}).name || k;
  const rows = list.map((h) => [
    esc(h.name) + (h.enabled ? '' : ' <span class="tag">已停用</span>'),
    esc(kindCN(h.kind)),
    `<span class="mono ellipsis" title="${esc(h.url)}">${esc(shortUrl(h.url))}</span>`,
    h.levels ? esc(h.levels) : '<span class="mono">全部级别</span>',
    h.lastTime
      ? `${esc(h.lastTime)} ${h.lastState === 'ok' ? '<span class="tag ok">成功</span>' : `<span class="tag danger" title="${esc(h.lastError || '')}">失败</span>`}`
      : '<span class="mono">尚未推送</span>',
    `<span class="row-acts">
       <button class="btn small ghost" data-hook-test="${esc(h.id)}">测试</button>
       <button class="btn small ghost" data-hook-edit="${esc(h.id)}">编辑</button>
       <button class="btn small ghost danger" data-hook-del="${esc(h.id)}">删除</button>
     </span>`,
  ]);
  box.innerHTML = panel('机器人列表',
    dataTable(['名称', '类型', '地址', '订阅级别', '最近推送', '操作'], rows, '还没有配置机器人，点右上角「新增机器人」开始'),
    '<button class="btn primary small" id="hookAdd">新增机器人</button>')
    + '<div id="hookLogBox"></div>';

  $('#hookAdd').onclick = () => openWebhookDialog(null);
  box.querySelectorAll('[data-hook-edit]').forEach((b) => {
    b.onclick = () => openWebhookDialog(list.find((x) => x.id === b.dataset.hookEdit));
  });
  box.querySelectorAll('[data-hook-test]').forEach((b) => {
    b.onclick = async () => {
      b.disabled = true;
      const old = b.textContent;
      b.textContent = '发送中…';
      try {
        const r = await post(`/api/webhooks/${encodeURIComponent(b.dataset.hookTest)}/test`, {});
        toast(r.msg || '测试消息已发送');
      } catch (e) { toast('发送失败：' + e.message, true); }
      b.disabled = false;
      b.textContent = old;
      loadWebhooks();
    };
  });
  box.querySelectorAll('[data-hook-del]').forEach((b) => {
    b.onclick = async () => {
      const h = list.find((x) => x.id === b.dataset.hookDel) || {};
      if (!confirm(`确认删除机器人「${h.name || ''}」？`)) return;
      try { await del(`/api/webhooks/${encodeURIComponent(b.dataset.hookDel)}`); toast('已删除'); loadWebhooks(); }
      catch (e) { toast('删除失败：' + e.message, true); }
    };
  });
  loadWebhookLogs();
}

async function loadWebhookLogs() {
  const box = $('#hookLogBox');
  if (!box) return;
  let logs = [];
  try { logs = await getList('/api/mail/logs?limit=100'); } catch (e) { /* 接口异常不阻塞设置页 */ }
  const rows = (logs || []).filter((l) => l.kind === 'webhook').slice(0, 30).map((l) => [
    esc(l.createTime), l.subject === '测试消息' ? '测试消息' : '告警推送', esc(l.recipients || '-'),
    String(l.itemCount || 0),
    l.status === 'ok' ? '<span class="tag ok">成功</span>' : '<span class="tag danger">失败</span>',
    esc(l.error || '-'),
  ]);
  box.innerHTML = panel('推送记录',
    dataTable(['时间', '类型', '机器人', '条数', '结果', '失败原因'], rows, '暂无推送记录'),
    '<button class="btn small ghost" id="hookLogClear">清空记录</button>');
  const clr = $('#hookLogClear');
  if (clr) {
    clr.onclick = async () => {
      if (!confirm('确认清空全部发送记录（含邮件记录）？')) return;
      try { await del('/api/mail/logs'); toast('已清空'); loadWebhookLogs(); } catch (e) { toast('清空失败：' + e.message, true); }
    };
  }
}

// openWebhookDialog 新增或编辑一个机器人。
function openWebhookDialog(h) {
  const kinds = window.__hookKinds || [];
  const m = ensureModal();
  const cur = h || { name: '', kind: 'dingtalk', url: '', atMobiles: '', atAll: false, enabled: true, levels: '', secretSet: false };
  // 级别与显示名一一对应，只能遍历这一份表；否则每个显示名都会把全部级别生成一遍
  const picked = (cur.levels || '').split(',').map((s) => s.trim());
  const levelBox = [['critical', '严重'], ['warning', '警告'], ['info', '提示']].map(([k, name]) =>
    `<label><input type="checkbox" class="hkLevel" value="${k}" ${picked.indexOf(k) >= 0 ? 'checked' : ''}> ${name}</label>`).join('');
  m.show(`<div class="modal-head"><h3>${h ? '编辑机器人' : '新增机器人'}</h3>
      <button class="btn ghost" data-close aria-label="关闭">✕</button></div>
    <div class="modal-body">
      <div class="form-grid">
        <label>名称<input id="hkName" value="${esc(cur.name)}" placeholder="例如：运维告警群"></label>
        <label>类型<select id="hkKind">${kinds.map((k) =>
          `<option value="${k.key}" ${cur.kind === k.key ? 'selected' : ''}>${esc(k.name)}</option>`).join('')}</select></label>
        <label class="full">Webhook 地址<input id="hkUrl" value="${esc(cur.url)}" placeholder="粘贴机器人 Webhook 地址"></label>
        <label class="full" id="hkSecretWrap">密钥<input type="password" id="hkSecret" value=""
          placeholder="${cur.secretSet ? '已保存，留空表示不修改' : '未设置，如无需加签请留空'}">
          <span class="seg-note" id="hkSecretHint"></span>
        </label>
        <label class="full" id="hkAtWrap">@ 成员手机号（多个以逗号分隔，钉钉/企业微信有效）
          <input id="hkAt" value="${esc(cur.atMobiles)}" placeholder="例如：13800000000">
        </label>
        <label class="full" id="hkAtAllWrap"><input type="checkbox" id="hkAtAll" ${cur.atAll ? 'checked' : ''}> @ 所有人</label>
        <label class="full">订阅的告警级别<span class="check-grid" style="margin-top:6px">${levelBox}</span></label>
        <label class="full">不勾选表示订阅全部级别</label>
        <label class="full"><input type="checkbox" id="hkEnabled" ${cur.enabled ? 'checked' : ''}> 启用该机器人</label>
        <label class="full" id="hkClearWrap" ${cur.secretSet ? '' : 'style="display:none"'}>
          <input type="checkbox" id="hkClearSecret"> 清除已保存的密钥</label>
      </div>
      <div class="seg-note" id="hkKindHint"></div>
      <div id="hkResult"></div>
    </div>
    <div class="modal-actions">
      <span class="spacer"></span>
      <button class="btn" data-close>取消</button>
      <button class="btn primary" id="hkSave">保存</button>
    </div>`);
  bindModalClose();

  const sync = () => {
    const k = (kinds.find((x) => x.key === $('#hkKind').value) || {});
    $('#hkUrl').placeholder = k.urlHint || '粘贴机器人 Webhook 地址';
    $('#hkSecretWrap').style.display = k.secretLabel ? '' : 'none';
    $('#hkSecretHint').textContent = k.secretHint || '';
    $('#hkAtWrap').style.display = k.atSupport ? '' : 'none';
    $('#hkAtAllWrap').style.display = k.atSupport ? '' : 'none';
    $('#hkKindHint').textContent = k.atSupport
      ? '推送内容使用 markdown 格式，可 @ 指定成员。'
      : '推送内容会以该平台支持的格式发送。';
  };
  $('#hkKind').onchange = sync;
  sync();

  $('#hkSave').onclick = async () => {
    const levels = Array.prototype.slice.call(document.querySelectorAll('.hkLevel:checked')).map((x) => x.value).join(',');
    const body = {
      name: $('#hkName').value.trim(),
      kind: $('#hkKind').value,
      url: $('#hkUrl').value.trim(),
      secret: $('#hkSecret').value.trim(),
      atMobiles: $('#hkAt').value.trim(),
      atAll: $('#hkAtAll').checked,
      enabled: $('#hkEnabled').checked,
      levels,
      clearSecret: $('#hkClearSecret') ? $('#hkClearSecret').checked : false,
    };
    if (!body.url) { $('#hkResult').innerHTML = '<div class="test-result bad" style="margin-top:12px">请填写 Webhook 地址</div>'; return; }
    try {
      if (h) await put(`/api/webhooks/${encodeURIComponent(h.id)}`, body);
      else await post('/api/webhooks', body);
      toast('已保存');
      m.close();
      loadWebhooks();
    } catch (e) { $('#hkResult').innerHTML = `<div class="test-result bad" style="margin-top:12px">${esc(e.message)}</div>`; }
  };
}

/* ---------------- 弹窗组件 ---------------- */
function ensureModal() {
  let mask = $('.modal-mask');
  if (!mask) {
    mask = document.createElement('div');
    mask.className = 'modal-mask';
    mask.innerHTML = '<div class="modal-box"></div>';
    document.body.appendChild(mask);

    // 点遮罩空白处关闭。事件挂在 mask 上而不是 box 上，
    // 因此只在点击目标确实是遮罩本身时才关闭，点弹窗内容不会误关。
    mask.addEventListener('mousedown', (ev) => {
      if (ev.target === mask) mask.classList.remove('show');
    });
    // 兜底：ESC 关闭，避免只有取消按钮时用户被卡住
    document.addEventListener('keydown', (ev) => {
      if (ev.key === 'Escape' && mask.classList.contains('show')) {
        mask.classList.remove('show');
      }
    });
  }
  const box = mask.querySelector('.modal-box');
  return {
    show(html) { box.innerHTML = html; mask.classList.add('show'); },
    close() { mask.classList.remove('show'); },
  };
}

/* ---------------- 事件绑定与启动 ---------------- */
document.addEventListener('DOMContentLoaded', () => {
  loadPublicSettings();
  bootstrap();
  $('#loginForm').onsubmit = async (ev) => {
    ev.preventDefault();
    const btn = $('#loginForm button[type="submit"]');
    $('#loginErr').textContent = '';
    if (btn) { btn.disabled = true; btn.dataset.label = btn.textContent; btn.textContent = '登录中…'; }
    try {
      await request('/api/login', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: $('#username').value, password: $('#password').value }),
      });
      await bootstrap();
    } catch (e) {
      // 直接展示后端原因（「用户名或密码错误」「账号已停用」等），不再静默吞掉，
      // 避免点登录后毫无反馈让人以为「按钮坏了」。
      $('#loginErr').textContent = (e && e.message && e.message !== '未登录') ? e.message : '登录失败，请重试';
    } finally {
      if (btn) { btn.disabled = false; if (btn.dataset.label) btn.textContent = btn.dataset.label; }
    }
  };
  $('#logoutBtn').onclick = async () => {
    try { await post('/api/logout', {}); } catch (e) { /* 忽略 */ }
    state.user = null;
    showLogin(true);
  };
  $('#refreshBtn').onclick = () => render();
  $('#sidebarToggle').onclick = () => toggleSidebar();
  $('#sidebarMask').onclick = () => toggleSidebar(false);
  $('#nav').addEventListener('click', (ev) => {
    if (ev.target.tagName === 'A' && window.innerWidth <= 900) toggleSidebar(false);
  });
  window.addEventListener('keydown', (ev) => { if (ev.key === 'Escape') toggleSidebar(false); });
  window.addEventListener('hashchange', router);
  // 含表单/筛选的页面不做自动重绘，避免把用户正在填写或查询的条件冲掉
  const NO_AUTO_REFRESH = ['settings', 'audit', 'onboard', 'releases', 'license', 'members'];
  setInterval(() => {
    // 有弹窗打开时先不自动重绘：否则用户正在填的弹窗会被后台刷新打断，
    // 产生「点了没反应 / 填到一半被清掉」的错觉。
    if (document.querySelector('.modal-mask.show')) return;
    if (state.user && !window.__paused && NO_AUTO_REFRESH.indexOf(state.route) < 0) render();
  }, REFRESH_SEC * 1000);
});
