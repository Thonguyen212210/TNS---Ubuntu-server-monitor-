'use strict';
/* Azure Cloud Console — Tailscale VPN & Subnet Router Manager.
   Depends on app.js & pages.js. */

pages.tailscale = {
  title: 'Tailscale VPN',
  sub: 'Manage Tailscale mesh network, Subnet Router, Exit Node, and connectivity diagnostics',
  mount(v) {
    v.innerHTML = `
      <div id="tsNote"></div>
      <div id="tsHealth"></div>
      <div class="grid g5">
        <div class="card stat"><div class="k">Status</div><div class="v ok" id="tsState" style="font-size:24px;margin-top:8px">—</div></div>
        <div class="card stat"><div class="k">Tailscale IPv4</div><div class="v acc mono" id="tsIp" style="font-size:20px;margin-top:10px">—</div></div>
        <div class="card stat"><div class="k">Peers Online</div><div class="v" id="tsPeersCnt">—</div></div>
        <div class="card stat"><div class="k">Subnet Routes</div><div class="v" id="tsRoutesCnt">—</div></div>
        <div class="card stat"><div class="k">DERP Relay</div><div class="v mono" id="tsRelay" style="font-size:24px;margin-top:8px">—</div></div>
      </div>
      <div class="grid g-main mt">
        <div class="grid" style="align-content:start">
          <div class="card">
            <div class="card-h">
              <span class="card-t">Tailnet Devices</span>
              <span class="card-sub" id="tsTailnet"></span>
              <span class="sp"></span>
              <div class="search">${ico('search')}<input id="tsSearch" placeholder="Search host, IP, OS…" style="width:180px"></div>
              <div class="seg" id="tsFilter">
                <button data-f="all" class="on">All</button>
                <button data-f="online">Online</button>
                <button data-f="offline">Offline</button>
              </div>
              <button class="btn sm" id="tsRefresh">${ico('refresh', 'sm')}Refresh</button>
            </div>
            <div class="tbl-wrap" style="max-height:460px">
              <table class="tbl">
                <thead><tr><th>Device</th><th>Tailscale IP</th><th>Connection</th><th class="num">Traffic</th><th>Last Seen</th><th></th></tr></thead>
                <tbody id="tsPeersBody"><tr><td colspan="6" class="empty">Loading…</td></tr></tbody>
              </table>
            </div>
          </div>
          <div class="card">
            <div class="card-h">
              <span class="card-t">Network Diagnostics (Ping &amp; Netcheck)</span>
              <span class="sp"></span>
              <button class="btn sm" id="tsNetcheck">${ico('plug', 'sm')}Run Netcheck (DERP)</button>
            </div>
            <form class="row" id="tsPingForm">
              <input name="target" class="mono" style="flex:1;min-width:200px" placeholder="Enter Tailscale IP or hostname (e.g. 100.127.127.35)">
              <button class="btn sm primary" type="submit">${ico('play', 'sm')}Tailscale Ping</button>
            </form>
            <pre class="probe-out hidden" id="tsDiagOut"></pre>
          </div>
        </div>

        <div class="grid" style="align-content:start">
          <div class="card">
            <div class="card-h">
              <span class="card-t">Node Control</span>
              <span class="sp"></span>
              <span id="tsVer" class="card-sub mono"></span>
            </div>
            <div class="row" style="justify-content:space-between;margin-bottom:14px">
              <div>
                <div style="font-weight:700" id="tsSelfName">—</div>
                <div class="mono dim" style="font-size:12px" id="tsSelfDns">—</div>
              </div>
              <button class="btn sm" id="tsPowerBtn">—</button>
            </div>
            <dl class="kv" id="tsSelfKv"></dl>
          </div>

          <div class="card">
            <div class="card-h"><span class="card-t">Subnet Router &amp; Node Options</span></div>
            <form class="form" id="tsCfgForm">
              <label class="field">
                <span>Advertise Subnet Routes (comma-separated CIDRs)</span>
                <input name="routes" class="mono" placeholder="10.0.0.0/24,10.0.1.0/24">
              </label>
              <div class="row" style="gap:6px;margin-top:-8px" id="tsRoutePresets">
                <button type="button" class="btn sm" data-r="10.0.0.0/24,10.0.1.0/24">2 TNS-Lab Subnets</button>
                <button type="button" class="btn sm" data-r="10.0.0.0/16">Full VNet /16</button>
                <button type="button" class="btn sm" data-r="">Clear routes</button>
              </div>
              <label class="field">
                <span>Use Exit Node</span>
                <select name="exitNode" id="tsExitSel"><option value="">— No Exit Node —</option></select>
              </label>
              <div class="grid" style="gap:10px">
                <label class="row" style="justify-content:space-between;cursor:pointer">
                  <span><b>Advertise Exit Node</b><div class="dim" style="font-size:12px">Route other devices' internet traffic through this VM</div></span>
                  <label class="switch"><input type="checkbox" name="advExit"><i></i></label>
                </label>
                <label class="row" style="justify-content:space-between;cursor:pointer">
                  <span><b>Tailscale SSH</b><div class="dim" style="font-size:12px">Enable SSH authenticated via Tailscale identity</div></span>
                  <label class="switch"><input type="checkbox" name="ssh"><i></i></label>
                </label>
                <label class="row" style="justify-content:space-between;cursor:pointer">
                  <span><b>Accept Routes</b><div class="dim" style="font-size:12px">Accept subnet routes advertised by other peers</div></span>
                  <label class="switch"><input type="checkbox" name="acceptRoutes"><i></i></label>
                </label>
                <label class="row" style="justify-content:space-between;cursor:pointer">
                  <span><b>Accept DNS (MagicDNS)</b><div class="dim" style="font-size:12px">Use DNS settings from your Tailnet</div></span>
                  <label class="switch"><input type="checkbox" name="acceptDns"><i></i></label>
                </label>
                <label class="row" style="justify-content:space-between;cursor:pointer">
                  <span><b>Exit Node Allow LAN</b><div class="dim" style="font-size:12px">Keep local LAN access while using an Exit Node</div></span>
                  <label class="switch"><input type="checkbox" name="allowLan"><i></i></label>
                </label>
                <label class="row" style="justify-content:space-between;cursor:pointer">
                  <span><b>Shields Up</b><div class="dim" style="font-size:12px">Block all incoming connections from the Tailnet</div></span>
                  <label class="switch"><input type="checkbox" name="shieldsUp"><i></i></label>
                </label>
              </div>
              <button class="btn primary" type="submit" id="tsSaveBtn">${ico('save', 'sm')}Apply Settings</button>
            </form>
          </div>
        </div>
      </div>`;

    let data = null;
    let filter = 'all';
    let q = '';
    let formSynced = false;

    const renderPeers = () => {
      if (!data || !data.available) return;
      const rows = (data.peers || []).filter(p => {
        if (filter === 'online' && !p.online) return false;
        if (filter === 'offline' && p.online) return false;
        if (!q) return true;
        return (p.hostName + ' ' + p.dnsName + ' ' + p.os + ' ' + (p.ips || []).join(' ') + ' ' + (p.relay || '')).toLowerCase().includes(q);
      });
      $('#tsPeersBody').innerHTML = rows.map(p => {
        const ip4 = (p.ips || []).find(x => !x.includes(':')) || (p.ips || [])[0] || '—';
        const conn = !p.online
          ? '<span class="badge">Offline</span>'
          : p.direct
            ? `<span class="badge ok">Direct</span><div class="mono dim" style="font-size:11.5px">${esc(p.curAddr)}</div>`
            : `<span class="badge acc">DERP · ${esc(p.relay || '?')}</span>`;
        const tags = [
          p.active ? '<span class="badge ok">Active</span>' : '',
          p.exitNode ? '<span class="badge warn">Exit Node</span>' : (p.exitNodeOption ? '<span class="badge">Exit Option</span>' : ''),
          ...(p.primaryRoutes || []).map(r => `<span class="badge mono">${esc(r)}</span>`),
        ].filter(Boolean).join(' ');
        return `<tr>
          <td>
            <div class="row" style="gap:7px">
              <i class="dot ${p.online ? 'ok' : ''}"></i>
              <b>${esc(p.hostName)}</b>
              <span class="badge">${esc(p.os || '?')}</span>
              ${tags}
            </div>
            <div class="mono dim" style="font-size:11.5px;margin-top:2px">${esc(p.dnsName)}</div>
          </td>
          <td class="mono"><b>${esc(ip4)}</b></td>
          <td>${conn}</td>
          <td class="num mono" style="font-size:12px">↓ ${fmtBytes(p.rxBytes)}<br>↑ ${fmtBytes(p.txBytes)}</td>
          <td class="dim" style="white-space:nowrap">${p.online ? '<span style="color:var(--ok)">Connected</span>' : (p.lastSeen ? fmtAgo(p.lastSeen) : '—')}</td>
          <td><div class="acts">
            <button class="btn sm" data-ping="${esc(ip4)}" title="Tailscale ping this peer">Ping</button>
          </div></td>
        </tr>`;
      }).join('') || '<tr><td colspan="6" class="empty">No matching devices</td></tr>';
    };

    const syncForm = () => {
      if (!data || !data.prefs) return;
      const p = data.prefs;
      const f = $('#tsCfgForm');
      f.routes.value = (p.advertiseRoutes || []).join(',');
      f.advExit.checked = !!p.advertiseExitNode;
      f.ssh.checked = !!p.runSsh;
      f.acceptRoutes.checked = !!p.acceptRoutes;
      f.acceptDns.checked = !!p.acceptDns;
      f.allowLan.checked = !!p.exitNodeAllowLan;
      f.shieldsUp.checked = !!p.shieldsUp;

      const exitOpts = (data.peers || []).filter(x => x.exitNodeOption);
      const curExit = p.exitNodeIp || '';
      $('#tsExitSel').innerHTML = `<option value="">— No Exit Node —</option>` +
        exitOpts.map(x => {
          const ip = (x.ips || [])[0] || x.hostName;
          return `<option value="${esc(ip)}" ${ip === curExit || x.exitNode ? 'selected' : ''}>${esc(x.hostName)} (${esc(ip)})</option>`;
        }).join('');
      formSynced = true;
    };

    const render = () => {
      const d = data;
      if (!d) return;
      if (!d.available) {
        $('#tsNote').innerHTML = `<div class="note info">${esc(d.note || 'Tailscale is not available on this server.')}</div>`;
        $('#tsHealth').innerHTML = '';
        $('#tsState').textContent = 'Unavailable';
        $('#tsState').className = 'v dim';
        $('#tsIp').textContent = '—';
        $('#tsPeersCnt').textContent = '—';
        $('#tsRoutesCnt').textContent = '—';
        $('#tsRelay').textContent = '—';
        $('#tsPeersBody').innerHTML = '<tr><td colspan="6" class="empty">Tailscale is not installed on the selected server</td></tr>';
        $('#tsCfgForm').querySelectorAll('input,select,button').forEach(el => { el.disabled = true; });
        $('#tsPowerBtn').disabled = true;
        return;
      }

      $('#tsCfgForm').querySelectorAll('input,select,button').forEach(el => { el.disabled = false; });
      $('#tsPowerBtn').disabled = false;
      $('#tsNote').innerHTML = d.authUrl ? `<div class="note">Tailscale login required: <a href="${esc(d.authUrl)}" target="_blank" rel="noopener">${esc(d.authUrl)}</a></div>` : '';
      const health = d.health || [];
      $('#tsHealth').innerHTML = health.length ? `<div class="note">${health.map(h => `• ${esc(h)}`).join('<br>')}</div>` : '';

      const running = d.backendState === 'Running';
      $('#tsState').textContent = d.backendState || '—';
      $('#tsState').className = 'v ' + (running ? 'ok' : 'bad');

      const self = d.self || {};
      const ip4 = (self.ips || []).find(x => !x.includes(':')) || '—';
      const ip6 = (self.ips || []).find(x => x.includes(':')) || '—';
      $('#tsIp').textContent = ip4;
      const peers = d.peers || [];
      const online = peers.filter(p => p.online).length;
      $('#tsPeersCnt').textContent = `${online}/${peers.length}`;
      const routes = (d.prefs && d.prefs.advertiseRoutes) || self.primaryRoutes || [];
      $('#tsRoutesCnt').textContent = String(routes.length);
      $('#tsRelay').textContent = (self.relay || '—').toUpperCase();

      $('#tsTailnet').textContent = d.tailnet ? `${d.tailnet.name} · ${d.tailnet.magicDnsSuffix || ''}` : '';
      $('#tsVer').textContent = 'v' + (d.version || '').split('-')[0];
      $('#tsSelfName').textContent = self.hostName || S.currentNode().name;
      $('#tsSelfDns').textContent = self.dnsName || '';

      const pbtn = $('#tsPowerBtn');
      pbtn.textContent = running ? 'Disconnect' : 'Connect';
      pbtn.className = 'btn sm ' + (running ? 'danger' : 'primary');
      pbtn.dataset.op = running ? 'down' : 'up';

      $('#tsSelfKv').innerHTML = `
        <dt>IPv4</dt><dd>${esc(ip4)}</dd>
        <dt>IPv6</dt><dd>${esc(ip6)}</dd>
        <dt>Active Routes</dt><dd>${(self.primaryRoutes || []).length ? self.primaryRoutes.map(r => `<span class="badge acc">${esc(r)}</span>`).join(' ') : '<span class="dim">None</span>'}</dd>
        <dt>Key Expiry</dt><dd>${self.keyExpiry && !self.keyExpiry.startsWith('0001-') ? esc(fmtDT(self.keyExpiry)) : 'Never expires'}</dd>`;

      if (!formSynced) syncForm();
      renderPeers();
    };

    const load = async (manual = false) => {
      try {
        data = await api('/api/tailscale/status');
        if (manual) formSynced = false;
        render();
        if (manual) toast('Refreshed Tailscale status');
      } catch (e) { toastErr(e); }
    };

    const runDiag = async (label, body) => {
      const out = $('#tsDiagOut');
      out.classList.remove('hidden');
      out.textContent = `Running ${label}…`;
      try {
        const r = await api('/api/tailscale/action', { body });
        out.textContent = r.output || '(no output)';
      } catch (e) { out.textContent = 'Error: ' + e.message; }
    };

    every(15000, () => load(false));
    $('#tsRefresh').onclick = () => load(true);
    $('#tsSearch').oninput = e => { q = e.target.value.trim().toLowerCase(); renderPeers(); };
    $('#tsFilter').onclick = e => {
      const b = e.target.closest('button'); if (!b) return;
      filter = b.dataset.f;
      $$('#tsFilter button').forEach(x => x.classList.toggle('on', x === b));
      renderPeers();
    };
    $('#tsPeersBody').onclick = e => {
      const b = e.target.closest('[data-ping]'); if (!b) return;
      const target = b.dataset.ping;
      $('#tsPingForm').target.value = target;
      runDiag('tailscale ping ' + target, { op: 'ping', target });
    };
    $('#tsPingForm').onsubmit = e => {
      e.preventDefault();
      const target = e.target.target.value.trim();
      if (!target) return;
      runDiag('tailscale ping ' + target, { op: 'ping', target });
    };
    $('#tsNetcheck').onclick = () => runDiag('tailscale netcheck', { op: 'netcheck' });

    $('#tsPowerBtn').onclick = async e => {
      const op = e.currentTarget.dataset.op;
      if (op === 'down') {
        const ok = await confirmBox('⚠️ WARNING: Disconnecting Tailscale will drop SSH and Tailnet connections to this VM.\n\nContinue disconnecting Tailscale?', { ok: 'Disconnect', danger: true });
        if (!ok) return;
      }
      try {
        await api('/api/tailscale/action', { body: { op } });
        toast(op === 'up' ? 'Tailscale connected' : 'Tailscale disconnected');
        formSynced = false;
        load();
      } catch (ex) { toastErr(ex); }
    };

    $('#tsRoutePresets').onclick = e => {
      const b = e.target.closest('[data-r]'); if (!b) return;
      $('#tsCfgForm').routes.value = b.dataset.r;
    };

    $('#tsCfgForm').onsubmit = async e => {
      e.preventDefault();
      const f = e.target;
      if (f.shieldsUp.checked && (!data || !data.prefs || !data.prefs.shieldsUp)) {
        const ok = await confirmBox('⚠️ WARNING: Enabling Shields Up blocks all incoming Tailnet traffic to this VM.\n\nEnable Shields Up?', { ok: 'Enable', danger: true });
        if (!ok) { f.shieldsUp.checked = false; return; }
      }
      const btn = $('#tsSaveBtn'); btn.disabled = true;
      try {
        await api('/api/tailscale/action', {
          body: {
            op: 'set',
            advertiseRoutes: f.routes.value.trim(),
            advertiseExitNode: f.advExit.checked,
            ssh: f.ssh.checked,
            acceptRoutes: f.acceptRoutes.checked,
            acceptDns: f.acceptDns.checked,
            exitNode: f.exitNode.value,
            exitNodeAllowLan: f.allowLan.checked,
            shieldsUp: f.shieldsUp.checked,
          },
        });
        toast('Applied Tailscale configuration');
        formSynced = false;
        load();
      } catch (ex) { toastErr(ex); }
      btn.disabled = false;
    };
  },
};
