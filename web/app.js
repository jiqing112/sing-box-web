/* ============================================================
   sing-box panel — prototype runtime
   vanilla JS on purpose: the shipped build is Svelte-compiled,
   this file only simulates the same data contracts.
   ============================================================ */

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const rnd = (a, b) => a + Math.random() * (b - a);
const pick = (a) => a[Math.floor(Math.random() * a.length)];

/* ------------------------------------------------------------ data */

const REGIONS = {
  hk: { code: 'HK', label: '香港', color: 'linear-gradient(150deg,#c0392b,#7d1f16)' },
  jp: { code: 'JP', label: '日本', color: 'linear-gradient(150deg,#c8102e,#6d0a1a)' },
  sg: { code: 'SG', label: '新加坡', color: 'linear-gradient(150deg,#e03a3e,#8a1b1e)' },
  us: { code: 'US', label: '美国', color: 'linear-gradient(150deg,#3c3b6e,#20204a)' },
  tw: { code: 'TW', label: '台湾', color: 'linear-gradient(150deg,#1f8a70,#0d4a3c)' },
  kr: { code: 'KR', label: '韩国', color: 'linear-gradient(150deg,#0047a0,#002a60)' }
};

const PROTO = ['hysteria2', 'vless', 'trojan', 'shadowsocks', 'tuic'];

const nodes = [];
Object.entries({ hk: 12, jp: 9, sg: 7, us: 8 }).forEach(([region, count]) => {
  for (let i = 1; i <= count; i++) {
    const dead = (region === 'us' && i === 7) || (region === 'sg' && i === 6);
    nodes.push({
      id: `${region}-${i}`,
      region,
      name: `${REGIONS[region].label} ${String(i).padStart(2, '0')}${i % 4 === 0 ? ' · IPLC' : i % 3 === 0 ? ' · BGP' : ''}`,
      proto: PROTO[(i + region.length) % PROTO.length],
      ip: `${Math.floor(rnd(20, 210))}.${Math.floor(rnd(1, 250))}.${Math.floor(rnd(1, 250))}.${Math.floor(rnd(1, 250))}`,
      latency: dead ? -1 : Math.round(rnd(24, region === 'us' ? 320 : region === 'jp' ? 120 : 90)),
      jitter: dead ? 0 : Math.round(rnd(1, 26)),
      enabled: !dead,
      history: dead ? [] : Array.from({ length: 5 }, () => Math.round(rnd(20, 160)))
    });
  }
});

const subs = [
  { id: 's1', name: 'Nexitally · 主订阅', url: 'https://sub.nexitally.com/api/v1/client/subscribe?token=8f2a…c91d', nodes: 24, used: 186.4, total: 500, expire: '2027-03-14', updated: '8 分钟前', auto: true, tone: 'lime' },
  { id: 's2', name: 'ImmortalWrt 备用', url: 'https://sub.immortal.example/api/v1/client/subscribe?token=2c7b…9e40', nodes: 9, used: 42.1, total: 100, expire: '2026-12-01', updated: '2 小时前', auto: true, tone: 'teal' },
  { id: 's3', name: '自建节点 · HK', url: 'https://panel.selfhosted.io/api/v1/subscribe?token=91aa…73f2', nodes: 3, used: 0.8, total: 20, expire: '2027-06-30', updated: '昨天 21:04', auto: false, tone: 'violet' }
];

const snapshots = [
  { v: 3, time: '10-19 22:04', note: '更新订阅 · 34 个出站', current: true, ok: true },
  { v: 2, time: '10-19 18:32', note: '新增路由规则 geosite:openai', current: false, ok: true },
  { v: 1, time: '10-18 09:15', note: '初始配置 · redirect 入站', current: false, ok: true }
];

const configObj = {
  log: { level: 'warn', timestamp: true },
  dns: {
    servers: [
      { tag: 'remote', address: 'https://1.1.1.1/dns-query', detour: 'proxy' },
      { tag: 'local', address: '223.5.5.5', detour: 'direct' }
    ],
    rules: [{ domain_suffix: ['.cn'], server: 'local' }],
    final: 'remote'
  },
  inbounds: [
    { type: 'redirect', tag: 'redirect-in', listen: '0.0.0.0', listen_port: 12345 },
    { type: 'tun', tag: 'tun-in', interface_name: 'singbox0', address: ['172.19.0.1/30'], auto_route: true, stack: 'gvisor' }
  ],
  outbounds: [
    { type: 'selector', tag: 'proxy', outbounds: ['auto', 'hk-01', 'jp-01'], default: 'auto' },
    { type: 'urltest', tag: 'auto', outbounds: ['hk-01', 'jp-01', 'sg-01'], url: 'https://www.gstatic.com/generate_204', interval: '10m', tolerance: 50 },
    { type: 'hysteria2', tag: 'hk-01', server: '45.77.12.9', server_port: 8443, password: '••••••••' },
    { type: 'vless', tag: 'jp-01', server: '103.21.44.8', server_port: 443, uuid: '••••••••', flow: 'xtls-rprx-vision' },
    { type: 'direct', tag: 'direct' }
  ],
  route: {
    rules: [
      { protocol: 'dns', outbound: 'dns-out' },
      { geoip: ['private'], outbound: 'direct' },
      { geosite: ['cn'], outbound: 'direct' },
      { domain_suffix: ['.openai.com'], outbound: 'proxy' }
    ],
    final: 'proxy',
    auto_detect_interface: true
  },
  experimental: {
    clash_api: { external_controller: '127.0.0.1:9091', default_mode: 'rule' },
    cache_file: { enabled: true, path: 'cache.db', store_rdrc: true }
  }
};

const LOG_POOL = [
  ['info', 'router: selected outbound <b>proxy → auto → hk-01</b>'],
  ['info', 'inbound/redirect[redirect-in]: inbound connection from <b>192.168.1.24:51244</b>'],
  ['info', 'outbound/hysteria2[hk-01]: outbound connection to <b>www.google.com:443</b>'],
  ['debug', 'dns: lookup domain <b>api.github.com</b> via remote, 18ms'],
  ['info', 'route: match <b>geosite:cn</b> → direct (rule #3)'],
  ['warn', 'outbound/tuic[sg-06]: health check timeout after 5000ms'],
  ['info', 'clash-api: websocket client connected (127.0.0.1)'],
  ['debug', 'cache: save selected outbound <b>proxy=auto</b>'],
  ['info', 'inbound/tun[tun-in]: started at singbox0'],
  ['error', 'outbound/vless[us-07]: dial tcp 198.51.100.7:443: i/o timeout'],
  ['info', 'router: rule #6 <b>domain_suffix:.openai.com</b> → proxy'],
  ['debug', 'dns: cached <b>clash.razord.top</b> 300s']
];

const CONN_HOSTS = [
  ['github.com', '140.82.113.4', 'proxy', 'geosite:github'],
  ['api.telegram.org', '149.154.167.220', 'proxy', 'geosite:telegram'],
  ['www.bilibili.com', '119.3.70.188', 'direct', 'geosite:cn'],
  ['registry.npmjs.org', '104.16.24.34', 'proxy', 'final'],
  ['api.openai.com', '104.18.7.192', 'proxy', 'domain_suffix:.openai.com'],
  ['update.openwrt.org', '185.233.187.20', 'direct', 'geosite:cn'],
  ['cdn.jsdelivr.net', '151.101.1.229', 'proxy', 'final'],
  ['192.168.1.1', '192.168.1.1', 'direct', 'geoip:private'],
  ['www.youtube.com', '142.250.66.78', 'proxy', 'geosite:google'],
  ['time.apple.com', '17.253.12.34', 'direct', 'geosite:apple'],
  ['pypi.org', '151.101.0.223', 'proxy', 'final'],
  ['mqtt.eclipseprojects.io', '137.135.83.39', 'proxy', 'final']
];

/* ------------------------------------------------------------ state */

const state = {
  page: 'dashboard',
  range: '1h',
  testing: false,
  tproxyOn: false,
  tproxyMode: 'redirect',
  paused: false,
  conns: [],
  logs: []
};

/* ------------------------------------------------------------ utils */

function escapeHtml(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
}

function fmtBytes(b) {
  if (b > 1024 ** 3) return (b / 1024 ** 3).toFixed(2) + ' GB';
  if (b > 1024 ** 2) return (b / 1024 ** 2).toFixed(1) + ' MB';
  if (b > 1024) return (b / 1024).toFixed(0) + ' KB';
  return b + ' B';
}

function fmtRate(bps) {
  if (bps > 1024 ** 2) return (bps / 1024 ** 2).toFixed(1) + ' MB/s';
  if (bps > 1024) return (bps / 1024).toFixed(0) + ' KB/s';
  return bps + ' B/s';
}

function latClass(v) {
  if (v < 0) return 'none';
  if (v < 80) return 'good';
  if (v < 150) return 'ok';
  if (v < 250) return 'mid';
  return 'bad';
}

function latText(v) {
  return v < 0 ? '超时' : v + ' ms';
}

/* ------------------------------------------------------------ toast */

function toast(kind, title, desc) {
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  const icon = kind === 'ok' ? 'i-check' : kind === 'err' ? 'i-alert' : 'i-info';
  el.innerHTML = `<svg class="ic" width="14" height="14"><use href="#${icon}"/></svg>
    <div><div class="t">${escapeHtml(title)}</div>${desc ? `<div class="d">${escapeHtml(desc)}</div>` : ''}</div>`;
  $('#toasts').appendChild(el);
  setTimeout(() => {
    el.classList.add('out');
    setTimeout(() => el.remove(), 320);
  }, 3200);
}

/* ------------------------------------------------------------ router */

const PAGE_META = {
  dashboard: ['概览', '控制台'],
  subs: ['订阅', '资源'],
  nodes: ['节点', '资源'],
  config: ['配置', '管理'],
  service: ['服务', '管理'],
  tproxy: ['透明代理', '网络'],
  conns: ['连接', '网络'],
  settings: ['设置', '系统']
};

function go(page) {
  state.page = page;
  $$('.page').forEach((p) => p.classList.toggle('active', p.id === `page-${page}`));
  $$('.nav-item').forEach((n) => n.classList.toggle('active', n.dataset.page === page));
  const [t, section] = PAGE_META[page] || ['概览', '控制台'];
  $('#crumbTitle').textContent = t;
  $('#crumbPath').innerHTML = `/ <b>${section}</b>`;
  $('#content').scrollTop = 0;
  if (location.hash !== `#${page}`) history.replaceState(null, '', `#${page}`);
}

/* deep link: #nodes / #config … */
const initial = location.hash.replace('#', '');
if (PAGE_META[initial]) go(initial);

$$('.nav-item').forEach((n) => n.addEventListener('click', () => go(n.dataset.page)));
$$('[data-goto]').forEach((b) => b.addEventListener('click', () => go(b.dataset.goto)));

/* ------------------------------------------------------------ sparklines */

const SPARKS = {
  a: [12, 16, 14, 22, 19, 28, 24, 33, 29, 38, 34, 41],
  b: [18, 12, 24, 20, 30, 26, 38, 31, 44, 36, 48, 42],
  c: [30, 31, 29, 32, 30, 33, 31, 34, 32, 33, 34, 34],
  d: [70, 74, 72, 78, 76, 80, 79, 84, 82, 88, 86, 98]
};

function drawSpark(svg, data, color) {
  const w = 90, h = 42, pad = 4;
  const max = Math.max(...data), min = Math.min(...data);
  const pts = data.map((v, i) => [
    (i / (data.length - 1)) * w,
    h - pad - ((v - min) / (max - min || 1)) * (h - pad * 2)
  ]);
  let d = `M${pts[0][0]},${pts[0][1]}`;
  for (let i = 1; i < pts.length; i++) {
    const [x0, y0] = pts[i - 1], [x1, y1] = pts[i];
    const cx = (x0 + x1) / 2;
    d += ` C${cx},${y0} ${cx},${y1} ${x1},${y1}`;
  }
  const area = `${d} L${w},${h} L0,${h} Z`;
  svg.innerHTML = `
    <defs><linearGradient id="sg-${color.replace('#', '')}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="${color}" stop-opacity="0.24"/>
      <stop offset="100%" stop-color="${color}" stop-opacity="0"/>
    </linearGradient></defs>
    <path d="${area}" fill="url(#sg-${color.replace('#', '')})"/>
    <path d="${d}" fill="none" stroke="${color}" stroke-width="1.4" stroke-opacity="0.85"/>`;
}

drawSpark($('[data-spark="a"]'), SPARKS.a, '#c6ff4d');
drawSpark($('[data-spark="b"]'), SPARKS.b, '#52d9c4');
drawSpark($('[data-spark="c"]'), SPARKS.c, '#8f7dff');
drawSpark($('[data-spark="d"]'), SPARKS.d, '#c6ff4d');

/* ------------------------------------------------------------ traffic chart */

const CHART = { w: 760, h: 208, pl: 34, pr: 8, pt: 14, pb: 24 };
let chartData = [];

function genSeries(n, base, amp) {
  const out = [];
  let v = base;
  for (let i = 0; i < n; i++) {
    v += rnd(-amp, amp);
    v = Math.max(base * 0.25, Math.min(base * 2.4, v));
    out.push(v);
  }
  return out;
}

function buildChartData(range) {
  const n = range === '24h' ? 72 : range === '6h' ? 60 : 48;
  const down = genSeries(n, 42, 7);
  const up = genSeries(n, 5.5, 1.8);
  return down.map((d, i) => ({ d, u: up[i] }));
}

function smoothPath(pts) {
  if (!pts.length) return '';
  let d = `M${pts[0][0]},${pts[0][1]}`;
  for (let i = 1; i < pts.length; i++) {
    const [x0, y0] = pts[i - 1], [x1, y1] = pts[i];
    const cx = (x0 + x1) / 2;
    d += ` C${cx},${y0} ${cx},${y1} ${x1},${y1}`;
  }
  return d;
}

function drawChart() {
  const svg = $('#trafficChart');
  const { w, h, pl, pr, pt, pb } = CHART;
  const maxV = Math.max(...chartData.map((p) => p.d)) * 1.18;
  const iw = w - pl - pr, ih = h - pt - pb;
  const X = (i) => pl + (i / (chartData.length - 1)) * iw;
  const Y = (v) => pt + ih - (v / maxV) * ih;

  const dPts = chartData.map((p, i) => [X(i), Y(p.d)]);
  const uPts = chartData.map((p, i) => [X(i), Y(p.u)]);
  const dLine = smoothPath(dPts);
  const uLine = smoothPath(uPts);

  const grid = [0, 0.25, 0.5, 0.75, 1].map((f) => {
    const y = pt + ih * f;
    const val = Math.round(maxV * (1 - f));
    return `<line x1="${pl}" y1="${y}" x2="${w - pr}" y2="${y}" stroke="#191d23" stroke-width="1" stroke-dasharray="${f === 1 ? '0' : '2 4'}"/>
      <text x="${pl - 8}" y="${y + 3}" fill="#4a505a" font-size="8.5" font-family="JetBrains Mono, monospace" text-anchor="end">${val}</text>`;
  }).join('');

  const labels = ['-60m', '-45m', '-30m', '-15m', '现在'];
  const xLabels = labels.map((l, i) => {
    const x = pl + (i / 4) * iw;
    return `<text x="${x}" y="${h - 6}" fill="#4a505a" font-size="8.5" font-family="JetBrains Mono, monospace" text-anchor="${i === 0 ? 'start' : i === 4 ? 'end' : 'middle'}">${l}</text>`;
  }).join('');

  svg.innerHTML = `
    <defs>
      <linearGradient id="gDown" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="#c6ff4d" stop-opacity="0.26"/>
        <stop offset="70%" stop-color="#c6ff4d" stop-opacity="0.03"/>
        <stop offset="100%" stop-color="#c6ff4d" stop-opacity="0"/>
      </linearGradient>
      <linearGradient id="gUp" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="#8f7dff" stop-opacity="0.2"/>
        <stop offset="100%" stop-color="#8f7dff" stop-opacity="0"/>
      </linearGradient>
    </defs>
    ${grid}
    <path d="${dLine} L${w - pr},${pt + ih} L${pl},${pt + ih} Z" fill="url(#gDown)"/>
    <path d="${uLine} L${w - pr},${pt + ih} L${pl},${pt + ih} Z" fill="url(#gUp)"/>
    <path d="${dLine}" fill="none" stroke="#c6ff4d" stroke-width="1.6" vector-effect="non-scaling-stroke"/>
    <path d="${uLine}" fill="none" stroke="#8f7dff" stroke-width="1.4" stroke-opacity="0.9" vector-effect="non-scaling-stroke"/>
    <circle cx="${X(chartData.length - 1)}" cy="${Y(chartData[chartData.length - 1].d)}" r="2.6" fill="#c6ff4d"/>
    <circle cx="${X(chartData.length - 1)}" cy="${Y(chartData[chartData.length - 1].d)}" r="2.6" fill="none" stroke="#c6ff4d" stroke-width="1.2">
      <animate attributeName="r" values="2.6;9" dur="2.4s" repeatCount="indefinite"/>
      <animate attributeName="opacity" values="0.55;0" dur="2.4s" repeatCount="indefinite"/>
    </circle>
    <line id="crosshair" x1="0" y1="${pt}" x2="0" y2="${pt + ih}" stroke="#3a424c" stroke-width="1" stroke-dasharray="3 3" opacity="0"/>
    <circle id="dotD" r="3" fill="#c6ff4d" stroke="#07080a" stroke-width="1.5" opacity="0"/>
    <circle id="dotU" r="2.6" fill="#8f7dff" stroke="#07080a" stroke-width="1.5" opacity="0"/>
    ${xLabels}`;

  // hover
  const tip = $('#chartTip');
  svg.onmousemove = (e) => {
    const rect = svg.getBoundingClientRect();
    const scale = rect.width / w;
    const x = (e.clientX - rect.left) / scale;
    let idx = Math.round(((x - pl) / iw) * (chartData.length - 1));
    idx = Math.max(0, Math.min(chartData.length - 1, idx));
    const px = X(idx), p = chartData[idx];
    const cross = $('#crosshair');
    cross.setAttribute('x1', px); cross.setAttribute('x2', px); cross.setAttribute('opacity', '1');
    const dd = $('#dotD'), du = $('#dotU');
    dd.setAttribute('cx', px); dd.setAttribute('cy', Y(p.d)); dd.setAttribute('opacity', '1');
    du.setAttribute('cx', px); du.setAttribute('cy', Y(p.u)); du.setAttribute('opacity', '1');
    const mins = Math.round((idx / (chartData.length - 1)) * 60);
    tip.innerHTML = `<div style="color:#656c77;font-size:9.5px;margin-bottom:3px">-${60 - mins} 分钟</div>
      <div class="r"><span style="color:#c6ff4d">↓</span><b>${p.d.toFixed(1)} Mbps</b></div>
      <div class="r"><span style="color:#8f7dff">↑</span><b>${p.u.toFixed(1)} Mbps</b></div>`;
    tip.style.left = (px * scale) + 'px';
    tip.style.top = (Y(p.d) * (rect.height / h)) + 'px';
    tip.style.opacity = '1';
  };
  svg.onmouseleave = () => {
    tip.style.opacity = '0';
    ['#crosshair', '#dotD', '#dotU'].forEach((s) => $(s).setAttribute('opacity', '0'));
  };
}

chartData = buildChartData('1h');
drawChart();

$$('[data-range]').forEach((b) => {
  b.addEventListener('click', () => {
    $$('[data-range]').forEach((x) => x.classList.toggle('on', x === b));
    state.range = b.dataset.range;
    chartData = buildChartData(state.range);
    drawChart();
  });
});

/* ------------------------------------------------------------ dashboard nodes */

function renderDashNodes() {
  const list = [...nodes].filter((n) => n.enabled).sort((a, b) => a.latency - b.latency).slice(0, 6);
  $('#dashNodes').innerHTML = list.map((n) => {
    const c = latClass(n.latency);
    const h = n.history.slice(-5);
    const mx = Math.max(...h, 1);
    return `<div class="node-mini">
      <span class="flag" style="background:${REGIONS[n.region].color}">${REGIONS[n.region].code}</span>
      <span class="nm">${n.name}<em>${n.proto}</em></span>
      <span class="bars lat ${c}">${h.map((v) => `<i style="height:${Math.max(3, (v / mx) * 14)}px"></i>`).join('')}</span>
      <span class="lat ${c}" style="width:52px;text-align:right">${latText(n.latency)}</span>
    </div>`;
  }).join('');
}
renderDashNodes();

/* ------------------------------------------------------------ subscriptions */

function renderSubs() {
  $('#subGrid').innerHTML = subs.map((s) => {    const pct = Math.min(100, (s.used / s.total) * 100);
    return `<div class="card sub-card">
      <div class="sub-head">
        <div class="sub-avatar ${s.tone}">${s.name.slice(0, 1).toUpperCase()}</div>
        <div style="min-width:0;flex:1">
          <div class="sub-title">${s.name}</div>
          <div class="sub-url">${escapeHtml(s.url)}</div>
        </div>
        <span class="badge ${s.auto ? 'lime' : ''}" style="height:18px">${s.auto ? '自动更新' : '手动'}</span>
      </div>
      <div class="usage">
        <div class="usage-bar"><i style="width:${pct}%"></i></div>
        <div class="usage-meta"><span>${s.used} GB / ${s.total} GB</span><span>${pct.toFixed(0)}%</span></div>
      </div>
      <div class="sub-stats">
        <div><div class="k">节点</div><div class="v">${s.nodes}</div></div>
        <div><div class="k">到期</div><div class="v">${s.expire}</div></div>
        <div><div class="k">上次更新</div><div class="v">${s.updated}</div></div>
      </div>
      <div style="display:flex;gap:6px">
        <button class="btn sm" style="flex:1" onclick="toast('ok','更新完成','解析出 ${s.nodes} 个节点，原始响应已丢弃')">
          <svg width="11" height="11"><use href="#i-refresh"/></svg>立即更新</button>
        <button class="btn sm ghost" onclick="toast('info','编辑订阅','敏感字段将以掩码显示')"><svg width="11" height="11"><use href="#i-edit"/></svg></button>
        <button class="btn sm ghost" onclick="confirmDialog('delSub')"><svg width="11" height="11"><use href="#i-trash"/></svg></button>
      </div>
    </div>`;
  }).join('') + `
    <button class="sub-add" onclick="openDialog('addSub')">
      <span class="plus"><svg width="14" height="14"><use href="#i-plus"/></svg></span>
      新增订阅链接
      <span style="font-size:10.5px;color:var(--text-4)">Clash / sing-box / v2ray 格式</span>
    </button>`;
}
renderSubs();

/* ------------------------------------------------------------ node table */

let nodeFilter = { group: 'all', q: '' };

function nodeRows() {
  return nodes.filter((n) => {
    if (nodeFilter.group !== 'all' && n.region !== nodeFilter.group) return false;
    if (nodeFilter.q) {
      const q = nodeFilter.q.toLowerCase();
      return n.name.toLowerCase().includes(q) || n.proto.includes(q) || n.ip.includes(q);
    }
    return true;
  });
}

function renderNodeTable() {
  const rows = nodeRows();
  $('#nodeTable tbody').innerHTML = rows.map((n) => {
    const c = latClass(n.latency);
    const h = n.history.slice(-5);
    const mx = Math.max(...h, 1);
    return `<tr data-node="${n.id}">
      <td><input type="checkbox" style="accent-color:#c6ff4d" ${n.enabled ? '' : 'disabled'} /></td>
      <td><div class="cell-main">
        <span class="flag" style="background:${REGIONS[n.region].color}">${REGIONS[n.region].code}</span>
        <div class="col"><div class="t">${n.name}${n.id === 'hk-9' ? ' <span class="badge lime" style="height:15px;font-size:9px;vertical-align:1px">使用中</span>' : ''}</div><div class="s">${n.id}</div></div>
      </div></td>
      <td><span class="badge mono p-${n.proto}">${n.proto}</span></td>
      <td><div class="s" style="font-family:var(--mono);font-size:11px;color:var(--text-3)">${n.ip}</div></td>
      <td><div style="display:flex;align-items:center;gap:8px">
        <span class="bars lat ${c}">${h.map((v) => `<i style="height:${Math.max(3, (v / mx) * 14)}px"></i>`).join('')}</span>
        <span class="lat ${c}">${latText(n.latency)}</span>
      </div></td>
      <td><span class="num" style="color:var(--text-3)">${n.jitter ? '±' + n.jitter : '—'}</span></td>
      <td><button class="switch sm ${n.enabled ? 'on' : ''}" onclick="toggleNode('${n.id}', this)"></button></td>
      <td class="t-right"><button class="btn ghost xs" onclick="pingOne('${n.id}')">测速</button></td>
    </tr>`;
  }).join('');
  if (!rows.length) {
    $('#nodeTable tbody').innerHTML = `<tr><td colspan="8" style="text-align:center;padding:40px;color:var(--text-4)">没有匹配的节点</td></tr>`;
  }
}
renderNodeTable();

$('#nodeSearch').addEventListener('input', (e) => { nodeFilter.q = e.target.value; renderNodeTable(); });
$$('#nodeGroups button').forEach((b) => b.addEventListener('click', () => {
  $$('#nodeGroups button').forEach((x) => x.classList.toggle('on', x === b));
  nodeFilter.group = b.dataset.group;
  renderNodeTable();
}));

function toggleNode(id, el) {
  const n = nodes.find((x) => x.id === id);
  n.enabled = !n.enabled;
  el.classList.toggle('on', n.enabled);
  toast(n.enabled ? 'ok' : 'info', n.enabled ? '节点已启用' : '节点已禁用', `${n.name} · 变更将在下次渲染配置时生效`);
}

function pingOne(id) {
  const n = nodes.find((x) => x.id === id);
  n.latency = Math.round(rnd(28, 180));
  n.jitter = Math.round(rnd(1, 20));
  n.history.push(n.latency);
  n.history = n.history.slice(-5);
  renderNodeTable();
  renderDashNodes();
}

/* speedtest with concurrency 3 */
async function runSpeedtest() {
  if (state.testing) return;
  state.testing = true;
  const queue = [...nodes];
  let done = 0;
  const total = queue.length;
  const tick = () => {
    $('#stProgress').textContent = `测速中 ${done}/${total} · 并发 3`;
  };
  tick();
  const worker = async () => {
    while (queue.length) {
      const n = queue.shift();
      await new Promise((r) => setTimeout(r, rnd(160, 420)));
      n.latency = Math.random() < 0.06 ? -1 : Math.round(rnd(24, n.region === 'us' ? 340 : 140));
      n.jitter = n.latency < 0 ? 0 : Math.round(rnd(1, 24));
      if (n.latency > 0) { n.history.push(n.latency); n.history = n.history.slice(-5); }
      done++;
      tick();
      renderNodeTable();
      renderDashNodes();
    }
  };
  await Promise.all([worker(), worker(), worker()]);
  state.testing = false;
  $('#stProgress').textContent = `上次测速 刚刚 · ${total}/${total}`;
  toast('ok', '测速完成', `${total} 个节点 · 结果缓存 5 分钟，仅存内存`);
}

/* ------------------------------------------------------------ config editor */

function highlightJson(json) {
  const token = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|(-?\d+(?:\.\d+)?)/g;
  let out = '', last = 0, m;
  while ((m = token.exec(json))) {
    out += escapeHtml(json.slice(last, m.index));
    if (m[1] !== undefined) {
      if (m[2]) out += `<span class="k">${escapeHtml(m[1])}</span><span class="p">${escapeHtml(m[2])}</span>`;
      else out += `<span class="s">${escapeHtml(m[1])}</span>`;
    } else if (m[3]) {
      out += `<span class="b">${m[3]}</span>`;
    } else {
      out += `<span class="n">${m[4]}</span>`;
    }
    last = token.lastIndex;
  }
  return out + escapeHtml(json.slice(last));
}

function renderCode() {
  const json = JSON.stringify(configObj, null, 2);
  const lines = highlightJson(json).split('\n');
  $('#codeView').innerHTML = lines.map((l, i) =>
    `<div class="ln">${i + 1}</div><div class="lc">${l || ' '}</div>`
  ).join('');
  $('#codeStat').textContent = `行 ${lines.length}，列 1`;
}
renderCode();

$$('[data-editor-tab]').forEach((t) => t.addEventListener('click', () => {
  $$('[data-editor-tab]').forEach((x) => x.classList.toggle('on', x === t));
  if (t.dataset.editorTab === 'visual') {
    toast('info', '可视化模式', '仅暴露常用字段，其余保留为原始 JSON');
  }
}));

function validateConfig() {
  const out = $('#checkOut');
  out.innerHTML = `<div class="dim">$ sing-box check -c /etc/sing-box/config.json</div>
    <div class="dim">  parsing…</div>`;
  setTimeout(() => {
    out.innerHTML = `<div class="dim">$ sing-box check -c /etc/sing-box/config.json</div>
      <div class="ok">✓ 校验通过（exit 0，耗时 41ms）</div>
      <div class="dim">  route.rules: 14 · outbounds: 34 · inbounds: 2</div>`;
    toast('ok', '校验通过', 'exit 0 · 41ms · 可安全写入');
  }, 700);
}

function saveConfig() {
  toast('info', '正在写入', 'tmp → fsync → rename，随后重载核心');
  setTimeout(() => {
    toast('ok', '配置已应用', '快照 v4 已生成，最旧的 v1 已回收');
    snapshots.unshift({ v: 4, time: '刚刚', note: '手动保存 · 34 个出站', current: true, ok: true });
    snapshots.forEach((s, i) => (s.current = i === 0));
    while (snapshots.length > 3) snapshots.pop();
    renderSnaps();
  }, 1100);
}

function renderSnaps() {
  $('#snapList').innerHTML = snapshots.map((s) => `
    <div class="snap ${s.current ? 'current' : ''}">
      <div class="v">v${s.v}</div>
      <div class="meta">
        <div class="t">${s.note} ${s.current ? '<span class="badge lime" style="height:16px;font-size:9px">当前</span>' : ''}</div>
        <div class="d">${s.time} · 校验通过</div>
      </div>
      ${s.current ? '<span class="badge mono" style="height:17px">active</span>'
        : `<button class="btn ghost xs" onclick="confirmDialog('rollback')">回滚</button>`}
    </div>`).join('');
}
renderSnaps();

/* ------------------------------------------------------------ logs */

function seedLogs(n) {
  const now = Date.now();
  const arr = [];
  for (let i = n; i > 0; i--) {
    const [lv, msg] = pick(LOG_POOL);
    const d = new Date(now - i * rnd(900, 2600));
    arr.push({ t: d, lv, msg });
  }
  return arr;
}

function logLine(l, fresh) {
  const ts = l.t.toTimeString().slice(0, 8);
  return `<div class="logline ${fresh ? 'fresh' : ''}">
    <span class="ts">${ts}</span><span class="lv ${l.lv}">${l.lv.toUpperCase()}</span>
    <span class="msg">${l.msg}</span></div>`;
}

state.logs = seedLogs(50);

function renderLogs() {
  const full = $('#fullLog');
  const nearBottom = full.scrollHeight - full.scrollTop - full.clientHeight < 60;
  full.innerHTML = state.logs.map((l) => logLine(l, false)).join('');
  if (nearBottom) full.scrollTop = full.scrollHeight;
  $('#dashLog').innerHTML = state.logs.slice(-7).map((l) => logLine(l, false)).join('');
}

function pushLog() {
  const lastMsg = state.logs.length ? state.logs[state.logs.length - 1].msg : '';
  let entry = pick(LOG_POOL);
  let guard = 0;
  while (entry[1] === lastMsg && guard++ < 8) entry = pick(LOG_POOL);
  const [lv, msg] = entry;
  state.logs.push({ t: new Date(), lv, msg });
  if (state.logs.length > 50) state.logs.shift();
  const full = $('#fullLog');
  const nearBottom = full.scrollHeight - full.scrollTop - full.clientHeight < 80;
  if (nearBottom) {
    full.insertAdjacentHTML('beforeend', logLine(state.logs[state.logs.length - 1], true));
    full.scrollTop = full.scrollHeight;
    const first = full.firstElementChild;
    if (full.children.length > 50) first.remove();
  }
  const dl = $('#dashLog');
  dl.insertAdjacentHTML('beforeend', logLine(state.logs[state.logs.length - 1], true));
  if (dl.children.length > 7) dl.firstElementChild.remove();
}

renderLogs();
setInterval(pushLog, 2400);

/* ------------------------------------------------------------ connections */

let connSeq = 0;
function makeConn() {
  const [host, ip, out, rule] = pick(CONN_HOSTS);
  return {
    id: ++connSeq,
    host,
    ip,
    out,
    rule,
    net: out === 'direct' ? 'tcp' : pick(['tcp', 'tcp', 'h2', 'quic']),
    up: Math.round(rnd(2e3, 4e5)),
    down: Math.round(rnd(1e4, 9e6)),
    start: Date.now() - Math.round(rnd(1e3, 9e5))
  };
}

state.conns = Array.from({ length: 12 }, makeConn);

function renderConns() {
  const q = ($('#connSearch')?.value || '').toLowerCase();
  const rows = state.conns.filter((c) => !q || c.host.includes(q) || c.rule.includes(q) || c.ip.includes(q)).slice(0, 200);
  $('#connTable tbody').innerHTML = rows.map((c) => {
    const dur = Math.floor((Date.now() - c.start) / 1000);
    const durTxt = dur > 3600 ? `${Math.floor(dur / 3600)}h ${Math.floor((dur % 3600) / 60)}m` : dur > 60 ? `${Math.floor(dur / 60)}m ${dur % 60}s` : `${dur}s`;
    return `<tr>
      <td><div class="cell-main"><span class="conn-ic ${c.out === 'proxy' ? 'proxy' : 'direct'}">
        ${c.out === 'proxy'
          ? '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"><path d="M7 17 17 7M9 7h8v8"/></svg>'
          : '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12h16M14 6l6 6-6 6"/></svg>'}
      </span>
        <div class="col"><div class="t">${c.host}</div><div class="s">${c.net.toUpperCase()}</div></div></div></td>
      <td><span class="num" style="color:var(--text-3)">${c.ip}</span></td>
      <td><div class="t" style="font-size:11.5px">${c.out}</div><div class="s" style="font-size:10px;color:var(--text-4);font-family:var(--mono)">${c.rule}</div></td>
      <td><span class="badge ${c.out === 'proxy' ? 'lime' : ''}" style="height:17px">${c.net}</span></td>
      <td class="t-right"><span class="conn-rate" style="justify-content:flex-end"><span class="up">↑</span>${fmtRate(c.up)}</span></td>
      <td class="t-right"><span class="conn-rate" style="justify-content:flex-end"><span class="dn">↓</span>${fmtRate(c.down)}</span></td>
      <td><span class="num" style="color:var(--text-3)">${durTxt}</span></td>
      <td class="t-right"><button class="btn ghost xs" onclick="this.closest('tr').style.opacity=.35;toast('info','已断开','连接将在 2s 内释放')">断开</button></td>
    </tr>`;
  }).join('');
  if (!rows.length) {
    $('#connTable tbody').innerHTML = `<tr><td colspan="8" style="text-align:center;padding:40px;color:var(--text-4)">暂无匹配连接</td></tr>`;
  }
}
renderConns();
$('#connSearch').addEventListener('input', renderConns);

setInterval(() => {
  if (state.paused) return;
  state.conns.forEach((c) => {
    c.up = Math.max(0, c.up * rnd(0.8, 1.25));
    c.down = Math.max(0, c.down * rnd(0.75, 1.3));
  });
  if (Math.random() < 0.55) state.conns.unshift(makeConn());
  if (state.conns.length > 200) state.conns.length = 200;
  renderConns();
}, 2000);

$('#pauseBtn')?.addEventListener('click', (e) => {
  state.paused = !state.paused;
  e.target.textContent = state.paused ? '继续' : '暂停';
  toast(state.paused ? 'info' : 'ok', state.paused ? '已暂停' : '已恢复', state.paused ? 'WebSocket 仍在接收，仅暂停渲染' : '继续按 2s 节流渲染');
});

/* ------------------------------------------------------------ tproxy */

$$('.mode').forEach((m) => m.addEventListener('click', () => {
  $$('.mode').forEach((x) => x.classList.toggle('on', x === m));
  state.tproxyMode = m.dataset.mode;
  $('#tproxyWarn').style.display = m.dataset.mode === 'tproxy' ? 'flex' : 'none';
  $('#tproxyBtn').textContent = state.tproxyOn ? '关闭透明代理' : '启用透明代理';
  if (m.dataset.mode === 'tproxy') {
    toast('err', 'tproxy 内存风险', '256 MB 设备上实测 RSS 可达 219 MB，建议改回 redirect');
  }
}));

function toggleTproxy(el) {
  state.tproxyOn = !state.tproxyOn;
  el.classList.toggle('on', state.tproxyOn);
  $('#tproxyBtn').textContent = state.tproxyOn ? '关闭透明代理' : '启用透明代理';
  $('#topo').classList.toggle('on', state.tproxyOn);
  const tb = $('#topoBadge');
  if (tb) {
    tb.textContent = state.tproxyOn ? 'active' : 'bypass';
    tb.className = 'badge ' + (state.tproxyOn ? 'lime' : '');
  }
  const status = el.closest('.card').querySelector('span[style*="font-size:26px"]');
  if (status) {
    status.textContent = state.tproxyOn ? '已接管' : '未接管';
    status.style.color = state.tproxyOn ? 'var(--lime)' : 'var(--text-3)';
  }
  toast(state.tproxyOn ? 'ok' : 'info',
    state.tproxyOn ? '透明代理已启用' : '透明代理已关闭',
    state.tproxyOn ? `模式 ${state.tproxyMode} · nftables 规则已原子替换` : 'nftables 规则已清空，流量恢复直连');
}

/* ------------------------------------------------------------ dialog */

const DIALOGS = {
  restart: {
    title: '重启 sing-box 核心？',
    body: '现有 128 条连接将被中断，预计 1.2 秒恢复。若新配置启动失败，面板会自动回滚到 v2 快照。',
    confirm: '重启核心', danger: false,
    done: () => toast('ok', '核心已重启', 'PID 4932 · 启动耗时 812ms')
  },
  stop: {
    title: '停止 sing-box 核心？',
    body: '停止后所有代理流量将回落为直连，透明代理规则会被一并清除。',
    confirm: '确认停止', danger: true,
    done: () => toast('err', '核心已停止', '透明代理规则已清空')
  },
  rollback: {
    title: '回滚到 v2 快照？',
    body: '当前配置 v3 会被保留为快照。回滚前会先执行 sing-box check，校验失败则中止。',
    confirm: '回滚并重启', danger: true,
    done: () => toast('ok', '已回滚到 v2', '10-19 18:32 的配置已生效')
  },
  tproxy: {
    title: '启用透明代理？',
    body: '将在 br-lan 上写入 nftables redirect 规则，并接管本机出站流量。规则可一键撤销。',
    confirm: '启用 redirect', danger: false,
    done: () => {
      state.tproxyOn = true;
      const sw = $('#tproxySwitch');
      sw?.classList.add('on');
      $('#topo')?.classList.add('on');
      const tb = $('#topoBadge');
      if (tb) { tb.textContent = 'active'; tb.className = 'badge lime'; }
      const status = sw?.closest('.card')?.querySelector('span[style*="font-size:26px"]');
      if (status) { status.textContent = '已接管'; status.style.color = 'var(--lime)'; }
      $('#tproxyBtn').textContent = '关闭透明代理';
      toast('ok', '透明代理已启用', 'redirect · br-lan · 3 条 nftables 规则');
    }
  },
  closeAll: {
    title: '断开全部连接？',
    body: '将向 Clash API 发送 DELETE /connections，当前 128 条连接会立即中断。',
    confirm: '全部断开', danger: true,
    done: () => { state.conns = []; renderConns(); toast('info', '已断开全部连接', '128 条连接已释放'); }
  },
  delSub: {
    title: '删除这个订阅？',
    body: '订阅元数据与其解析出的节点会一并移除，已生成的配置不会自动变更。',
    confirm: '删除订阅', danger: true,
    done: () => toast('info', '订阅已删除', 'subs.json 已原子更新')
  },
  addSub: {
    title: '新增订阅',
    body: '支持 Clash / sing-box / v2ray 订阅格式。原始响应解析后即丢弃，仅保留节点。',
    confirm: '拉取并解析', danger: false, form: true,
    done: () => {
      subs.unshift({ id: 's' + Date.now(), name: '新订阅 · 待命名', url: 'https://sub.example.com/api/v1/subscribe?token=••••', nodes: 12, used: 0, total: 100, expire: '2027-01-01', updated: '刚刚', auto: true, tone: 'teal' });
      renderSubs();
      toast('ok', '订阅已添加', '解析出 12 个节点，原始响应未落盘');
    }
  }
};

let dialogKey = null;

function openDialog(key) {
  dialogKey = key;
  const d = DIALOGS[key];
  const form = d.form ? `
    <div class="field"><label>订阅名称</label><input class="input" placeholder="例如：主订阅" /></div>
    <div class="field"><label>订阅链接</label><input class="input" placeholder="https://…" /></div>
    <div class="callout"><svg width="14" height="14"><use href="#i-info"/></svg><div>链接会加密存储，日志中仅显示掩码。</div></div>` : '';
  $('#dialog').innerHTML = `
    <div class="dialog-hd">
      <h3>${d.danger ? '<svg width="15" height="15" style="color:var(--red)"><use href="#i-alert"/></svg>' : ''}${d.title}</h3>
      <p>${d.body}</p>
    </div>
    ${form ? `<div class="dialog-bd">${form}</div>` : ''}
    <div class="dialog-ft">
      <button class="btn" onclick="closeDialog()">取消</button>
      <button class="btn ${d.danger ? 'danger' : 'primary'}" id="dialogOk">${d.confirm}</button>
    </div>`;
  $('#dialogOverlay').classList.add('open');
  $('#dialogOk').onclick = () => { closeDialog(); setTimeout(d.done, 260); };
}

function closeDialog() { $('#dialogOverlay').classList.remove('open'); }
window.closeDialog = closeDialog;
window.openDialog = openDialog;
window.confirmDialog = openDialog;
window.toast = toast;
window.toggleNode = toggleNode;
window.pingOne = pingOne;
window.runSpeedtest = runSpeedtest;
window.validateConfig = validateConfig;
window.saveConfig = saveConfig;
window.toggleTproxy = toggleTproxy;

$('#dialogOverlay').addEventListener('click', (e) => { if (e.target.id === 'dialogOverlay') closeDialog(); });

/* ------------------------------------------------------------ command palette */

const COMMANDS = [
  { g: '页面', t: '概览', p: 'dashboard', i: 'i-dash' },
  { g: '页面', t: '订阅管理', p: 'subs', i: 'i-link' },
  { g: '页面', t: '节点列表', p: 'nodes', i: 'i-server' },
  { g: '页面', t: '配置编辑', p: 'config', i: 'i-code' },
  { g: '页面', t: '服务控制', p: 'service', i: 'i-activity' },
  { g: '页面', t: '透明代理', p: 'tproxy', i: 'i-shield' },
  { g: '页面', t: '实时连接', p: 'conns', i: 'i-list' },
  { g: '操作', t: '重启 sing-box 核心', a: () => openDialog('restart'), i: 'i-refresh' },
  { g: '操作', t: '校验当前配置', a: () => { go('config'); validateConfig(); }, i: 'i-check' },
  { g: '操作', t: '对全部节点测速', a: () => { go('nodes'); runSpeedtest(); }, i: 'i-zap' },
  { g: '操作', t: '启用透明代理', a: () => openDialog('tproxy'), i: 'i-shield' },
  { g: '操作', t: '新增订阅', a: () => { go('subs'); openDialog('addSub'); }, i: 'i-plus' }
];

let palIdx = 0;

function renderPalette(q = '') {
  const items = COMMANDS.filter((c) => c.t.toLowerCase().includes(q.toLowerCase()));
  palIdx = 0;
  let html = '', lastG = '';
  items.forEach((c, i) => {
    if (c.g !== lastG) { html += `<div class="palette-group">${c.g}</div>`; lastG = c.g; }
    html += `<button class="palette-item ${i === 0 ? 'on' : ''}" data-i="${i}">
      <svg width="14" height="14"><use href="#${c.i}"/></svg>${c.t}
      ${c.h ? `<span class="hint">${c.h}</span>` : ''}</button>`;
  });
  $('#paletteList').innerHTML = html || `<div style="padding:24px;text-align:center;color:var(--text-4);font-size:12px">无匹配命令</div>`;
  $$('.palette-item').forEach((el) => el.addEventListener('click', () => execPalette(items[+el.dataset.i])));
  $('#paletteList')._items = items;
}

function execPalette(c) {
  $('#paletteOverlay').classList.remove('open');
  $('#paletteInput').value = '';
  if (c.p) go(c.p);
  if (c.a) c.a();
}

function openPalette() {
  renderPalette('');
  $('#paletteOverlay').classList.add('open');
  setTimeout(() => $('#paletteInput').focus(), 60);
}

$('#cmdkBtn').addEventListener('click', openPalette);
$('#paletteOverlay').addEventListener('click', (e) => { if (e.target.id === 'paletteOverlay') $('#paletteOverlay').classList.remove('open'); });
$('#paletteInput').addEventListener('input', (e) => renderPalette(e.target.value));

document.addEventListener('keydown', (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); openPalette(); return; }
  if (e.key === 'Escape') {
    $('#paletteOverlay').classList.remove('open');
    closeDialog();
  }
  if (!$('#paletteOverlay').classList.contains('open')) return;
  const items = $('#paletteList')._items || [];
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    palIdx = (palIdx + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
    $$('.palette-item').forEach((el, i) => el.classList.toggle('on', i === palIdx));
  }
  if (e.key === 'Enter' && items[palIdx]) execPalette(items[palIdx]);
});

/* ------------------------------------------------------------ memory arc */

setTimeout(() => {
  const arc = $('#memArc');
  if (arc) arc.style.transition = 'stroke-dashoffset 1.4s cubic-bezier(.16,1,.3,1)';
  if (arc) arc.style.strokeDashoffset = (301.6 * (1 - 0.71)).toFixed(1);
}, 250);

/* ------------------------------------------------------------ live counters */

setInterval(() => {
  const el = $('.stat-value[data-count]');
  if (!el) return;
  const cur = +el.textContent.replace(/\D/g, '');
  el.textContent = Math.max(96, Math.min(180, cur + Math.round(rnd(-4, 5))));
}, 4000);

/* ------------------------------------------------------------ demo hooks (screenshots only) */

const demo = new URLSearchParams(location.search).get('demo');
if (demo === 'palette') setTimeout(openPalette, 400);
if (demo === 'dialog') setTimeout(() => openDialog('restart'), 400);
if (demo === 'toast') setTimeout(() => toast('ok', '配置已应用', '快照 v4 已生成，最旧的 v1 已回收'), 400);
