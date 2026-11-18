'use strict';
/* TNS Panel — firewall, apps, files, terminal, settings. Depends on app.js. */

const LS = {
  get(k, d) { try { const v = localStorage.getItem('tns.' + k); return v == null ? d : JSON.parse(v); } catch (e) { return d; } },
  set(k, v) { try { localStorage.setItem('tns.' + k, JSON.stringify(v)); } catch (e) { /* private mode */ } },
};

// ---------- shared: lightweight code editor ----------
function codeEditor(host, text, { readOnly = false, onSave } = {}) {
  host.innerHTML = `<div class="editor"><div class="gut"></div><textarea spellcheck="false" autocapitalize="off" autocomplete="off"></textarea></div>`;
  const ta = $('textarea', host), gut = $('.gut', host);
  ta.value = text; ta.readOnly = readOnly;
  let lines = 0;
  const upd = () => {
    const n = ta.value.split('\n').length;
    if (n !== lines) { lines = n; gut.textContent = Array.from({ length: n }, (_, i) => i + 1).join('\n'); }
  };
  upd();
  ta.addEventListener('input', upd);
  ta.addEventListener('scroll', () => { gut.scrollTop = ta.scrollTop; });
  ta.addEventListener('keydown', e => {
    if (e.key === 'Tab' && !readOnly) {
      e.preventDefault();
      const s = ta.selectionStart, en = ta.selectionEnd;
      ta.setRangeText('    ', s, en, 'end'); upd();
    } else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault(); onSave && onSave(ta.value);
    }
  });
  return { get value() { return ta.value; }, focus() { ta.setSelectionRange(0, 0); ta.focus({ preventScroll: true }); ta.scrollTop = 0; }, ta };
}

function logModal(title, loader) {
  const m = openModal({ title, size: 'lg', flush: true, body: `<div style="padding:14px 18px;overflow:auto;flex:1" class="lg-scroll"><pre class="log">Đang tải…</pre></div>` });
  $('.mh-extra', m).innerHTML = `<button class="btn sm" data-r>${ico('refresh', 'sm')}Làm mới</button>`;
  const load = async () => {
    try {
      const t = await loader();
      $('pre', m).textContent = t || '(trống)';
      const sc = $('.lg-scroll', m); sc.scrollTop = sc.scrollHeight;
    } catch (e) { $('pre', m).textContent = e.message; }
  };
  $('[data-r]', m).onclick = load;
  load();
}

// ================= firewall =================
pages.firewall = {
  title: 'Tường lửa', sub: 'Quản lý quy tắc truy cập mạng',
  mount(v) {
    v.innerHTML = `<div id="fwNote"></div>
    <div class="grid g-main">
      <div class="card"><div class="card-h"><span class="card-t">Quy tắc</span><span class="card-sub" id="fwCount"></span><span class="sp"></span>
        <button class="btn sm" id="fwRefresh">${ico('refresh', 'sm')}Làm mới</button></div>
        <div class="tbl-wrap"><table class="tbl"><thead><tr><th>#</th><th>Đích</th><th>Hành động</th><th>Nguồn</th><th>Ghi chú</th><th></th></tr></thead><tbody id="fwBody"></tbody></table></div></div>
      <div class="grid" style="align-content:start">
        <div class="card"><div class="card-h"><span class="card-t">Trạng thái</span><span class="sp"></span><span id="fwBackend"></span></div>
          <div class="row" style="justify-content:space-between"><div><div style="font-weight:600" id="fwState">—</div><div class="card-sub" id="fwDefault"></div></div>
          <label class="switch" title="Bật/tắt tường lửa"><input type="checkbox" id="fwToggle"><i></i></label></div>
          <div class="row mt" id="fwPolicy"><span class="card-sub">Mặc định chiều vào:</span><div class="seg"><button data-p="deny">Chặn</button><button data-p="allow">Cho phép</button></div></div>
        </div>
        <div class="card"><div class="card-h"><span class="card-t">Thêm quy tắc</span></div>
          <form class="form" id="fwForm">
            <div class="row" style="flex-wrap:wrap;gap:6px" id="fwPresets">
              ${[['SSH', 22], ['HTTP', 80], ['HTTPS', 443], ['TNS', 1611], ['MySQL', 3306], ['Postgres', 5432], ['Redis', 6379]].map(([n, p]) => `<button type="button" class="btn sm" data-port="${p}">${n} <span class="dim mono">${p}</span></button>`).join('')}
            </div>
            <div class="grid g2" style="gap:10px">
              <label class="field"><span>Hành động</span><select name="action"><option value="allow">Cho phép (allow)</option><option value="deny">Chặn (deny)</option><option value="reject">Từ chối (reject)</option><option value="limit">Giới hạn (limit)</option></select></label>
              <label class="field"><span>Giao thức</span><select name="proto"><option value="tcp">TCP</option><option value="udp">UDP</option><option value="any">Bất kỳ</option></select></label>
              <label class="field"><span>Cổng</span><input name="port" placeholder="80 hoặc 8000:8100" class="mono"></label>
              <label class="field"><span>Nguồn (IP/CIDR)</span><input name="from" placeholder="any" class="mono"></label>
            </div>
            <label class="field"><span>Ghi chú</span><input name="comment" maxlength="60" placeholder="tuỳ chọn"></label>
            <button class="btn primary" type="submit">${ico('plus', 'sm')}Thêm quy tắc</button>
          </form></div>
      </div>
    </div>`;

    let st = null;
    const load = async () => {
      st = await api('/api/firewall');
      $('#fwNote').innerHTML = st.note ? `<div class="note ${st.backend === 'demo' ? '' : 'info'}">${esc(st.note)}</div>` : '';
      $('#fwBackend').innerHTML = `<span class="badge acc">${esc(st.backend)}</span>`;
      $('#fwState').innerHTML = st.backend === 'none' ? 'Không khả dụng' : st.active ? '<span style="color:var(--ok)">● Đang hoạt động</span>' : '<span style="color:var(--warn)">● Đang tắt</span>';
      $('#fwDefault').textContent = st.default || '';
      $('#fwToggle').checked = st.active;
      $('#fwToggle').disabled = st.backend === 'none' || st.backend === 'iptables';
      $('#fwPolicy').classList.toggle('hidden', !(st.backend === 'ufw' || st.backend === 'demo'));
      const pol = (st.default || '').startsWith('allow') ? 'allow' : 'deny';
      $$('#fwPolicy button').forEach(b => b.classList.toggle('on', b.dataset.p === pol));
      $('#fwCount').textContent = st.rules.length + ' quy tắc';
      $('#fwBody').innerHTML = st.rules.map((r, i) => {
        const a = r.action.split(' ')[0];
        const cls = a === 'ALLOW' ? 'ok' : a === 'LIMIT' ? 'warn' : 'bad';
        return `<tr><td class="mono dim">${r.num || i + 1}</td><td class="mono">${esc(r.to)}</td>
          <td><span class="badge ${cls}">${esc(r.action)}</span></td><td class="mono">${esc(r.from)}</td>
          <td class="dim"><div class="trunc">${esc(r.comment || '')}</div></td>
          <td><div class="acts"><button class="btn sm icon danger" data-del="${esc(r.id)}" data-desc="${esc(r.action + ' ' + r.to + ' từ ' + r.from)}" title="Xoá">${ico('trash', 'sm')}</button></div></td></tr>`;
      }).join('') || `<tr><td colspan="6" class="empty">Chưa có quy tắc nào</td></tr>`;
    };
    const act = async (body, msg) => {
      try { const r = await api('/api/firewall', { body }); toast(msg || r.output || 'Đã cập nhật'); } catch (e) { toastErr(e); }
      load();
    };
    load().catch(toastErr);
    $('#fwRefresh').onclick = () => load().catch(toastErr);
    $('#fwToggle').onchange = async e => {
      const on = e.target.checked;
      const ok = await confirmBox(on
        ? 'Bật tường lửa? TNS sẽ tự động mở cổng SSH và cổng bảng điều khiển trước để tránh bị khoá ngoài.'
        : 'Tắt tường lửa? Mọi cổng sẽ được mở.', { ok: on ? 'Bật' : 'Tắt', danger: !on });
      if (!ok) { e.target.checked = !on; return; }
      act({ op: on ? 'enable' : 'disable' });
    };
    $('#fwPolicy').onclick = e => { const b = e.target.closest('button'); if (b) act({ op: 'default', policy: b.dataset.p }); };
    $('#fwPresets').onclick = e => {
      const b = e.target.closest('[data-port]'); if (!b) return;
      const f = $('#fwForm'); f.port.value = b.dataset.port; f.proto.value = 'tcp'; f.action.value = 'allow';
      f.comment.value = b.textContent.trim().split(' ')[0].toLowerCase();
    };
    $('#fwForm').onsubmit = e => {
      e.preventDefault();
      const f = e.target;
      act({ op: 'add', action: f.action.value, proto: f.proto.value, port: f.port.value.trim(), from: f.from.value.trim() || 'any', comment: f.comment.value.trim() }, 'Đã thêm quy tắc');
      f.port.value = ''; f.from.value = ''; f.comment.value = '';
    };
    $('#fwBody').onclick = async e => {
      const b = e.target.closest('[data-del]'); if (!b) return;
      if (await confirmBox(`Xoá quy tắc <b class="mono">${b.dataset.desc}</b>?`, { ok: 'Xoá', danger: true })) act({ op: 'delete', id: b.dataset.del }, 'Đã xoá quy tắc');
    };
  },
};

// ================= apps =================
pages.apps = {
  title: 'Ứng dụng & dịch vụ', sub: 'Docker, website Nginx, systemd và cổng mạng',
  mount(v) {
    const tabs = [['docker', 'Docker'], ['sites', 'Website'], ['services', 'Dịch vụ'], ['ports', 'Cổng mở']];
    let tab = LS.get('appsTab', 'docker');
    v.innerHTML = `<div class="tabs" id="appTabs">${tabs.map(([k, l]) => `<button class="tab" data-t="${k}">${l}</button>`).join('')}</div><div id="appBody"></div>`;
    let stop = null;
    const show = t => {
      tab = t; LS.set('appsTab', t);
      $$('#appTabs .tab').forEach(b => b.classList.toggle('on', b.dataset.t === t));
      if (stop) stop();
      timers.forEach(clearInterval); timers = [];
      stop = appTabs[t]($('#appBody')) || null;
    };
    $('#appTabs').onclick = e => { const b = e.target.closest('.tab'); if (b) show(b.dataset.t); };
    show(tab);
    return () => stop && stop();
  },
};

const stateDot = s => s === 'running' || s === 'active' ? 'ok' : s === 'paused' || s === 'activating' || s === 'restarting' ? 'warn' : s === 'failed' || s === 'dead' ? 'bad' : '';

const appTabs = {
  docker(el) {
    el.innerHTML = `<div id="dkNote"></div>
      <div class="card"><div class="card-h"><span class="card-t">Container</span><span class="card-sub" id="dkSum"></span><span class="sp"></span>
        <button class="btn sm" id="dkRefresh">${ico('refresh', 'sm')}Làm mới</button></div>
        <div class="tbl-wrap"><table class="tbl"><thead><tr><th></th><th>Tên</th><th>Image</th><th>Cổng</th><th class="num">CPU</th><th class="num">RAM</th><th>Trạng thái</th><th></th></tr></thead><tbody id="dkBody"></tbody></table></div></div>
      <div class="card mt"><div class="card-h"><span class="card-t">Images</span><span class="card-sub" id="imgSum"></span></div>
        <div class="tbl-wrap" style="max-height:320px"><table class="tbl"><thead><tr><th>ID</th><th>Tag</th><th class="num">Dung lượng</th><th></th></tr></thead><tbody id="imgBody"></tbody></table></div></div>`;
    const load = async () => {
      const d = await api('/api/docker');
      if (!d.available) {
        $('#dkNote').innerHTML = `<div class="note info">${esc(d.note)}</div>`;
        $('#dkBody').innerHTML = `<tr><td colspan="8" class="empty">Docker không khả dụng</td></tr>`;
        return;
      }
      $('#dkNote').innerHTML = d.demo ? `<div class="note">Chế độ demo — trên Linux sẽ điều khiển Docker thật qua /var/run/docker.sock.</div>` : '';
      const run = d.containers.filter(c => c.state === 'running').length;
      $('#dkSum').textContent = `${run}/${d.containers.length} đang chạy · Docker ${d.version || ''}`;
      $('#dkBody').innerHTML = d.containers.map(c => {
        const running = c.state === 'running', paused = c.state === 'paused';
        return `<tr><td><i class="dot ${stateDot(c.state)} ${running ? 'pulse' : ''}"></i></td>
        <td><b>${esc(c.name)}</b>${c.compose ? ` <span class="badge">${esc(c.compose)}</span>` : ''}<div class="mono dim" style="font-size:11px">${esc(c.id)}</div></td>
        <td class="mono dim"><div class="trunc" style="max-width:200px">${esc(c.image)}</div></td>
        <td class="mono" style="font-size:12px">${c.ports.map(esc).join('<br>') || '<span class="dim">—</span>'}</td>
        <td class="num">${running ? c.cpu.toFixed(1) + '%' : '<span class="dim">—</span>'}</td>
        <td class="num">${running ? fmtBytes(c.mem) : '<span class="dim">—</span>'}</td>
        <td class="dim" style="white-space:nowrap">${esc(c.status)}</td>
        <td><div class="acts">
          ${running || paused ? '' : `<button class="btn sm icon" data-op="start" data-id="${c.id}" title="Khởi động">${ico('play', 'sm')}</button>`}
          ${running ? `<button class="btn sm icon" data-op="pause" data-id="${c.id}" title="Tạm dừng">${ico('pause', 'sm')}</button>` : ''}
          ${paused ? `<button class="btn sm icon" data-op="unpause" data-id="${c.id}" title="Tiếp tục">${ico('play', 'sm')}</button>` : ''}
          ${running || paused ? `<button class="btn sm icon" data-op="stop" data-id="${c.id}" title="Dừng">${ico('stop', 'sm')}</button>` : ''}
          <button class="btn sm icon" data-op="restart" data-id="${c.id}" title="Khởi động lại">${ico('restart', 'sm')}</button>
          <button class="btn sm icon" data-op="logs" data-id="${c.id}" data-name="${esc(c.name)}" title="Xem log">${ico('log', 'sm')}</button>
          ${running ? `<button class="btn sm icon" data-op="shell" data-id="${c.id}" data-name="${esc(c.name)}" title="Mở shell">${ico('term', 'sm')}</button>` : ''}
          <button class="btn sm icon danger" data-op="remove" data-id="${c.id}" data-name="${esc(c.name)}" title="Xoá">${ico('trash', 'sm')}</button>
        </div></td></tr>`;
      }).join('') || `<tr><td colspan="8" class="empty">Chưa có container nào</td></tr>`;
      const imgs = d.images || [];
      $('#imgSum').textContent = `${imgs.length} images · ${fmtBytes(imgs.reduce((s, i) => s + i.size, 0))}`;
      $('#imgBody').innerHTML = imgs.map(i => `<tr><td class="mono dim">${esc(i.id)}</td><td class="mono">${esc((i.tags || []).join(', ') || '<none>')}</td>
        <td class="num">${fmtBytes(i.size)}</td><td><div class="acts"><button class="btn sm icon danger" data-op="rmi" data-id="${esc(i.id)}" data-name="${esc((i.tags || [])[0] || i.id)}" title="Xoá image">${ico('trash', 'sm')}</button></div></td></tr>`).join('')
        || `<tr><td colspan="4" class="empty">—</td></tr>`;
    };
    every(8000, load);
    $('#dkRefresh').onclick = () => load().catch(toastErr);
    el.onclick = async e => {
      const b = e.target.closest('[data-op]'); if (!b) return;
      const { op, id, name } = b.dataset;
      if (op === 'logs') return logModal('Log · ' + name, async () => (await api('/api/docker/logs?id=' + encodeURIComponent(id))).logs);
      if (op === 'shell') return Term.open({ container: id, title: name });
      if ((op === 'remove' || op === 'rmi') && !await confirmBox(`Xoá ${op === 'rmi' ? 'image' : 'container'} <b class="mono">${esc(name)}</b>?`, { ok: 'Xoá', danger: true })) return;
      b.disabled = true;
      try { await api('/api/docker/action', { body: { id, op } }); toast(`${op} ${name || id}: OK`); } catch (ex) { toastErr(ex); }
      load();
    };
  },

  sites(el) {
    el.innerHTML = `<div id="stNote"></div>
      <div class="card" style="margin-bottom:14px"><div class="row">
        <span class="card-t">Nginx</span><span id="ngStatus"></span><span class="card-sub mono" id="ngVer"></span><span class="sp" style="flex:1"></span>
        <button class="btn sm" id="ngTest">${ico('check', 'sm')}Kiểm tra cấu hình</button>
        <button class="btn sm" id="ngReload">${ico('refresh', 'sm')}Reload</button>
        <button class="btn sm primary" id="ngNew">${ico('plus', 'sm')}Website mới</button></div></div>
      <div class="sites" id="siteList"></div>`;
    const load = async () => {
      const d = await api('/api/sites');
      if (!d.available) { $('#stNote').innerHTML = `<div class="note info">${esc(d.note)}</div>`; $('#siteList').innerHTML = ''; el.querySelectorAll('.btn').forEach(b => b.disabled = true); return; }
      $('#stNote').innerHTML = d.demo ? `<div class="note">Chế độ demo — cấu hình mẫu lưu trong ${esc(d.root)}. Trên Linux sẽ quản lý /etc/nginx thật.</div>` : '';
      $('#ngStatus').innerHTML = d.running ? '<span class="badge ok">RUNNING</span>' : '<span class="badge bad">STOPPED</span>';
      $('#ngVer').textContent = d.version || '';
      $('#siteList').innerHTML = (d.sites || []).map(s => `<div class="card site">
        <h4><i class="dot ${s.enabled ? 'ok' : ''}"></i><span title="${esc(s.name)}">${esc(s.name)}</span>${s.ssl ? '<span class="badge ok">SSL</span>' : ''}<span class="badge">${esc(s.kind)}</span></h4>
        <div class="meta">
          <div>${(s.domains || []).map(dm => `<a href="http${s.ssl ? 's' : ''}://${esc(dm)}" target="_blank" rel="noopener">${esc(dm)}</a>`).join(' ') || '<span>(không có server_name)</span>'}</div>
          <div>listen <b>${esc((s.listen || []).join(', ') || '—')}</b></div>
          ${s.proxy ? `<div>→ proxy <b>${esc(s.proxy)}</b></div>` : ''}${s.root ? `<div>root <b>${esc(s.root)}</b></div>` : ''}
        </div>
        <div class="row"><label class="switch" title="${s.enabled ? 'Tắt' : 'Bật'} site"><input type="checkbox" ${s.enabled ? 'checked' : ''} data-toggle="${esc(s.name)}" data-kind="${esc(s.kind)}"><i></i></label>
          <span class="card-sub">${s.enabled ? 'Đang bật' : 'Đang tắt'}</span><span style="flex:1"></span>
          <button class="btn sm" data-edit="${esc(s.name)}" data-kind="${esc(s.kind)}">${ico('edit', 'sm')}Sửa</button>
          <button class="btn sm icon danger" data-delsite="${esc(s.name)}" data-kind="${esc(s.kind)}" title="Xoá">${ico('trash', 'sm')}</button></div></div>`).join('')
        || '<div class="empty">Chưa có website nào</div>';
    };
    const act = async (body, msg) => {
      try { const r = await api('/api/sites/action', { body }); toast(msg || (r.output || 'OK').trim()); return true; }
      catch (e) { toastErr(e); return false; } finally { load(); }
    };
    load().catch(toastErr);
    $('#ngTest').onclick = () => act({ op: 'test' });
    $('#ngReload').onclick = () => act({ op: 'reload' }, 'Đã reload nginx');
    $('#ngNew').onclick = () => {
      const m = openModal({
        title: 'Website mới', body: `<form class="form" id="nsForm">
          <label class="field"><span>Tên file cấu hình</span><input name="name" placeholder="myapp.example.com" required class="mono"></label>
          <label class="field"><span>Tên miền (cách nhau bởi dấu cách)</span><input name="domain" placeholder="example.com www.example.com" required class="mono"></label>
          <div class="seg" id="nsType"><button type="button" data-t="proxy" class="on">Reverse proxy (app / docker)</button><button type="button" data-t="static">Web tĩnh</button></div>
          <label class="field" id="nsUp"><span>Upstream</span><input name="upstream" value="http://127.0.0.1:3000" class="mono"></label>
          <label class="field hidden" id="nsRoot"><span>Thư mục web</span><input name="root" value="/var/www/html" class="mono"></label>
        </form>`, foot: `<button class="btn" data-close>Huỷ</button><button class="btn primary" id="nsOk">Tạo & bật</button>`,
      });
      let type = 'proxy';
      $('#nsType', m).onclick = e => {
        const b = e.target.closest('button'); if (!b) return; type = b.dataset.t;
        $$('#nsType button', m).forEach(x => x.classList.toggle('on', x === b));
        $('#nsUp', m).classList.toggle('hidden', type !== 'proxy'); $('#nsRoot', m).classList.toggle('hidden', type === 'proxy');
      };
      $('#nsOk', m).onclick = async () => {
        const f = $('#nsForm', m);
        if (await act({ op: 'create', template: type, name: f.name.value.trim(), domain: f.domain.value.trim(), upstream: f.upstream.value.trim(), root: f.root.value.trim() }, 'Đã tạo website')) m.close();
      };
      $('input', m).focus();
    };
    el.onclick = async e => {
      const ed = e.target.closest('[data-edit]'), del = e.target.closest('[data-delsite]');
      if (ed) {
        const { edit: name, kind } = ed.dataset;
        const r = await api(`/api/sites/file?kind=${encodeURIComponent(kind)}&name=${encodeURIComponent(name)}`).catch(toastErr);
        if (!r) return;
        const m = openModal({ title: esc(r.path), size: 'lg', flush: true, body: '<div style="flex:1;display:flex;min-height:0" id="ed"></div>',
          foot: `<span class="card-sub" style="margin-right:auto">Lưu sẽ tự chạy <span class="mono">nginx -t</span>; lỗi sẽ tự khôi phục bản cũ.</span><button class="btn" data-close>Đóng</button><button class="btn primary" id="edSave">${ico('save', 'sm')}Lưu & reload</button>` });
        const save = async val => { if (await act({ op: 'save', kind, name, content: val })) m.close(); };
        const ce = codeEditor($('#ed', m), r.content, { onSave: save });
        $('#edSave', m).onclick = () => save(ce.value);
        ce.focus();
      } else if (del) {
        if (await confirmBox(`Xoá website <b class="mono">${esc(del.dataset.delsite)}</b>? (bản sao lưu được giữ trong thư mục dữ liệu TNS)`, { ok: 'Xoá', danger: true }))
          act({ op: 'delete', name: del.dataset.delsite, kind: del.dataset.kind }, 'Đã xoá');
      }
    };
    el.onchange = e => {
      const t = e.target.closest('[data-toggle]'); if (!t) return;
      act({ op: t.checked ? 'enable' : 'disable', name: t.dataset.toggle, kind: t.dataset.kind }, t.checked ? 'Đã bật site' : 'Đã tắt site');
    };
  },

  services(el) {
    el.innerHTML = `<div id="svNote"></div><div class="card"><div class="card-h"><span class="card-t">systemd</span><span class="card-sub" id="svSum"></span><span class="sp"></span>
      <div class="search">${ico('search')}<input id="svQ" placeholder="Tìm dịch vụ…" style="width:200px"></div>
      <div class="seg" id="svSeg"><button data-f="all" class="on">Tất cả</button><button data-f="active">Đang chạy</button><button data-f="failed">Lỗi</button><button data-f="inactive">Đã dừng</button></div></div>
      <div class="tbl-wrap" style="max-height:calc(100vh - 290px)"><table class="tbl"><thead><tr><th></th><th>Dịch vụ</th><th>Trạng thái</th><th>Khởi động cùng máy</th><th></th></tr></thead><tbody id="svBody"></tbody></table></div></div>`;
    let list = [], f = 'all', q = '';
    const render = () => {
      const rows = list.filter(s => (f === 'all' || s.active === f) && (!q || (s.unit + ' ' + s.desc).toLowerCase().includes(q)));
      $('#svBody').innerHTML = rows.map(s => {
        const on = s.active === 'active';
        const canToggle = s.enabled === 'enabled' || s.enabled === 'disabled';
        return `<tr><td><i class="dot ${s.active === 'active' ? 'ok' : s.active === 'failed' ? 'bad' : ''}"></i></td>
        <td><b class="mono" style="font-size:12.5px">${esc(s.unit.replace(/\.service$/, ''))}</b><div class="dim trunc" style="max-width:420px;font-size:12px">${esc(s.desc)}</div></td>
        <td><span class="badge ${s.active === 'active' ? 'ok' : s.active === 'failed' ? 'bad' : ''}">${esc(s.active)} · ${esc(s.sub)}</span></td>
        <td>${canToggle ? `<label class="switch"><input type="checkbox" data-en="${esc(s.unit)}" ${s.enabled === 'enabled' ? 'checked' : ''}><i></i></label>` : `<span class="dim mono" style="font-size:12px">${esc(s.enabled || '—')}</span>`}</td>
        <td><div class="acts">
          ${on ? `<button class="btn sm icon" data-op="stop" data-u="${esc(s.unit)}" title="Dừng">${ico('stop', 'sm')}</button>` : `<button class="btn sm icon" data-op="start" data-u="${esc(s.unit)}" title="Khởi động">${ico('play', 'sm')}</button>`}
          <button class="btn sm icon" data-op="restart" data-u="${esc(s.unit)}" title="Khởi động lại">${ico('restart', 'sm')}</button>
          <button class="btn sm icon" data-op="logs" data-u="${esc(s.unit)}" title="Log">${ico('log', 'sm')}</button></div></td></tr>`;
      }).join('') || `<tr><td colspan="5" class="empty">Không có dịch vụ phù hợp</td></tr>`;
    };
    const load = async () => {
      const d = await api('/api/services');
      if (!d.available) { $('#svNote').innerHTML = `<div class="note info">${esc(d.note)}</div>`; return; }
      $('#svNote').innerHTML = d.demo ? '<div class="note">Chế độ demo — trên Linux sẽ điều khiển systemd thật.</div>' : '';
      list = d.services;
      $('#svSum').textContent = `${list.filter(s => s.active === 'active').length} chạy · ${list.filter(s => s.active === 'failed').length} lỗi · ${list.length} tổng`;
      render();
    };
    every(10000, load);
    $('#svQ').oninput = e => { q = e.target.value.trim().toLowerCase(); render(); };
    $('#svSeg').onclick = e => { const b = e.target.closest('button'); if (!b) return; f = b.dataset.f; $$('#svSeg button').forEach(x => x.classList.toggle('on', x === b)); render(); };
    const act = async (unit, op) => {
      try { await api('/api/services/action', { body: { unit, op } }); toast(`${op} ${unit}: OK`); } catch (e) { toastErr(e); }
      load();
    };
    $('#svBody').onclick = async e => {
      const b = e.target.closest('[data-op]'); if (!b) return;
      const { op, u } = b.dataset;
      if (op === 'logs') return logModal('Log · ' + u, async () => { const r = await api('/api/services/logs?unit=' + encodeURIComponent(u)); return (r.status ? r.status + '\n' : '') + r.logs; });
      if (op === 'stop' && /^(ssh|sshd|tns)\.service$/.test(u) && !await confirmBox(`Dừng <b>${esc(u)}</b> có thể làm mất kết nối tới máy chủ. Tiếp tục?`, { ok: 'Vẫn dừng', danger: true })) return;
      b.disabled = true; act(u, op);
    };
    $('#svBody').onchange = e => { const t = e.target.closest('[data-en]'); if (t) act(t.dataset.en, t.checked ? 'enable' : 'disable'); };
  },

  ports(el) {
    el.innerHTML = `<div class="card"><div class="card-h"><span class="card-t">Cổng đang lắng nghe</span><span class="card-sub" id="ptSum"></span></div>
      <div class="tbl-wrap"><table class="tbl"><thead><tr><th>Giao thức</th><th>Địa chỉ</th><th class="num">Cổng</th><th>Tiến trình</th><th class="num">PID</th><th>Phạm vi</th></tr></thead><tbody id="ptBody"></tbody></table></div></div>`;
    every(10000, async () => {
      const d = await api('/api/sys/ports');
      const ps = d.ports || [];
      $('#ptSum').textContent = ps.length + ' socket';
      $('#ptBody').innerHTML = ps.map(p => {
        const local = /^(127\.|::1$|localhost)/.test(p.addr);
        return `<tr><td><span class="badge">${p.proto.toUpperCase()}</span></td><td class="mono">${esc(p.addr)}</td><td class="num" style="color:var(--accent)">${p.port}</td>
        <td>${esc(p.name || '—')}</td><td class="num dim">${p.pid || '—'}</td><td>${local ? '<span class="badge">local</span>' : '<span class="badge warn">public</span>'}</td></tr>`;
      }).join('') || `<tr><td colspan="6" class="empty">Không có dữ liệu (có thể cần quyền root)</td></tr>`;
    });
  },
};

// ================= files =================
const EXT = {
  img: ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'svg', 'avif'],
  video: ['mp4', 'webm', 'ogv', 'mov', 'm4v', 'mkv'],
  audio: ['mp3', 'wav', 'ogg', 'oga', 'm4a', 'flac', 'aac', 'opus'],
  pdf: ['pdf'],
  zip: ['zip', 'gz', 'tgz', 'xz', 'bz2', 'zst', '7z', 'rar', 'tar', 'deb', 'rpm', 'iso'],
  code: ['js', 'ts', 'jsx', 'tsx', 'go', 'py', 'rb', 'php', 'java', 'c', 'h', 'cpp', 'rs', 'sh', 'bash', 'zsh', 'json', 'yml', 'yaml', 'toml', 'ini', 'conf', 'cfg', 'html', 'htm', 'css', 'scss', 'xml', 'sql', 'md', 'env', 'service', 'lua', 'vue', 'svelte', 'dockerfile'],
};
const extOf = n => (n.includes('.') ? n.split('.').pop() : n).toLowerCase();
function kindOf(name) {
  const e = extOf(name);
  for (const k in EXT) if (EXT[k].includes(e)) return k;
  return 'file';
}
const kindIcon = { img: 'img', video: 'film', audio: 'music', pdf: 'pdf', zip: 'zip', code: 'code', file: 'file' };
const joinPath = (d, n) => (d.endsWith('/') ? d : d + '/') + n;
const rawURL = (p, dl) => '/api/files/raw?path=' + encodeURIComponent(p) + (dl ? '&dl=1' : '');

pages.files = {
  title: 'Tệp tin', sub: 'Duyệt, xem và chỉnh sửa tệp trên máy chủ',
  mount(v) {
    let path = LS.get('filesPath', ''), entries = [], showHidden = LS.get('showHidden', false), sel = new Set(), editingPath = false;
    v.innerHTML = `<div class="card" id="fCard" style="padding-bottom:0">
      <div class="fbar">
        <button class="btn icon" id="fUp" title="Thư mục cha">${ico('parent')}</button>
        <button class="btn icon" id="fHome" title="Thư mục home">${ico('home')}</button>
        <button class="btn icon" id="fRefresh" title="Làm mới">${ico('refresh')}</button>
        <div class="crumbs" id="crumbs"></div>
        <button class="btn" id="fMkdir">${ico('plus', 'sm')}Thư mục</button>
        <button class="btn" id="fTouch">${ico('file', 'sm')}Tệp mới</button>
        <button class="btn" id="fUpload">${ico('up', 'sm')}Tải lên</button>
        <button class="btn icon" id="fTerm" title="Mở terminal tại đây">${ico('term')}</button>
        <button class="btn ${showHidden ? 'on' : ''}" id="fHidden" title="Hiện tệp ẩn">.*</button>
        <input type="file" id="fFile" multiple class="hidden">
      </div>
      <div class="row hidden" id="fSel" style="padding:8px 20px;margin:0 -20px;border-top:1px solid var(--line);background:var(--accent-soft)">
        <span id="fSelN"></span><span style="flex:1"></span><button class="btn sm" id="fSelClear">Bỏ chọn</button><button class="btn sm danger" id="fSelDel">${ico('trash', 'sm')}Xoá</button></div>
      <div class="tbl-wrap" style="max-height:calc(100vh - 230px);position:relative;margin-bottom:0" id="fWrap">
        <table class="tbl ftbl"><thead><tr><th><input type="checkbox" id="fAll"></th><th>Tên</th><th class="num">Kích thước</th><th>Sửa đổi</th><th>Quyền</th><th>Chủ</th><th></th></tr></thead><tbody id="fBody"></tbody></table></div>
      <div id="fStatus" class="card-sub mono" style="padding:10px 20px;border-top:1px solid var(--line);margin:0 -20px"></div></div>`;

    const crumbs = () => {
      const c = $('#crumbs');
      if (editingPath) return;
      const parts = path.split('/').filter(Boolean);
      let acc = '';
      c.innerHTML = `<button data-p="/">/</button>` + parts.map((p, i) => { acc += '/' + p; return `${i ? '<i>/</i>' : ''}<button data-p="${esc(acc)}">${esc(p)}</button>`; }).join('');
    };
    const updSel = () => {
      $('#fSel').classList.toggle('hidden', !sel.size);
      $('#fSelN').textContent = `${sel.size} mục đã chọn`;
      $$('#fBody tr[data-n]').forEach(tr => { const on = sel.has(tr.dataset.n); tr.classList.toggle('sel', on); $('input', tr).checked = on; });
    };
    const render = () => {
      const list = entries.filter(e => showHidden || !e.name.startsWith('.'));
      const totalSize = list.filter(e => !e.dir).reduce((s, e) => s + e.size, 0);
      $('#fStatus').textContent = `${list.filter(e => e.dir).length} thư mục · ${list.filter(e => !e.dir).length} tệp · ${fmtBytes(totalSize)}`;
      $('#fBody').innerHTML = list.map(e => {
        const k = e.dir ? 'dir' : kindOf(e.name);
        return `<tr data-n="${esc(e.name)}" data-dir="${e.dir ? 1 : 0}"><td><input type="checkbox"></td>
        <td><div class="fname">${ico(e.dir ? 'folder' : e.link && !e.dir ? 'link' : kindIcon[k], e.dir ? 'dir' : '')}<a data-open>${esc(e.name)}</a>${e.link ? `<small>→ ${esc(e.target)}</small>` : ''}</div></td>
        <td class="num dim">${e.dir ? '—' : fmtBytes(e.size)}</td>
        <td class="dim mono" style="font-size:12px;white-space:nowrap">${fmtDT(e.mtime * 1000)}</td>
        <td class="mono dim" style="font-size:12px">${esc(e.mode)}</td><td class="dim">${esc(e.owner || '')}</td>
        <td><div class="acts">
          <a class="btn sm icon ghost" href="${rawURL(joinPath(path, e.name), true)}" title="${e.dir ? 'Tải về (zip)' : 'Tải về'}">${ico('down', 'sm')}</a>
          <button class="btn sm icon ghost" data-ren title="Đổi tên">${ico('edit', 'sm')}</button>
          <button class="btn sm icon ghost" data-del title="Xoá">${ico('trash', 'sm')}</button></div></td></tr>`;
      }).join('') || `<tr><td colspan="7" class="empty">Thư mục trống — kéo thả tệp vào đây để tải lên</td></tr>`;
      updSel();
    };
    const go = async p => {
      try {
        const r = await api('/api/files/list?path=' + encodeURIComponent(p || ''));
        path = r.path; entries = r.entries; sel.clear();
        LS.set('filesPath', path);
        $('#pageSub').textContent = path;
        crumbs(); render();
        $('#fWrap').scrollTop = 0;
      } catch (e) {
        toastErr(e);
        if (!entries.length && p) go('');
      }
    };
    const reload = () => go(path);
    go(path);

    $('#crumbs').onclick = e => {
      const b = e.target.closest('[data-p]');
      if (b) return go(b.dataset.p);
      if (e.target.id === 'crumbs' && !editingPath) {
        editingPath = true;
        $('#crumbs').innerHTML = `<input class="mono" value="${esc(path)}">`;
        const inp = $('#crumbs input'); inp.focus(); inp.select();
        const done = ok => { editingPath = false; if (ok && inp.value.trim()) go(inp.value.trim()); else crumbs(); };
        inp.onkeydown = ev => { if (ev.key === 'Enter') done(true); if (ev.key === 'Escape') done(false); };
        inp.onblur = () => done(false);
      }
    };
    $('#fUp').onclick = () => go(path === '/' ? '/' : path.replace(/\/[^/]+\/?$/, '') || '/');
    $('#fHome').onclick = () => go('');
    $('#fRefresh').onclick = reload;
    $('#fHidden').onclick = e => { showHidden = !showHidden; LS.set('showHidden', showHidden); e.currentTarget.classList.toggle('on', showHidden); render(); };
    $('#fTerm').onclick = () => Term.open({ cwd: path, title: path.split('/').pop() || '/' });
    $('#fMkdir').onclick = async () => {
      const n = await promptBox('Thư mục mới', 'Tên thư mục'); if (!n) return;
      try { await api('/api/files/op', { body: { op: 'mkdir', path: joinPath(path, n) } }); reload(); } catch (e) { toastErr(e); }
    };
    $('#fTouch').onclick = async () => {
      const n = await promptBox('Tệp mới', 'Tên tệp'); if (!n) return;
      try { await api('/api/files/op', { body: { op: 'touch', path: joinPath(path, n) } }); await reload(); openFile(joinPath(path, n), n, 0); } catch (e) { toastErr(e); }
    };
    $('#fAll').onchange = e => { sel = new Set(e.target.checked ? entries.filter(x => showHidden || !x.name.startsWith('.')).map(x => x.name) : []); updSel(); };
    $('#fSelClear').onclick = () => { sel.clear(); $('#fAll').checked = false; updSel(); };
    $('#fSelDel').onclick = async () => {
      if (!await confirmBox(`Xoá vĩnh viễn ${sel.size} mục?\n${[...sel].slice(0, 8).map(esc).join('\n')}${sel.size > 8 ? '\n…' : ''}`, { ok: 'Xoá', danger: true })) return;
      try { await api('/api/files/op', { body: { op: 'delete', paths: [...sel].map(n => joinPath(path, n)) } }); toast('Đã xoá'); } catch (e) { toastErr(e); }
      $('#fAll').checked = false; reload();
    };

    $('#fBody').onclick = async e => {
      const tr = e.target.closest('tr[data-n]'); if (!tr) return;
      const name = tr.dataset.n, full = joinPath(path, name), isDir = tr.dataset.dir === '1';
      if (e.target.matches('input[type=checkbox]')) { e.target.checked ? sel.add(name) : sel.delete(name); updSel(); return; }
      if (e.target.closest('[data-open]')) { isDir ? go(full) : openFile(full, name, (entries.find(x => x.name === name) || {}).size); return; }
      if (e.target.closest('[data-ren]')) {
        const n = await promptBox('Đổi tên', 'Tên mới', name); if (!n || n === name) return;
        try { await api('/api/files/op', { body: { op: 'rename', path: full, to: n.startsWith('/') ? n : joinPath(path, n) } }); reload(); } catch (ex) { toastErr(ex); }
        return;
      }
      if (e.target.closest('[data-del]')) {
        if (!await confirmBox(`Xoá vĩnh viễn <b class="mono">${esc(name)}</b>${isDir ? ' và toàn bộ nội dung bên trong' : ''}?`, { ok: 'Xoá', danger: true })) return;
        try { await api('/api/files/op', { body: { op: 'delete', paths: [full] } }); toast('Đã xoá ' + name); reload(); } catch (ex) { toastErr(ex); }
      }
    };
    $('#fBody').ondblclick = e => { const tr = e.target.closest('tr[data-n]'); if (tr && !e.target.closest('button,a,input')) $('[data-open]', tr).click(); };

    // ---- upload (button + drag & drop) ----
    const upload = files => {
      if (!files.length) return;
      const fd = new FormData();
      let total = 0;
      for (const f of files) { fd.append('file', f, f.name); total += f.size; }
      const t = document.createElement('div');
      t.className = 'toast';
      $('#toasts').appendChild(t);
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/files/upload?dir=' + encodeURIComponent(path));
      xhr.setRequestHeader('X-TNS', '1');
      xhr.upload.onprogress = ev => { t.innerHTML = `${ico('up')}<div>Đang tải lên ${files.length} tệp… ${Math.round(ev.loaded / (ev.total || total) * 100)}%</div>`; };
      xhr.onload = () => {
        t.remove();
        if (xhr.status === 200) toast(`Đã tải lên ${files.length} tệp (${fmtBytes(total)})`);
        else { let m = xhr.statusText; try { m = JSON.parse(xhr.responseText).error; } catch (e) { } toast(m, 'err'); }
        reload();
      };
      xhr.onerror = () => { t.remove(); toast('Tải lên thất bại', 'err'); };
      xhr.send(fd);
    };
    $('#fUpload').onclick = () => $('#fFile').click();
    $('#fFile').onchange = e => { upload([...e.target.files]); e.target.value = ''; };
    const wrap = $('#fWrap');
    let hint = null, depth = 0;
    wrap.addEventListener('dragenter', e => { if (![...e.dataTransfer.types].includes('Files')) return; e.preventDefault(); depth++; if (!hint) { hint = document.createElement('div'); hint.className = 'drop-hint'; hint.textContent = 'Thả để tải lên ' + path; wrap.appendChild(hint); } });
    wrap.addEventListener('dragover', e => e.preventDefault());
    wrap.addEventListener('dragleave', () => { if (--depth <= 0 && hint) { hint.remove(); hint = null; depth = 0; } });
    wrap.addEventListener('drop', e => { e.preventDefault(); depth = 0; if (hint) { hint.remove(); hint = null; } upload([...e.dataTransfer.files]); });
  },
};

async function openFile(full, name, size) {
  const k = kindOf(name);
  const dl = `<a class="btn sm" href="${rawURL(full, true)}">${ico('down', 'sm')}Tải về</a>`;
  if (k === 'img' || k === 'video' || k === 'audio' || k === 'pdf') {
    const src = rawURL(full);
    const inner = k === 'img' ? `<img src="${src}" alt="">` : k === 'video' ? `<video src="${src}" controls autoplay></video>`
      : k === 'audio' ? `<audio src="${src}" controls autoplay style="width:min(520px,90%)"></audio>` : `<iframe src="${src}"></iframe>`;
    const m = openModal({ title: esc(full), size: 'lg', flush: true, body: `<div class="viewer">${inner}</div>` });
    $('.mh-extra', m).innerHTML = `<span class="card-sub mono">${fmtBytes(size)}</span>${dl}`;
    return;
  }
  if (k === 'zip') {
    const m = openModal({ title: esc(name), body: `<div class="empty">${ico(kindIcon[k])}<br>Không xem trước được loại tệp này (${fmtBytes(size)}).</div>`, foot: dl });
    return m;
  }
  let r;
  try { r = await api('/api/files/text?path=' + encodeURIComponent(full)); } catch (e) { return toastErr(e); }
  if (r.binary) {
    openModal({ title: esc(name), body: `<div class="empty">${ico('file')}<br>Tệp nhị phân (${fmtBytes(r.size)}) — không hiển thị dạng văn bản.</div>`, foot: dl });
    return;
  }
  const ro = r.truncated;
  const m = openModal({
    title: esc(full), size: 'lg', flush: true, body: '<div style="flex:1;display:flex;min-height:0" id="ed"></div>',
    foot: `<span class="card-sub" style="margin-right:auto">${ro ? 'Tệp lớn hơn 5MB — chỉ xem 5MB đầu, không thể lưu.' : 'Ctrl/⌘ + S để lưu'}</span>${dl}<button class="btn" data-close>Đóng</button>${ro ? '' : `<button class="btn primary" id="edSave">${ico('save', 'sm')}Lưu</button>`}`,
  });
  $('.mh-extra', m).innerHTML = `<span class="card-sub mono">${fmtBytes(r.size)}</span>`;
  const save = async val => {
    try { await api('/api/files/save', { body: { path: full, content: val } }); toast('Đã lưu ' + name); } catch (e) { toastErr(e); }
  };
  const ce = codeEditor($('#ed', m), r.content, { readOnly: ro, onSave: ro ? null : save });
  if (!ro) $('#edSave', m).onclick = () => save(ce.value);
  ce.focus();
}

// ================= terminal =================
const loadScript = src => new Promise((res, rej) => { const s = document.createElement('script'); s.src = src; s.onload = res; s.onerror = rej; document.head.appendChild(s); });
const TERM_THEME = {
  background: '#030914', foreground: '#cfe3f8', cursor: '#38bdf8', cursorAccent: '#030914', selectionBackground: 'rgba(56,189,248,.32)',
  black: '#0b1526', red: '#f87171', green: '#34d399', yellow: '#fbbf24', blue: '#60a5fa', magenta: '#a78bfa', cyan: '#22d3ee', white: '#cbd5e1',
  brightBlack: '#4b5d7a', brightRed: '#fca5a5', brightGreen: '#6ee7b7', brightYellow: '#fde68a', brightBlue: '#93c5fd', brightMagenta: '#c4b5fd', brightCyan: '#67e8f9', brightWhite: '#f1f5f9',
};

const Term = {
  root: null, sessions: [], active: null, seq: 0, pending: [], fontSize: LS.get('termFont', 13.5), ready: null,

  async ensureLib() {
    if (window.Terminal) return;
    if (!this.ready) {
      const l = document.createElement('link'); l.rel = 'stylesheet'; l.href = 'vendor/xterm.css'; document.head.appendChild(l);
      this.ready = loadScript('vendor/xterm.js').then(() => loadScript('vendor/addon-fit.js'));
    }
    await this.ready;
  },

  open(opts) { this.pending.push(opts || {}); if (location.hash !== '#/terminal') location.hash = '#/terminal'; else this.flush(); },

  build() {
    const r = document.createElement('div');
    r.className = 'term-page';
    r.innerHTML = `<div class="card term-shell">
      <div class="term-tabs"><div id="tTabs" style="display:flex;gap:2px"></div>
        <button class="btn sm ghost icon" id="tNew" title="Phiên mới">${ico('plus', 'sm')}</button>
        <div class="term-tools">
          <button class="btn sm ghost" id="tFontDn" title="Thu nhỏ chữ">A−</button><button class="btn sm ghost" id="tFontUp" title="Phóng to chữ">A+</button>
          <button class="btn sm ghost icon" id="tClear" title="Xoá màn hình">${ico('trash', 'sm')}</button>
          <button class="btn sm ghost icon" id="tFull" title="Toàn màn hình">${ico('max', 'sm')}</button></div></div>
      <div class="term-body" id="tBody"></div>
      <div class="term-status"><span id="tConn"><i class="dot"></i> —</span><span id="tSize"></span><span style="flex:1"></span><span>${esc(S.host)}</span></div></div>`;
    $('#tNew', r).onclick = () => this.add({});
    $('#tFontDn', r).onclick = () => this.font(-1);
    $('#tFontUp', r).onclick = () => this.font(1);
    $('#tClear', r).onclick = () => this.active && this.active.term.clear();
    $('#tFull', r).onclick = () => { r.classList.toggle('full'); setTimeout(() => this.fit(), 30); };
    $('#tTabs', r).onclick = e => {
      const x = e.target.closest('.x'), t = e.target.closest('.term-tab');
      if (x) { this.close(+x.dataset.id); return; }
      if (t) this.focus(+t.dataset.id);
    };
    this.ro = new ResizeObserver(() => this.fit());
    this.ro.observe($('#tBody', r));
    this.root = r;
  },

  font(d) {
    this.fontSize = clamp(this.fontSize + d, 10, 22);
    LS.set('termFont', this.fontSize);
    this.sessions.forEach(s => { s.term.options.fontSize = this.fontSize; });
    this.fit();
  },

  add({ container, cwd, title }) {
    const id = ++this.seq;
    const pane = document.createElement('div');
    pane.className = 'term-pane';
    $('#tBody', this.root).appendChild(pane);
    const term = new window.Terminal({
      fontFamily: getComputedStyle(document.body).getPropertyValue('--mono'), fontSize: this.fontSize, lineHeight: 1.18,
      cursorBlink: true, scrollback: 5000, theme: TERM_THEME, allowProposedApi: false, macOptionIsMeta: true,
    });
    const fit = new window.FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(pane);
    const s = { id, term, fit, pane, title: title || (container ? 'docker' : 'shell') + ' ' + id, container, cwd, ws: null, alive: false };
    if (container) s.title = '⧉ ' + (title || container);
    this.sessions.push(s);
    const enc = new TextEncoder();
    term.onData(d => {
      if (s.alive) s.ws.send(enc.encode(d));
      else if (d === '\r') this.connect(s);
    });
    term.onResize(({ cols, rows }) => { if (s.alive) s.ws.send(JSON.stringify({ t: 'resize', cols, rows })); if (s === this.active) this.status(); });
    this.connect(s);
    this.focus(id);
    return s;
  },

  connect(s) {
    const q = new URLSearchParams();
    if (s.container) q.set('container', s.container);
    if (s.cwd) q.set('cwd', s.cwd);
    const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws/term?${q}`);
    ws.binaryType = 'arraybuffer';
    s.ws = ws;
    ws.onopen = () => {
      s.alive = true;
      try { s.fit.fit(); } catch (e) { }
      ws.send(JSON.stringify({ t: 'resize', cols: s.term.cols, rows: s.term.rows }));
      this.status();
    };
    ws.onmessage = ev => {
      if (typeof ev.data === 'string') return;
      s.term.write(new Uint8Array(ev.data));
    };
    ws.onclose = () => {
      if (s.alive) s.term.write('\r\n\x1b[38;2;112;136;168m── phiên đã kết thúc · nhấn Enter để kết nối lại ──\x1b[0m\r\n');
      else s.term.write('\r\n\x1b[31mKhông kết nối được. Nhấn Enter để thử lại.\x1b[0m\r\n');
      s.alive = false;
      this.status();
    };
  },

  focus(id) {
    const s = this.sessions.find(x => x.id === id);
    if (!s) return;
    this.active = s;
    this.sessions.forEach(x => { x.pane.style.visibility = x === s ? 'visible' : 'hidden'; });
    this.tabs();
    requestAnimationFrame(() => { this.fit(); s.term.focus(); });
  },

  close(id) {
    const i = this.sessions.findIndex(x => x.id === id);
    if (i < 0) return;
    const s = this.sessions[i];
    s.alive = false;
    try { s.ws.close(); } catch (e) { }
    s.term.dispose(); s.pane.remove();
    this.sessions.splice(i, 1);
    if (!this.sessions.length) { this.add({}); return; }
    if (this.active === s) this.focus(this.sessions[Math.max(0, i - 1)].id); else this.tabs();
  },

  tabs() {
    $('#tTabs', this.root).innerHTML = this.sessions.map(s => `<button class="term-tab ${s === this.active ? 'on' : ''}" data-id="${s.id}">
      <i class="dot ${s.alive ? 'ok' : ''}"></i>${esc(s.title)}<span class="x" data-id="${s.id}" title="Đóng">${ico('x', 'sm')}</span></button>`).join('');
  },

  status() {
    if (!this.root) return;
    const s = this.active;
    this.tabs();
    if (!s) return;
    $('#tConn', this.root).innerHTML = s.alive ? '<i class="dot ok"></i> đã kết nối' : '<i class="dot bad"></i> mất kết nối';
    $('#tSize', this.root).textContent = `${s.term.cols}×${s.term.rows}`;
  },

  fit() {
    const s = this.active;
    if (!s || !this.root || !this.root.isConnected) return;
    try { s.fit.fit(); } catch (e) { }
  },

  flush() {
    while (this.pending.length) this.add(this.pending.shift());
  },
};

pages.terminal = {
  title: 'Terminal', sub: 'Phiên shell trực tiếp trên máy chủ', fullBleed: true,
  mount(v) {
    const wrap = document.createElement('div');
    wrap.style.cssText = 'height:100%;padding:14px 16px 16px';
    v.appendChild(wrap);
    wrap.innerHTML = '<div class="empty">Đang tải terminal…</div>';
    let alive = true;
    Term.ensureLib().then(() => {
      if (!alive) return;
      if (!Term.root) Term.build();
      wrap.innerHTML = '';
      wrap.appendChild(Term.root);
      if (Term.pending.length) Term.flush();
      else if (!Term.sessions.length) Term.add({});
      else Term.focus(Term.active.id);
    }).catch(() => { wrap.innerHTML = '<div class="empty">Không tải được xterm.js</div>'; });
    // keep sessions alive when navigating away: just detach the DOM
    return () => { alive = false; if (Term.root) { Term.root.classList.remove('full'); Term.root.remove(); } };
  },
};

// ================= settings =================
pages.settings = {
  title: 'Cài đặt', sub: 'Bảo mật bảng điều khiển',
  mount(v) {
    v.innerHTML = `<div class="grid g2" style="max-width:1100px">
      <div class="card"><div class="card-h"><span class="card-t">Đổi mật khẩu</span></div>
        <form class="form" id="pwForm">
          <label class="field"><span>Mật khẩu hiện tại</span><input type="password" name="cur" autocomplete="current-password" required></label>
          <label class="field"><span>Mật khẩu mới (≥ 8 ký tự)</span><input type="password" name="n1" autocomplete="new-password" required minlength="8"></label>
          <label class="field"><span>Nhập lại mật khẩu mới</span><input type="password" name="n2" autocomplete="new-password" required></label>
          <button class="btn primary" type="submit">Cập nhật mật khẩu</button>
          <p class="card-sub" style="margin:0">Đổi mật khẩu sẽ đăng xuất mọi phiên khác. Quên mật khẩu? Chạy <span class="mono">tns -reset-password</span> trên máy chủ.</p>
        </form></div>
      <div class="card"><div class="card-h"><span class="card-t">Khuyến nghị bảo mật</span></div>
        <div class="alerts" style="max-height:none">
          <div class="alert info">${ico('shield')}<div><b>Dùng HTTPS</b><p>Chạy với <span class="mono">-tls</span> (chứng chỉ tự ký) hoặc đặt sau Nginx + Let's Encrypt.</p></div></div>
          <div class="alert info">${ico('ban')}<div><b>Giới hạn IP truy cập cổng 1611</b><p>Trong trang Tường lửa, chỉ cho phép IP văn phòng / VPN truy cập cổng bảng điều khiển.</p></div></div>
          <div class="alert info">${ico('term')}<div><b>TNS chạy với quyền root</b><p>Terminal và trình duyệt tệp có toàn quyền trên máy. Chỉ chia sẻ mật khẩu cho quản trị viên.</p></div></div>
          <div class="alert info">${ico('globe')}<div><b>Định vị IP</b><p>Vị trí IP được tra qua ip-api.com và lưu cache. Tắt bằng cờ <span class="mono">-nogeo</span>.</p></div></div>
        </div></div>
      <div class="card"><div class="card-h"><span class="card-t">Thông tin</span></div><dl class="kv" id="aboutKv"></dl></div>
    </div>`;
    const i = S.info || {};
    $('#aboutKv').innerHTML = `<dt>Phiên bản</dt><dd>TNS 1.0.0</dd><dt>Máy chủ</dt><dd>${esc(S.host)}</dd><dt>Hệ điều hành</dt><dd>${esc((i.platform || i.os || '') + ' ' + (i.platformVersion || ''))}</dd>
      <dt>Kết nối</dt><dd>${esc(location.protocol.replace(':', '').toUpperCase())} · ${esc(location.host)}</dd><dt>Chế độ demo</dt><dd>${S.demo ? 'bật' : 'tắt'}</dd>`;
    $('#pwForm').onsubmit = async e => {
      e.preventDefault();
      const f = e.target;
      if (f.n1.value !== f.n2.value) return toast('Mật khẩu mới không khớp', 'err');
      try { await api('/api/auth/password', { body: { current: f.cur.value, new: f.n1.value } }); toast('Đã đổi mật khẩu'); f.reset(); } catch (ex) { toastErr(ex); }
    };
  },
};
