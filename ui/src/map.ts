import { endpointOf, type Req } from './types';

type Kind = 'route' | 'svc' | 'dep';
type Pt = [number, number];

interface Node {
  id: string;
  kind: Kind;
  label: string;
  sub: string;
  tags: { text: string; err?: boolean }[];
  err: boolean;
  x: number;
  y: number;
  w: number;
  h: number;
  flash: number;
  fc?: Particle['color'];
  endpoint?: string;
}

interface Edge {
  id: string;
  a: string;
  b: string;
  width: number;
  err: boolean;
  ingress?: boolean;
  path: [Pt, Pt, Pt, Pt];
}

interface Particle {
  path: [Pt, Pt, Pt, Pt];
  start: number;
  dur: number;
  color: 'base' | 'slow' | 'err';
  target: string;
  hit: boolean;
}

const NODE_H = 48;
const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));
const fmtMs = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(2)}s` : ms >= 10 ? `${Math.round(ms)}ms` : `${ms.toFixed(1)}ms`);

function bez(p: [Pt, Pt, Pt, Pt], t: number): Pt {
  const u = 1 - t;
  const a = u * u * u, b = 3 * u * u * t, c = 3 * u * t * t, d = t * t * t;
  return [a * p[0][0] + b * p[1][0] + c * p[2][0] + d * p[3][0], a * p[0][1] + b * p[1][1] + c * p[2][1] + d * p[3][1]];
}

function curve(from: Pt, to: Pt): [Pt, Pt, Pt, Pt] {
  const dx = Math.max(30, (to[0] - from[0]) * 0.5);
  return [from, [from[0] + dx, from[1]], [to[0] - dx, to[1]], to];
}

function p95(xs: number[]) {
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.max(0, Math.ceil(0.95 * s.length) - 1)] ?? 0;
}

export class ServiceMap {
  private nodes = new Map<string, Node>();
  private edges = new Map<string, Edge>();
  private routeLinks = new Map<string, Set<string>>();
  private particles: Particle[] = [];
  private ctx: CanvasRenderingContext2D;
  private raf = 0;
  private colors = { base: '#fff', slow: '#f44e00', err: '#f2554a', halo: '#f44e00' };
  private reduce = matchMedia('(prefers-reduced-motion: reduce)');
  private hovered: string | null = null;
  private last: Req[] = [];
  private hidden = 0;
  slowMs = 500;
  selected: string | null = null;
  onRoute: (endpoint: string | null) => void = () => {};

  constructor(
    private wrap: HTMLElement,
    private svg: SVGSVGElement,
    private canvas: HTMLCanvasElement,
  ) {
    this.ctx = canvas.getContext('2d')!;
    new ResizeObserver(() => this.update(this.last)).observe(wrap);
    svg.addEventListener('pointerover', (e) => this.hover((e.target as Element).closest<SVGGElement>('.node.route')?.dataset.id ?? null));
    svg.addEventListener('pointerleave', () => this.hover(null));
    svg.addEventListener('focusin', (e) => this.hover((e.target as Element).closest<SVGGElement>('.node.route')?.dataset.id ?? null));
    svg.addEventListener('focusout', () => this.hover(null));
    const pick = (e: Event) => {
      const g = (e.target as Element).closest<SVGGElement>('.node.route');
      if (!g) return;
      const ep = this.nodes.get(g.dataset.id!)?.endpoint ?? null;
      this.onRoute(ep === this.selected ? null : ep);
    };
    svg.addEventListener('click', pick);
    svg.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        pick(e);
      }
    });
  }

  private readColors() {
    const cs = getComputedStyle(document.documentElement);
    const v = (n: string) => cs.getPropertyValue(n).trim();
    this.colors = { base: v('--particle'), slow: v('--accent'), err: v('--err'), halo: v('--accent') };
  }

  update(reqs: Req[]) {
    this.last = reqs;
    this.readColors();
    const W = this.wrap.clientWidth;
    const H = this.wrap.clientHeight;
    if (!W || !H) return;
    const dpr = devicePixelRatio || 1;
    if (this.canvas.width !== Math.round(W * dpr) || this.canvas.height !== Math.round(H * dpr)) {
      this.canvas.width = Math.round(W * dpr);
      this.canvas.height = Math.round(H * dpr);
      this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    }

    const now = Date.now();
    const routes = new Map<string, { svc: string; ep: string; durs: number[]; recent: number; errs: number; n1: boolean; deps: Set<string>; last: number }>();
    const svcs = new Map<string, { recent: number; count: number }>();
    const deps = new Map<string, { label: string; system: string; calls: number; ms: number; errs: number }>();
    const flows = new Map<string, { recent: number; errs: number }>();
    const bump = (id: string, recent: boolean, err: boolean) => {
      const f = flows.get(id) ?? { recent: 0, errs: 0 };
      if (recent) f.recent++;
      if (err) f.errs++;
      flows.set(id, f);
    };

    for (const r of reqs) {
      const rid = `r:${r.service}|${endpointOf(r)}`;
      const sid = `s:${r.service}`;
      const recent = now - r.startMs < 60_000;
      const rt = routes.get(rid) ?? { svc: sid, ep: endpointOf(r), durs: [], recent: 0, errs: 0, n1: false, deps: new Set<string>(), last: 0 };
      rt.durs.push(r.durationMs);
      if (recent) rt.recent++;
      if (r.error) rt.errs++;
      if (r.nPlusOne?.length) rt.n1 = true;
      rt.last = Math.max(rt.last, r.startMs);
      routes.set(rid, rt);
      const sv = svcs.get(sid) ?? { recent: 0, count: 0 };
      sv.count++;
      if (recent) sv.recent++;
      svcs.set(sid, sv);
      bump(`in>${rid}`, recent, r.error);
      bump(`${rid}>${sid}`, recent, r.error);
      for (const d of r.deps ?? []) {
        const did = `d:${d.key}`;
        const dp = deps.get(did) ?? { label: d.label, system: d.kind === 'db' ? d.system ?? 'db' : 'http', calls: 0, ms: 0, errs: 0 };
        dp.calls += d.calls;
        dp.ms += d.ms;
        dp.errs += d.errors;
        deps.set(did, dp);
        rt.deps.add(did);
        const f = flows.get(`${sid}>${did}`) ?? { recent: 0, errs: 0 };
        if (recent) f.recent += d.calls;
        f.errs += d.errors;
        flows.set(`${sid}>${did}`, f);
      }
    }

    const top = 34, bottom = 40;
    const avail = H - top - bottom;
    const maxRows = Math.max(1, Math.floor((avail + 12) / (NODE_H + 12)));
    const score = (r: { durs: number[]; errs: number; n1: boolean }) => (r.n1 || r.errs || p95(r.durs) > this.slowMs ? 1e6 : 0) + r.durs.length;
    const routeList = [...routes.entries()].sort((a, b) => score(b[1]) - score(a[1]) || a[1].ep.localeCompare(b[1].ep)).slice(0, maxRows);
    this.hidden = routes.size - routeList.length;
    routeList.sort((a, b) => a[1].ep.localeCompare(b[1].ep));
    const depList = [...deps.entries()].sort((a, b) => b[1].calls - a[1].calls).slice(0, maxRows);
    const svcList = [...svcs.keys()].sort().slice(0, maxRows);

    const rw = clamp(W * 0.27, 170, 280);
    const sw = clamp(W * 0.17, 140, 220);
    const dw = clamp(W * 0.24, 160, 250);
    const rx = 48;
    const dx = W - 20 - dw;
    const sx = (rx + rw + dx) / 2 - sw / 2;
    const place = (n: number, i: number) => {
      const gap = n > 1 ? Math.min(22, (avail - n * NODE_H) / (n - 1)) : 0;
      const total = n * NODE_H + (n - 1) * gap;
      return top + (avail - total) / 2 + i * (NODE_H + gap);
    };

    const old = this.nodes;
    this.nodes = new Map();
    this.edges = new Map();
    this.routeLinks = new Map();
    const keep = (n: Node) => {
      n.flash = old.get(n.id)?.flash ?? 0;
      n.fc = old.get(n.id)?.fc;
      this.nodes.set(n.id, n);
    };

    routeList.forEach(([id, r], i) => {
      const p = p95(r.durs);
      const tags: Node['tags'] = [];
      if (r.n1) tags.push({ text: 'N+1' });
      if (p > this.slowMs) tags.push({ text: 'SLOW' });
      if (r.errs) tags.push({ text: `${r.errs} ERR`, err: true });
      keep({ id, kind: 'route', label: r.ep, sub: `${r.durs.length} req  p95 ${fmtMs(p)}`, tags, err: r.errs > 0, x: rx, y: place(routeList.length, i), w: rw, h: NODE_H, flash: 0, endpoint: r.ep });
    });
    svcList.forEach((id, i) => {
      const nRoutes = [...routes.values()].filter((r) => r.svc === id).length;
      keep({ id, kind: 'svc', label: id.slice(2), sub: `${nRoutes} route${nRoutes === 1 ? '' : 's'}`, tags: [], err: false, x: sx, y: place(svcList.length, i), w: sw, h: NODE_H, flash: 0 });
    });
    depList.forEach(([id, d], i) => {
      keep({ id, kind: 'dep', label: d.label, sub: `${d.calls} calls  avg ${fmtMs(d.ms / Math.max(1, d.calls))}`, tags: [{ text: d.system.toUpperCase(), err: false }], err: d.errs > 0, x: dx, y: place(depList.length, i), w: dw, h: NODE_H, flash: 0 });
    });

    const width = (n: number) => 1.2 + Math.log2(1 + n) * 1.4;
    const link = (id: string, a: string, b: string, ingress = false) => {
      const na = this.nodes.get(a), nb = this.nodes.get(b);
      if (!nb || (!na && !ingress)) return;
      const f = flows.get(id) ?? { recent: 0, errs: 0 };
      const from: Pt = na ? [na.x + na.w, na.y + na.h / 2] : [0, nb.y + nb.h / 2];
      this.edges.set(id, { id, a, b, width: ingress ? 1 : width(f.recent), err: f.errs > 0 && !ingress, ingress, path: curve(from, [nb.x, nb.y + nb.h / 2]) });
    };
    for (const [rid, r] of routeList) {
      link(`in>${rid}`, 'in', rid, true);
      link(`${rid}>${r.svc}`, rid, r.svc);
      const on = new Set([rid, r.svc, `in>${rid}`, `${rid}>${r.svc}`]);
      for (const d of r.deps) {
        link(`${r.svc}>${d}`, r.svc, d);
        on.add(d).add(`${r.svc}>${d}`);
      }
      this.routeLinks.set(rid, on);
    }

    this.render(W, H, reqs.length === 0);
    if (this.hovered) this.hover(this.hovered);
    this.kick();
  }

  private render(W: number, H: number, empty: boolean) {
    this.svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
    if (empty) {
      this.svg.innerHTML = '';
      return;
    }
    const cols = [...this.nodes.values()];
    const colLabel = (kind: Kind, text: string) => {
      const n = cols.find((c) => c.kind === kind);
      return n ? `<text class="col-label" x="${n.x}" y="20">${text}</text>` : '';
    };
    let out = colLabel('route', this.hidden ? `Routes (${this.hidden} more not shown)` : 'Routes') + colLabel('svc', 'Service') + colLabel('dep', 'Databases &amp; APIs');
    for (const e of this.edges.values()) {
      const [a, b, c, d] = e.path;
      out += `<path class="edge${e.err ? ' err' : ''}${e.ingress ? ' ingress' : ''}" data-id="${esc(e.id)}" stroke-width="${e.width.toFixed(2)}" d="M${a[0]},${a[1]} C${b[0]},${b[1]} ${c[0]},${c[1]} ${d[0]},${d[1]}"/>`;
    }
    for (const n of this.nodes.values()) {
      const charW = n.kind === 'svc' ? 7.6 : 7.3;
      const tagW = n.tags.reduce((s, t) => s + t.text.length * 6.4 + 8, 0);
      const room = Math.max(4, Math.floor((n.w - 26 - (n.kind === 'dep' ? 0 : tagW)) / charW));
      const label = n.label.length > room ? n.label.slice(0, room - 1) + '…' : n.label;
      let tx = n.x + n.w - 10;
      let tags = '';
      for (const t of [...n.tags].reverse()) {
        tags += `<text class="tag${t.err ? ' err' : ''}" x="${tx}" y="${n.y + 19}" text-anchor="end">${esc(t.text)}</text>`;
        tx -= t.text.length * 6.4 + 8;
      }
      const flag = n.kind === 'route' && n.tags.length ? `<rect class="flag" x="${n.x}" y="${n.y + 10}" width="2" height="${n.h - 20}" rx="1"/>` : '';
      const role = n.kind === 'route' ? ` tabindex="0" role="button" aria-pressed="${n.endpoint === this.selected}" aria-label="${esc(`${n.label}, ${n.sub}. Filter requests`)}"` : '';
      const sel = n.kind === 'route' && n.endpoint === this.selected ? ' sel' : '';
      out += `<g class="node ${n.kind}${n.err ? ' err' : ''}${sel}" data-id="${esc(n.id)}"${role}>
        <rect class="box" x="${n.x}" y="${n.y}" width="${n.w}" height="${n.h}" rx="6"/>${flag}
        <text class="t1" x="${n.x + 12}" y="${n.y + 20}">${esc(label)}</text>
        <text class="t2" x="${n.x + 12}" y="${n.y + 37}">${esc(n.sub)}</text>${tags}</g>`;
    }
    this.svg.innerHTML = out;
  }

  private hover(id: string | null) {
    this.hovered = id;
    const on = id ? this.routeLinks.get(id) : undefined;
    this.svg.classList.toggle('focus', !!on);
    for (const el of this.svg.querySelectorAll<SVGElement>('[data-id]')) {
      el.classList.toggle('on', !!on?.has(el.dataset.id!));
    }
  }

  pulse(r: Req) {
    if (this.reduce.matches || document.hidden) return;
    const rid = `r:${r.service}|${endpointOf(r)}`;
    const sid = `s:${r.service}`;
    const color: Particle['color'] = r.error ? 'err' : r.slow ? 'slow' : 'base';
    const t = performance.now();
    const add = (edge: string, start: number, dur: number, target: string) => {
      const e = this.edges.get(edge);
      if (e) this.particles.push({ path: e.path, start: t + start, dur, color, target, hit: false });
    };
    add(`in>${rid}`, 0, 420, rid);
    add(`${rid}>${sid}`, 420, 380, sid);
    for (const d of r.deps ?? []) {
      const offs = d.offsets.length > 12 ? d.offsets.filter((_, i) => i % Math.ceil(d.offsets.length / 12) === 0) : d.offsets;
      for (const o of offs) add(`${sid}>d:${d.key}`, 800 + o * 900, 520, `d:${d.key}`);
    }
    if (this.particles.length > 1500) this.particles.splice(0, this.particles.length - 1500);
    this.kick();
  }

  private kick() {
    if (!this.raf) this.raf = requestAnimationFrame(this.frame);
  }

  private frame = (now: number) => {
    this.raf = 0;
    const ctx = this.ctx;
    ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
    const wall = Date.now();
    let busy = false;

    for (const n of this.nodes.values()) {
      const age = wall - n.flash;
      if (age < 500) {
        busy = true;
        ctx.globalAlpha = 0.9 * (1 - age / 500);
        ctx.strokeStyle = n.fc === 'err' ? this.colors.err : this.colors.halo;
        ctx.lineWidth = 1.5;
        const g = 3 + (age / 500) * 3;
        ctx.beginPath();
        ctx.roundRect(n.x - g, n.y - g, n.w + 2 * g, n.h + 2 * g, 8);
        ctx.stroke();
      }
    }

    this.particles = this.particles.filter((p) => now < p.start + p.dur);
    for (const p of this.particles) {
      busy = true;
      if (now < p.start) continue;
      const t = (now - p.start) / p.dur;
      const ease = t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
      const col = this.colors[p.color];
      for (let k = 4; k >= 0; k--) {
        const tt = Math.max(0, ease - k * 0.035);
        const [x, y] = bez(p.path, tt);
        ctx.globalAlpha = k === 0 ? 1 : 0.28 * (1 - k / 5);
        ctx.fillStyle = col;
        ctx.beginPath();
        ctx.arc(x, y, k === 0 ? 2.8 : 2.2, 0, Math.PI * 2);
        ctx.fill();
      }
      if (!p.hit && t > 0.97) {
        p.hit = true;
        const n = this.nodes.get(p.target);
        if (n) {
          n.flash = wall;
          n.fc = p.color;
        }
      }
    }
    ctx.globalAlpha = 1;
    if (busy) this.kick();
  };
}
