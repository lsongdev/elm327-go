// UI building blocks for the pages: the @lsongdev/scripts components under
// the names and tone vocabulary the pages use.
import { html } from './html.js';
import {
  Alert, Badge as ScriptsBadge, KeyValue,
} from '../node_modules/@lsongdev/scripts/react/components/index.js';

export {
  Loading, Empty, Field, Tabs, LogView, CopyButton, ConfirmButton, Toaster, DialogHost, Table,
} from '../node_modules/@lsongdev/scripts/react/components/index.js';
export { KeyValue as KV };

export function Page({ title, subtitle, actions, children }) {
  return html`<section class="page">
    <header class="page-header">
      <div class="page-title">
        <h1>${title}</h1>
        ${subtitle ? html`<div class="page-subtitle">${subtitle}</div>` : null}
      </div>
      ${actions ? html`<div class="page-actions">${actions}</div>` : null}
    </header>
    ${children}
  </section>`;
}

// Notice tones as the pages name them, mapped onto alert variants.
const variants = { error: 'danger', warn: 'warning', success: 'success', info: 'info' };

export function Notice({ error, tone = 'error', children }) {
  return html`<${Alert} variant=${variants[tone]} error=${error}>${children}<//>`;
}

// Badge tones as colours, mapped onto badge variants; gray is the plain badge.
const badges = { green: 'success', amber: 'warning', red: 'danger', blue: 'info' };

export function Badge({ tone, title, dot, children }) {
  return html`<${ScriptsBadge} variant=${badges[tone]} dot=${dot} title=${title}>${children}<//>`;
}
