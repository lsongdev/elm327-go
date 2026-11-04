// The small figures of the dashboard. Marks use the single accent colour; values
// and labels stay in text colours, and every plot has a text equivalent.
import { html, useState } from './html.js';

const W = 240;
const H = 56;
const PAD = 4;

// Sparkline plots one series against time, with a crosshair on hover.
// format(v) renders a value for the tooltip.
export function Sparkline({ points, format, label, floor = 0 }) {
  const [hover, setHover] = useState(null);
  if (points.length < 2) return html`<div class="spark spark-empty" aria-hidden="true"></div>`;

  const t0 = points[0].t;
  const t1 = points[points.length - 1].t;
  const values = points.map(p => p.v);
  let low = Math.min(...values);
  let high = Math.max(...values);
  // Keep tiny jitter from filling the plot: floor is the least span worth drawing.
  if (high - low < floor) {
    const middle = (high + low) / 2;
    low = middle - floor / 2;
    high = middle + floor / 2;
  }
  const span = high - low || 1;
  const x = t => PAD + ((t - t0) / (t1 - t0 || 1)) * (W - 2 * PAD);
  const y = v => H - PAD - ((v - low) / span) * (H - 2 * PAD);
  const line = points.map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)} ${y(p.v).toFixed(1)}`).join(' ');
  const area = `${line} L${x(t1).toFixed(1)} ${H} L${x(t0).toFixed(1)} ${H} Z`;

  const onMove = e => {
    const box = e.currentTarget.getBoundingClientRect();
    const target = t0 + ((e.clientX - box.left) / box.width) * (t1 - t0);
    let best = points[0];
    for (const p of points) if (Math.abs(p.t - target) < Math.abs(best.t - target)) best = p;
    setHover(best);
  };
  const last = points[points.length - 1];
  const shown = hover || last;
  const left = (x(shown.t) / W) * 100;
  return html`<div class="spark" onPointerMove=${onMove} onPointerLeave=${() => setHover(null)}>
    <svg viewBox=${`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img"
      aria-label=${`${label}: ${format(low)} to ${format(high)} over the last ${Math.round((t1 - t0) / 1000)} seconds`}>
      <path class="spark-area" d=${area} />
      <path class="spark-line" d=${line} vector-effect="non-scaling-stroke" />
      ${hover ? html`<line class="spark-cross" x1=${x(hover.t)} x2=${x(hover.t)} y1="0" y2=${H} vector-effect="non-scaling-stroke" />` : null}
    </svg>
    <span class="spark-dot" style=${{ left: `${left}%`, top: `${(y(shown.v) / H) * 100}%` }}></span>
    ${hover ? html`<div class=${`spark-tip ${left > 60 ? 'spark-tip-left' : ''}`} style=${{ left: `${left}%` }}>
      <strong>${format(hover.v)}</strong>
      <span>${Math.round((last.t - hover.t) / 1000)}s ago</span>
    </div>` : null}
  </div>`;
}

// Meter shows a value on its scale, filled from zero when the scale spans zero.
export function Meter({ value, min, max, label }) {
  if (value == null || min === max) return null;
  const at = v => (Math.min(Math.max(v, min), max) - min) / (max - min) * 100;
  const zero = at(0);
  const here = at(value);
  return html`<div class="meter" role="meter" aria-label=${label} aria-valuemin=${min} aria-valuemax=${max} aria-valuenow=${value}>
    <span class="meter-fill" style=${{ left: `${Math.min(zero, here)}%`, width: `${Math.abs(here - zero)}%` }}></span>
  </div>`;
}
