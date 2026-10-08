export interface Dep {
  key: string;
  kind: 'db' | 'external';
  label: string;
  system?: string;
  calls: number;
  errors: number;
  ms: number;
  offsets: number[];
}

export interface Finding {
  fingerprint: string;
  statement: string;
  dbSystem?: string;
  count: number;
  totalMs: number;
  spanIds?: string[];
}

export interface Req {
  traceId: string;
  service: string;
  method?: string;
  route: string;
  path?: string;
  status?: number;
  startMs: number;
  durationMs: number;
  error: boolean;
  spans: number;
  dbCalls: number;
  dbMs: number;
  external: number;
  slow: boolean;
  nPlusOne?: Finding[];
  deps?: Dep[];
}

export interface SpanEvent {
  name: string;
  attrs?: Record<string, unknown>;
}

export interface SpanView {
  spanId: string;
  parentId?: string;
  name: string;
  kind: string;
  service: string;
  type: 'http' | 'db' | 'external' | 'internal';
  error?: boolean;
  statusMessage?: string;
  attrs?: Record<string, unknown>;
  events?: SpanEvent[];
  dbSystem?: string;
  statement?: string;
  peer?: string;
  depth: number;
  offsetMs: number;
  durMs: number;
  slow?: boolean;
  nPlusOne?: boolean;
}

export interface Detail extends Req {
  hints: string[];
  tree: SpanView[];
}

export interface Endpoint {
  service: string;
  endpoint: string;
  count: number;
  errors: number;
  p50Ms: number;
  p95Ms: number;
  maxMs: number;
  avgDbCalls: number;
  dbShare: number;
  slow: boolean;
  slowestTraceId: string;
  nPlusOne?: Finding[];
  hints: string[];
}

export interface Meta {
  version: string;
  app: { command?: string; runtime?: string; pid?: number; running: boolean; exitCode?: number };
  traces: number;
  capacity: number;
  slowMs: number;
  slowQueryMs: number;
  nPlusOneMin: number;
}

export const endpointOf = (r: Req) => (r.method ? `${r.method} ${r.route}` : r.route);
