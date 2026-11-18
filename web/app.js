'use strict';
/* TNS Panel — core, charts, map, dashboard, security. Vanilla JS, no build step. */

// ================= utils =================
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const ico = (n, cls = '') => `<svg class="ico ${cls}"><use href="#i-${n}"/></svg>`;
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));

function fmtBytes(b, d = 1) {
  if (b == null || isNaN(b)) return '—';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (Math.abs(b) >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return (i === 0 ? Math.round(b) : b.toFixed(b >= 100 ? 0 : d)) + ' ' + u[i];
}
const fmtRate = b => fmtBytes(b) + '/s';
function fmtDur(s) {
  s = Math.floor(s);
  const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  return `${m}m`;
}
const pad2 = n => String(n).padStart(2, '0');
const fmtClock = d => `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
function fmtDT(t) {
  const d = new Date(t);
  return `${pad2(d.getDate())}/${pad2(d.getMonth() + 1)} ${fmtClock(d)}`;
}
function fmtAgo(t) {
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 60) return 'vừa xong';
  if (s < 3600) return Math.floor(s / 60) + ' phút trước';
  if (s < 86400) return Math.floor(s / 3600) + ' giờ trước';
  return Math.floor(s / 86400) + ' ngày trước';
}
const cssv = v => { const k = v.startsWith('var(') ? v.slice(4, -1) : v; return k.startsWith('--') ? getComputedStyle(document.documentElement).getPropertyValue(k).trim() : v; };
const levelCls = p => p >= 90 ? 'bad' : p >= 70 ? 'warn' : '';
const levelColor = p => p >= 90 ? 'var(--bad)' : p >= 70 ? 'var(--warn)' : 'var(--text)';

// ================= api =================
async function api(path, opts = {}) {
  const o = { method: opts.method || (opts.body !== undefined ? 'POST' : 'GET'), headers: { 'X-TNS': '1' } };
  if (opts.body !== undefined) {
    if (opts.body instanceof FormData) o.body = opts.body;
    else { o.body = JSON.stringify(opts.body); o.headers['Content-Type'] = 'application/json'; }
  }
  const r = await fetch(path, o);
  if (r.status === 401 && !path.startsWith('/api/auth')) { showAuth('login'); throw new Error('Phiên đăng nhập đã hết hạn'); }
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}

// ================= toast / modal =================
function toast(msg, type = 'ok', ms = 3800) {
  const t = document.createElement('div');
  t.className = 'toast ' + type;
  t.innerHTML = ico(type === 'err' ? 'alert' : 'check') + `<div>${esc(msg)}</div>`;
  $('#toasts').appendChild(t);
  setTimeout(() => t.remove(), type === 'err' ? ms * 1.6 : ms);
}
const toastErr = e => toast(e.message || String(e), 'err');

function openModal({ title, body = '', foot = '', size = '', flush = false, onClose }) {
  const bg = document.createElement('div');
  bg.className = 'modal-bg';
  bg.innerHTML = `<div class="modal card ${size}" role="dialog" aria-modal="true">
    <div class="modal-h"><h3>${title}</h3><span class="mh-extra row"></span><button class="btn ghost icon" data-close title="Đóng">${ico('x')}</button></div>
    <div class="modal-b ${flush ? 'flush' : ''}">${body}</div>${foot ? `<div class="modal-f">${foot}</div>` : ''}</div>`;
  const close = () => { bg.remove(); document.removeEventListener('keydown', onKey); onClose && onClose(); };
  const onKey = e => { if (e.key === 'Escape') close(); };
  bg.addEventListener('mousedown', e => { if (e.target === bg) bg._down = true; });
  bg.addEventListener('click', e => { if ((e.target === bg && bg._down) || e.target.closest('[data-close]')) close(); bg._down = false; });
  document.addEventListener('keydown', onKey);
  $('#modalRoot').appendChild(bg);
  const m = $('.modal', bg);
  m.close = close;
  return m;
}

function confirmBox(msg, { ok = 'Xác nhận', danger = false, title = 'Xác nhận' } = {}) {
  return new Promise(res => {
    let done = false;
    const m = openModal({
      title, body: `<p style="margin:0;white-space:pre-wrap">${msg}</p>`,
      foot: `<button class="btn" data-close>Huỷ</button><button class="btn ${danger ? 'danger' : 'primary'}" data-ok>${ok}</button>`,
      onClose: () => { if (!done) res(false); },
    });
    $('[data-ok]', m).onclick = () => { done = true; res(true); m.close(); };
    $('[data-ok]', m).focus();
  });
}

function promptBox(title, label, value = '') {
  return new Promise(res => {
    let done = false;
    const m = openModal({
      title, body: `<form class="form"><label class="field"><span>${label}</span><input value="${esc(value)}"></label></form>`,
      foot: `<button class="btn" data-close>Huỷ</button><button class="btn primary" data-ok>OK</button>`,
      onClose: () => { if (!done) res(null); },
    });
    const inp = $('input', m);
    const ok = () => { done = true; res(inp.value.trim()); m.close(); };
    $('[data-ok]', m).onclick = ok;
    $('form', m).onsubmit = e => { e.preventDefault(); ok(); };
    inp.focus(); inp.select();
  });
}

async function blockIP(ip) {
  if (!await confirmBox(`Chặn toàn bộ kết nối từ <b class="mono">${esc(ip)}</b> bằng tường lửa?`, { ok: 'Chặn IP', danger: true })) return;
  try {
    await api('/api/firewall', { body: { op: 'add', action: 'deny', from: ip, comment: 'blocked by tns' } });
    toast('Đã chặn ' + ip);
  } catch (e) { toastErr(e); }
}

// ================= charts =================
function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v))), n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 4 ? 4 : n <= 6 ? 6 : n <= 8 ? 8 : 10) * p;
}

class Chart {
  // opts: { series:[{key,color,label,fill}], max, fmt, type:'line'|'bar', xfmt }
  constructor(el, opts) {
    this.el = el;
    this.o = Object.assign({ fmt: v => v, type: 'line', xfmt: t => fmtClock(new Date(t)) }, opts);
    this.cv = document.createElement('canvas');
    this.tip = document.createElement('div');
    this.tip.className = 'chart-tip hidden';
    el.append(this.cv, this.tip);
    this.data = [];
    this.hover = -1;
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(el);
    this.cv.addEventListener('mousemove', e => this.onMove(e));
    this.cv.addEventListener('mouseleave', () => { this.hover = -1; this.tip.classList.add('hidden'); this.draw(); });
  }
  resize() {
    const r = this.el.getBoundingClientRect();
    if (!r.width) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.w = r.width; this.h = r.height;
    this.cv.width = Math.round(r.width * dpr); this.cv.height = Math.round(r.height * dpr);
    this.ctx = this.cv.getContext('2d');
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.draw();
  }
  set(data) { this.data = data || []; this.draw(); }
  geom() { return { l: this.padL || 44, r: 8, t: 8, b: 20 }; }
  onMove(e) {
    if (!this.data.length) return;
    const g = this.geom(), r = this.cv.getBoundingClientRect();
    const x = e.clientX - r.left, iw = this.w - g.l - g.r, n = this.data.length;
    const i = this.o.type === 'bar' ? Math.floor((x - g.l) / iw * n) : Math.round((x - g.l) / iw * (n - 1));
    this.hover = clamp(i, 0, n - 1);
    this.draw();
  }
  draw() {
    const c = this.ctx;
    if (!c) return;
    const { w, h, data, o } = this;
    c.clearRect(0, 0, w, h);
    let max = o.max;
    if (max == null) {
      let m = 0;
      for (const d of data) {
        if (o.type === 'bar') m = Math.max(m, o.series.reduce((s, se) => s + (d[se.key] || 0), 0));
        else for (const se of o.series) m = Math.max(m, d[se.key] || 0);
      }
      max = niceMax(m * 1.1);
    }
    c.font = '10px ' + getComputedStyle(document.body).getPropertyValue('--mono');
    this.padL = Math.ceil(Math.max(c.measureText(o.fmt(max)).width, c.measureText(o.fmt(max / 4)).width, 18)) + 14;
    const g = this.geom(), iw = w - g.l - g.r, ih = h - g.t - g.b, n = data.length;
    c.textBaseline = 'middle';
    for (let i = 0; i <= 4; i++) {
      const y = g.t + ih - ih * i / 4;
      c.strokeStyle = cssv(i === 0 ? '--chart-axis' : '--chart-grid');
      c.lineWidth = 1;
      c.beginPath(); c.moveTo(g.l, Math.round(y) + .5); c.lineTo(w - g.r, Math.round(y) + .5); c.stroke();
      c.fillStyle = cssv('--muted'); c.textAlign = 'right';
      c.fillText(o.fmt(max * i / 4), g.l - 8, y);
    }
    if (!n) {
      c.fillStyle = cssv('--muted'); c.textAlign = 'center';
      c.fillText('Đang thu thập dữ liệu…', g.l + iw / 2, g.t + ih / 2);
      return;
    }
    c.textAlign = 'center'; c.textBaseline = 'top'; c.fillStyle = cssv('--muted');
    const ticks = Math.min(n, Math.max(2, Math.floor(iw / 110)));
    for (let k = 0; k < ticks; k++) {
      const i = Math.round(k * (n - 1) / Math.max(1, ticks - 1));
      const x = this.xAt(i, g, iw, n);
      c.textAlign = k === 0 ? 'left' : k === ticks - 1 ? 'right' : 'center';
      c.fillText(o.xfmt(data[i].t), x, h - g.b + 6);
    }
    const yOf = v => g.t + ih - clamp(v / max, 0, 1) * ih;
    if (o.type === 'bar') {
      const bw = Math.max(1, iw / n - (iw / n > 4 ? 1.5 : .5));
      data.forEach((d, i) => {
        let base = 0;
        for (const se of o.series) {
          const v = d[se.key] || 0;
          if (!v) continue;
          const y0 = yOf(base), y1 = yOf(base + v);
          c.fillStyle = cssv(se.color);
          c.globalAlpha = this.hover === -1 || this.hover === i ? .9 : .45;
          c.fillRect(g.l + i * iw / n, y1, bw, Math.max(1, y0 - y1));
          base += v;
        }
      });
      c.globalAlpha = 1;
    } else {
      for (const se of o.series) {
        c.beginPath();
        data.forEach((d, i) => { const x = this.xAt(i, g, iw, n), y = yOf(d[se.key] || 0); i ? c.lineTo(x, y) : c.moveTo(x, y); });
        if (se.fill !== false) {
          c.save();
          c.lineTo(this.xAt(n - 1, g, iw, n), g.t + ih); c.lineTo(this.xAt(0, g, iw, n), g.t + ih); c.closePath();
          c.fillStyle = cssv(se.color); c.globalAlpha = .1; c.fill(); c.globalAlpha = 1;
          c.restore();
          c.beginPath();
          data.forEach((d, i) => { const x = this.xAt(i, g, iw, n), y = yOf(d[se.key] || 0); i ? c.lineTo(x, y) : c.moveTo(x, y); });
        }
        c.strokeStyle = cssv(se.color); c.lineWidth = 2; c.lineJoin = 'round';
        c.stroke();
      }
    }
    if (this.hover >= 0 && this.hover < n) {
      const d = data[this.hover];
      const x = o.type === 'bar' ? g.l + (this.hover + .5) * iw / n : this.xAt(this.hover, g, iw, n);
      if (o.type !== 'bar') {
        c.strokeStyle = cssv('--chart-axis'); c.setLineDash([3, 3]);
        c.beginPath(); c.moveTo(x + .5, g.t); c.lineTo(x + .5, g.t + ih); c.stroke(); c.setLineDash([]);
        for (const se of o.series) {
          c.fillStyle = cssv(se.color); c.beginPath(); c.arc(x, yOf(d[se.key] || 0), 3.5, 0, 7); c.fill();
        }
      }
      this.tip.innerHTML = `<div style="color:var(--muted);margin-bottom:3px">${esc(o.tipTime ? o.tipTime(d.t) : fmtDT(d.t))}</div>` +
        o.series.map(se => `<div><i style="background:${se.color}"></i>${esc(se.label)}: <b>${esc(o.fmt(d[se.key] || 0, true))}</b></div>`).join('');
      this.tip.classList.remove('hidden');
      const tw = this.tip.offsetWidth;
      this.tip.style.left = clamp(x + 12, 0, w - tw) + 'px';
      if (x + 12 + tw > w) this.tip.style.left = Math.max(0, x - tw - 12) + 'px';
      this.tip.style.top = '6px';
    }
  }
  xAt(i, g, iw, n) { return this.o.type === 'bar' ? g.l + (i + .5) * iw / n : g.l + (n === 1 ? iw : i * iw / (n - 1)); }
  destroy() { this.ro.disconnect(); }
}

function gaugeHTML(id, label) {
  return `<div class="card metric" id="${id}">
    <div class="m-top"><span class="m-label">${label}</span><span class="m-main mono">—</span></div>
    <div class="m-val mono">—</div>
    <div class="meter"><i></i></div>
    <div class="m-sub">—</div></div>`;
}
function setGauge(el, pct, main, sub) {
  const p = clamp(pct || 0, 0, 100), bar = $('.meter i', el);
  bar.style.width = p + '%';
  bar.className = levelCls(p);
  $('.m-val', el).innerHTML = `${p.toFixed(p < 10 ? 1 : 0)}<small>%</small>`;
  $('.m-main', el).textContent = main;
  $('.m-sub', el).textContent = sub;
}

// ================= world map =================
class WorldMap {
  constructor(el) {
    this.el = el;
    this.cv = document.createElement('canvas');
    this.tip = document.createElement('div');
    this.tip.className = 'chart-tip hidden';
    el.append(this.cv, this.tip);
    this.markers = [];
    this.hover = null;
    const M = window.WORLD_MASK;
    this.M = M;
    this.bits = new Uint8Array(M.w * M.h);
    M.rows.forEach((row, y) => {
      for (let i = 0; i < row.length; i++) {
        const v = parseInt(row[i], 16);
        for (let b = 0; b < 4; b++) if (v & (8 >> b)) this.bits[y * M.w + i * 4 + b] = 1;
      }
    });
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(el);
    this.cv.addEventListener('mousemove', e => this.onMove(e));
    this.cv.addEventListener('mouseleave', () => { this.hover = null; this.tip.classList.add('hidden'); this.draw(); });
    this.cv.addEventListener('click', () => { if (this.hover && this.onPick) this.onPick(this.hover); });
  }
  proj(lat, lon) {
    const M = this.M;
    return [(lon + 180) / 360 * this.w, (M.lat0 - lat) / (M.lat0 - M.lat1) * this.h];
  }
  resize() {
    const r = this.el.getBoundingClientRect();
    if (!r.width) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.w = r.width; this.h = r.height; this.dpr = dpr;
    this.cv.width = Math.round(r.width * dpr); this.cv.height = Math.round(r.height * dpr);
    this.ctx = this.cv.getContext('2d');
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    // pre-render the dotted continents once per size
    const base = document.createElement('canvas');
    base.width = this.cv.width; base.height = this.cv.height;
    const b = base.getContext('2d');
    b.setTransform(dpr, 0, 0, dpr, 0, 0);
    const M = this.M, cw = this.w / M.w, ch = this.h / M.h;
    const step = Math.max(1, Math.ceil(5 / cw));
    b.strokeStyle = cssv('--map-grid'); b.lineWidth = 1;
    for (const lat of [60, 30, 0, -30]) { const y = Math.round(this.proj(lat, 0)[1]) + .5; b.beginPath(); b.moveTo(0, y); b.lineTo(this.w, y); b.stroke(); }
    for (let lon = -150; lon < 180; lon += 30) { const x = Math.round(this.proj(0, lon)[0]) + .5; b.beginPath(); b.moveTo(x, 0); b.lineTo(x, this.h); b.stroke(); }
    b.fillStyle = cssv('--map-land');
    const sz = Math.max(1.5, Math.min(2.5, cw * step * .45));
    for (let y = Math.floor(step / 2); y < M.h; y += step) {
      for (let x = Math.floor(step / 2); x < M.w; x += step) {
        if (!this.bits[y * M.w + x]) continue;
        b.beginPath(); b.arc((x + .5) * cw, (y + .5) * ch, sz / 2, 0, 7); b.fill();
      }
    }
    this.base = base;
    this.draw();
  }
  set(markers) {
    this.markers = markers;
    this.draw();

  }
  loop() {
    let last = 0;
    const tick = ts => {
      if (!this.el.isConnected) { this.raf = null; return; }
      this.raf = requestAnimationFrame(tick);
      if (document.hidden || ts - last < 50) return;
      last = ts;
      this.draw(ts);
    };
    this.raf = requestAnimationFrame(tick);
  }
  radius(m) { return 2.2 + Math.log2(m.total + 1) * 1.15; }
  draw(ts = performance.now()) {
    const c = this.ctx;
    if (!c || !this.base) return;
    c.setTransform(1, 0, 0, 1, 0, 0);
    c.clearRect(0, 0, this.cv.width, this.cv.height);
    c.drawImage(this.base, 0, 0);
    c.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    const sorted = [...this.markers].sort((a, b) => b.total - a.total);
    for (const m of sorted) {
      const [x, y] = this.proj(m.lat, m.lon), r = this.radius(m);
      const col = cssv(m.flag ? '--warnc' : m.ok ? '--okc' : '--badc');
      c.fillStyle = col; c.globalAlpha = .22; c.beginPath(); c.arc(x, y, r * 1.5, 0, 7); c.fill();
      c.globalAlpha = 1; c.strokeStyle = col; c.lineWidth = 1; c.beginPath(); c.arc(x, y, r * 1.5, 0, 7); c.stroke();
      c.beginPath(); c.arc(x, y, Math.max(2, r * .5), 0, 7); c.fill();
      if (this.hover === m) { c.strokeStyle = cssv('--text'); c.lineWidth = 1.3; c.beginPath(); c.arc(x, y, r + 2, 0, 7); c.stroke(); }
    }
  }
  onMove(e) {
    const r = this.cv.getBoundingClientRect(), mx = e.clientX - r.left, my = e.clientY - r.top;
    let best = null, bd = 1e9;
    for (const m of this.markers) {
      const [x, y] = this.proj(m.lat, m.lon), d = Math.hypot(x - mx, y - my);
      if (d < Math.max(10, this.radius(m) + 4) && d < bd) { bd = d; best = m; }
    }
    if (best !== this.hover) { this.hover = best; this.draw(); }
    this.cv.style.cursor = best ? 'pointer' : '';
    if (!best) { this.tip.classList.add('hidden'); return; }
    this.tip.innerHTML = best.html;
    this.tip.classList.remove('hidden');
    const tw = this.tip.offsetWidth, th = this.tip.offsetHeight;
    this.tip.style.left = clamp(mx + 14, 0, this.w - tw) + 'px';
    this.tip.style.top = clamp(my - th - 10, 0, this.h - th) + 'px';
  }
  destroy() { this.ro.disconnect(); if (this.raf) cancelAnimationFrame(this.raf); this.raf = null; }
}

// ================= app state / router =================
const S = { host: '', demo: false, info: null, stats: null, statListeners: new Set() };
const pages = {};
const NAV = [
  ['Giám sát', [['dashboard', 'dash', 'Tổng quan'], ['security', 'globe', 'Đăng nhập & IP']]],
  ['Bảo mật', [['firewall', 'shield', 'Tường lửa']]],
  ['Tài nguyên', [['apps', 'box', 'Ứng dụng & dịch vụ'], ['files', 'folder', 'Tệp tin'], ['terminal', 'term', 'Terminal']]],
  ['Quản trị', [['settings', 'gear', 'Cài đặt']]],
];
const NAV_FLAT = NAV.flatMap(g => g[1]);
const Theme = {
  get() { try { return localStorage.getItem('tns.theme') || 'light'; } catch (e) { return 'light'; } },
  apply(t) { document.documentElement.dataset.theme = t; },
  toggle() {
    const t = this.get() === 'dark' ? 'light' : 'dark';
    try { localStorage.setItem('tns.theme', t); } catch (e) { }
    this.apply(t);
    if (curPage && curPage !== 'terminal') { curPage = null; route(); }
  },
};
Theme.apply(Theme.get());
let cleanup = [], curPage = null, timers = [];

function every(ms, fn, immediate = true) {
  let busy = false;
  const run = async () => {
    if (document.hidden || busy) return;
    busy = true;
    try { await fn(); } catch (e) { console.warn(e); }
    busy = false;
  };
  if (immediate) run();
  timers.push(setInterval(run, ms));
}

function route() {
  const name = (location.hash.replace(/^#\/?/, '') || 'dashboard').split('?')[0];
  const page = pages[name] ? name : 'dashboard';
  if (page === curPage) return;
  cleanup.forEach(f => { try { f(); } catch (e) { console.warn(e); } });
  cleanup = [];
  timers.forEach(clearInterval);
  timers = [];
  curPage = page;
  $$('#navList .nav-item').forEach(a => a.classList.toggle('active', a.dataset.page === page));
  const p = pages[page];
  $('#pageTitle').textContent = p.title;
  $('#pageSub').textContent = p.sub || '';
  $('#crumbCur').textContent = (NAV_FLAT.find(n => n[0] === page) || [, , p.title])[2];
  document.body.classList.remove('nav-open');
  const v = $('#view');
  v.innerHTML = '';
  v.scrollTop = 0;
  v.style.padding = p.fullBleed ? '0' : '';
  v.style.overflow = p.fullBleed ? 'hidden' : '';
  const c = p.mount(v);
  if (typeof c === 'function') cleanup.push(c);
  else if (Array.isArray(c)) cleanup.push(...c);
}

function onStats(fn) { S.statListeners.add(fn); if (S.stats) fn(S.stats); return () => S.statListeners.delete(fn); }

let statTimer = null;
function startStatsLoop() {
  if (statTimer) return;
  const tick = async () => {
    if (!document.hidden) {
      try {
        const r = await api('/api/sys/stats');
        S.stats = r.current;
        updateTopbar();
        S.statListeners.forEach(fn => fn(r.current));
      } catch (e) { /* offline */ }
    }
    statTimer = setTimeout(tick, curPage === 'dashboard' ? 2000 : 5000);
  };
  tick();
}

function updateTopbar() {
  const s = S.stats;
  if (!s) return;
  $('#tbLive').textContent = `CPU ${s.cpu.toFixed(0)}% · RAM ${s.mem.toFixed(0)}%`;
  $('#tbUp').textContent = 'Uptime ' + fmtDur(s.uptime);
}

// ================= auth =================
async function boot() {
  let st;
  try { st = await (await fetch('/api/auth/state')).json(); } catch (e) { document.body.innerHTML = '<p style="padding:20px">Không kết nối được máy chủ.</p>'; return; }
  S.host = st.hostname; S.demo = st.demo;
  document.title = `TNS · ${st.hostname}`;
  if (st.setup) showAuth('setup');
  else if (!st.authed) showAuth('login');
  else startApp();
}

function pwScore(p) {
  let s = 0;
  if (p.length >= 8) s++;
  if (p.length >= 12) s++;
  if (/[a-z]/.test(p) && /[A-Z]/.test(p)) s++;
  if (/\d/.test(p)) s++;
  if (/[^A-Za-z0-9]/.test(p)) s++;
  return s;
}

function showAuth(mode) {
  $('#app').classList.add('hidden');
  $('#auth').classList.remove('hidden');
  $('#authHost').textContent = `Máy chủ ${S.host || ''} · cổng ${location.port || (location.protocol === 'https:' ? 443 : 80)}`;
  const setup = mode === 'setup';
  $('#authHint').textContent = setup ? 'Lần chạy đầu tiên — hãy đặt mật khẩu quản trị (tối thiểu 8 ký tự). Mật khẩu được lưu dạng băm bcrypt trên máy chủ.' : '';
  $('#pw2wrap').classList.toggle('hidden', !setup);
  $('#strength').classList.toggle('hidden', !setup);
  $('#authBtn').textContent = setup ? 'Khởi tạo & vào bảng điều khiển' : 'Đăng nhập';
  $('#authTitle').textContent = setup ? 'Thiết lập lần đầu' : 'Đăng nhập';
  $('#pw1').autocomplete = setup ? 'new-password' : 'current-password';
  $('#pw1').value = ''; $('#pw2').value = ''; $('#authErr').textContent = '';
  const chars = s => [...s].length;
  const count = () => {
    const n = chars($('#pw1').value);
    $('#pwCount').innerHTML = setup ? `<span class="${n >= 8 ? 'ok' : ''}">${n}/8 ký tự</span>` : '';
  };
  $('#pwShow').checked = false;
  $('#pwShow').onchange = e => { const t = e.target.checked ? 'text' : 'password'; $('#pw1').type = t; $('#pw2').type = t; };
  $('#pw1').type = 'password'; $('#pw2').type = 'password';
  count();
  $('#pw1').oninput = () => {
    count();
    if (!setup) return;
    const s = pwScore($('#pw1').value), bar = $('#strength i');
    bar.style.width = (s * 20) + '%';
    bar.style.background = s <= 2 ? 'var(--bad)' : s <= 3 ? 'var(--warn)' : 'var(--ok)';
  };
  $('#authForm').onsubmit = async e => {
    e.preventDefault();
    const pw = $('#pw1').value;
    const err = $('#authErr');
    err.textContent = '';
    if (setup) {
      if (chars(pw) < 8) { err.textContent = `Mật khẩu cần ít nhất 8 ký tự (ô mật khẩu đang có ${chars(pw)})`; return; }
      if (pw !== $('#pw2').value) { err.textContent = 'Hai mật khẩu không khớp'; return; }
    }
    $('#authBtn').disabled = true;
    try {
      await api(setup ? '/api/auth/setup' : '/api/auth/login', { body: { password: pw } });
      startApp();
    } catch (ex) { err.textContent = ex.message; }
    $('#authBtn').disabled = false;
  };
  setTimeout(() => $('#pw1').focus(), 50);
}

let appStarted = false;
function startApp() {
  $('#auth').classList.add('hidden');
  $('#app').classList.remove('hidden');
  $('#tbHost').textContent = S.host;
  $('#demoBadge').classList.toggle('hidden', !S.demo);
  if (appStarted) { curPage = null; route(); return; }
  appStarted = true;
  $('#navList').innerHTML = NAV.map(([group, items]) => `<div class="nav-group">${group}</div>` + items.map(([id, icon, label]) =>
    `<a class="nav-item" href="#/${id}" data-page="${id}" title="${label}">${ico(icon)}<span>${label}</span></a>`).join('')).join('');
  $('#menuBtn').onclick = () => document.body.classList.toggle('nav-open');
  $('#navScrim').onclick = () => document.body.classList.remove('nav-open');
  $('#themeBtn').onclick = () => Theme.toggle();
  $('#logoutBtn').onclick = async () => { await api('/api/auth/logout', { body: {} }).catch(() => { }); location.hash = ''; location.reload(); };
  window.addEventListener('hashchange', route);
  setInterval(() => { $('#tbClock').textContent = new Date().toLocaleTimeString('vi-VN', { hour12: false }); }, 1000);
  startStatsLoop();
  api('/api/sys/info').then(i => { S.info = i; }).catch(() => { });
  route();
}

// ================= page: dashboard =================
pages.dashboard = {
  title: 'Tổng quan', sub: 'Trạng thái máy chủ theo thời gian thực',
  mount(v) {
    v.innerHTML = `
    <div class="grid g4">${gaugeHTML('gCpu', 'CPU')}${gaugeHTML('gMem', 'Bộ nhớ')}${gaugeHTML('gDisk', 'Ổ đĩa /')}${gaugeHTML('gLoad', 'Tải hệ thống')}</div>
    <div class="grid g-main mt">
      <div class="card"><div class="card-h"><span class="card-t">CPU &amp; bộ nhớ</span>
        <span class="legend"><span><i style="background:var(--c1)"></i>CPU <b id="lgCpu">—</b></span><span><i style="background:var(--c2)"></i>RAM <b id="lgMem">—</b></span></span>
        <span class="sp"></span><div class="seg" id="rangeSeg"><button data-r="short" class="on">10 phút</button><button data-r="long">24 giờ</button></div></div>
        <div class="chart" id="cCpu" style="height:200px"></div></div>
      <div class="card"><div class="card-h"><span class="card-t">Hệ thống</span></div><dl class="kv" id="sysInfo"><dt>…</dt><dd></dd></dl></div>
    </div>
    <div class="grid g2 mt">
      <div class="card"><div class="card-h"><span class="card-t">Mạng</span><span class="sp"></span>
        <span class="legend"><span><i style="background:var(--c3)"></i>↓ <b id="lgRx">—</b></span><span><i style="background:var(--c2)"></i>↑ <b id="lgTx">—</b></span></span></div>
        <div class="chart" id="cNet"></div></div>
      <div class="card"><div class="card-h"><span class="card-t">Disk I/O</span><span class="sp"></span>
        <span class="legend"><span><i style="background:var(--okc)"></i>Đọc <b id="lgDr">—</b></span><span><i style="background:var(--warnc)"></i>Ghi <b id="lgDw">—</b></span></span></div>
        <div class="chart" id="cDisk"></div></div>
    </div>
    <div class="grid g-main mt">
      <div class="card"><div class="card-h"><span class="card-t">Tiến trình</span><span class="card-sub" id="procTotal"></span><span class="sp"></span>
        <div class="seg" id="procSeg"><button data-s="cpu" class="on">CPU</button><button data-s="mem">RAM</button></div></div>
        <div class="tbl-wrap" style="max-height:420px"><table class="tbl"><thead><tr><th>PID</th><th>Tên</th><th>User</th><th class="num">CPU</th><th class="num">RAM</th><th></th></tr></thead><tbody id="procBody"></tbody></table></div></div>
      <div class="grid" style="align-content:start">
        <div class="card"><div class="card-h"><span class="card-t">Nhân CPU</span><span class="sp"></span><span class="card-sub mono" id="tempLbl"></span></div><div class="cores" id="cores"></div></div>
        <div class="card"><div class="card-h"><span class="card-t">Phân vùng</span></div><div class="bars" id="disks"></div></div>
      </div>
    </div>`;

    const pct = v => v.toFixed(0) + '%';
    const cCpu = new Chart($('#cCpu'), { max: 100, fmt: (x, tip) => tip ? x.toFixed(1) + '%' : pct(x), series: [{ key: 'cpu', color: 'var(--c1)', label: 'CPU' }, { key: 'mem', color: 'var(--c2)', label: 'RAM' }] });
    const cNet = new Chart($('#cNet'), { fmt: fmtRate, series: [{ key: 'rx', color: 'var(--c3)', label: 'Nhận' }, { key: 'tx', color: 'var(--c2)', label: 'Gửi' }] });
    const cDisk = new Chart($('#cDisk'), { fmt: fmtRate, series: [{ key: 'dr', color: 'var(--okc)', label: 'Đọc' }, { key: 'dw', color: 'var(--warnc)', label: 'Ghi' }] });
    const charts = [cCpu, cNet, cDisk];
    let range = 'short', hist = [];

    const loadHist = async () => {
      const r = await api('/api/sys/stats?history=' + range);
      hist = r.history || [];
      charts.forEach(c => c.set(hist));
    };
    loadHist();
    $('#rangeSeg').onclick = e => {
      const b = e.target.closest('button'); if (!b) return;
      range = b.dataset.r;
      $$('#rangeSeg button').forEach(x => x.classList.toggle('on', x === b));
      loadHist();
    };
    every(60000, () => range === 'long' && loadHist(), false);

    const info = () => {
      const i = S.info;
      if (!i) return;
      const ips = (i.ips || []).slice(0, 4).map(x => `${esc(x.addr)} <span class="dim">(${esc(x.iface)})</span>`).join('<br>');
      $('#sysInfo').innerHTML = `
        <dt>Hostname</dt><dd>${esc(i.hostname)}</dd>
        <dt>Hệ điều hành</dt><dd>${esc(i.platform || i.os)} ${esc(i.platformVersion || '')}</dd>
        <dt>Kernel</dt><dd>${esc(i.kernel || '—')} · ${esc(i.arch)}</dd>
        <dt>CPU</dt><dd>${esc(i.cpuModel || '—')}<br><span class="dim">${i.cores} nhân / ${i.threads} luồng</span></dd>
        ${i.virtualization ? `<dt>Ảo hoá</dt><dd>${esc(i.virtualization)}</dd>` : ''}
        <dt>Uptime</dt><dd id="kvUp">—</dd>
        <dt>Địa chỉ IP</dt><dd>${ips || '—'}</dd>`;
    };
    if (S.info) info(); else api('/api/sys/info').then(i => { S.info = i; info(); });

    const off = onStats(s => {
      setGauge($('#gCpu'), s.cpu, `${(s.cores || []).length} luồng`, s.temp ? `Nhiệt độ ${s.temp.toFixed(0)}°C` : 'Trung bình tất cả các nhân');
      setGauge($('#gMem'), s.mem, `${fmtBytes(s.memUsed)} / ${fmtBytes(s.memTotal)}`, s.swapTotal ? `Swap ${fmtBytes(s.swapUsed)} / ${fmtBytes(s.swapTotal)}` : 'Không có swap');
      const root = (s.disks || []).find(d => d.mount === '/' || d.mount === '/System/Volumes/Data') || (s.disks || [])[0];
      if (root) setGauge($('#gDisk'), root.pct, `${fmtBytes(root.used)} / ${fmtBytes(root.total)}`, `${root.fs} · ${root.device}`);
      const th = (s.cores || []).length || 1;
      setGauge($('#gLoad'), s.load / th * 100, `${s.load.toFixed(2)} · ${s.load5.toFixed(2)} · ${s.load15.toFixed(2)}`, `${s.procs} tiến trình · 1/5/15 phút`);
      $('#lgCpu').textContent = s.cpu.toFixed(1) + '%';
      $('#lgMem').textContent = s.mem.toFixed(1) + '%';
      $('#lgRx').textContent = fmtRate(s.rx); $('#lgTx').textContent = fmtRate(s.tx);
      $('#lgDr').textContent = fmtRate(s.dr); $('#lgDw').textContent = fmtRate(s.dw);
      const up = $('#kvUp'); if (up) up.textContent = fmtDur(s.uptime);
      $('#tempLbl').textContent = s.temp ? s.temp.toFixed(0) + '°C' : '';
      $('#cores').innerHTML = (s.cores || []).map((c, i) => `<div class="core" title="Luồng ${i}: ${c}%"><i style="height:${clamp(c, 0, 100)}%"></i><span>${Math.round(c)}</span></div>`).join('');
      $('#disks').innerHTML = (s.disks || []).map(d => `<div class="bar-row"><span class="n">${esc(d.mount)} <span class="mono">${esc(d.fs)}</span></span>
        <span class="p">${fmtBytes(d.used)} / ${fmtBytes(d.total)}</span><div class="bar"><i class="${levelCls(d.pct)}" style="width:${d.pct}%"></i></div></div>`).join('') || '<div class="empty">—</div>';
      if (range === 'short' && (!hist.length || hist[hist.length - 1].t < s.t)) {
        hist.push({ t: s.t, cpu: s.cpu, mem: s.mem, rx: s.rx, tx: s.tx, dr: s.dr, dw: s.dw });
        if (hist.length > 300) hist.shift();
        charts.forEach(c => c.set(hist));
      }
    });

    let sortBy = 'cpu';
    const loadProcs = async () => {
      const r = await api('/api/sys/processes?sort=' + sortBy);
      $('#procTotal').textContent = r.total + ' tiến trình';
      $('#procBody').innerHTML = r.processes.map(p => `<tr>
        <td class="mono dim">${p.pid}</td>
        <td><div class="trunc" title="${esc(p.cmd)}">${esc(p.name)}</div></td>
        <td class="dim">${esc(p.user)}</td>
        <td class="num" style="color:${levelColor(p.cpu)}">${p.cpu.toFixed(1)}%</td>
        <td class="num">${fmtBytes(p.rss)}</td>
        <td><div class="acts"><button class="btn sm icon ghost" data-kill="${p.pid}" data-name="${esc(p.name)}" title="Dừng tiến trình">${ico('x', 'sm')}</button></div></td></tr>`).join('');
    };
    every(4000, loadProcs);
    $('#procSeg').onclick = e => {
      const b = e.target.closest('button'); if (!b) return;
      sortBy = b.dataset.s;
      $$('#procSeg button').forEach(x => x.classList.toggle('on', x === b));
      loadProcs();
    };
    $('#procBody').onclick = async e => {
      const b = e.target.closest('[data-kill]'); if (!b) return;
      if (!await confirmBox(`Gửi SIGTERM tới tiến trình <b>${b.dataset.name}</b> (PID ${b.dataset.kill})?`, { ok: 'Dừng', danger: true })) return;
      try { await api('/api/sys/kill', { body: { pid: +b.dataset.kill, signal: 'TERM' } }); toast('Đã gửi tín hiệu dừng'); loadProcs(); } catch (ex) { toastErr(ex); }
    };
    return [off, () => charts.forEach(c => c.destroy())];
  },
};

// ================= page: security / logins =================
pages.security = {
  title: 'Đăng nhập toàn cầu', sub: 'Thống kê IP đăng nhập SSH & bảng điều khiển trong 30 ngày',
  mount(v) {
    v.innerHTML = `
    <div id="secNote"></div>
    <div class="grid g5">
      <div class="card stat"><div class="k">Lượt thử</div><div class="v acc" id="stAtt">—</div></div>
      <div class="card stat"><div class="k">Thất bại</div><div class="v bad" id="stFail">—</div></div>
      <div class="card stat"><div class="k">Thành công</div><div class="v ok" id="stOk">—</div></div>
      <div class="card stat"><div class="k">IP duy nhất</div><div class="v" id="stIps">—</div></div>
      <div class="card stat"><div class="k">Quốc gia</div><div class="v" id="stCc">—</div></div>
    </div>
    <div class="grid g-main mt">
      <div class="card"><div class="card-h"><span class="card-t">Bản đồ đăng nhập</span><span class="sp"></span><span class="card-sub" id="geoSrc"></span></div>
        <div class="map-wrap" id="map"><div class="map-legend"><span><i class="dot ok"></i>Thành công</span><span><i class="dot bad"></i>Thất bại</span><span><i class="dot warn"></i>Bất thường</span></div></div></div>
      <div class="card alerts-card"><div class="card-h"><span class="card-t">Cảnh báo</span><span class="sp"></span><span id="alertCount"></span></div><div class="alerts" id="alerts"></div></div>
    </div>
    <div class="grid g-main mt">
      <div class="card"><div class="card-h"><span class="card-t">Hoạt động 7 ngày</span><span class="sp"></span>
        <span class="legend"><span><i style="background:var(--okc)"></i>Thành công</span><span><i style="background:var(--badc)"></i>Thất bại</span></span></div>
        <div class="chart" id="cHourly" style="height:180px"></div></div>
      <div class="card"><div class="card-h"><span class="card-t">Top quốc gia</span></div><div class="bars" id="topCc"></div></div>
    </div>
    <div class="grid g-main mt">
      <div class="card"><div class="card-h"><span class="card-t">Địa chỉ IP</span><span class="sp"></span>
        <div class="search">${ico('search')}<input id="ipSearch" placeholder="Tìm IP, quốc gia, user…" style="width:200px"></div>
        <div class="seg" id="ipSeg"><button data-f="all" class="on">Tất cả</button><button data-f="ok">Thành công</button><button data-f="fail">Chỉ thất bại</button></div></div>
        <div class="tbl-wrap" style="max-height:480px"><table class="tbl"><thead><tr><th>IP</th><th>Vị trí</th><th class="num">OK</th><th class="num">Lỗi</th><th>User</th><th>Gần nhất</th><th></th></tr></thead><tbody id="ipBody"></tbody></table></div></div>
      <div class="card"><div class="card-h"><span class="card-t">Sự kiện gần đây</span></div><div id="recent" style="max-height:520px;overflow:auto"></div></div>
    </div>`;

    const map = new WorldMap($('#map'));
    const hourly = new Chart($('#cHourly'), {
      type: 'bar', fmt: x => Math.round(x), xfmt: t => { const d = new Date(t); return `${pad2(d.getDate())}/${pad2(d.getMonth() + 1)}`; },
      tipTime: t => { const d = new Date(t); return `${pad2(d.getDate())}/${pad2(d.getMonth() + 1)} ${pad2(d.getHours())}:00–${pad2(d.getHours() + 1)}:00`; },
      series: [{ key: 'ok', color: 'var(--okc)', label: 'Thành công' }, { key: 'fail', color: 'var(--badc)', label: 'Thất bại' }],
    });
    let data = null, filter = 'all', q = '';
    map.onPick = m => { $('#ipSearch').value = m.city || m.country; q = $('#ipSearch').value.toLowerCase(); renderIPs(); };

    const loc = g => g ? `<span class="cc">${esc(g.cc)}</span>${esc(g.city ? g.city + ', ' : '')}${esc(g.country)}` : '<span class="dim">đang định vị…</span>';

    const renderIPs = () => {
      if (!data) return;
      const rows = data.ips.filter(a => {
        if (filter === 'ok' && !a.success) return false;
        if (filter === 'fail' && a.success) return false;
        if (!q) return true;
        return (a.ip + ' ' + (a.geo ? a.geo.country + ' ' + a.geo.city + ' ' + a.geo.cc : '') + ' ' + a.users.join(' ')).toLowerCase().includes(q);
      }).slice(0, 300);
      $('#ipBody').innerHTML = rows.map(a => `<tr>
        <td class="mono">${esc(a.ip)}${a.service !== 'ssh' ? ` <span class="badge acc">${esc(a.service)}</span>` : ''}</td>
        <td><div class="trunc" style="max-width:220px">${loc(a.geo)}</div></td>
        <td class="num" style="color:${a.success ? 'var(--ok)' : 'var(--dim)'}">${a.success}</td>
        <td class="num" style="color:${a.failed ? 'var(--bad)' : 'var(--dim)'}">${a.failed}</td>
        <td><div class="trunc mono dim" style="max-width:160px" title="${esc(a.users.join(', '))}">${esc(a.users.slice(0, 4).join(', '))}${a.users.length > 4 ? '…' : ''}</div></td>
        <td class="dim" title="${esc(fmtDT(a.last))}">${fmtAgo(a.last)}</td>
        <td><div class="acts">${a.geo && a.geo.local ? '' : `<button class="btn sm danger" data-block="${esc(a.ip)}">${ico('ban', 'sm')}Chặn</button>`}</div></td></tr>`).join('')
        || `<tr><td colspan="7" class="empty">Không có dữ liệu</td></tr>`;
    };

    const render = () => {
      const d = data;
      $('#secNote').innerHTML = d.note ? `<div class="note ${d.source === 'demo' ? '' : 'info'}">${esc(d.note)}</div>` : '';
      $('#stAtt').textContent = d.totals.attempts.toLocaleString();
      $('#stFail').textContent = d.totals.failed.toLocaleString();
      $('#stOk').textContent = d.totals.success.toLocaleString();
      $('#stIps').textContent = d.totals.ips.toLocaleString();
      $('#stCc').textContent = d.totals.countries.toLocaleString();
      $('#geoSrc').textContent = `nguồn: ${d.source} · geo: ${d.geo}`;

      const flagged = new Set(d.alerts.filter(a => a.level !== 'info' && a.ip).map(a => a.ip));
      const groups = new Map();
      const now = Date.now();
      for (const a of d.ips) {
        if (!a.geo || a.geo.local || (a.geo.lat === 0 && a.geo.lon === 0)) continue;
        const k = a.geo.lat.toFixed(1) + ',' + a.geo.lon.toFixed(1);
        let m = groups.get(k);
        if (!m) { m = { lat: a.geo.lat, lon: a.geo.lon, city: a.geo.city, country: a.geo.country, cc: a.geo.cc, ok: 0, fail: 0, total: 0, ips: [], recent: false, flag: false }; groups.set(k, m); }
        m.ok += a.success; m.fail += a.failed; m.total += a.success + a.failed; m.ips.push(a.ip);
        if (now - new Date(a.last).getTime() < 2 * 3600e3) m.recent = true;
        if (flagged.has(a.ip)) m.flag = true;
      }
      const markers = [...groups.values()].map(m => ({
        ...m, ok: m.ok > 0,
        html: `<div style="margin-bottom:4px"><span class="cc">${esc(m.cc)}</span><b>${esc(m.city || m.country)}</b></div>
          <div style="color:var(--okc)">✓ ${m.ok} thành công</div><div style="color:var(--badc)">✕ ${m.fail} thất bại</div>
          <div style="color:var(--muted);margin-top:3px">${m.ips.slice(0, 3).map(esc).join('<br>')}${m.ips.length > 3 ? '<br>+' + (m.ips.length - 3) + ' IP' : ''}</div>`,
      }));
      map.set(markers);

      $('#alertCount').innerHTML = d.alerts.length ? `<span class="badge ${d.alerts.some(a => a.level === 'crit') ? 'bad' : 'warn'}">${d.alerts.length}</span>` : '';
      $('#alerts').innerHTML = d.alerts.map(a => `<div class="alert ${a.level}">${ico(a.level === 'info' ? 'shield' : 'alert')}
        <div style="min-width:0;flex:1"><b>${esc(a.title)}</b><p>${esc(a.detail)}</p><small>${fmtDT(a.t)} · ${fmtAgo(a.t)}</small></div>
        ${a.ip ? `<button class="btn sm danger" data-block="${esc(a.ip)}" title="Chặn IP">${ico('ban', 'sm')}</button>` : ''}</div>`).join('')
        || `<div class="empty">${ico('check')}<br>Không phát hiện đăng nhập bất thường</div>`;

      const hs = d.hourlyStart;
      hourly.set(d.hourly.map((x, i) => ({ t: hs + i * 3600e3, ok: x[0], fail: x[1] })));

      const top = d.countries.slice(0, 9), mx = Math.max(1, ...top.map(c => c.success + c.failed));
      $('#topCc').innerHTML = top.map(c => `<div class="bar-row"><span class="n"><span class="cc">${esc(c.cc)}</span>${esc(c.country)} <span class="mono">${c.ips} IP</span></span>
        <span class="p">${(c.success + c.failed).toLocaleString()}</span>
        <div class="bar"><i style="width:${c.success / mx * 100}%;background:var(--ok)"></i><i class="b2" style="left:${c.success / mx * 100}%;width:${c.failed / mx * 100}%"></i></div></div>`).join('')
        || '<div class="empty">Chưa có dữ liệu</div>';

      $('#recent').innerHTML = d.recent.slice(0, 120).map(e => `<div class="ev">
        <i class="dot ${e.ok ? 'ok' : 'bad'}"></i><time>${fmtDT(e.t)}</time>
        <span class="who"><b class="mono">${esc(e.user || '?')}</b> <span class="dim">từ</span> <span class="mono">${esc(e.ip)}</span></span>
        <span class="dim" style="font-size:11.5px">${e.geo ? `<span class="cc">${esc(e.geo.cc)}</span>` : ''}${esc(e.service === 'panel' ? 'panel' : e.method)}</span></div>`).join('')
        || '<div class="empty">Chưa có sự kiện</div>';
      renderIPs();
    };

    every(30000, async () => { data = await api('/api/logins'); render(); });
    $('#ipSearch').oninput = e => { q = e.target.value.trim().toLowerCase(); renderIPs(); };
    $('#ipSeg').onclick = e => {
      const b = e.target.closest('button'); if (!b) return;
      filter = b.dataset.f;
      $$('#ipSeg button').forEach(x => x.classList.toggle('on', x === b));
      renderIPs();
    };
    v.addEventListener('click', e => { const b = e.target.closest('[data-block]'); if (b) blockIP(b.dataset.block); });
    return () => { map.destroy(); hourly.destroy(); };
  },
};

document.addEventListener('DOMContentLoaded', () => setTimeout(boot, 0));
