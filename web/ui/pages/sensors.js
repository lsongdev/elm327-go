import { html } from '../lib/html.js';
import { Page, Notice, Loading, Empty, Table } from '../lib/ui.js';
import { useLive, StateBadge, SourceNotice, GROUPS, display, number, age } from '../obd.js';

export default function Sensors({ query, setQuery }) {
  const { snap, error } = useLive();
  if (!snap) return html`<${Page} title="Sensors">${error ? html`<${Notice} error=${error} />` : html`<${Loading} />`}<//>`;
  const search = (query.q || '').toLowerCase();
  const group = query.group || '';
  const matches = m => (!group || m.group === group)
    && (!search || m.name.toLowerCase().includes(search) || m.pid.toLowerCase() === search);
  const shown = snap.metrics.filter(matches);
  const sections = GROUPS.map(([id, title]) => [title, shown.filter(m => m.group === id)]).filter(([, list]) => list.length);
  const other = shown.filter(m => !m.group);
  if (other.length) sections.push(['Not decoded', other]);
  return html`<${Page} title="Sensors"
    subtitle=${`${snap.metrics.length} values from ${snap.supported.length} advertised PIDs`}
    actions=${html`<${StateBadge} status=${snap.status} />`}>
    <${Notice} error=${error} />
    <${SourceNotice} status=${snap.status} />
    <div class="toolbar">
      <input class="input" type="search" placeholder="Filter by name or PID" aria-label="Filter sensors"
        value=${query.q || ''} onInput=${e => setQuery({ q: e.currentTarget.value })} />
      <select class="input" aria-label="Group" value=${group} onChange=${e => setQuery({ group: e.currentTarget.value })}>
        <option value="">All groups</option>
        ${GROUPS.map(([id, title]) => html`<option key=${id} value=${id}>${title}</option>`)}
      </select>
    </div>
    ${sections.length ? sections.map(([title, list], i) => html`<div key=${title} class=${i ? 'section' : ''}>
      <h2 class="section-title">${title}</h2>
      <${Table}>
        <thead><tr><th>PID</th><th>Sensor</th><th class="num">Value</th><th>Raw</th><th class="num">Updated</th></tr></thead>
        <tbody>${list.map(m => html`<${Row} key=${m.pid} metric=${m} />`)}</tbody>
      <//>
    </div>`) : html`<div class="card"><${Empty}>No values match.<//></div>`}
  <//>`;
}

function Row({ metric }) {
  const { value, unit } = display(metric);
  return html`<tr>
    <td class="mono">${metric.pid}</td>
    <td>
      ${metric.name}
      ${metric.fields?.length > 1 ? html`<div class="fields">${metric.fields.map(f => html`<span key=${f.key}>
        ${f.name}: ${f.text || `${number(f.value, f.unit)}${f.unit ? ` ${f.unit}` : ''}`}
      </span>`)}</div>` : null}
    </td>
    <td class="num sensor-value">${metric.value == null && !metric.text ? html`<span class="muted">Not decoded</span>` : `${value}${unit ? ` ${unit}` : ''}`}</td>
    <td class="mono muted">${metric.raw}</td>
    <td class="num muted">${age(metric.updated)}</td>
  </tr>`;
}
