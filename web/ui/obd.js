// Shared state and formatting for the pages.
import { html, useEffect, useRef, useState } from './lib/html.js';
import { usePanel } from './lib/panel.js';
import { Badge, Notice } from './lib/ui.js';

const HISTORY_WINDOW_MS = 5 * 60 * 1000;

// history keeps recent values per PID for the sparklines. It lives outside the
// pages so a chart survives moving between them.
const history = new Map();

function record(metrics) {
  for (const m of metrics) {
    if (m.value == null) continue;
    const t = Date.parse(m.updated);
    const series = history.get(m.pid) || [];
    if (series.length && series[series.length - 1].t === t) continue;
    series.push({ t, v: m.value });
    while (series.length && series[0].t < t - HISTORY_WINDOW_MS) series.shift();
    history.set(m.pid, series);
  }
}

export const historyOf = pid => history.get(pid) || [];

// useLive returns the latest /state snapshot, kept current by the /events
// stream. onLog, when given, receives every exchange with the adapter.
export function useLive(onLog) {
  const { client } = usePanel();
  const [snap, setSnap] = useState(null);
  const [error, setError] = useState(null);
  const log = useRef(onLog);
  log.current = onLog;
  useEffect(() => {
    const handlers = {
      state: s => { record(s.metrics); setSnap(s); setError(null); },
      onerror: () => setError(new Error('The live stream closed; reload the page to reconnect.')),
    };
    if (onLog) handlers.log = entry => log.current?.(entry);
    return client.stream('events', onLog ? { log: 1 } : null, handlers);
  }, []);
  return { snap, error };
}

const STATES = {
  online: { tone: 'green', label: 'Online' },
  searching: { tone: 'amber', label: 'Searching' },
  no_vehicle: { tone: 'amber', label: 'No vehicle' },
  connecting: { tone: 'red', label: 'No adapter' },
};

export function StateBadge({ status }) {
  const s = STATES[status?.state] || STATES.connecting;
  return html`<${Badge} tone=${s.tone} dot>${s.label}<//>`;
}

// SourceNotice explains why live values are not arriving.
export function SourceNotice({ status }) {
  if (!status) return null;
  const detail = status.error ? `: ${status.error}` : '';
  switch (status.state) {
    case 'connecting':
      return html`<${Notice} tone="warn">Cannot reach the adapter at <code>${status.adapter}</code>${detail}. Retrying…<//>`;
    case 'searching':
      return html`<${Notice} tone="info">Adapter connected. Looking for the vehicle; the first search can take up to 30 seconds.<//>`;
    case 'no_vehicle':
      return html`<${Notice} tone="info">The adapter is connected but the vehicle does not answer${detail}. Turn the ignition on. Values below are the last ones read.<//>`;
  }
  return null;
}

// digits picks decimals by magnitude; whole numbers read better for these units.
const WHOLE = new Set(['rpm', 'km/h', '°C', 'km', 's', 'min', 'kPa', 'Pa', 'N·m']);

export function number(value, unit) {
  if (value == null || Number.isNaN(value)) return '—';
  if (WHOLE.has(unit)) return Math.round(value).toLocaleString();
  const abs = Math.abs(value);
  return value.toFixed(abs >= 100 ? 0 : abs >= 10 ? 1 : 2);
}

export function duration(seconds) {
  const s = Math.max(0, Math.round(seconds));
  const pad = n => String(n).padStart(2, '0');
  const h = Math.floor(s / 3600);
  return h ? `${h}:${pad(Math.floor(s / 60) % 60)}:${pad(s % 60)}` : `${Math.floor(s / 60)}:${pad(s % 60)}`;
}

// display gives a metric's main text and its unit.
export function display(m) {
  if (m.text) return { value: m.text, unit: '' };
  if (m.key === 'run_time') return { value: duration(m.value), unit: '' };
  return { value: number(m.value, m.unit), unit: m.unit || '' };
}

export function age(value) {
  const t = Date.parse(value);
  if (!t || new Date(t).getUTCFullYear() < 1980) return 'never';
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 2) return 'now';
  if (s < 90) return `${s}s ago`;
  if (s < 5400) return `${Math.round(s / 60)}m ago`;
  return `${Math.round(s / 3600)}h ago`;
}

export const GROUPS = [
  ['engine', 'Engine'],
  ['driver', 'Driver inputs'],
  ['fuel', 'Fuel'],
  ['air', 'Air'],
  ['temperature', 'Temperature'],
  ['oxygen', 'Oxygen sensors'],
  ['emissions', 'Emissions'],
  ['electrical', 'Electrical'],
  ['status', 'Status'],
];
