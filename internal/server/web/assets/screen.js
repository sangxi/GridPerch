/* GridPerch 监控大屏：8 套模板，数据来自 /api/screen，图表由内嵌 ECharts 渲染。
 *
 * 设计约束：
 *  1. 零 CDN 依赖 —— ECharts 已内嵌为 /assets/echarts.min.js，内网离线可用；
 *  2. 模板切换不重建已存在的图表实例，避免每 10 秒刷新都闪烁一次；
 *  3. 窗口尺寸变化与全屏切换都要 resize，ECharts 不会自动跟随。 */

const REFRESH_MS = 10000;

// LAYOUTS 描述全部模板：编号、名称、布局类名。
const LAYOUTS = {
  1: { name: '经典三栏', cls: 'layout-1' },
  2: { name: 'KPI 矩阵', cls: 'layout-2' },
  3: { name: '环形指标', cls: 'layout-3' },
  4: { name: '主机明细', cls: 'layout-4' },
  5: { name: '告警视图', cls: 'layout-5' },
  6: { name: '资源全景', cls: 'layout-6' },
  7: { name: '双屏对称', cls: 'layout-7' },
  8: { name: '容量与硬件', cls: 'layout-8' },
};

const LAYOUT_IDS = Object.keys(LAYOUTS).map(Number);
const STORE_KEY = 'gridperch_screen_layout';

let layout = Number(localStorage.getItem(STORE_KEY) || 1);
let payload = null;
let minutes = 180;
let autoTimer = null;

// charts 保存当前布局下每个图表容器的 ECharts 实例，key 用容器 id。
// 换模板时整体销毁，避免旧实例持有已移除的 DOM。
let charts = {};
let chartsLayout = 0;

function el(id) { return document.getElementById(id); }

function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, c => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

async function api(url) {
  const res = await fetch(url, { credentials: 'same-origin' });
  if (res.status === 401) { location.href = '/'; throw new Error('未登录'); }
  if (!res.ok) throw new Error('HTTP ' + res.status);
  return res.json();
}

/* ---------- 大屏标题 ---------- */

// 标题以前写死在 screen.html 里，用户没法改成自己单位的名字。
// 现在由后端算好（系统设置 →「界面定制」→ 大屏标题，留空则「系统名称 + 运维监控中心」），
// 前端只负责套用：页顶 h1 与浏览器标签页标题一起改。
const DEFAULT_TITLE = 'GridPerch 运维监控中心';

function applyTitle(t) {
  const title = String(t == null ? '' : t).trim() || DEFAULT_TITLE;
  const h = document.querySelector('.screen-head h1');
  if (h) {
    if (h.textContent !== title) h.textContent = title;
    // 名字过长时头部会省略号截断，悬停/无障碍朗读仍能看到全称
    if (h.getAttribute('title') !== title) h.setAttribute('title', title);
  }
  if (document.title !== title) document.title = title;
}

/* ---------- 格式化 ---------- */

function fmt(v, digits) {
  if (v == null || isNaN(v)) return '-';
  const n = Number(v);
  if (Math.abs(n) >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (Math.abs(n) >= 10000) return (n / 10000).toFixed(1) + 'w';
  return n.toFixed(digits == null ? 1 : digits);
}

function unitKB(v) {
  if (!v) return '0';
  if (Math.abs(v) >= 1048576) return (v / 1048576).toFixed(1) + 'GB/s';
  if (Math.abs(v) >= 1024) return (v / 1024).toFixed(1) + 'MB/s';
  return v.toFixed(0) + 'KB/s';
}

// 色阶：越高越红，投屏远处也能一眼看出压力
function colorOf(v) {
  return v >= 90 ? 'var(--red)' : v >= 75 ? 'var(--amber)' : 'var(--green)';
}
function colorHex(v) {
  if (v >= 90) return '#f87171';
  if (v >= 75) return '#fbbf24';
  return '#34d399';
}

function typeCN(t) {
  return {
    process: '进程存活', port: '端口监听', http: '服务接口', ping: '网络连通',
    docker: 'Docker 容器', file: '文件防篡改', log: '日志关键字', nginxlog: 'Nginx 日志',
    script: '自定义脚本', icmp: 'ICMP', tcp: 'TCP 端口', httpapi: 'HTTP 接口',
    snmp: 'SNMP', mysql: 'MySQL', redis: 'Redis', kafka: 'Kafka',
    rabbitmq: 'RabbitMQ', elasticsearch: 'ES', k8s: 'K8S', ftp: 'FTP',
    ssh: 'SSH', sftp: 'SFTP', ipmi: 'IPMI',
  }[t] || t;
}

function levelCN(l) {
  return { critical: '严重', warning: '警告', info: '提示' }[l] || l || '其他';
}

/* ---------- ECharts 基础配置 ---------- */

const AXIS_COLOR = 'rgba(126, 166, 201, .75)';
const SPLIT_COLOR = 'rgba(56, 189, 248, .10)';

function baseGrid(extra) {
  return Object.assign({ left: 8, right: 16, top: 28, bottom: 6, containLabel: true }, extra || {});
}

function axisX(data) {
  return {
    type: 'category',
    data,
    boundaryGap: false,
    axisLine: { lineStyle: { color: 'rgba(56, 189, 248, .3)' } },
    axisTick: { show: false },
    axisLabel: { color: AXIS_COLOR, fontSize: 11, hideOverlap: true },
  };
}

function axisY(name) {
  return {
    type: 'value',
    name,
    nameTextStyle: { color: AXIS_COLOR, fontSize: 10, padding: [0, 0, 0, -4] },
    axisLabel: { color: AXIS_COLOR, fontSize: 11 },
    splitLine: { lineStyle: { color: SPLIT_COLOR } },
    axisLine: { show: false },
    axisTick: { show: false },
  };
}

function tooltipBase() {
  return {
    trigger: 'axis',
    backgroundColor: 'rgba(8, 26, 46, .94)',
    borderColor: 'rgba(56, 189, 248, .4)',
    textStyle: { color: '#e8f4ff', fontSize: 12 },
    axisPointer: { lineStyle: { color: 'rgba(56, 189, 248, .5)' } },
  };
}

function smoothLine(name, data, color, stack) {
  return {
    name, type: 'line', smooth: true, symbol: 'none', stack,
    data,
    lineStyle: { width: 2, color },
    areaStyle: color ? {
      color: {
        type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
        colorStops: [
          { offset: 0, color: color + '55' },
          { offset: 1, color: color + '05' },
        ],
      },
    } : undefined,
  };
}

// getChart 拿到（或创建）某个容器的 ECharts 实例。
// ⚠️ render() 每次刷新都用 stage.innerHTML 重建整块 DOM，新容器的 DOM 节点已经不是
// 旧实例绑定的那一个。此时若继续对旧实例 setOption，内容会画在**已脱离文档的 canvas**
// 上——表现就是「大屏刚打开有图，刷新一次（默认 10 秒）后图表全空白」。
// 所以只要容器换了（或实例已销毁），就必须先 dispose 再重新 init。
function getChart(id, option) {
  const dom = el(id);
  if (!dom || !window.echarts) return null;

  let c = charts[id];
  if (c && (c.isDisposed() || c.getDom() !== dom)) {
    try { c.dispose(); } catch (e) { /* 忽略 */ }
    c = null;
  }
  if (!c) {
    c = window.echarts.init(dom, null, { renderer: 'canvas' });
    charts[id] = c;
  }
  c.setOption(option, true);
  return c;
}

function destroyCharts() {
  for (const k in charts) {
    try { charts[k].dispose(); } catch (e) { /* 实例可能已被移除 */ }
  }
  charts = {};
}

function resizeCharts() {
  for (const k in charts) {
    try { charts[k].resize(); } catch (e) { /* 忽略 */ }
  }
}

/* ---------- 通用片段 ---------- */

function kpi(label, value, unit, cls) {
  return `<div class="kpi ${cls || ''}">
    <div class="label">${esc(label)}</div>
    <div class="num">${value}${unit ? `<span class="u">${esc(unit)}</span>` : ''}</div>
  </div>`;
}

// kpiRow 生成一行 KPI，按数量自动分列。
function kpiRow(items) {
  return `<div class="kpi-grid" style="grid-template-columns:repeat(${items.length}, 1fr)">
    ${items.join('')}
  </div>`;
}

// rankRows 生成带序号的排行条。
function rankRows(rows, unit) {
  if (!rows || !rows.length) return '<div class="empty">暂无数据</div>';
  const max = Math.max.apply(null, rows.map(r => r.value || 0)) || 1;
  return rows.map((r, i) => `
    <div class="rank-row">
      <span class="no">${i + 1}</span>
      <span class="nm" title="${esc(r.name)}">${esc(r.name)}</span>
      <span class="bar"><i style="width:${Math.min(100, (r.value || 0) / max * 100).toFixed(1)}%;background:${colorHex(r.value)}"></i></span>
      <span class="vv">${fmt(r.value)}${unit ? ' ' + esc(unit) : ''}</span>
    </div>`).join('');
}

function alertList(alerts, limit) {
  const list = limit ? (alerts || []).slice(0, limit) : (alerts || []);
  if (!list.length) return '<div class="empty">当前无活跃告警</div>';
  return list.map(a => `
    <div class="alert-item ${esc(a.level || 'warning')}">
      <div class="top">
        <span class="host">${esc(a.hostname)}</span>
        <span class="time">${esc((a.createTime || '').slice(5, 19))}</span>
      </div>
      <div class="msg">${esc(a.content)}</div>
    </div>`).join('');
}

function runtimeRows(list, limit) {
  const rows = limit ? list.slice(0, limit) : list;
  if (!rows.length) return '<tr><td colspan="10"><div class="empty">暂无主机</div></td></tr>';
  return rows.map(h => `
    <tr>
      <td><i class="dot ${h.state === '1' ? 'on' : 'off'}"></i>${esc(h.hostname)}</td>
      <td>${esc(h.ip || '-')}</td>
      <td>${esc(h.catalogName || '未分组')}</td>
      <td class="num" style="color:${colorOf(h.cpuPer)}">${h.cpuPer.toFixed(1)}%</td>
      <td class="num" style="color:${colorOf(h.memPer)}">${h.memPer.toFixed(1)}%</td>
      <td class="num" style="color:${colorOf(h.diskPer)}">${h.diskPer.toFixed(1)}%</td>
      <td class="num">${fmt(h.load1, 2)}</td>
      <td class="num">${h.cpuTemp > 0 ? h.cpuTemp.toFixed(0) + '°' : '-'}</td>
      <td class="num">${unitKB(h.netRxKb + h.netTxKb)}</td>
      <td class="num">${esc((h.lastSeen || '').slice(11, 19) || '-')}</td>
    </tr>`).join('');
}

function runtimeTableHead() {
  return `<thead><tr>
    <th>主机</th><th>IP</th><th>分组</th>
    <th class="num">CPU</th><th class="num">内存</th><th class="num">磁盘</th>
    <th class="num">负载</th><th class="num">温度</th><th class="num">网速</th><th class="num">上报</th>
  </tr></thead>`;
}

// 趋势曲线的时间轴：只保留 MM-DD HH:MM，日期部分在 tooltip 里显示
function trendAxis(key) {
  const pts = (payload.trends || {})[key] || [];
  return pts.map(p => (p.time || '').slice(5, 16));
}
function trendData(key) {
  const pts = (payload.trends || {})[key] || [];
  return pts.map(p => p.value);
}

// rtRank 从主机运行时指标里按字段排序生成排行数据。
function rtRank(key) {
  const rt = (payload.runtimes || []).filter(h => h[key] > 0);
  return rt.slice()
    .sort((a, b) => b[key] - a[key])
    .slice(0, 10)
    .map(h => ({ name: h.hostname, value: h[key] }));
}

function onlineRate() {
  return payload.hostTotal ? (payload.hostOnline / payload.hostTotal) * 100 : 0;
}
function healthRate() {
  const m = payload.monitorSummary || {};
  return m.items ? Math.max(0, 100 - (m.badItems / m.items) * 100) : 100;
}

function smartTotal() {
  const sh = payload.smartHealth || {};
  return (sh.pass || 0) + (sh.warn || 0) + (sh.fail || 0);
}

/* ---------- 图表构造 ---------- */

function drawCpuMemTrend(id) {
  return getChart(id, {
    grid: baseGrid(),
    tooltip: tooltipBase(),
    legend: {
      data: ['CPU 使用率', '内存使用率'], top: 0, right: 4,
      itemWidth: 12, itemHeight: 8, textStyle: { color: AXIS_COLOR, fontSize: 11 },
    },
    xAxis: axisX(trendAxis('cpu')),
    yAxis: Object.assign(axisY('%'), { max: 100 }),
    series: [
      smoothLine('CPU 使用率', trendData('cpu'), '#22d3ee'),
      smoothLine('内存使用率', trendData('mem'), '#a78bfa'),
    ],
  });
}

function drawNetTrend(id) {
  return getChart(id, {
    grid: baseGrid(),
    tooltip: tooltipBase(),
    legend: {
      data: ['下行', '上行'], top: 0, right: 4,
      itemWidth: 12, itemHeight: 8, textStyle: { color: AXIS_COLOR, fontSize: 11 },
    },
    xAxis: axisX(trendAxis('netRx')),
    yAxis: axisY('KB/s'),
    series: [
      smoothLine('下行', trendData('netRx'), '#34d399'),
      smoothLine('上行', trendData('netTx'), '#60a5fa'),
    ],
  });
}

function drawDiskIOTrend(id) {
  return getChart(id, {
    grid: baseGrid(),
    tooltip: tooltipBase(),
    legend: {
      data: ['读速率', '写速率'], top: 0, right: 4,
      itemWidth: 12, itemHeight: 8, textStyle: { color: AXIS_COLOR, fontSize: 11 },
    },
    xAxis: axisX(trendAxis('diskRead')),
    yAxis: axisY('KB/s'),
    series: [
      smoothLine('读速率', trendData('diskRead'), '#fbbf24'),
      smoothLine('写速率', trendData('diskWrite'), '#f472b6'),
    ],
  });
}

function drawLoadTrend(id) {
  return getChart(id, {
    grid: baseGrid(),
    tooltip: tooltipBase(),
    xAxis: axisX(trendAxis('load')),
    yAxis: axisY('负载'),
    series: [smoothLine('1 分钟负载', trendData('load'), '#c084fc')],
  });
}

// drawRings 画一组环形指标。
function drawRings(id, items) {
  const total = items.reduce((a, b) => a + (b.value || 0), 0);
  const option = {
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(8, 26, 46, .94)',
      borderColor: 'rgba(56, 189, 248, .4)',
      textStyle: { color: '#e8f4ff', fontSize: 12 },
      formatter: p => `${p.name}<br/>${p.value.toFixed(1)}%${p.data.sub ? ' (' + p.data.sub + ')' : ''}`,
    },
    series: [{
      type: 'pie',
      radius: ['58%', '80%'],
      center: ['50%', '52%'],
      avoidLabelOverlap: false,
      label: {
        show: true, position: 'inside',
        formatter: '{b|{b}}\n{c|{c}%}',
        rich: {
          b: { color: '#9dc3e0', fontSize: 11, lineHeight: 16 },
          c: { color: '#e8f4ff', fontSize: 13, fontWeight: 'bold' },
        },
      },
      labelLine: { show: false },
      data: items.map(it => ({
        name: it.name,
        value: Math.max(0, Math.min(100, it.value)),
        sub: it.sub,
        itemStyle: { color: it.color, shadowBlur: 12, shadowColor: it.color },
      })),
    }],
  };
  if (!items.length || total <= 0) {
    option.series[0].data = [];
    option.graphic = [{
      type: 'text', left: 'center', top: 'middle',
      style: { text: '暂无数据', fill: '#7ea6c9', fontSize: 13 },
    }];
  }
  return getChart(id, option);
}

// drawHostRank 画横向排行条形图。
function drawHostRank(id, rows, color, unit) {
  const list = (rows || []).slice(0, 10).reverse();
  if (!list.length) {
    return getChart(id, {
      graphic: [{
        type: 'text', left: 'center', top: 'middle',
        style: { text: '暂无数据', fill: '#7ea6c9', fontSize: 13 },
      }],
      xAxis: { show: false }, yAxis: { show: false }, series: [],
    });
  }
  return getChart(id, {
    grid: { left: 4, right: 48, top: 6, bottom: 4, containLabel: true },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(8, 26, 46, .94)',
      borderColor: 'rgba(56, 189, 248, .4)',
      textStyle: { color: '#e8f4ff', fontSize: 12 },
      formatter: p => `${p[0].name}<br/>${fmt(p[0].value)}${unit ? ' ' + unit : ''}`,
    },
    xAxis: Object.assign(axisY(''), { axisLabel: { show: false }, splitLine: { show: false }, name: '' }),
    yAxis: {
      type: 'category',
      data: list.map(r => r.name),
      axisLine: { show: false }, axisTick: { show: false },
      axisLabel: { color: AXIS_COLOR, fontSize: 11, width: 80, overflow: 'truncate' },
    },
    series: [{
      type: 'bar',
      data: list.map(r => ({
        value: r.value,
        itemStyle: {
          color: {
            type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
            colorStops: [
              { offset: 0, color: color + '30' },
              { offset: 1, color },
            ],
          },
          borderRadius: [0, 4, 4, 0],
        },
      })),
      barMaxWidth: 13,
      label: {
        show: true, position: 'right', color: '#bfdeff', fontSize: 11,
        formatter: p => fmt(p.value, 0),
      },
    }],
  });
}

// drawAlertPie 画告警类型 / 级别分布。
function drawAlertPie(id, stats, key) {
  const list = (stats || []).filter(s => s[key]);
  if (!list.length) {
    return getChart(id, {
      graphic: [{
        type: 'text', left: 'center', top: 'middle',
        style: { text: '近 7 天无告警', fill: '#7ea6c9', fontSize: 13 },
      }],
      xAxis: { show: false }, yAxis: { show: false }, series: [],
    });
  }
  return getChart(id, {
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(8, 26, 46, .94)',
      borderColor: 'rgba(56, 189, 248, .4)',
      textStyle: { color: '#e8f4ff', fontSize: 12 },
      formatter: p => `${p.name}<br/>${p.value} 次 (${p.percent}%)`,
    },
    legend: {
      orient: 'vertical', right: 4, top: 'center',
      itemWidth: 9, itemHeight: 9, textStyle: { color: AXIS_COLOR, fontSize: 11 },
    },
    series: [{
      type: 'pie',
      radius: ['40%', '66%'],
      center: ['34%', '50%'],
      label: { show: false },
      labelLine: { show: false },
      data: list.map(s => ({
        name: key === 'level' ? levelCN(s.level) : typeCN(s.type),
        value: s.count,
        itemStyle: { color: pieColor(key === 'level' ? s.level : s.type) },
      })),
    }],
  });
}

// drawAlertDaily 画近 7 天告警柱状图。
function drawAlertDaily(id) {
  const pts = payload.alertDaily || [];
  return getChart(id, {
    grid: baseGrid({ top: 18 }),
    tooltip: tooltipBase(),
    xAxis: axisX(pts.map(p => (p.time || '').slice(5))),
    yAxis: Object.assign(axisY('条'), { minInterval: 1 }),
    series: [{
      type: 'bar',
      data: pts.map(p => ({
        value: p.value,
        itemStyle: {
          borderRadius: [4, 4, 0, 0],
          color: p.value > 0
            ? { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [
                { offset: 0, color: '#f87171' }, { offset: 1, color: 'rgba(248,113,113,.15)' }] }
            : 'rgba(148,163,184,.2)',
        },
      })),
      barMaxWidth: 28,
    }],
  });
}

// drawBarList 画横向分类计数条（监控项类型、SMART 等）。
function drawBarList(id, list, color) {
  const data = (list || []).slice().reverse();
  if (!data.length) {
    return getChart(id, {
      graphic: [{
        type: 'text', left: 'center', top: 'middle',
        style: { text: '暂无数据', fill: '#7ea6c9', fontSize: 13 },
      }],
      xAxis: { show: false }, yAxis: { show: false }, series: [],
    });
  }
  return getChart(id, {
    grid: { left: 4, right: 40, top: 6, bottom: 4, containLabel: true },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(8, 26, 46, .94)', borderColor: 'rgba(56, 189, 248, .4)',
      textStyle: { color: '#e8f4ff', fontSize: 12 },
    },
    xAxis: Object.assign(axisY(''), { axisLabel: { show: false }, splitLine: { show: false }, name: '' }),
    yAxis: {
      type: 'category',
      data: data.map(x => x.name),
      axisLine: { show: false }, axisTick: { show: false },
      axisLabel: { color: AXIS_COLOR, fontSize: 11 },
    },
    series: [{
      type: 'bar', data: data.map(x => x.value), barMaxWidth: 13,
      itemStyle: {
        borderRadius: [0, 4, 4, 0],
        color: { type: 'linear', x: 0, y: 0, x2: 1, y2: 0, colorStops: [
          { offset: 0, color: color + '30' }, { offset: 1, color }] },
      },
      label: { show: true, position: 'right', color: '#bfdeff', fontSize: 11 },
    }],
  });
}

// drawTaskType 画监控项类型分布。
function drawTaskType(id) {
  const list = Object.entries(payload.taskTypeCount || {})
    .map(([k, v]) => ({ name: typeCN(k), value: v }));
  return drawBarList(id, list, '#22d3ee');
}

// drawCatalogBar 画分组健康度堆叠条。
function drawCatalogBar(id) {
  const list = payload.catalogs || [];
  if (!list.length) {
    return getChart(id, {
      graphic: [{
        type: 'text', left: 'center', top: 'middle',
        style: { text: '尚未建立主机分组', fill: '#7ea6c9', fontSize: 13 },
      }],
      xAxis: { show: false }, yAxis: { show: false }, series: [],
    });
  }
  const order = list.slice().reverse();
  return getChart(id, {
    grid: { left: 4, right: 36, top: 24, bottom: 4, containLabel: true },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      backgroundColor: 'rgba(8, 26, 46, .94)', borderColor: 'rgba(56, 189, 248, .4)',
      textStyle: { color: '#e8f4ff', fontSize: 12 },
    },
    legend: {
      data: ['正常', '异常'], top: 0, right: 4,
      itemWidth: 10, itemHeight: 8, textStyle: { color: AXIS_COLOR, fontSize: 11 },
    },
    xAxis: Object.assign(axisY('主机数'), { axisLabel: { show: false }, splitLine: { show: false } }),
    yAxis: {
      type: 'category',
      data: order.map(c => c.name),
      axisLine: { show: false }, axisTick: { show: false },
      axisLabel: { color: AXIS_COLOR, fontSize: 11 },
    },
    series: [
      {
        name: '正常', type: 'bar', stack: 's', barMaxWidth: 14,
        data: order.map(c => Math.max(0, c.total - c.bad)),
        itemStyle: { color: '#34d399', borderRadius: [4, 0, 0, 4] },
      },
      {
        name: '异常', type: 'bar', stack: 's', barMaxWidth: 14,
        data: order.map(c => c.bad),
        itemStyle: { color: '#f87171', borderRadius: [0, 4, 4, 0] },
        label: { show: true, position: 'right', color: '#fca5a5', fontSize: 11, formatter: p => p.value > 0 ? p.value : '' },
      },
    ],
  });
}

// drawSmartPie 画硬盘 SMART 健康度分布。
function drawSmartPie(id) {
  const sh = payload.smartHealth || {};
  const data = [
    { name: '健康', value: sh.pass || 0, color: '#34d399' },
    { name: '预警', value: sh.warn || 0, color: '#fbbf24' },
    { name: '异常', value: sh.fail || 0, color: '#f87171' },
    { name: '未采集', value: sh.unknown || 0, color: 'rgba(148,163,184,.45)' },
  ].filter(d => d.value > 0);

  if (!data.length) {
    return getChart(id, {
      graphic: [{
        type: 'text', left: 'center', top: 'middle',
        style: {
          text: '暂无 SMART 数据', fill: '#7ea6c9', fontSize: 13,
        },
      }],
      xAxis: { show: false }, yAxis: { show: false }, series: [],
    });
  }
  return getChart(id, {
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(8, 26, 46, .94)', borderColor: 'rgba(56, 189, 248, .4)',
      textStyle: { color: '#e8f4ff', fontSize: 12 },
      formatter: p => `${p.name}<br/>${p.value} 台 (${p.percent}%)`,
    },
    legend: {
      orient: 'vertical', right: 4, top: 'center',
      itemWidth: 9, itemHeight: 9, textStyle: { color: AXIS_COLOR, fontSize: 11 },
    },
    series: [{
      type: 'pie', radius: ['42%', '68%'], center: ['34%', '50%'],
      label: { show: false }, labelLine: { show: false },
      data: data.map(d => ({ name: d.name, value: d.value, itemStyle: { color: d.color } })),
    }],
  });
}

const PIE_COLORS = ['#22d3ee', '#34d399', '#fbbf24', '#f87171', '#a78bfa', '#60a5fa', '#f472b6', '#2dd4bf'];
function pieColor(key) {
  if (key === 'critical') return '#f87171';
  if (key === 'warning') return '#fbbf24';
  if (key === 'info') return '#60a5fa';
  let h = 0;
  const s = String(key);
  for (let i = 0; i < s.length; i++) { h = (h * 31 + s.charCodeAt(i)) | 0; }
  return PIE_COLORS[Math.abs(h) % PIE_COLORS.length];
}

/* ---------- 模板 ---------- */

// 每个模板返回 { html, draw }：html 负责结构，draw 在 DOM 就绪后创建图表。
const TEMPLATES = {

  // 1 经典三栏：左 KPI+排行 / 中趋势与主机表 / 右告警
  1() {
    const rt = payload.runtimes || [];
    return {
      html: `
        <div class="panel">
          <h3>运行概览</h3>
          ${kpiRow([
            kpi('主机总数', payload.hostTotal, '台'),
            kpi('在线', payload.hostOnline, '台', 'green'),
            kpi('离线', payload.hostOffline, '台', payload.hostOffline ? 'red' : ''),
            kpi('活跃告警', payload.alertActive, '条', payload.alertActive ? 'red' : 'green'),
          ])}
          <h3 style="margin-top:14px">CPU 占用 TOP</h3>
          <div class="scroll-list">${rankRows(rtRank('cpuPer'), '%')}</div>
        </div>

        <div style="display:grid;grid-template-columns:1fr 1fr;grid-template-rows:1.1fr 1fr;gap:12px;min-height:0">
          <div class="panel"><h3>CPU / 内存趋势<span class="sub">近 ${minutes} 分钟</span></h3><div id="c-cpu" class="chart"></div></div>
          <div class="panel"><h3>网络流量趋势</h3><div id="c-net" class="chart"></div></div>
          <div class="panel" style="grid-column:span 2">
            <h3>主机实时状态<span class="sub">${rt.length} 台</span></h3>
            <div class="tbl-wrap"><table>${runtimeTableHead()}<tbody>${runtimeRows(rt, 40)}</tbody></table></div>
          </div>
        </div>

        <div class="panel">
          <h3>实时告警<span class="sub">最新 20 条</span></h3>
          <div class="scroll-list">${alertList(payload.alerts, 20)}</div>
        </div>`,
      draw() {
        drawCpuMemTrend('c-cpu');
        drawNetTrend('c-net');
      },
    };
  },

  // 2 KPI 矩阵：十大指标 + 六宫格
  2() {
    const st = smartTotal();
    return {
      html: `
        <div class="area-kpi">
          ${kpi('主机总数', payload.hostTotal, '台')}
          ${kpi('在线', payload.hostOnline, '台', 'green')}
          ${kpi('离线', payload.hostOffline, '', payload.hostOffline ? 'red' : '')}
          ${kpi('活跃告警', payload.alertActive, '', payload.alertActive ? 'red' : 'green')}
          ${kpi('平均CPU', fmt(payload.avgCpu), '%', payload.avgCpu >= 90 ? 'red' : 'cyan')}
          ${kpi('平均内存', fmt(payload.avgMem), '%', payload.avgMem >= 90 ? 'red' : 'violet')}
          ${kpi('平均磁盘', fmt(payload.avgDisk), '%', payload.avgDisk >= 90 ? 'red' : 'blue')}
          ${kpi('最高温度', payload.maxCpuTemp > 0 ? fmt(payload.maxCpuTemp, 0) : '-', '°', payload.maxCpuTemp >= 80 ? 'amber' : '')}
          ${kpi('连接数', payload.connTotal || 0, '', '')}
          ${kpi('SMART', st ? `${(payload.smartHealth || {}).pass || 0}/${st}` : '-', '', st && ((payload.smartHealth || {}).pass || 0) < st ? 'amber' : 'green')}
        </div>
        <div class="area-main">
          <div class="panel"><h3>CPU / 内存趋势<span class="sub">近 ${minutes} 分钟</span></h3><div id="c-trend" class="chart"></div></div>
          <div class="panel"><h3>近 7 天告警</h3><div id="c-daily" class="chart"></div></div>
          <div class="panel"><h3>磁盘 IO 趋势</h3><div id="c-dio" class="chart"></div></div>
          <div class="panel"><h3>磁盘占用 TOP</h3><div id="c-disk" class="chart"></div></div>
          <div class="panel"><h3>CPU 占用 TOP</h3><div id="c-cpu-rank" class="chart"></div></div>
          <div class="panel"><h3>监控项类型</h3><div id="c-task" class="chart"></div></div>
        </div>`,
      draw() {
        drawCpuMemTrend('c-trend');
        drawAlertDaily('c-daily');
        drawDiskIOTrend('c-dio');
        drawHostRank('c-disk', payload.topDisk, '#22d3ee', '%');
        drawHostRank('c-cpu-rank', rtRank('cpuPer'), '#f87171', '%');
        drawTaskType('c-task');
      },
    };
  },

  // 3 环形指标：四组环形
  3() {
    const sh = payload.smartHealth || {};
    const st = smartTotal();
    return {
      html: `
        <div class="panel">
          <h3>主机在线率<span class="sub">${payload.hostOnline}/${payload.hostTotal} 台</span></h3>
          <div id="c-ring-1" class="chart"></div>
        </div>
        <div class="panel">
          <h3>资源压力<span class="sub">CPU / 内存 / 磁盘</span></h3>
          <div id="c-ring-2" class="chart"></div>
        </div>
        <div class="panel">
          <h3>监控项健康度<span class="sub">${((payload.monitorSummary || {}).items || 0) - ((payload.monitorSummary || {}).badItems || 0)}/${(payload.monitorSummary || {}).items || 0}</span></h3>
          <div id="c-ring-3" class="chart"></div>
        </div>
        <div class="panel">
          <h3>硬盘 SMART<span class="sub">${st ? `已采集 ${st} 台` : '需要 smartctl'}</span></h3>
          <div id="c-ring-4" class="chart"></div>
        </div>`,
      draw() {
        drawRings('c-ring-1', [{
          name: '在线率', value: onlineRate(), sub: `${payload.hostOnline}/${payload.hostTotal}`,
          color: payload.hostOffline ? '#fbbf24' : '#34d399',
        }]);
        drawRings('c-ring-2', [
          { name: 'CPU', value: payload.avgCpu, sub: '平均', color: '#22d3ee' },
          { name: '内存', value: payload.avgMem, sub: '平均', color: '#a78bfa' },
          { name: '磁盘', value: payload.avgDisk, sub: '平均', color: '#60a5fa' },
        ]);
        drawRings('c-ring-3', [{
          name: '健康度', value: healthRate(),
          sub: `${((payload.monitorSummary || {}).items || 0) - ((payload.monitorSummary || {}).badItems || 0)}/${(payload.monitorSummary || {}).items || 0}`,
          color: (payload.monitorSummary || {}).badItems ? '#fbbf24' : '#34d399',
        }]);
        drawSmartPie('c-ring-4');
      },
    };
  },

  // 4 主机明细：KPI + 全量表格
  4() {
    const rt = payload.runtimes || [];
    return {
      html: `
        <div class="area-kpi">
          ${kpi('主机总数', payload.hostTotal, '台')}
          ${kpi('在线', payload.hostOnline, '台', 'green')}
          ${kpi('离线', payload.hostOffline, '', payload.hostOffline ? 'red' : '')}
          ${kpi('平均CPU', fmt(payload.avgCpu), '%')}
          ${kpi('平均内存', fmt(payload.avgMem), '%')}
          ${kpi('平均磁盘', fmt(payload.avgDisk), '%')}
          ${kpi('平均负载', fmt(payload.avgLoad, 2), '')}
          ${kpi('进程快照', payload.totalProcess || 0, '个')}
          ${kpi('连接数', payload.connTotal || 0, '', '')}
          ${kpi('活跃告警', payload.alertActive, '', payload.alertActive ? 'red' : 'green')}
        </div>
        <div class="panel area-table">
          <h3>主机实时状态全表<span class="sub">${rt.length} 台 · 每 ${REFRESH_MS / 1000} 秒刷新</span></h3>
          <div class="tbl-wrap"><table>${runtimeTableHead()}<tbody>${runtimeRows(rt)}</tbody></table></div>
        </div>`,
      draw() {},
    };
  },

  // 5 告警视图
  5() {
    return {
      html: `
        <div class="panel">
          <h3>告警概览</h3>
          ${kpiRow([
            kpi('活跃告警', payload.alertActive, '条', payload.alertActive ? 'red' : 'green'),
            kpi('异常监控项', (payload.monitorSummary || {}).badItems || 0, '项',
              (payload.monitorSummary || {}).badItems ? 'red' : 'green'),
          ])}
          <div style="height:12px"></div>
          ${kpiRow([
            kpi('离线主机', payload.hostOffline, '台', payload.hostOffline ? 'red' : ''),
            kpi('在线率', fmt(onlineRate()), '%', 'green'),
          ])}
          <h3 style="margin-top:14px">告警级别分布</h3>
          <div id="c-alert-level" class="chart"></div>
        </div>
        <div class="panel">
          <h3>告警流水<span class="sub">实时</span></h3>
          <div class="scroll-list">${alertList(payload.alerts, 60)}</div>
        </div>
        <div style="display:grid;grid-template-rows:1fr 1fr;gap:12px;min-height:0">
          <div class="panel"><h3>近 7 天告警趋势</h3><div id="c-alert-daily" class="chart"></div></div>
          <div class="panel"><h3>告警类型分布</h3><div id="c-alert-type" class="chart"></div></div>
        </div>`,
      draw() {
        drawAlertPie('c-alert-level', (payload.alertStats || []).filter(s => s.level), 'level');
        drawAlertDaily('c-alert-daily');
        drawAlertPie('c-alert-type', (payload.alertStats || []).filter(s => s.type), 'type');
      },
    };
  },

  // 6 资源全景：十二宫格
  6() {
    return {
      html: `
        <div class="panel"><h3>CPU / 内存趋势<span class="sub">近 ${minutes} 分钟</span></h3><div id="c6-trend" class="chart"></div></div>
        <div class="panel"><h3>网络流量趋势</h3><div id="c6-net" class="chart"></div></div>
        <div class="panel"><h3>磁盘 IO 趋势</h3><div id="c6-dio" class="chart"></div></div>
        <div class="panel"><h3>系统负载趋势</h3><div id="c6-load" class="chart"></div></div>
        <div class="panel"><h3>CPU 占用 TOP</h3><div id="c6-cpu" class="chart"></div></div>
        <div class="panel"><h3>内存占用 TOP</h3><div id="c6-mem" class="chart"></div></div>
        <div class="panel"><h3>磁盘占用 TOP</h3><div id="c6-disk" class="chart"></div></div>
        <div class="panel"><h3>网络流量 TOP</h3><div id="c6-netrank" class="chart"></div></div>
        <div class="panel"><h3>CPU 温度 TOP</h3><div id="c6-temp" class="chart"></div></div>
        <div class="panel"><h3>主机分组健康度</h3><div id="c6-cat" class="chart"></div></div>
        <div class="panel"><h3>监控项类型</h3><div id="c6-task" class="chart"></div></div>
        <div class="panel"><h3>近 7 天告警</h3><div id="c6-daily" class="chart"></div></div>`,
      draw() {
        drawCpuMemTrend('c6-trend');
        drawNetTrend('c6-net');
        drawDiskIOTrend('c6-dio');
        drawLoadTrend('c6-load');
        drawHostRank('c6-cpu', rtRank('cpuPer'), '#f87171', '%');
        drawHostRank('c6-mem', rtRank('memPer'), '#a78bfa', '%');
        drawHostRank('c6-disk', payload.topDisk, '#22d3ee', '%');
        drawHostRank('c6-netrank', payload.topNet, '#34d399', 'KB/s');
        drawHostRank('c6-temp', rtRank('cpuTemp'), '#fbbf24', '°');
        drawCatalogBar('c6-cat');
        drawTaskType('c6-task');
        drawAlertDaily('c6-daily');
      },
    };
  },

  // 7 双屏对称：左主机 / 右告警
  7() {
    const rt = payload.runtimes || [];
    return {
      html: `
        <div class="area-kpi">
          ${kpi('主机总数', payload.hostTotal, '台')}
          ${kpi('在线', payload.hostOnline, '台', 'green')}
          ${kpi('离线', payload.hostOffline, '', payload.hostOffline ? 'red' : '')}
          ${kpi('活跃告警', payload.alertActive, '', payload.alertActive ? 'red' : 'green')}
          ${kpi('平均CPU', fmt(payload.avgCpu), '%', 'cyan')}
          ${kpi('平均内存', fmt(payload.avgMem), '%', 'violet')}
          ${kpi('平均磁盘', fmt(payload.avgDisk), '%', 'blue')}
          ${kpi('最高温度', payload.maxCpuTemp > 0 ? fmt(payload.maxCpuTemp, 0) : '-', '°', '')}
          ${kpi('连接数', payload.connTotal || 0, '', '')}
          ${kpi('进程快照', payload.totalProcess || 0, '个', '')}
        </div>
        <div class="area-left">
          <div class="panel"><h3>CPU / 内存趋势<span class="sub">近 ${minutes} 分钟</span></h3><div id="c7-trend" class="chart"></div></div>
          <div class="panel"><h3>主机明细<span class="sub">${rt.length} 台</span></h3>
            <div class="tbl-wrap"><table>${runtimeTableHead()}<tbody>${runtimeRows(rt, 30)}</tbody></table></div>
          </div>
        </div>
        <div class="area-right">
          <div class="panel"><h3>告警流水</h3><div class="scroll-list">${alertList(payload.alerts, 40)}</div></div>
          <div class="panel"><h3>告警类型分布<span class="sub">近 7 天</span></h3><div id="c7-type" class="chart"></div></div>
        </div>`,
      draw() {
        drawCpuMemTrend('c7-trend');
        drawAlertPie('c7-type', (payload.alertStats || []).filter(s => s.type), 'type');
      },
    };
  },

  // 8 容量与硬件
  8() {
    const st = smartTotal();
    return {
      html: `
        <div class="area-kpi">
          ${kpi('主机总数', payload.hostTotal, '台')}
          ${kpi('在线', payload.hostOnline, '台', 'green')}
          ${kpi('平均磁盘', fmt(payload.avgDisk), '%', payload.avgDisk >= 90 ? 'red' : 'blue')}
          ${kpi('磁盘读', fmt(payload.diskReadKb, 0), 'KB/s', '')}
          ${kpi('磁盘写', fmt(payload.diskWriteKb, 0), 'KB/s', '')}
          ${kpi('读IOPS', fmt(payload.readIops, 0), '', '')}
          ${kpi('写IOPS', fmt(payload.writeIops, 0), '', '')}
          ${kpi('最高温度', payload.maxCpuTemp > 0 ? fmt(payload.maxCpuTemp, 0) : '-', '°', payload.maxCpuTemp >= 80 ? 'amber' : '')}
          ${kpi('SMART', st ? `${(payload.smartHealth || {}).pass || 0}/${st}` : '-', '', '')}
          ${kpi('连接数', payload.connTotal || 0, '', '')}
        </div>
        <div class="panel area-c1"><h3>磁盘占用 TOP</h3><div id="c8-disk" class="chart"></div></div>
        <div class="panel area-c2"><h3>网络流量 TOP</h3><div id="c8-net" class="chart"></div></div>
        <div class="panel area-c3"><h3>CPU 温度 TOP</h3><div id="c8-temp" class="chart"></div></div>
        <div class="panel area-c4"><h3>硬盘 SMART</h3><div id="c8-smart" class="chart"></div></div>
        <div class="panel area-t1"><h3>磁盘 IO 趋势<span class="sub">近 ${minutes} 分钟</span></h3><div id="c8-dio" class="chart"></div></div>
        <div class="panel area-t2"><h3>主机分组健康度</h3><div id="c8-cat" class="chart"></div></div>`,
      draw() {
        drawHostRank('c8-disk', payload.topDisk, '#22d3ee', '%');
        drawHostRank('c8-net', payload.topNet, '#34d399', 'KB/s');
        drawHostRank('c8-temp', rtRank('cpuTemp'), '#fbbf24', '°');
        drawSmartPie('c8-smart');
        drawDiskIOTrend('c8-dio');
        drawCatalogBar('c8-cat');
      },
    };
  },
};

/* ---------- 渲染主流程 ---------- */

function render() {
  if (!payload) return;
  const tpl = TEMPLATES[layout] || TEMPLATES[1];
  const stage = el('stage');

  // 换模板时销毁上一套的实例，否则旧实例会持有已移除的 DOM 造成内存泄漏
  if (chartsLayout !== layout) {
    destroyCharts();
    chartsLayout = layout;
  }

  stage.className = 'stage ' + (LAYOUTS[layout] ? LAYOUTS[layout].cls : 'layout-1');
  const built = tpl();
  stage.innerHTML = built.html;

  el('layoutName').textContent = LAYOUTS[layout] ? LAYOUTS[layout].name : '';

  // 等 DOM 完成布局后再初始化图表，否则容器高度为 0，ECharts 画不出来
  requestAnimationFrame(() => {
    try { built.draw(); } catch (e) { console.error('图表渲染失败', e); }
    resizeCharts();
  });
}

async function refresh() {
  try {
    payload = await api('/api/screen?minutes=' + minutes);
    // 后台改了标题，最迟下一次刷新（10 秒内）就反映到大屏上，不用重开
    applyTitle(payload.title);
    const net = el('netState');
    net.textContent = '数据连接正常';
    net.className = 'tag-pill';
    render();
    const ts = el('updateTip');
    if (ts) {
      ts.textContent = '最后更新 ' + new Date().toLocaleTimeString('zh-CN', { hour12: false });
    }
  } catch (err) {
    const net = el('netState');
    if (!net) return;
    net.textContent = '数据获取异常：' + err.message;
    net.className = 'tag-pill bad';
  }
}

function setLayout(n) {
  if (!LAYOUTS[n]) return;
  layout = n;
  localStorage.setItem(STORE_KEY, String(n));
  const picker = el('layoutPicker');
  if (picker) picker.value = String(n);
  // 独立地址：/screen/3 直接打开模板 3，便于给不同屏幕固定一套版式
  const url = new URL(location.href);
  url.pathname = '/screen/' + n;
  history.replaceState(null, '', url.toString());
  render();
}

function currentFromPath() {
  const m = location.pathname.match(/^\/screen\/(\d+)$/);
  return m ? Number(m[1]) : 0;
}

function toggleFullscreen() {
  if (document.fullscreenElement) {
    document.exitFullscreen();
  } else {
    document.documentElement.requestFullscreen().catch(err => console.warn('进入全屏失败', err));
  }
}

function windowLabelText() {
  return minutes >= 1440 ? '近 ' + Math.round(minutes / 1440) + ' 天' : '近 ' + minutes + ' 分钟';
}

/* ---------- 事件绑定 ---------- */

el('layoutPicker').addEventListener('change', e => setLayout(Number(e.target.value)));
el('exitBtn').addEventListener('click', () => { location.href = '/'; });
el('fullscreenBtn').addEventListener('click', toggleFullscreen);
el('refreshBtn').addEventListener('click', refresh);

el('minutesPicker').addEventListener('change', e => {
  minutes = Number(e.target.value);
  refresh();
});

el('autoBtn').addEventListener('click', () => {
  const btn = el('autoBtn');
  if (autoTimer) {
    clearInterval(autoTimer);
    autoTimer = null;
    btn.textContent = '▶ 自动轮播';
    btn.classList.remove('warn');
  } else {
    // 自动轮播每 30 秒切一套模板，投屏长时间无人值守时用
    autoTimer = setInterval(() => {
      const i = LAYOUT_IDS.indexOf(layout);
      setLayout(LAYOUT_IDS[(i + 1) % LAYOUT_IDS.length]);
    }, 30000);
    btn.textContent = '⏸ 停止轮播';
    btn.classList.add('warn');
  }
});

document.addEventListener('keydown', e => {
  if (e.key === 'ArrowLeft') {
    const i = LAYOUT_IDS.indexOf(layout);
    setLayout(LAYOUT_IDS[(i - 1 + LAYOUT_IDS.length) % LAYOUT_IDS.length]);
  }
  if (e.key === 'ArrowRight') {
    const i = LAYOUT_IDS.indexOf(layout);
    setLayout(LAYOUT_IDS[(i + 1) % LAYOUT_IDS.length]);
  }
  if (e.key === 'f' || e.key === 'F') toggleFullscreen();
  if (e.key === 'Escape' && document.fullscreenElement) document.exitFullscreen();
});

// 全屏切换与窗口缩放后 ECharts 不会自适应，必须手动 resize
window.addEventListener('resize', resizeCharts);
document.addEventListener('fullscreenchange', () => setTimeout(resizeCharts, 120));

/* ---------- 启动 ---------- */

setInterval(() => {
  el('clock').textContent = new Date().toLocaleString('zh-CN', { hour12: false });
}, 1000);
el('clock').textContent = new Date().toLocaleString('zh-CN', { hour12: false });

// URL 里的模板编号优先于本地记忆，方便多屏各固定一套
const fromPath = currentFromPath();
if (fromPath && LAYOUTS[fromPath]) layout = fromPath;
el('layoutPicker').value = String(layout);
el('windowLabel').textContent = windowLabelText();

// 先用公开设置里的标题占位，避免自定义过名称的大屏先闪一下默认名
api('/api/settings/public')
  .then(cfg => applyTitle(cfg && cfg.screenTitle))
  .catch(() => { /* 拿不到就沿用 HTML 里的默认名，不影响大屏出图 */ });

refresh();
setInterval(refresh, REFRESH_MS);