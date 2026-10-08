import './style.css';
import geist from '../node_modules/geist/dist/fonts/geist-sans/Geist-Variable.woff2?url';
import mono from '@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2?url';
import { ServiceMap } from './map';
import { endpointOf, type Detail, type Endpoint, type Meta, type Req, type SpanView } from './types';

for (const [family, url, weight] of [['Geist', geist, '100 900'], ['JetBrains Mono', mono, '100 800']]) {
  const f = new FontFace(family, `url(${url}) format('woff2')`, { weight, display: 'swap' });
  f.load().then(() => document.fonts.add(f)).catch(() => {});
}

const $ = <T extends Element = HTMLElement>(s: string) => document.querySelector(s) as T;
const esc = (s: unknown) => String(s ?? '').replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const nf = new Intl.NumberFormat();
const tf = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
const fmtMs = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : ms >= 10 ? `${Math.round(ms)} ms` : `${ms.toFixed(1)} ms`);
const statusClass = (s?: number) => (!s ? 's2' : s >= 500 ? 's5' : s >= 400 ? 's4' : 's2');

let current: Detail | null = null;

const state = {
  reqs: new Map<string, Req>(),
  sorted: [] as Req[],
  meta: null as Meta | null,
  filter: '',
  only: new Set<string>(),
  route: null as string | null,
  fresh: new Set<string>(),
  traceId: null as string | null,
  endpoints: [] as Endpoint[],
};

const map = new ServiceMap($('#map-wrap'), $<SVGSVGElement>('#map'), $<HTMLCanvasElement>('#particles'));
map.onRoute = (ep) => {
  state.route = ep;
  map.selected = ep;
  schedule();
};

let pending = 0;
function schedule() {
  if (!pending) pending = requestAnimationFrame(() => {
    pending = 0;
    render();
  });
}

function upsert(r: Req) {
  const isNew = !state.reqs.has(r.traceId);
  state.reqs.set(r.traceId, r);
  if (isNew) {
    state.fresh.add(r.traceId);
    setTimeout(() => state.fresh.delete(r.traceId), 1300);
  }
  return isNew;
}

function resort() {
  state.sorted = [...state.reqs.values()].sort((a, b) => b.startMs - a.startMs);
  if (state.sorted.length > 1000) {
    for (const r of state.sorted.splice(1000)) state.reqs.delete(r.traceId);
  }
}

function visible() {
  const q = state.filter.toLowerCase();
  return state.sorted.filter((r) => {
    if (state.route && endpointOf(r) !== state.route) return false;
    if (state.only.has('errors') && !r.error) return false;
    if (state.only.has('slow') && !r.slow) return false;
    if (state.only.has('n1') && !r.nPlusOne?.length) return false;
    if (q && !`${endpointOf(r)} ${r.path ?? ''} ${r.status ?? ''} ${r.service}`.toLowerCase().includes(q)) return false;
    return true;
  });
}

function renderApp() {
  const m = state.meta;
  const el = $('#app');
  if (!m) return;
  const a = m.app;
  if (!a.command) {
    el.innerHTML = `<span class="state live">receiving OTLP</span>`;
    return;
  }
  const st = a.running ? `<span class="state live">pid ${a.pid}</span>` : `<span class="state down">exited ${a.exitCode ?? ''}</span>`;
  el.innerHTML = `<span class="cmd" title="${esc(a.command)}">${esc(a.command)}</span>${st}`;
}

function renderStats() {
  const now = Date.now();
  const recent = state.sorted.filter((r) => now - r.startMs < 60_000);
  const durs = recent.map((r) => r.durationMs).sort((a, b) => a - b);
  const p95 = durs.length ? durs[Math.max(0, Math.ceil(durs.length * 0.95) - 1)] : 0;
  const errs = recent.filter((r) => r.error).length;
  const n1 = new Set(state.sorted.filter((r) => r.nPlusOne?.length).map(endpointOf)).size;
  const slowMs = state.meta?.slowMs ?? 500;
  $('#stats').innerHTML = `
    <div><dt>req / min</dt><dd>${nf.format(recent.length)}</dd></div>
    <div><dt>p95 (1m)</dt><dd class="${p95 > slowMs ? 'hot' : ''}">${durs.length ? fmtMs(p95) : '-'}</dd></div>
    <div><dt>errors (1m)</dt><dd class="${errs ? 'bad' : ''}">${nf.format(errs)}</dd></div>
    <div><dt>N+1 routes</dt><dd class="${n1 ? 'hot' : ''}">${n1}</dd></div>`;
}

function renderList() {
  const rows = visible();
  const list = $('#list');
  if (!state.sorted.length) {
    list.innerHTML = `<p class="list-empty">No requests yet.</p>`;
    return;
  }
  if (!rows.length) {
    list.innerHTML = `<p class="list-empty">No requests match these filters.</p>`;
    return;
  }
  const max = Math.max(...rows.slice(0, 200).map((r) => r.durationMs), 1);
  const body = rows
    .slice(0, 200)
    .map((r) => {
      const flags = [
        r.nPlusOne?.length ? `<span class="pill">N+1</span>` : '',
        r.slow ? `<span class="pill">SLOW</span>` : '',
        r.error ? `<span class="pill err">ERR</span>` : '',
      ].join('');
      const w = Math.max(2, Math.round((Math.log1p(r.durationMs) / Math.log1p(max)) * 48));
      const cls = ['row', state.traceId === r.traceId ? 'sel' : '', state.fresh.has(r.traceId) ? 'fresh' : ''].join(' ');
      return `<tr class="${cls}" data-id="${r.traceId}">
        <td class="col-hide">${tf.format(r.startMs)}</td>
        <td><a href="#/trace/${r.traceId}"><span class="method">${esc(r.method ?? '')}</span>${esc(r.route)}</a></td>
        <td class="r ${statusClass(r.status)}">${r.status ?? '-'}</td>
        <td class="r ${r.slow ? 'slowv' : ''}">${fmtMs(r.durationMs)}<span class="durbar ${r.error ? 'err' : r.slow ? 'slow' : ''}" style="width:${w}px"></span></td>
        <td class="r col-hide">${r.dbCalls}</td>
        <td class="r col-hide">${r.external}</td>
        <td><span class="flags">${flags}</span></td>
      </tr>`;
    })
    .join('');
  list.innerHTML = `<table>
    <colgroup><col class="col-hide" style="width:84px"><col><col style="width:62px"><col style="width:132px"><col class="col-hide" style="width:54px"><col class="col-hide" style="width:54px"><col style="width:118px"></colgroup>
    <thead><tr><th class="col-hide">Time</th><th>Endpoint</th><th class="r">Status</th><th class="r">Duration</th><th class="r col-hide">DB</th><th class="r col-hide">Ext</th><th>Flags</th></tr></thead>
    <tbody>${body}</tbody></table>`;
}

function renderInsights() {
  if (state.traceId) return;
  const panel = $('#panel');
  const eps = state.endpoints;
  const route = state.route ? `<p class="sub">Filtered to <code>${esc(state.route)}</code>. <a href="#" data-clear-route>Show all</a></p>` : '';
  if (!eps.length) {
    panel.innerHTML = `<div class="pane"><h3>Endpoints</h3><p class="sub">Latency, N+1 queries and hints appear here once requests come in.</p>
      <div class="skeleton" style="width:70%"></div><div class="skeleton" style="width:52%"></div><div class="skeleton" style="width:61%"></div></div>`;
    return;
  }
  const n1 = eps.flatMap((e) => (e.nPlusOne ?? []).map((f) => ({ e, f })));
  const n1html = n1.length
    ? `<ul class="hints">${n1
        .map(({ e, f }) => `<li><a class="ep-name" href="#/trace/${e.slowestTraceId}" style="color:inherit">${esc(e.endpoint)}</a><br><code>${esc(f.statement)}</code><br>${f.count}× in one request, ${fmtMs(f.totalMs)} total</li>`)
        .join('')}</ul>`
    : `<p class="sub">None detected. A query shape repeated ${state.meta?.nPlusOneMin ?? 5}+ times in one request shows up here.</p>`;
  const list = eps
    .map(
      (e) => `<div class="ep">
      <div class="ep-top"><span class="ep-name"><a href="#/trace/${e.slowestTraceId}" title="Open slowest trace">${esc(e.endpoint)}</a></span>
        ${e.nPlusOne?.length ? '<span class="pill">N+1</span>' : ''}${e.slow ? '<span class="pill">SLOW</span>' : ''}${e.errors ? `<span class="pill err">${e.errors} ERR</span>` : ''}</div>
      <div class="ep-nums"><span><b>${nf.format(e.count)}</b> req</span><span>p50 <b>${fmtMs(e.p50Ms)}</b></span><span>p95 <b class="${e.slow ? 'slowv' : ''}">${fmtMs(e.p95Ms)}</b></span><span>db <b>${Math.round(e.dbShare * 100)}%</b></span></div>
      ${e.hints.length ? `<ul class="hints">${e.hints.map((h) => `<li>${hint(h)}</li>`).join('')}</ul>` : ''}
    </div>`,
    )
    .join('');
  panel.innerHTML = `<div class="pane">${route}
    <div class="section"><h3>N+1 queries</h3>${n1html}</div>
    <div class="section"><h3>Endpoints by p95</h3><p class="sub">Click an endpoint to open its slowest trace.</p>${list}</div></div>`;
}

const hint = (h: string) => esc(h).replace(/&#34;(.+?)&#34;/g, '<code>$1</code>');

function render() {
  renderStats();
  renderList();
  renderInsights();
  const empty = !state.sorted.length;
  const el = $('#map-empty');
  el.hidden = !empty;
  if (empty) {
    const base = location.origin;
    el.innerHTML = `<h3>Waiting for the first request</h3>
      <p>${state.meta?.app.command ? 'Your app is running under runlens. Send it a request and it shows up here as it happens.' : 'Point any OpenTelemetry SDK at this endpoint, or restart your dev server with runlens in front of it.'}</p>
      <pre translate="no">${state.meta?.app.command ? 'curl http://localhost:3000/' : `runlens npm run dev\n\nOTEL_EXPORTER_OTLP_ENDPOINT=${esc(base)}\nOTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`}</pre>`;
  }
  map.update(state.sorted);
}

async function loadDetail(id: string) {
  const panel = $('#panel');
  panel.innerHTML = `<div class="pane"><div class="skeleton" style="width:60%"></div><div class="skeleton" style="width:40%"></div><div class="skeleton" style="width:80%"></div><div class="skeleton" style="width:72%"></div></div>`;
  const res = await fetch(`/api/traces/${encodeURIComponent(id)}`);
  if (state.traceId !== id) return;
  if (!res.ok) {
    panel.innerHTML = `<div class="pane"><a class="back" href="#/">Back to endpoints</a><h3>Trace not found</h3><p class="sub">It may have been evicted from the in-memory buffer. Raise <code>--capacity</code> to keep more.</p></div>`;
    return;
  }
  renderDetail(await res.json());
}

function renderDetail(d: Detail) {
  current = d;
  const total = Math.max(d.durationMs, 0.001);
  const ticks = [0, 0.5, 1].map((f) => `<span>${fmtMs(total * f)}</span>`).join('');
  const hints = d.hints.length ? `<ul class="hints" style="margin:14px 20px 0">${d.hints.map((h) => `<li class="${h.startsWith('N+1') || h.startsWith('Slow') ? '' : 'mute'}">${hint(h)}</li>`).join('')}</ul>` : '';
  const errHint = d.error ? `<ul class="hints" style="margin:14px 20px 0"><li class="err">Request failed${d.status ? ` with HTTP ${d.status}` : ''}. Expand the red span for the stack.</li></ul>` : '';
  const rows = d.tree.map((s, i) => spanRow(s, i, total)).join('');
  $('#panel').innerHTML = `
    <div class="detail-head">
      <a class="back" href="#/">Back to endpoints</a>
      <h3 class="detail-title" translate="no">${esc(d.method ?? '')} ${esc(d.path || d.route)}</h3>
      <div class="detail-meta">
        <span class="${statusClass(d.status)}">${d.status ?? '-'}</span>
        <span><b>${fmtMs(d.durationMs)}</b></span>
        <span><b>${d.spans}</b> spans</span>
        <span><b>${d.dbCalls}</b> queries, ${fmtMs(d.dbMs)}</span>
        <span><b>${d.external}</b> outbound</span>
        <button class="copy" type="button" data-copy="${d.traceId}" title="Copy trace id">${d.traceId.slice(0, 12)}…</button>
      </div>
    </div>
    ${errHint}${hints}
    <div class="wf">
      <div class="wf-axis"><span>Span</span><span class="ticks">${ticks}</span><span></span></div>
      ${rows}
    </div>`;
}

function spanRow(s: SpanView, i: number, total: number) {
  const left = (s.offsetMs / total) * 100;
  const width = Math.max(0.3, (s.durMs / total) * 100);
  const sub = s.statement ?? (s.type === 'external' ? s.peer : '') ?? '';
  const cls = ['span', s.type, s.error ? 'err' : '', s.slow ? 'slow' : '', s.nPlusOne ? 'n1' : ''].join(' ');
  return `<button class="${cls}" type="button" data-i="${i}" aria-expanded="false">
    <div class="span-row">
      <div class="span-name" style="padding-left:${s.depth * 12}px">
        <span class="nm">${esc(s.name)}${s.nPlusOne ? ' <span class="pill">N+1</span>' : ''}${s.slow ? ' <span class="pill">SLOW</span>' : ''}</span>
        ${sub ? `<span class="st" translate="no">${esc(sub)}</span>` : ''}
      </div>
      <div class="track"><span class="b" style="left:${left}%;width:${width}%"></span></div><span class="d">${fmtMs(s.durMs)}</span>
    </div></button>`;
}

$('#panel').addEventListener('click', async (e) => {
  const t = e.target as HTMLElement;
  const copy = t.closest<HTMLElement>('[data-copy]');
  if (copy) {
    await navigator.clipboard?.writeText(copy.dataset.copy!).catch(() => {});
    copy.textContent = 'Copied';
    return;
  }
  if (t.closest('[data-clear-route]')) {
    e.preventDefault();
    map.onRoute(null);
    return;
  }
  const btn = t.closest<HTMLButtonElement>('.span');
  if (!btn || !current) return;
  const next = btn.nextElementSibling;
  if (next?.classList.contains('span-more')) {
    next.remove();
    btn.setAttribute('aria-expanded', 'false');
    return;
  }
  const s = current.tree[Number(btn.dataset.i)];
  const exc = (s.events ?? []).filter((ev) => ev.name === 'exception');
  const attrs = Object.entries(s.attrs ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const more = document.createElement('div');
  more.className = 'span-more';
  more.innerHTML = `
    ${s.statement ? `<pre translate="no">${esc(s.statement)}</pre>` : ''}
    ${s.error && s.statusMessage ? `<pre class="stack">${esc(s.statusMessage)}</pre>` : ''}
    ${exc.map((ev) => `<pre class="stack">${esc(ev.attrs?.['exception.type'])}: ${esc(ev.attrs?.['exception.message'])}\n${esc(ev.attrs?.['exception.stacktrace'] ?? '')}</pre>`).join('')}
    <dl class="kv"><dt>service</dt><dd>${esc(s.service)}</dd><dt>kind</dt><dd>${esc(s.kind)}</dd><dt>start</dt><dd>+${fmtMs(s.offsetMs)}</dd>
    ${attrs.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(typeof v === 'object' ? JSON.stringify(v) : v)}</dd>`).join('')}</dl>`;
  btn.after(more);
  btn.setAttribute('aria-expanded', 'true');
});

async function route() {
  const m = location.hash.match(/^#\/trace\/([0-9a-f]+)/);
  state.traceId = m ? m[1] : null;
  current = null;
  if (state.traceId) {
    const id = state.traceId;
    $('#panel').scrollTop = 0;
    await loadDetail(id).catch(() => {});
  } else {
    renderInsights();
  }
  renderList();
}

$('#list').addEventListener('click', (e) => {
  const t = e.target as HTMLElement;
  if (t.closest('a')) return;
  const tr = t.closest<HTMLElement>('tr.row');
  if (tr) location.hash = `#/trace/${tr.dataset.id}`;
});

$<HTMLInputElement>('#filter').addEventListener('input', (e) => {
  state.filter = (e.target as HTMLInputElement).value;
  renderList();
});

document.querySelectorAll<HTMLButtonElement>('[data-only]').forEach((b) =>
  b.addEventListener('click', () => {
    const k = b.dataset.only!;
    state.only.has(k) ? state.only.delete(k) : state.only.add(k);
    b.setAttribute('aria-pressed', String(state.only.has(k)));
    renderList();
  }),
);

$('#clear').addEventListener('click', async () => {
  if (!confirm('Clear all captured requests?')) return;
  await fetch('/api/clear', { method: 'POST' });
  state.reqs.clear();
  state.endpoints = [];
  resort();
  location.hash = '#/';
  render();
});

$('#theme').addEventListener('click', () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  $<HTMLMetaElement>('meta[name="theme-color"]').content = next === 'light' ? '#f4f4f4' : '#0a0a0a';
  try {
    localStorage.setItem('runlens-theme', next);
  } catch {}
  map.update(state.sorted);
});

let epTimer = 0;
function refreshEndpoints() {
  clearTimeout(epTimer);
  epTimer = window.setTimeout(async () => {
    state.endpoints = await fetch('/api/endpoints').then((r) => r.json()).catch(() => state.endpoints);
    renderInsights();
  }, 400);
}

async function loadMeta() {
  state.meta = await fetch('/api/meta').then((r) => r.json()).catch(() => state.meta);
  if (state.meta) map.slowMs = state.meta.slowMs;
  renderApp();
}

function connect() {
  const es = new EventSource('/api/stream');
  es.addEventListener('request', (e) => {
    const r: Req = JSON.parse((e as MessageEvent).data);
    const isNew = upsert(r);
    resort();
    schedule();
    if (isNew) requestAnimationFrame(() => map.pulse(r));
    refreshEndpoints();
  });
}

async function boot() {
  await loadMeta();
  const reqs: Req[] = await fetch('/api/requests').then((r) => r.json()).catch(() => []);
  reqs.forEach(upsert);
  state.fresh.clear();
  resort();
  render();
  refreshEndpoints();
  connect();
  await route();
  setInterval(loadMeta, 3000);
  setInterval(renderStats, 5000);
}

window.addEventListener('hashchange', route);
boot();
