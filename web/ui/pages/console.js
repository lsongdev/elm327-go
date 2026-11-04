import { html, useEffect, useRef, useState } from '../lib/html.js';
import { usePanel } from '../lib/panel.js';
import { useAction } from '../lib/hooks.js';
import { Page, Notice, LogView, Field } from '../lib/ui.js';
import { useLive, StateBadge, SourceNotice, display } from '../obd.js';

// The view keeps this many exchanges.
const KEEP = 300;

const PRESETS = [
  ['ATI', 'Adapter version'],
  ['ATDP', 'Protocol'],
  ['ATRV', 'Battery voltage'],
  ['0100', 'Supported PIDs'],
  ['010C', 'Engine speed'],
  ['03', 'Stored codes'],
  ['0902', 'VIN'],
];

export default function Console() {
  const { api } = usePanel();
  const [entries, setEntries] = useState([]);
  const [paused, setPaused] = useState(false);
  const pausedRef = useRef(paused);
  pausedRef.current = paused;

  // Start from the recent history, then follow the stream.
  useEffect(() => {
    const controller = new AbortController();
    api.get('log', { query: { limit: KEEP }, signal: controller.signal })
      .then(recent => setEntries(old => merge(recent, old)))
      .catch(() => {});
    return () => controller.abort();
  }, []);

  const { snap, error } = useLive(entry => {
    if (!pausedRef.current) setEntries(old => merge(old, [entry]));
  });

  const text = e => `${e.command.padEnd(8)} ${e.duration_ms.toFixed(0).padStart(5)} ms  ${(e.response || '').replace(/\n/g, ' | ')}${e.error ? `    ← ${e.error}` : ''}`;
  return html`<${Page} title="Console" subtitle="Traffic between this server and the adapter" actions=${html`<${StateBadge} status=${snap?.status} />`}>
    <${Notice} error=${error} />
    <${SourceNotice} status=${snap?.status} />
    <div class="toolbar">
      <button class="button" type="button" onClick=${() => setPaused(p => !p)}>${paused ? 'Resume' : 'Pause'}</button>
      <button class="button button-ghost" type="button" onClick=${() => setEntries([])}>Clear</button>
      <span class="flex-1"></span>
      <span class="muted">${paused ? 'Paused' : 'Live'} · ${entries.length} exchanges</span>
    </div>
    <${LogView} lines=${entries.map(e => (e.error ? { text: text(e), stream: 'stderr' } : text(e)))}
      follow=${!paused} empty="Waiting for traffic…" />
    <div class="grid grid-auto grid-pair section">
      <${SendTool} />
      <${DecodeTool} />
    </div>
  <//>`;
}

// merge appends exchanges not yet shown, by sequence number, keeping the last KEEP.
function merge(old, add) {
  const last = old.length ? old[old.length - 1].seq : 0;
  const next = old.concat(add.filter(e => e.seq > last));
  return next.length > KEEP ? next.slice(-KEEP) : next;
}

function SendTool() {
  const { api } = usePanel();
  const [command, setCommand] = useState('010C');
  const [result, setResult] = useState(null);
  const { busy, error, run } = useAction();
  const send = value => {
    setResult(null);
    run(async () => setResult(await api.post('command', { command: value })));
  };
  const submit = e => {
    e.preventDefault();
    send(command);
  };
  return html`<form class="card form" onSubmit=${submit}>
    <h2>Send a command</h2>
    <${Field} label="Command" hint="Read-only commands only: services 01, 02, 03, 06, 07, 09, 0A and adapter queries. Anything that changes the car or the adapter is refused.">
      <input class="input mono" value=${command} onInput=${e => setCommand(e.currentTarget.value)} spellcheck="false" autocomplete="off" />
    <//>
    <div class="button-row">
      ${PRESETS.map(([value, title]) => html`<button key=${value} class="button button-small" type="button" title=${title}
        onClick=${() => { setCommand(value); send(value); }}>${title}</button>`)}
    </div>
    <div class="form-actions">
      <button class="button button-primary" type="submit" disabled=${busy || !command.trim()}>${busy ? 'Sending…' : 'Send'}</button>
    </div>
    <${Notice} error=${error} />
    ${result ? html`<pre class="decoded">${result.response || '(empty response)'}</pre><${DecodedView} decoded=${result} />` : null}
  </form>`;
}

function DecodeTool() {
  const { api } = usePanel();
  const [response, setResponse] = useState('7E8 04 41 0C 1A F8');
  const [result, setResult] = useState(null);
  const { busy, error, run } = useAction();
  const submit = e => {
    e.preventDefault();
    setResult(null);
    run(async () => setResult(await api.post('decode', { response })));
  };
  return html`<form class="card form" onSubmit=${submit}>
    <h2>Decode a response</h2>
    <${Field} label="Adapter output" hint="Paste captured lines. Decoded on the server; nothing is sent to the vehicle.">
      <textarea class="input mono" rows="3" value=${response} onInput=${e => setResponse(e.currentTarget.value)} spellcheck="false"></textarea>
    <//>
    <div class="form-actions">
      <button class="button button-primary" type="submit" disabled=${busy || !response.trim()}>Decode</button>
    </div>
    <${Notice} error=${error} />
    ${result ? html`<${DecodedView} decoded=${result} />` : null}
  </form>`;
}

function DecodedView({ decoded }) {
  const { messages = [], readings = [], dtcs = [], error } = decoded;
  return html`<div class="decoded-list">
    ${error ? html`<${Notice} tone="warn">${error}<//>` : null}
    ${messages.map((m, i) => html`<div key=${i} class="mono muted">${m.ecu || 'ECU ?'}: ${m.data}</div>`)}
    ${readings.map(r => {
      const { value, unit } = display(r);
      return html`<div key=${`${r.ecu}${r.pid}`}><strong>${r.name}</strong>: ${value}${unit ? ` ${unit}` : ''}</div>`;
    })}
    ${dtcs.map(c => html`<div key=${c.code}><strong class="mono">${c.code}</strong> ${c.description || ''}</div>`)}
  </div>`;
}
