import { html } from '../lib/html.js';
import { usePanel } from '../lib/panel.js';
import { useResource } from '../lib/hooks.js';
import { Page, Notice, Loading, Empty, KV, CopyButton, Table } from '../lib/ui.js';
import { useLive, StateBadge, SourceNotice, number } from '../obd.js';

export default function Vehicle() {
  const { api } = usePanel();
  const { snap, error } = useLive();
  const supported = snap?.supported.join(',');
  const pids = useResource(signal => api.get('pids', { signal }), [supported]);
  if (!snap) return html`<${Page} title="Vehicle">${error ? html`<${Notice} error=${error} />` : html`<${Loading} />`}<//>`;
  const { status, vehicle: v, adapter: a } = snap;
  const list = (pids.data || []).filter(p => p.supported);
  return html`<${Page} title="Vehicle" subtitle=${v.vin || null} actions=${html`<${StateBadge} status=${status} />`}>
    <${Notice} error=${error || pids.error} />
    <${SourceNotice} status=${status} />
    <div class="grid grid-auto grid-pair">
      <div class="card">
        <h2>Identification</h2>
        ${v.vin || v.ecus?.length ? html`<${KV} items=${[
          ['VIN', v.vin ? html`<code>${v.vin}</code> <${CopyButton} text=${v.vin} label="Copy" />` : null],
          ['ECU name', v.ecu_name],
          ['ECU addresses', v.ecus?.join(', ')],
          ['Calibration IDs', v.calibration_ids?.join(', ')],
          ['Calibration checksums', v.cvns?.join(', ')],
        ]} />` : html`<${Empty}>The vehicle has not provided Mode 09 identification.<//>`}
      </div>
      <div class="card">
        <h2>Adapter</h2>
        <${KV} items=${[
          ['Address', html`<code>${status.adapter}</code>`],
          ['Version', a.version],
          ['Identifier', a.identifier],
          ['Protocol', a.protocol],
          ['Connector voltage', a.voltage == null ? null : `${number(a.voltage, 'V')} V`],
          ['Polls', `${status.polls.toLocaleString()}${status.errors ? ` (${status.errors.toLocaleString()} failed reads)` : ''}`],
        ]} />
      </div>
    </div>
    <div class="section">
      <h2 class="section-title">Supported PIDs <span class="muted section-hint">${list.length} advertised by the vehicle</span></h2>
      ${list.length ? html`<${Table}>
        <thead><tr><th>PID</th><th>Name</th><th>Unit</th><th>Group</th></tr></thead>
        <tbody>${list.map(p => html`<tr key=${p.pid}>
          <td class="mono">${p.pid}</td><td>${p.name}</td><td>${p.unit || '—'}</td><td class="muted">${p.group || 'Not decoded'}</td>
        </tr>`)}</tbody>
      <//>` : html`<div class="card"><${Empty}>Nothing yet.<//></div>`}
    </div>
  <//>`;
}
