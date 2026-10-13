import './style.css';
import './agents.css';
import geist from '../node_modules/geist/dist/fonts/geist-sans/Geist-Variable.woff2?url';
import mono from '@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2?url';

for (const [family, url, weight] of [['Geist', geist, '100 900'], ['JetBrains Mono', mono, '100 800']]) {
  const f = new FontFace(family, `url(${url}) format('woff2')`, { weight, display: 'swap' });
  f.load().then(() => document.fonts.add(f)).catch(() => {});
}

type Usage = { input: number; output: number; cacheRead: number; cacheWrite: number };
type Agent = {
  id: string; session: string; description?: string; type?: string; cwd?: string;
  startedAt: string; lastAt: string; usage: Usage; toolCalls: number; errors: number; edits: number;
  final?: string; status: string; lastAction?: string; tokensPerHour: number; files: string[];
};
type Session = { id: string; project: string; cwd?: string; lastAt: string; agents: Agent[]; usage: Usage };
type Collision = { session: string; file: string; agents: string[]; lastAt: string };
type Snapshot = { sessions: Session[]; collisions: Collision[]; generatedAt: string };
type Event = { time: string; kind: string; tool?: string; summary?: string; file?: string; old?: string; new?: string; output?: string; isError?: boolean; edit?: boolean };
type Claim = { kind: string; text: string; verdict: string; evidence?: string; note: string };
type Detail = Agent & { events: Event[]; claims: Claim[] };

const $ = <T extends Element = HTMLElement>(s: string) => document.querySelector(s) as T;
const esc = (s: unknown) => String(s ?? '').replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const billable = (u: Usage) => u.input + u.output + u.cacheWrite;
const fmtN = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}k` : String(Math.round(n)));
const ago = (iso: string, now: number) => {
  const s = Math.max(0, (now - Date.parse(iso)) / 1000);
  return s < 60 ? `${Math.round(s)}s` : s < 3600 ? `${Math.round(s / 60)}m` : s < 86400 ? `${Math.round(s / 3600)}h` : `${Math.round(s / 86400)}d`;
};
const label = (a: Agent) => (a.id === 'main' ? 'main session' : a.description || a.id.slice(0, 12));
const rendered = new WeakMap<Element, string>();
const patch = (box: Element, rows: [string, string][], wrap = '') => {
  const t = document.createElement('template');
  const make = (h: string) => {
    t.innerHTML = wrap ? `<${wrap}>${h}</${wrap}>` : h;
    const root = wrap ? t.content.firstElementChild! : t.content;
    const el = root.firstElementChild!;
    rendered.set(el, h);
    return el;
  };
  const cur = [...box.children];
  if (cur.length === rows.length && cur.every((el, i) => (el as HTMLElement).dataset.key === rows[i][0])) {
    rows.forEach(([, h], i) => { if (rendered.get(cur[i]) !== h) cur[i].replaceWith(make(h)); });
    return;
  }
  box.replaceChildren(...rows.map(([, h]) => make(h)));
};
const short = (p: string) => p.replace(/\\/g, '/').split('/').slice(-3).join('/');

let snap: Snapshot = { sessions: [], collisions: [], generatedAt: '' };
let sessionId = '';
let filter = 'all';
let selected = '';
let showSolo = false;
let detailTimer = 0;

function current(): Session | undefined {
  return snap.sessions.find((s) => s.id === sessionId)
    ?? snap.sessions.find((s) => s.agents.some((a) => a.id !== 'main' && a.status === 'running'))
    ?? snap.sessions.find((s) => s.agents.length > 1)
    ?? snap.sessions[0];
}

function renderStats() {
  const s = current();
  const agents = s?.agents ?? [];
  const count = (st: string) => agents.filter((a) => a.status === st).length;
  const perHour = agents.reduce((n, a) => n + a.tokensPerHour, 0);
  const coll = snap.collisions.filter((c) => c.session === s?.id).length;
  $('#stats').innerHTML = [
    ['agents', String(agents.length), ''],
    ['running', String(count('running')), count('running') ? 'hot' : ''],
    ['stalled', String(count('stalled')), count('stalled') ? 'bad' : ''],
    ['collisions', String(coll), coll ? 'bad' : ''],
    ['tokens / h', fmtN(perHour), ''],
  ].map(([k, v, c]) => `<div><dt>${k}</dt><dd class="${c}">${v}</dd></div>`).join('');
}

function renderSessions() {
  const s = current();
  const solo = snap.sessions.filter((x) => x.agents.length < 2 && x.id !== s?.id).length;
  const shown = snap.sessions.filter((x) => showSolo || x.agents.length > 1 || x.id === s?.id);
  $('#sessions').innerHTML = shown.slice(0, showSolo ? 40 : 12).map((x) => {
    const running = x.agents.filter((a) => a.status === 'running').length;
    const name = (x.cwd || x.project).replace(/\\/g, '/').split('/').filter(Boolean).pop() || x.project;
    return `<button role="tab" aria-selected="${x.id === s?.id}" data-id="${esc(x.id)}" class="sess${running ? ' live' : ''}">
      <span class="sd" aria-hidden="true"></span>${esc(name)}<span class="sc">${x.agents.length}</span></button>`;
  }).join('') + (solo ? `<button type="button" class="sess more" aria-pressed="${showSolo}">${showSolo ? 'Hide' : 'Show'} ${solo} solo</button>` : '')
    || '<p class="empty-note">No Claude Code sessions in the selected window. Start an agent and it shows up here within two seconds.</p>';
}

function renderFleet() {
  const svg = $<SVGSVGElement>('#fleet');
  const s = current();
  if (!s) { svg.innerHTML = ''; return; }
  const order: Record<string, number> = { running: 0, stalled: 1, idle: 2, done: 3 };
  const main = s.agents.find((a) => a.id === 'main');
  const subs = s.agents.filter((a) => a.id !== 'main').sort((a, b) => (order[a.status] ?? 9) - (order[b.status] ?? 9) || Date.parse(b.lastAt) - Date.parse(a.lastAt));
  const nodeW = 232, nodeH = 50, gapX = 16, gapY = 16, pad = 20;
  const width = Math.max(nodeW + pad * 2, svg.parentElement?.clientWidth ?? 900);
  const cols = Math.max(1, Math.floor((width - pad * 2 + gapX) / (nodeW + gapX)));
  const gridW = cols * nodeW + (cols - 1) * gapX;
  const left = (width - gridW) / 2;
  const hubX = width / 2 - nodeW / 2, hubY = pad, top = hubY + nodeH + 70;
  const pos = subs.map((a, i) => ({ a, x: left + (i % cols) * (nodeW + gapX), y: top + Math.floor(i / cols) * (nodeH + gapY) }));
  const height = Math.max(240, (pos.length ? pos[pos.length - 1].y + nodeH : top) + pad + 30);
  svg.setAttribute('width', String(width));
  svg.setAttribute('height', String(height));
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  const out: [string, string][] = [];
  for (const p of pos) {
    const x1 = hubX + nodeW / 2, y1 = hubY + nodeH, x2 = p.x + nodeW / 2, y2 = p.y, my = (y1 + y2) / 2;
    out.push([`e-${p.a.id}`, `<path data-key="e-${esc(p.a.id)}" class="edge ${p.a.status}" d="M${x1},${y1} C${x1},${my} ${x2},${my} ${x2},${y2}"/>`]);
  }
  const node = (a: Agent, x: number, y: number, hub = false) => {
    const t = label(a);
    const max = a.errors ? 24 : 31;
    const t1 = t.length > max ? t.slice(0, max - 1) + '…' : t;
    const t2 = `${a.toolCalls} calls · ${a.edits} edits · ${fmtN(billable(a.usage))}`;
    return `<g class="anode ${a.status}${hub ? ' hub' : ''}${selected === a.id ? ' sel' : ''}${a.errors ? ' haserr' : ''}" data-id="${esc(a.id)}" data-key="n-${esc(a.id)}" tabindex="0" role="button" aria-label="${esc(t)}, ${a.status}" transform="translate(${x},${y})">
      <title>${esc(t)}${a.lastAction ? '\n' + esc(a.lastAction) : ''}</title>
      <rect class="box" width="${nodeW}" height="${nodeH}" rx="6"/>
      <circle class="dot" cx="15" cy="${nodeH / 2}" r="4"/>
      <text class="t1" x="28" y="21">${esc(t1)}</text>
      <text class="t2" x="28" y="38">${esc(t2)}</text>
      ${a.errors ? `<text class="tag err" x="${nodeW - 12}" y="21" text-anchor="end">${a.errors} err</text>` : ''}
    </g>`;
  };
  if (main) out.push([`n-main`, node(main, hubX, hubY, true)]);
  for (const p of pos) out.push([`n-${p.a.id}`, node(p.a, p.x, p.y)]);
  patch(svg, out, 'svg');
}

function renderRows() {
  const s = current();
  const now = Date.parse(snap.generatedAt) || Date.now();
  const list = (s?.agents ?? []).filter((a) => filter === 'all' || a.status === filter);
  const counts: Record<string, number> = {};
  for (const a of s?.agents ?? []) counts[a.status] = (counts[a.status] ?? 0) + 1;
  $('#filters').innerHTML = ['all', 'running', 'stalled', 'idle', 'done'].map((f) =>
    `<button class="chip${filter === f ? ' on' : ''}" data-f="${f}" aria-pressed="${filter === f}">${f}<span>${f === 'all' ? s?.agents.length ?? 0 : counts[f] ?? 0}</span></button>`).join('');
  const rows: [string, string][] = list.map((a) => [a.id, `<tr class="${selected === a.id ? 'sel' : ''}" data-id="${esc(a.id)}" data-key="${esc(a.id)}" tabindex="0">
      <td><span class="st ${a.status}" aria-hidden="true"></span><span class="nm">${esc(label(a))}</span><span class="sub">${esc(a.status)}${a.type ? ' · ' + esc(a.type) : ''}</span></td>
      <td class="r">${a.toolCalls}${a.errors ? ` <span class="pill err">${a.errors}</span>` : ''}</td>
      <td class="r">${a.edits}</td>
      <td class="r">${fmtN(billable(a.usage))}</td>
      <td class="la">${esc(a.lastAction ?? '')}</td>
      <td class="r faint">${ago(a.lastAt, now)}</td>
    </tr>`]);
  patch($('#rows'), rows.length ? rows : [['empty', `<tr data-key="empty"><td colspan="6" class="empty-note">No agents with status "${esc(filter)}".</td></tr>`]]);
}

function renderCollisions() {
  const s = current();
  const list = snap.collisions.filter((c) => c.session === s?.id);
  const name = (id: string) => label(s?.agents.find((a) => a.id === id) ?? ({ id } as Agent));
  $('#collisions').innerHTML = list.length
    ? list.slice(0, 30).map((c) => `<div class="coll"><code>${esc(short(c.file))}</code><span>${c.agents.map((id) => esc(name(id))).join(' · ')}</span></div>`).join('')
    : '<p class="empty-note">No file was edited by more than one agent.</p>';
}

function diff(e: Event) {
  const lines: string[] = [];
  if (e.old) for (const l of e.old.split('\n')) lines.push(`<span class="del">- ${esc(l)}</span>`);
  if (e.new) for (const l of e.new.split('\n')) lines.push(`<span class="add">+ ${esc(l)}</span>`);
  return `<pre class="diff">${lines.join('\n')}</pre>`;
}

function renderDetail(d: Detail) {
  const now = Date.parse(snap.generatedAt) || Date.now();
  const verdictClass: Record<string, string> = { supported: 'ok', stale: 'warn', contradicted: 'err', unverified: 'warn' };
  const events = [...d.events].reverse().slice(0, 200);
  $('#drawer').innerHTML = `
    <div class="dh">
      <div><h3>${esc(label(d))}</h3><p class="faint">${esc(d.id)} · ${esc(d.status)} · started ${ago(d.startedAt, now)} ago</p></div>
      <button class="btn ghost" id="close" type="button">Close</button>
    </div>
    <dl class="cells">
      <div><dt>tool calls</dt><dd>${d.toolCalls}</dd></div>
      <div><dt>errors</dt><dd class="${d.errors ? 'bad' : ''}">${d.errors}</dd></div>
      <div><dt>edits</dt><dd>${d.edits}</dd></div>
      <div><dt>tokens</dt><dd>${fmtN(billable(d.usage))}</dd></div>
      <div><dt>cache read</dt><dd>${fmtN(d.usage.cacheRead)}</dd></div>
    </dl>
    ${d.claims.length ? `<h4>Claim check</h4><table class="table claims"><tbody>${d.claims.map((c) => `<tr>
      <td><span class="pill ${verdictClass[c.verdict] ?? ''}">${esc(c.verdict)}</span></td>
      <td><b>${esc(c.kind)}</b><span class="sub">${esc(c.text)}</span><span class="sub mono">${esc(c.evidence ? '$ ' + c.evidence : c.note)}</span></td>
    </tr>`).join('')}</tbody></table>` : ''}
    ${d.final ? `<h4>Final report</h4><div class="final">${esc(d.final)}</div>` : ''}
    ${d.files.length ? `<h4>Files edited (${d.files.length})</h4><ul class="files">${d.files.map((f) => `<li title="${esc(f)}"><code>${esc(short(f))}</code></li>`).join('')}</ul>` : ''}
    <h4>Timeline</h4>
    <ol class="timeline">${events.map((e) => e.kind === 'text'
      ? `<li class="tx"><span class="tt">${ago(e.time, now)}</span><p>${esc(e.summary)}</p></li>`
      : `<li class="${e.isError ? 'bad' : ''}"><span class="tt">${ago(e.time, now)}</span>
          <details><summary><b>${esc(e.tool)}</b> <span class="mono">${esc(e.summary)}</span>${e.isError ? ' <span class="pill err">error</span>' : ''}</summary>
          ${e.edit ? diff(e) : ''}${e.output ? `<pre class="out">${esc(e.output)}</pre>` : ''}</details></li>`).join('')}</ol>`;
  $('#close').onclick = () => select('');
}

async function loadDetail() {
  const s = current();
  if (!selected || !s) return;
  const r = await fetch(`/api/agents/${encodeURIComponent(s.id)}/${encodeURIComponent(selected)}`);
  if (!r.ok) return;
  const open = new Set([...document.querySelectorAll('#drawer details[open] summary')].map((x) => x.textContent ?? ''));
  renderDetail(await r.json());
  document.querySelectorAll('#drawer details').forEach((d) => { if (open.has(d.querySelector('summary')?.textContent ?? '')) (d as HTMLDetailsElement).open = true; });
}

function select(id: string) {
  selected = id;
  const dr = $('#drawer');
  dr.hidden = !id;
  clearInterval(detailTimer);
  if (id) { loadDetail(); detailTimer = window.setInterval(loadDetail, 3000); }
  renderFleet();
  renderRows();
}

function renderAll() {
  renderStats();
  renderSessions();
  renderFleet();
  renderRows();
  renderCollisions();
}

async function poll() {
  try {
    const r = await fetch('/api/agents');
    if (r.ok) {
      snap = await r.json();
      if (!snap.sessions.some((s) => s.id === sessionId)) { sessionId = ''; sessionId = current()?.id ?? ''; }
      renderAll();
    }
  } catch {}
}

document.addEventListener('click', (ev) => {
  const t = ev.target as Element;
  if (t.closest('.sess.more')) { showSolo = !showSolo; renderSessions(); return; }
  const sess = t.closest<HTMLElement>('.sess');
  if (sess) { sessionId = sess.dataset.id ?? ''; select(''); renderAll(); return; }
  const chip = t.closest<HTMLElement>('.chip');
  if (chip) { filter = chip.dataset.f ?? 'all'; renderRows(); return; }
  const row = t.closest<HTMLElement>('#rows tr[data-id], .anode');
  if (row) select(row.dataset.id === selected ? '' : row.dataset.id ?? '');
});
document.addEventListener('keydown', (ev) => {
  const t = ev.target as HTMLElement;
  if ((ev.key === 'Enter' || ev.key === ' ') && t.matches('#rows tr[data-id], .anode')) { ev.preventDefault(); select(t.dataset.id ?? ''); }
  if (ev.key === 'Escape' && selected) select('');
});
$('#theme').addEventListener('click', () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  document.documentElement.dataset.theme = next;
  try { localStorage.setItem('runlens-theme', next); } catch {}
});

let resizeTimer = 0;
window.addEventListener('resize', () => {
  clearTimeout(resizeTimer);
  resizeTimer = window.setTimeout(renderFleet, 120);
});

poll();
setInterval(poll, 2000);
