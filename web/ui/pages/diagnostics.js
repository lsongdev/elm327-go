import { html, useCallback } from '../lib/html.js';
import { usePanel } from '../lib/panel.js';
import { useAction, useResource } from '../lib/hooks.js';
import { Page, Notice, Loading, Empty, Badge, Table, ConfirmButton } from '../lib/ui.js';
import { useLive, StateBadge, SourceNotice, display, age } from '../obd.js';

export default function Diagnostics() {
  const { api } = usePanel();
  const { snap, error } = useLive();
  const refresh = useAction();
  const frame = useResource(signal => api.get('freeze-frame', { signal }), []);
  const load = useCallback(() => refresh.run(async () => {
    await api.get('dtc', { query: { refresh: 1 } });
    frame.reload();
  }), []);
  if (!snap) return html`<${Page} title="Diagnostics">${error ? html`<${Notice} error=${error} />` : html`<${Loading} />`}<//>`;
  const { status, diagnostics: d } = snap;
  const clear = async () => {
    await api.post('dtc/clear', { confirm: true });
    frame.reload();
  };
  return html`<${Page} title="Diagnostics"
    subtitle=${`Read ${age(d.updated)}`}
    actions=${html`<div class="button-row">
      <button class="button" type="button" disabled=${refresh.busy} onClick=${load}>${refresh.busy ? 'Reading…' : 'Read now'}</button>
      ${status.clear_enabled ? html`<${ConfirmButton} label="Clear codes…" title="Clear trouble codes?" confirmLabel="Clear codes"
        description="This erases stored and pending codes, freeze frames and readiness monitors. The cause is not fixed by clearing, and the car will need a full drive cycle before it can pass an emissions test. Do this with the engine off."
        onConfirm=${clear} />` : null}
      <${StateBadge} status=${status} />
    </div>`}>
    <${Notice} error=${error || refresh.error} />
    <${SourceNotice} status=${status} />
    <${Codes} title="Stored" hint="Confirmed faults; these turn on the check engine light." codes=${d.stored} none="No stored codes." />
    <${Codes} title="Pending" hint="Seen in the current or last drive cycle, not yet confirmed." codes=${d.pending} none="No pending codes." />
    <${Codes} title="Permanent" hint="Cleared only by the ECU itself, after it verifies the repair." codes=${d.permanent} none="No permanent codes." />
    <div class="grid grid-auto grid-pair section">
      <${Readiness} monitor=${d.monitor} />
      <${FreezeFrame} frame=${frame} />
    </div>
    ${status.clear_enabled ? null : html`<p class="muted section-sm">Clearing codes is disabled. Start the server with <code>-allow-clear</code> to enable it.</p>`}
  <//>`;
}

function Codes({ title, hint, codes, none }) {
  return html`<div class="section">
    <h2 class="section-title">${title} <span class="muted section-hint">${hint}</span></h2>
    ${codes.length ? html`<${Table}>
      <thead><tr><th>Code</th><th>Description</th><th>System</th><th>ECU</th></tr></thead>
      <tbody>${codes.map(c => html`<tr key=${`${c.code}${c.ecu}`}>
        <td class="mono dtc">${c.code}</td>
        <td>${c.description || html`<span class="muted">${c.generic ? 'Not in the code table' : 'Manufacturer-specific code'}</span>`}</td>
        <td>${c.system}</td>
        <td class="mono muted">${c.ecu || '—'}</td>
      </tr>`)}</tbody>
    <//>` : html`<div class="card"><${Empty}>${none}<//></div>`}
  </div>`;
}

function Readiness({ monitor }) {
  if (!monitor) return html`<div class="card"><h2>Readiness monitors</h2><${Empty}>Not read yet.<//></div>`;
  const available = monitor.monitors.filter(m => m.available);
  const missing = monitor.monitors.filter(m => !m.available);
  return html`<div class="card">
    <div class="card-head"><h2>Readiness monitors</h2><span class="muted">${monitor.ignition} ignition</span></div>
    <${Table} framed=${false}>
      <tbody>${available.map(m => html`<tr key=${m.key}>
        <td>${m.name}</td>
        <td class="num">${m.complete ? html`<${Badge} tone="green" dot>Complete<//>` : html`<${Badge} tone="amber" dot>Incomplete<//>`}</td>
      </tr>`)}</tbody>
    <//>
    ${missing.length ? html`<p class="muted section-sm">Not on this vehicle: ${missing.map(m => m.name).join(', ')}.</p>` : null}
  </div>`;
}

function FreezeFrame({ frame }) {
  const { data, error, loading } = frame;
  return html`<div class="card">
    <div class="card-head"><h2>Freeze frame</h2>${data ? html`<span class="mono dtc">${data.dtc.code}</span>` : null}</div>
    <${Notice} error=${error} />
    ${data ? html`
      <p class="muted">The engine's state when this code was set.</p>
      <${Table} framed=${false}>
        <tbody>${data.readings.map(r => {
          const { value, unit } = display(r);
          return html`<tr key=${r.pid}><td>${r.name}</td><td class="num sensor-value">${value}${unit ? ` ${unit}` : ''}</td></tr>`;
        })}</tbody>
      <//>` : loading ? html`<${Loading} />` : error ? null : html`<${Empty}>No freeze frame stored.<//>`}
  </div>`;
}
