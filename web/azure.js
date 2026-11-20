'use strict';
/* Azure Cloud Console — Resource Group Network & Multi-VM Infrastructure Manager.
   Depends on app.js & pages.js. */

pages.azure = {
  title: 'Azure Infrastructure & Network',
  sub: 'Manage VNets, Subnets, NSGs, Route Tables, NAT Gateway, and all VMs in the Resource Group',
  mount(v) {
    const tabs = [
      ['topology', 'Topology & VMs'],
      ['nsgs', 'NSGs (Azure Firewall)'],
      ['vnets', 'VNets & Subnets'],
      ['routes', 'Route Tables (UDR)'],
      ['nics', 'NICs & Public IPs'],
      ['resources', 'Resources & Nodes'],
    ];
    let tab = LS.get('azureTab', 'topology');
    let data = null;
    let selectedNSG = '';

    v.innerHTML = `
      <div id="azNote"></div>
      <div class="grid g5" id="azStats">
        <div class="card stat"><div class="k">Resource Group</div><div class="v acc" id="azRg" style="font-size:24px;margin-top:8px">—</div></div>
        <div class="card stat"><div class="k">Virtual Machines</div><div class="v ok" id="azVmCnt">—</div></div>
        <div class="card stat"><div class="k">Subnets</div><div class="v" id="azSnCnt">—</div></div>
        <div class="card stat"><div class="k">NSG Rules</div><div class="v" id="azNsgCnt">—</div></div>
        <div class="card stat"><div class="k">Public IP</div><div class="v" id="azPip" style="font-size:20px;margin-top:10px">—</div></div>
      </div>
      <div class="row mt" style="align-items:flex-end;justify-content:space-between;border-bottom:1px solid var(--line-2);margin-bottom:20px">
        <div class="tabs" id="azTabs" style="border-bottom:0;margin-bottom:0">
          ${tabs.map(([k, l]) => `<button class="tab" data-t="${k}">${l}</button>`).join('')}
        </div>
        <button class="btn sm" id="azRefresh" style="margin-bottom:8px">${ico('refresh', 'sm')}Sync Azure</button>
      </div>
      <div id="azBody"><div class="empty">Loading Azure Resource Manager state…</div></div>`;

    const setTab = t => {
      tab = t;
      LS.set('azureTab', t);
      $$('#azTabs .tab').forEach(b => b.classList.toggle('on', b.dataset.t === t));
      renderTab();
    };
    $('#azTabs').onclick = e => { const b = e.target.closest('.tab'); if (b) setTab(b.dataset.t); };
    $('#azRefresh').onclick = () => load(true);

    const load = async (manual = false) => {
      try {
        const [az] = await Promise.all([api('/api/azure/overview'), refreshNodes()]);
        data = az;
        if (!selectedNSG && az.nsgs && az.nsgs.length) selectedNSG = az.nsgs[0].name;
        renderSummary();
        renderTab();
        if (manual) toast('Synced Azure infrastructure');
      } catch (e) {
        toastErr(e);
        $('#azBody').innerHTML = `<div class="note">${esc(e.message)}</div>`;
      }
    };

    const renderSummary = () => {
      const d = data;
      if (!d) return;
      const notes = [];
      if (d.note) notes.push(d.note);
      if (d.authError) notes.push('Auth details: ' + d.authError);
      if (d.errors && d.errors.length) notes.push('ARM sync warnings: ' + d.errors.join(' | '));
      $('#azNote').innerHTML = notes.length ? `<div class="note ${d.available ? 'info' : ''}">${notes.map(esc).join('<br>')}</div>` : '';

      $('#azRg').textContent = d.resourceGroup || '—';
      if (d.location) $('#pageSub').textContent = `Resource Group ${d.resourceGroup} · Region ${d.location} · Subscription ${d.subscriptionId || ''}`;
      const vms = d.vms || [];
      const running = vms.filter(x => /running/i.test(x.powerState)).length;
      $('#azVmCnt').textContent = vms.length ? `${running}/${vms.length}` : '0';
      const snCount = (d.vnets || []).reduce((s, vn) => s + (vn.subnets || []).length, 0);
      $('#azSnCnt').textContent = String(snCount);
      const ruleCount = (d.nsgs || []).reduce((s, g) => s + (g.rules || []).length, 0);
      $('#azNsgCnt').textContent = String(ruleCount);
      const pip = (d.publicIps || []).find(p => p.ip);
      $('#azPip').innerHTML = pip
        ? `<span class="mono">${esc(pip.ip)}</span>${pip.fqdn ? `<div class="mono dim" style="font-size:11.5px;margin-top:4px;overflow:hidden;text-overflow:ellipsis" title="${esc(pip.fqdn)}">${esc(pip.fqdn)}</div>` : ''}`
        : '—';
    };

    const nodeForVM = vm => {
      if (vm.isHub) return S.nodes.find(n => n.local);
      return S.nodes.find(n => n.id === vm.nodeId || (n.azureVmName && n.azureVmName.toLowerCase() === vm.name.toLowerCase()) || n.privateIp === vm.privateIp);
    };

    const renderTab = () => {
      const el = $('#azBody');
      if (!data) return;
      if (!data.available && tab !== 'resources') {
        renderTopologyFallback(el);
        return;
      }
      if (tab === 'topology') renderTopology(el);
      else if (tab === 'nsgs') renderNSGs(el);
      else if (tab === 'vnets') renderVNets(el);
      else if (tab === 'routes') renderRoutes(el);
      else if (tab === 'nics') renderNICs(el);
      else if (tab === 'resources') renderResources(el);
    };

    const renderTopologyFallback = el => {
      const gw = data.gateway || {};
      el.innerHTML = `<div class="card"><div class="card-h"><span class="card-t">Managed Nodes &amp; Gateway</span></div>
        <p class="dim">Azure ARM API is unavailable; showing directly connected servers:</p>
        ${renderGatewayHTML(gw)}</div>`;
      bindGatewayEvents(el);
    };

    const renderGatewayHTML = gw => `
      <div class="grid g2 mt">
        <div class="card">
          <div class="card-h"><span class="card-t">VM1 NAT Gateway &amp; Linux Routing</span><span class="sp"></span>
            <label class="row" style="gap:8px;cursor:pointer"><span class="card-sub">IP Forwarding (kernel)</span>
              <label class="switch"><input type="checkbox" id="gwFwdToggle" ${gw.ipForward ? 'checked' : ''}><i></i></label></label></div>
          <dl class="kv">
            <dt>Private IP (eth0)</dt><dd>${esc(gw.privateIp || '—')}</dd>
            <dt>Tailscale IP</dt><dd>${esc(gw.tailscaleIp || '—')}</dd>
            <dt>HTTP Proxy (tinyproxy)</dt><dd>${gw.tinyproxyActive ? '<span class="badge ok">ACTIVE :8888</span>' : '<span class="badge">INACTIVE</span>'}</dd>
            <dt>iptables POSTROUTING</dt><dd>${(gw.natRules || []).length ? gw.natRules.map(r => `<div>${esc(r)}</div>`).join('') : '<span class="dim">No NAT rules</span>'}</dd>
          </dl>
          ${(gw.peerChecks || []).length ? `<div class="mt"><div class="card-sub" style="font-weight:700;margin-bottom:8px">Internal Connectivity to Private VMs</div>
            <table class="tbl"><thead><tr><th>VM</th><th>Private IP</th><th>SSH :22</th><th>Agent :1611</th><th class="num">RTT</th></tr></thead><tbody>
              ${gw.peerChecks.map(p => `<tr><td><b>${esc(p.name)}</b></td><td class="mono">${esc(p.ip)}</td>
                <td>${p.sshOpen ? '<span class="badge ok">Open</span>' : '<span class="badge bad">Closed</span>'}</td>
                <td>${p.agentOpen ? '<span class="badge ok">Online</span>' : '<span class="badge bad">Offline</span>'}</td>
                <td class="num mono">${p.reachable ? p.rttMs + ' ms' : '—'}</td></tr>`).join('')}
            </tbody></table></div>` : ''}
        </div>
        <div class="card">
          <div class="card-h"><span class="card-t">Connectivity Check from VM1 (Ping &amp; TCP Probe)</span></div>
          <form class="form" id="gwProbeForm">
            <div class="grid g2" style="gap:10px">
              <label class="field"><span>Target IP in VNet</span><input name="host" class="mono" value="${esc((gw.peerChecks && gw.peerChecks[0] && gw.peerChecks[0].ip) || '10.0.1.4')}" required></label>
              <label class="field"><span>TCP Port (optional)</span><input name="port" type="number" class="mono" value="1611" min="1" max="65535"></label>
            </div>
            <div class="row">
              <button class="btn primary sm" type="submit">${ico('plug', 'sm')}Run Probe</button>
              <button class="btn sm" type="button" data-qprobe="10.0.1.4:22">VM2 SSH :22</button>
              <button class="btn sm" type="button" data-qprobe="10.0.1.4:1611">VM2 Agent :1611</button>
            </div>
          </form>
          <pre class="probe-out hidden" id="gwProbeOut"></pre>
        </div>
      </div>`;

    const bindGatewayEvents = el => {
      const tog = $('#gwFwdToggle', el);
      if (tog) tog.onchange = async e => {
        try {
          await api('/api/azure/gateway', { body: { op: 'ip_forward', enabled: e.target.checked } });
          toast(e.target.checked ? 'Enabled net.ipv4.ip_forward = 1' : 'Disabled net.ipv4.ip_forward');
        } catch (ex) { e.target.checked = !e.target.checked; toastErr(ex); }
      };
      const runProbe = async (host, port) => {
        const out = $('#gwProbeOut', el);
        out.classList.remove('hidden');
        out.textContent = `Probing ${host}${port ? ':' + port : ''} from VM1…`;
        try {
          const r = await api('/api/azure/gateway', { body: { op: 'probe', host, port: +port || 0 } });
          const lines = [];
          if (r.port) lines.push(`TCP ${r.host}:${r.port} => ${r.tcpOk ? 'OPEN (' + r.tcpMs + ' ms)' : 'FAILED: ' + (r.tcpErr || '')}`);
          if (r.pingOut) lines.push('\n' + r.pingOut);
          out.textContent = lines.join('\n');
        } catch (ex) { out.textContent = 'Error: ' + ex.message; }
      };
      const pf = $('#gwProbeForm', el);
      if (pf) {
        pf.onsubmit = e => { e.preventDefault(); runProbe(pf.host.value.trim(), pf.port.value); };
        el.querySelectorAll('[data-qprobe]').forEach(b => {
          b.onclick = () => {
            const [h, p] = b.dataset.qprobe.split(':');
            pf.host.value = h; pf.port.value = p;
            runProbe(h, p);
          };
        });
      }
    };

    const renderTopology = el => {
      const vnets = data.vnets || [];
      const vms = data.vms || [];
      const rts = data.routeTables || [];
      const gw = data.gateway || {};

      const hubVM = vms.find(x => x.isHub) || vms[0];
      const privVMs = vms.filter(x => !x.isHub);

      const flowHTML = hubVM ? `<div class="topo-flow">
        <span class="badge acc">Internet / Tailscale</span>
        <span class="arrow">→</span>
        <span class="mono"><b>${esc(hubVM.name)}</b> (${esc(hubVM.privateIp)}${hubVM.publicIp ? ' · Pub ' + esc(hubVM.publicIp) : ''})</span>
        ${privVMs.length ? `<span class="arrow">⇄</span><span class="badge">UDR 0.0.0.0/0 → ${esc(hubVM.privateIp)}</span><span class="arrow">→</span>` +
          privVMs.map(pv => `<span class="mono"><b>${esc(pv.name)}</b> (${esc(pv.privateIp)} · No Public IP)</span>`).join(', ') : ''}
      </div>` : '';

      const vnetHTML = vnets.map(vn => {
        const subnetsHTML = (vn.subnets || []).map(sn => {
          const snVMs = vms.filter(vm => vm.subnet === sn.name);
          const rt = rts.find(r => r.name === sn.routeTable);
          const isPriv = sn.name.toLowerCase().includes('priv') || (snVMs.length && snVMs.every(vm => !vm.publicIp));
          return `<div class="topo-subnet ${isPriv ? 'priv' : ''}">
            <div class="topo-sn-h">
              <div>
                <div style="font:700 15px var(--font)">${ico('net', 'sm')} Subnet <span class="mono">${esc(sn.name)}</span></div>
                <div class="mono dim" style="font-size:12.5px;margin-top:2px">${esc(sn.addressPrefix)}</div>
              </div>
              <span class="badge ${isPriv ? '' : 'acc'}">${isPriv ? 'Private Subnet' : 'Gateway / Public Subnet'}</span>
            </div>
            <div class="topo-sn-meta">
              ${sn.nsg ? `<span class="badge">NSG: ${esc(sn.nsg)}</span>` : ''}
              ${sn.routeTable ? `<span class="badge warn">UDR: ${esc(sn.routeTable)}</span>` : '<span class="dim">Azure default routing</span>'}
              ${rt && rt.routes.length ? rt.routes.map(ro => `<span class="mono dim">${esc(ro.addressPrefix)} → ${esc(ro.nextHopIpAddress || ro.nextHopType)}</span>`).join('') : ''}
            </div>
            ${snVMs.map(vm => {
              const nd = nodeForVM(vm);
              const st = nd && nd.stats;
              const running = /running/i.test(vm.powerState);
              const isCur = nd && nd.id === S.node;
              return `<div class="topo-vm ${isCur ? 'active-vm' : ''}">
                <div class="topo-vm-h">
                  <i class="dot ${running ? 'ok' : 'bad'}"></i>
                  <b style="font-size:15px">${esc(vm.name)}</b>
                  <span class="badge ${vm.isHub ? 'acc' : ''}">${vm.isHub ? 'Hub · NAT Gateway' : 'Managed Private VM'}</span>
                  <span class="badge ${running ? 'ok' : 'warn'}">${esc(vm.powerState)}</span>
                  <span style="flex:1"></span>
                  <span class="mono dim" style="font-size:12px">${esc(vm.vmSize)}</span>
                </div>
                <div class="row" style="font-size:12.5px;gap:12px">
                  <span class="mono">Private: <b>${esc(vm.privateIp || '—')}</b></span>
                  <span class="mono">Public: ${vm.publicIp ? `<b>${esc(vm.publicIp)}</b>` : '<span class="dim">None</span>'}</span>
                  ${vm.fqdn ? `<span class="mono">DNS: <a href="https://${esc(vm.fqdn)}${location.port ? ':' + esc(location.port) : ''}" target="_blank" rel="noopener"><b>${esc(vm.fqdn)}</b></a></span>` : ''}
                  ${vm.nsg ? `<span class="mono dim">NSG: ${esc(vm.nsg)}</span>` : ''}
                  ${vm.enableIPForwarding ? '<span class="badge ok">IP Forwarding</span>' : ''}
                </div>
                ${nd && nd.online && st ? `<div class="topo-vm-metrics">
                  <div class="topo-m"><span>CPU</span><b>${st.cpu.toFixed(1)}%</b></div>
                  <div class="topo-m"><span>RAM</span><b>${st.mem.toFixed(1)}%</b></div>
                  <div class="topo-m"><span>Uptime${nd.latencyMs != null && !nd.local ? ' · RTT' : ''}</span><b>${fmtDur(st.uptime)}${nd.latencyMs != null && !nd.local ? ' · ' + nd.latencyMs + 'ms' : ''}</b></div>
                </div>` : `<div class="dim" style="font-size:12.5px">${nd ? (nd.error ? 'Agent offline: ' + esc(nd.error) : 'Waiting for agent…') : 'No TNS Agent linked to this VM'}</div>`}
                <div class="row" style="margin-top:2px">
                  ${nd ? `<button class="btn sm primary" data-manage-node="${esc(nd.id)}">${ico('dash', 'sm')}Manage VM</button>
                    <button class="btn sm" data-term-node="${esc(nd.id)}">${ico('term', 'sm')}Terminal</button>` : ''}
                  <span style="flex:1"></span>
                  ${running
                    ? `<button class="btn sm" data-vm-op="restart" data-vm="${esc(vm.name)}" title="Restart VM">${ico('restart', 'sm')}Restart</button>
                       <button class="btn sm danger" data-vm-op="deallocate" data-vm="${esc(vm.name)}" data-hub="${vm.isHub ? 1 : 0}" title="Stop & deallocate VM">${ico('power', 'sm')}Stop</button>`
                    : `<button class="btn sm primary" data-vm-op="start" data-vm="${esc(vm.name)}">${ico('play', 'sm')}Start</button>`}
                </div>
              </div>`;
            }).join('') || '<div class="empty" style="padding:14px">No VMs in this subnet</div>'}
          </div>`;
        }).join('');

        return `<div class="topo-vnet">
          <div class="topo-vnet-h">
            ${ico('cloud')}
            <b style="font-size:16px">Virtual Network: ${esc(vn.name)}</b>
            <span class="badge acc mono">${esc((vn.addressPrefixes || []).join(', '))}</span>
            <span class="dim">${esc(vn.location)}</span>
          </div>
          ${flowHTML}
          <div class="topo-subnets">${subnetsHTML}</div>
        </div>`;
      }).join('') || '<div class="empty">No Virtual Networks found in this Resource Group.</div>';

      el.innerHTML = vnetHTML + renderGatewayHTML(gw);
      bindGatewayEvents(el);

      el.onclick = async e => {
        const mn = e.target.closest('[data-manage-node]');
        if (mn) { switchNode(mn.dataset.manageNode, 'dashboard'); return; }
        const tn = e.target.closest('[data-term-node]');
        if (tn) {
          switchNode(tn.dataset.termNode);
          Term.open({ node: tn.dataset.termNode });
          return;
        }
        const vo = e.target.closest('[data-vm-op]');
        if (vo) {
          const { vm, vmOp, hub } = vo.dataset;
          const warn = hub === '1' ? '\n\n⚠️ WARNING: This is the Hub VM hosting this console!' : '';
          if (!await confirmBox(`Execute Azure <b>${esc(vmOp.toUpperCase())}</b> on virtual machine <b class="mono">${esc(vm)}</b>?${warn}`, { ok: vmOp.toUpperCase(), danger: vmOp !== 'start' })) return;
          vo.disabled = true;
          try {
            await api('/api/azure/vm/action', { body: { vm, op: vmOp } });
            toast(`Sent ${vmOp} command to ${vm} (Azure is processing)`);
            setTimeout(() => load(), 3500);
          } catch (ex) { toastErr(ex); vo.disabled = false; }
        }
      };
    };

    const renderNSGs = el => {
      const nsgs = data.nsgs || [];
      if (!nsgs.length) { el.innerHTML = '<div class="empty">No NSGs found in this Resource Group.</div>'; return; }
      const cur = nsgs.find(g => g.name === selectedNSG) || nsgs[0];
      selectedNSG = cur.name;

      el.innerHTML = `
        <div class="row" style="margin-bottom:16px">
          <span class="card-sub" style="font-weight:700">Select NSG:</span>
          <div class="seg" id="nsgPick">${nsgs.map(g => `<button data-nsg="${esc(g.name)}" class="${g.name === cur.name ? 'on' : ''}">${esc(g.name)} (${g.rules.length})</button>`).join('')}</div>
        </div>
        <div class="grid g-main">
          <div class="grid" style="align-content:start">
            <div class="card">
              <div class="card-h"><span class="card-t">Custom Rules · ${esc(cur.name)}</span><span class="sp"></span>
                <span class="card-sub mono">NIC: ${esc(cur.attachedNics.join(', ') || '—')}${cur.attachedSubnets.length ? ' · Subnet: ' + esc(cur.attachedSubnets.join(', ')) : ''}</span></div>
              <div class="tbl-wrap"><table class="tbl">
                <thead><tr><th class="num">Priority</th><th>Name</th><th>Direction</th><th>Action</th><th>Protocol</th><th>Source</th><th>Dest Port</th><th></th></tr></thead>
                <tbody id="nsgRulesBody">${cur.rules.map(r => `<tr>
                  <td class="num mono">${r.priority}</td>
                  <td><b>${esc(r.name)}</b>${r.description ? `<div class="dim" style="font-size:12px">${esc(r.description)}</div>` : ''}</td>
                  <td><span class="badge ${r.direction === 'Inbound' ? 'acc' : ''}">${esc(r.direction)}</span></td>
                  <td><span class="badge ${r.access === 'Allow' ? 'ok' : 'bad'}">${esc(r.access)}</span></td>
                  <td class="mono">${esc(r.protocol)}</td>
                  <td class="mono">${esc(r.sourceAddressPrefix)}</td>
                  <td class="mono">${esc(r.destinationPortRange)}</td>
                  <td><div class="acts"><button class="btn sm icon danger" data-del-rule="${esc(r.name)}" title="Delete rule">${ico('trash', 'sm')}</button></div></td>
                </tr>`).join('') || '<tr><td colspan="8" class="empty">No custom rules yet</td></tr>'}</tbody>
              </table></div>
            </div>
            <div class="card">
              <div class="card-h"><span class="card-t">Azure Default Security Rules</span><span class="card-sub">Read-only</span></div>
              <div class="tbl-wrap"><table class="tbl">
                <thead><tr><th class="num">Priority</th><th>Name</th><th>Direction</th><th>Action</th><th>Source → Destination</th><th>Port</th></tr></thead>
                <tbody>${(cur.defaultRules || []).map(r => `<tr>
                  <td class="num mono dim">${r.priority}</td>
                  <td class="dim">${esc(r.name)}</td>
                  <td><span class="badge">${esc(r.direction)}</span></td>
                  <td><span class="badge ${r.access === 'Allow' ? 'ok' : 'bad'}">${esc(r.access)}</span></td>
                  <td class="mono dim">${esc(r.sourceAddressPrefix)} → ${esc(r.destinationAddressPrefix)}</td>
                  <td class="mono dim">${esc(r.destinationPortRange)}</td>
                </tr>`).join('')}</tbody>
              </table></div>
            </div>
          </div>
          <div class="card" style="align-self:start">
            <div class="card-h"><span class="card-t">Add NSG Rule</span></div>
            <form class="form" id="nsgForm">
              <div class="row" style="gap:6px" id="nsgPresets">
                ${[['SSH', 'allow-ssh', '22', 'Tcp', '*'], ['HTTP', 'allow-http', '80', 'Tcp', '*'], ['HTTPS', 'allow-https', '443', 'Tcp', '*'], ['Panel 1611', 'allow-tns-1611', '1611', 'Tcp', '*'], ['Private Subnet', 'allow-private-subnet', '*', '*', '10.0.1.0/24']].map(([l, n, p, pr, s]) =>
                  `<button type="button" class="btn sm" data-p="${n}|${p}|${pr}|${s}">${l}</button>`).join('')}
              </div>
              <label class="field"><span>Rule Name</span><input name="name" class="mono" placeholder="allow-https" required></label>
              <div class="grid g2" style="gap:10px">
                <label class="field"><span>Priority (100–4096)</span><input name="priority" type="number" class="mono" value="${nextPriority(cur.rules)}" min="100" max="4096" required></label>
                <label class="field"><span>Direction</span><select name="direction"><option value="Inbound">Inbound</option><option value="Outbound">Outbound</option></select></label>
                <label class="field"><span>Action</span><select name="access"><option value="Allow">Allow</option><option value="Deny">Deny</option></select></label>
                <label class="field"><span>Protocol</span><select name="protocol"><option value="Tcp">TCP</option><option value="Udp">UDP</option><option value="Icmp">ICMP</option><option value="*">Any (*)</option></select></label>
                <label class="field"><span>Source (IP/CIDR/*)</span><input name="src" class="mono" value="*" placeholder="* or 10.0.1.0/24"></label>
                <label class="field"><span>Dest Port (* or 80, 8000-8100)</span><input name="dport" class="mono" value="443" placeholder="*"></label>
              </div>
              <label class="field"><span>Destination (IP/CIDR/*)</span><input name="dst" class="mono" value="*"></label>
              <label class="field"><span>Description</span><input name="desc" maxlength="140" placeholder="optional"></label>
              <button class="btn primary" type="submit">${ico('plus', 'sm')}Save Rule to Azure</button>
            </form>
          </div>
        </div>`;

      $('#nsgPick', el).onclick = e => { const b = e.target.closest('[data-nsg]'); if (b) { selectedNSG = b.dataset.nsg; renderNSGs(el); } };
      $('#nsgPresets', el).onclick = e => {
        const b = e.target.closest('[data-p]'); if (!b) return;
        const [n, p, pr, s] = b.dataset.p.split('|');
        const f = $('#nsgForm', el);
        f.name.value = n; f.dport.value = p; f.protocol.value = pr; f.src.value = s;
      };
      $('#nsgForm', el).onsubmit = async e => {
        e.preventDefault();
        const f = e.target;
        const btn = $('button[type=submit]', f); btn.disabled = true;
        try {
          await api('/api/azure/nsg/rule', {
            body: {
              op: 'upsert', nsg: cur.name, name: f.name.value.trim(), priority: +f.priority.value,
              direction: f.direction.value, access: f.access.value, protocol: f.protocol.value,
              sourceAddressPrefix: f.src.value.trim() || '*', destinationAddressPrefix: f.dst.value.trim() || '*',
              destinationPortRange: f.dport.value.trim() || '*', description: f.desc.value.trim(),
            },
          });
          toast('Saved NSG rule to Azure');
          load();
        } catch (ex) { toastErr(ex); btn.disabled = false; }
      };
      $('#nsgRulesBody', el).onclick = async e => {
        const b = e.target.closest('[data-del-rule]'); if (!b) return;
        const rname = b.dataset.delRule;
        if (!await confirmBox(`Delete NSG rule <b class="mono">${esc(rname)}</b> from <b>${esc(cur.name)}</b>?`, { ok: 'Delete', danger: true })) return;
        b.disabled = true;
        try { await api('/api/azure/nsg/rule', { body: { op: 'delete', nsg: cur.name, name: rname } }); toast('Deleted rule ' + rname); load(); }
        catch (ex) { toastErr(ex); b.disabled = false; }
      };
    };

    const nextPriority = rules => {
      const used = new Set((rules || []).map(r => r.priority));
      for (let p = 300; p <= 4000; p += 10) if (!used.has(p)) return p;
      return 500;
    };

    const renderVNets = el => {
      const vnets = data.vnets || [];
      const nsgs = data.nsgs || [];
      const rts = data.routeTables || [];
      el.innerHTML = vnets.map(vn => `<div class="card" style="margin-bottom:20px">
        <div class="card-h"><span class="card-t">VNet · ${esc(vn.name)}</span>
          <span class="badge acc mono">${esc((vn.addressPrefixes || []).join(', '))}</span><span class="sp"></span>
          <button class="btn sm primary" data-add-sn="${esc(vn.name)}">${ico('plus', 'sm')}Add Subnet</button></div>
        <div class="tbl-wrap"><table class="tbl">
          <thead><tr><th>Subnet</th><th>CIDR</th><th>Attached NSG</th><th>Route Table (UDR)</th><th>Connected NICs</th><th></th></tr></thead>
          <tbody>${(vn.subnets || []).map(sn => `<tr>
            <td><b class="mono">${esc(sn.name)}</b></td>
            <td class="mono">${esc(sn.addressPrefix)}</td>
            <td>${sn.nsg ? `<span class="badge">${esc(sn.nsg)}</span>` : '<span class="dim">—</span>'}</td>
            <td>${sn.routeTable ? `<span class="badge warn">${esc(sn.routeTable)}</span>` : '<span class="dim">—</span>'}</td>
            <td class="mono dim">${esc((sn.connectedNics || []).join(', ') || '—')}</td>
            <td><div class="acts">
              <button class="btn sm" data-edit-sn="${esc(vn.name)}|${esc(sn.name)}|${esc(sn.addressPrefix)}|${esc(sn.nsg || '')}|${esc(sn.routeTable || '')}">${ico('edit', 'sm')}Edit</button>
              <button class="btn sm icon danger" data-del-sn="${esc(vn.name)}|${esc(sn.name)}" title="Delete subnet">${ico('trash', 'sm')}</button>
            </div></td>
          </tr>`).join('')}</tbody>
        </table></div>
      </div>`).join('') || '<div class="empty">No Virtual Networks found</div>';

      const openSubnetModal = (vnet, existing) => {
        const m = openModal({
          title: existing ? `Edit Subnet ${existing.name}` : `Add Subnet to ${vnet}`,
          body: `<form class="form" id="snForm">
            <label class="field"><span>Subnet Name</span><input name="name" class="mono" value="${esc(existing ? existing.name : '')}" ${existing ? 'readonly' : 'required'} placeholder="app-subnet"></label>
            <label class="field"><span>Address Prefix (CIDR)</span><input name="cidr" class="mono" value="${esc(existing ? existing.cidr : '10.0.2.0/24')}" required></label>
            <label class="field"><span>Network Security Group (optional)</span><select name="nsg"><option value="">— None —</option>${nsgs.map(g => `<option value="${esc(g.name)}" ${existing && existing.nsg === g.name ? 'selected' : ''}>${esc(g.name)}</option>`).join('')}</select></label>
            <label class="field"><span>Route Table (optional)</span><select name="rt"><option value="">— None —</option>${rts.map(r => `<option value="${esc(r.name)}" ${existing && existing.rt === r.name ? 'selected' : ''}>${esc(r.name)}</option>`).join('')}</select></label>
          </form>`,
          foot: `<button class="btn" data-close>Cancel</button><button class="btn primary" id="snSave">Save to Azure</button>`,
        });
        $('#snSave', m).onclick = async () => {
          const f = $('#snForm', m);
          $('#snSave', m).disabled = true;
          try {
            await api('/api/azure/subnet', { body: { op: 'upsert', vnet, name: f.name.value.trim(), addressPrefix: f.cidr.value.trim(), nsg: f.nsg.value, routeTable: f.rt.value } });
            toast('Updated subnet'); m.close(); load();
          } catch (ex) { toastErr(ex); $('#snSave', m).disabled = false; }
        };
      };

      el.onclick = async e => {
        const add = e.target.closest('[data-add-sn]');
        if (add) return openSubnetModal(add.dataset.addSn, null);
        const ed = e.target.closest('[data-edit-sn]');
        if (ed) {
          const [vnet, name, cidr, nsg, rt] = ed.dataset.editSn.split('|');
          return openSubnetModal(vnet, { name, cidr, nsg, rt });
        }
        const del = e.target.closest('[data-del-sn]');
        if (del) {
          const [vnet, name] = del.dataset.delSn.split('|');
          if (!await confirmBox(`Delete subnet <b class="mono">${esc(name)}</b> from VNet <b>${esc(vnet)}</b>?`, { ok: 'Delete', danger: true })) return;
          try { await api('/api/azure/subnet', { body: { op: 'delete', vnet, name } }); toast('Deleted subnet'); load(); } catch (ex) { toastErr(ex); }
        }
      };
    };

    const renderRoutes = el => {
      const rts = data.routeTables || [];
      if (!rts.length) { el.innerHTML = '<div class="empty">No Route Tables found in this Resource Group.</div>'; return; }
      el.innerHTML = rts.map(rt => `<div class="grid g-main" style="margin-bottom:20px">
        <div class="card">
          <div class="card-h"><span class="card-t">${ico('route', 'sm')} ${esc(rt.name)}</span><span class="sp"></span>
            <span class="card-sub mono">Subnets: ${esc((rt.subnets || []).join(', ') || 'Unassociated')}</span></div>
          <div class="tbl-wrap"><table class="tbl">
            <thead><tr><th>Route Name</th><th>Address Prefix</th><th>Next Hop Type</th><th>Next Hop IP</th><th></th></tr></thead>
            <tbody>${(rt.routes || []).map(ro => `<tr>
              <td><b class="mono">${esc(ro.name)}</b></td>
              <td class="mono">${esc(ro.addressPrefix)}</td>
              <td><span class="badge acc">${esc(ro.nextHopType)}</span></td>
              <td class="mono">${esc(ro.nextHopIpAddress || '—')}</td>
              <td><div class="acts"><button class="btn sm icon danger" data-del-ro="${esc(rt.name)}|${esc(ro.name)}" title="Delete route">${ico('trash', 'sm')}</button></div></td>
            </tr>`).join('') || '<tr><td colspan="5" class="empty">No routes configured</td></tr>'}</tbody>
          </table></div>
        </div>
        <div class="card" style="align-self:start">
          <div class="card-h"><span class="card-t">Add Route · ${esc(rt.name)}</span></div>
          <form class="form" data-ro-form="${esc(rt.name)}">
            <label class="field"><span>Route Name</span><input name="name" class="mono" placeholder="route-to-vm1" required></label>
            <label class="field"><span>Address Prefix (CIDR)</span><input name="prefix" class="mono" placeholder="0.0.0.0/0 or 192.168.0.0/16" required></label>
            <label class="field"><span>Next Hop Type</span><select name="hop">
              <option value="VirtualAppliance">VirtualAppliance (Via Gateway VM)</option>
              <option value="VnetLocal">VnetLocal</option>
              <option value="Internet">Internet</option>
              <option value="None">None (Drop)</option>
            </select></label>
            <label class="field"><span>Next Hop IP (for VirtualAppliance)</span><input name="ip" class="mono" value="10.0.0.4" placeholder="10.0.0.4"></label>
            <button class="btn primary" type="submit">${ico('plus', 'sm')}Save Route</button>
          </form>
        </div>
      </div>`).join('');

      el.querySelectorAll('[data-ro-form]').forEach(f => {
        f.onsubmit = async e => {
          e.preventDefault();
          const rt = f.dataset.roForm;
          const btn = $('button[type=submit]', f); btn.disabled = true;
          try {
            await api('/api/azure/route', { body: { op: 'upsert', routeTable: rt, name: f.name.value.trim(), addressPrefix: f.prefix.value.trim(), nextHopType: f.hop.value, nextHopIpAddress: f.ip.value.trim() } });
            toast('Saved route to Azure'); load();
          } catch (ex) { toastErr(ex); btn.disabled = false; }
        };
      });
      el.onclick = async e => {
        const d = e.target.closest('[data-del-ro]'); if (!d) return;
        const [rt, name] = d.dataset.delRo.split('|');
        if (!await confirmBox(`Delete route <b class="mono">${esc(name)}</b> from <b>${esc(rt)}</b>?`, { ok: 'Delete', danger: true })) return;
        try { await api('/api/azure/route', { body: { op: 'delete', routeTable: rt, name } }); toast('Deleted route'); load(); } catch (ex) { toastErr(ex); }
      };
    };

    const renderNICs = el => {
      const nics = data.nics || [];
      const pips = data.publicIps || [];
      el.innerHTML = `
        <div class="card">
          <div class="card-h"><span class="card-t">Network Interfaces (NICs)</span><span class="card-sub">${nics.length} interfaces</span></div>
          <div class="tbl-wrap"><table class="tbl">
            <thead><tr><th>NIC Name</th><th>VM</th><th>Subnet</th><th>Private IP</th><th>Public IP</th><th>NSG</th><th>MAC</th><th>IP Forwarding</th></tr></thead>
            <tbody id="nicBody">${nics.map(n => `<tr>
              <td class="mono"><b>${esc(n.name)}</b></td>
              <td>${esc(n.vm || '—')}</td>
              <td class="mono">${esc(n.subnet || '—')}</td>
              <td class="mono"><b>${esc(n.privateIp || '—')}</b></td>
              <td class="mono">${n.publicIp ? esc(n.publicIp) : '<span class="dim">None</span>'}</td>
              <td>${n.nsg ? `<span class="badge">${esc(n.nsg)}</span>` : '—'}</td>
              <td class="mono dim">${esc(n.mac || '—')}</td>
              <td><label class="switch" title="Toggle Azure NIC IP Forwarding"><input type="checkbox" data-nic-fwd="${esc(n.name)}" ${n.enableIPForwarding ? 'checked' : ''}><i></i></label></td>
            </tr>`).join('') || '<tr><td colspan="8" class="empty">No NICs found</td></tr>'}</tbody>
          </table></div>
        </div>
        <div class="card mt">
          <div class="card-h"><span class="card-t">Public IP Addresses</span><span class="card-sub">${pips.length} addresses</span></div>
          <div class="tbl-wrap"><table class="tbl">
            <thead><tr><th>Name</th><th>IP Address</th><th>DNS / FQDN</th><th>SKU</th><th>Allocation</th><th>Associated NIC</th></tr></thead>
            <tbody>${pips.map(p => `<tr>
              <td class="mono"><b>${esc(p.name)}</b></td>
              <td class="mono" style="color:var(--accent)"><b>${esc(p.ip || '—')}</b></td>
              <td class="mono">${p.fqdn ? `<a href="https://${esc(p.fqdn)}${location.port ? ':' + esc(location.port) : ''}" target="_blank" rel="noopener">${esc(p.fqdn)}</a>` : '<span class="dim">—</span>'}</td>
              <td><span class="badge">${esc(p.sku || '—')}</span></td>
              <td>${esc(p.allocation || '—')}</td>
              <td class="mono dim">${esc(p.nic || '—')}</td>
            </tr>`).join('') || '<tr><td colspan="6" class="empty">No Public IPs found</td></tr>'}</tbody>
          </table></div>
        </div>`;

      $('#nicBody', el).onchange = async e => {
        const cb = e.target.closest('[data-nic-fwd]'); if (!cb) return;
        const nic = cb.dataset.nicFwd, on = cb.checked;
        cb.disabled = true;
        try {
          await api('/api/azure/nic/forwarding', { body: { nic, enabled: on } });
          toast(`${on ? 'Enabled' : 'Disabled'} IP Forwarding on ${nic}`);
        } catch (ex) { cb.checked = !on; toastErr(ex); }
        cb.disabled = false;
      };
    };

    const renderResources = el => {
      const resources = data.resources || [];
      const nodes = S.nodes || [];
      el.innerHTML = `
        <div class="grid g-main">
          <div class="card">
            <div class="card-h"><span class="card-t">Managed Nodes (Cluster)</span><span class="card-sub">${nodes.length} servers</span></div>
            <div class="tbl-wrap"><table class="tbl">
              <thead><tr><th>Status</th><th>Name</th><th>Role</th><th>Private IP / Subnet</th><th>Arch</th><th></th></tr></thead>
              <tbody id="nodesTbl">${nodes.map(n => `<tr>
                <td><i class="dot ${n.online ? 'ok' : 'bad'}"></i> ${n.online ? 'Online' : 'Offline'}</td>
                <td><b>${esc(n.name)}</b><div class="mono dim" style="font-size:11.5px">${esc(n.endpoint || 'local hub')}</div></td>
                <td><span class="badge ${n.local ? 'acc' : ''}">${esc(n.role || (n.local ? 'Hub' : 'Private'))}</span></td>
                <td class="mono">${esc(n.privateIp || '—')}${n.subnet ? `<div class="dim" style="font-size:11.5px">${esc(n.subnet)}</div>` : ''}</td>
                <td class="mono dim">${esc(n.arch || '—')}</td>
                <td><div class="acts">
                  <button class="btn sm" data-sw-node="${esc(n.id)}">Select</button>
                  ${n.local ? '' : `<button class="btn sm icon danger" data-del-node="${esc(n.id)}" title="Remove node">${ico('trash', 'sm')}</button>`}
                </div></td>
              </tr>`).join('')}</tbody>
            </table></div>
          </div>
          <div class="card" style="align-self:start">
            <div class="card-h"><span class="card-t">Connect Private VM</span></div>
            <form class="form" id="addNodeForm">
              <div class="grid g2" style="gap:10px">
                <label class="field"><span>ID</span><input name="id" class="mono" placeholder="tns-lab-vm3" required></label>
                <label class="field"><span>Display Name</span><input name="name" placeholder="tns-lab-vm3"></label>
              </div>
              <label class="field"><span>Agent Endpoint (VNet internal)</span><input name="endpoint" class="mono" placeholder="https://10.0.1.5:1611" required></label>
              <div class="grid g2" style="gap:10px">
                <label class="field"><span>Subnet</span><input name="subnet" class="mono" placeholder="private (10.0.1.0/24)"></label>
                <label class="field"><span>Peer Key</span><input name="peerKey" type="password" class="mono" placeholder="agent-key" required></label>
              </div>
              <button class="btn primary" type="submit">${ico('plus', 'sm')}Add Server</button>
            </form>
          </div>
        </div>
        <div class="card mt">
          <div class="card-h"><span class="card-t">All Resources in Resource Group ${esc(data.resourceGroup || '')}</span><span class="card-sub">${resources.length} resources</span></div>
          <div class="tbl-wrap"><table class="tbl">
            <thead><tr><th>Resource Name</th><th>Azure Resource Type</th><th>Region</th></tr></thead>
            <tbody>${resources.map(r => `<tr>
              <td class="mono"><b>${esc(r.name)}</b></td>
              <td class="mono dim">${esc(r.type)}</td>
              <td>${esc(r.location)}</td>
            </tr>`).join('') || '<tr><td colspan="3" class="empty">No data</td></tr>'}</tbody>
          </table></div>
        </div>`;

      $('#nodesTbl', el).onclick = async e => {
        const sw = e.target.closest('[data-sw-node]');
        if (sw) { switchNode(sw.dataset.swNode, 'dashboard'); return; }
        const del = e.target.closest('[data-del-node]');
        if (del) {
          const id = del.dataset.delNode;
          if (!await confirmBox(`Remove server <b class="mono">${esc(id)}</b> from managed nodes?`, { ok: 'Remove', danger: true })) return;
          try { await api('/api/nodes/delete', { body: { id } }); toast('Removed ' + id); load(); } catch (ex) { toastErr(ex); }
        }
      };
      $('#addNodeForm', el).onsubmit = async e => {
        e.preventDefault();
        const f = e.target;
        try {
          const r = await api('/api/nodes', {
            body: { id: f.id.value.trim(), name: f.name.value.trim() || f.id.value.trim(), endpoint: f.endpoint.value.trim(), subnet: f.subnet.value.trim(), peerKey: f.peerKey.value.trim(), role: 'Private VM' },
          });
          toast(r.status && r.status.online ? 'Connected node successfully' : 'Saved node (currently offline)');
          f.reset(); load();
        } catch (ex) { toastErr(ex); }
      };
    };

    setTab(tab);
    every(20000, () => load(false));
  },
};
