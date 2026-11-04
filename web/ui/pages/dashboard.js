import { html } from '../lib/html.js';
import { Page, Notice, Loading, Empty, Badge } from '../lib/ui.js';
import { Sparkline, Meter } from '../lib/charts.js';
import { useLive, StateBadge, SourceNotice, historyOf, display, number } from '../obd.js';

// The gauges, in reading order, under the short names a dashboard wants. floor is
// the smallest change worth showing as a trend in that gauge's unit.
const GAUGES = [
  ['engine_rpm', 'Engine speed', 100],
  ['vehicle_speed', 'Speed', 5],
  ['engine_load', 'Engine load', 5],
  ['throttle_position', 'Throttle', 5],
  ['coolant_temp', 'Coolant', 5],
  ['oil_temp', 'Oil', 5],
  ['intake_temp', 'Intake air', 5],
  ['control_module_voltage', 'Voltage', 0.5],
  ['fuel_level', 'Fuel level', 2],
  ['fuel_rate', 'Fuel rate', 1],
  ['maf_rate', 'Air flow', 2],
  ['intake_pressure', 'Manifold pressure', 5],
  ['timing_advance', 'Timing advance', 3],
  ['short_fuel_trim_1', 'Short fuel trim', 3],
  ['long_fuel_trim_1', 'Long fuel trim', 3],
  ['run_time', 'Run time', 60],
];

export default function Dashboard({ href }) {
  const { snap, error } = useLive();
  if (!snap) return html`<${Page} title="Dashboard">${error ? html`<${Notice} error=${error} />` : html`<${Loading} />`}<//>`;
  const { status, metrics, diagnostics, vehicle, adapter } = snap;
  const byKey = Object.fromEntries(metrics.map(m => [m.key, m]));
  const gauges = GAUGES.filter(([key]) => byKey[key]);
  return html`<${Page} title="Dashboard" subtitle=${vehicle.vin || adapter.protocol || status.adapter} actions=${html`<${StateBadge} status=${status} />`}>
    <${Notice} error=${error} />
    <${SourceNotice} status=${status} />
    <${Health} diagnostics=${diagnostics} href=${href} />
    ${gauges.length
      ? html`<div class="gauges section">${gauges.map(([key, label, floor]) => html`<${Gauge} key=${key} metric=${byKey[key]} label=${label} floor=${floor} />`)}</div>`
      : html`<div class="card section"><${Empty}>No values yet.<//></div>`}
  <//>`;
}

function Gauge({ metric, label, floor }) {
  const { value, unit } = display(metric);
  return html`<div class="card gauge">
    <span class="stat-label">${label}</span>
    <span class="gauge-value">${value}${unit ? html`<span class="gauge-unit">${unit}</span>` : null}</span>
    <${Meter} value=${metric.value} min=${metric.min} max=${metric.max} label=${label} />
    <${Sparkline} points=${historyOf(metric.pid)} label=${label} floor=${floor}
      format=${v => (metric.key === 'run_time' ? `${Math.round(v)} s` : `${number(v, metric.unit)} ${metric.unit || ''}`.trim())} />
  </div>`;
}

// Health summarizes the fault state. Each state carries a word, not just a colour.
function Health({ diagnostics, href }) {
  const { monitor, stored, pending, permanent } = diagnostics;
  const available = (monitor?.monitors || []).filter(m => m.available);
  const incomplete = available.filter(m => !m.complete);
  return html`<div class="grid grid-auto-fill">
    <a class="card stat stat-link" href=${href('/diagnostics')}>
      <span class="stat-label">Check engine light</span>
      <span class="stat-value">${monitor ? (monitor.mil
        ? html`<${Badge} tone="red" dot>On<//>`
        : html`<${Badge} tone="green" dot>Off<//>`) : '—'}</span>
    </a>
    <a class="card stat stat-link" href=${href('/diagnostics')}>
      <span class="stat-label">Trouble codes</span>
      <span class="stat-value">${stored.length}</span>
      <span class="stat-sub">${pending.length} pending · ${permanent.length} permanent</span>
    </a>
    <a class="card stat stat-link" href=${href('/diagnostics')}>
      <span class="stat-label">Readiness</span>
      <span class="stat-value">${monitor ? `${available.length - incomplete.length} / ${available.length}` : '—'}</span>
      <span class="stat-sub">${incomplete.length ? `Incomplete: ${incomplete.map(m => m.name).join(', ')}` : 'Monitors complete'}</span>
    </a>
  </div>`;
}
