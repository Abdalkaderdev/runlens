# Design

## Goals

- One command in front of an existing dev command. No SDK, no config files, no code changes.
- Everything local: one Go binary, loopback only, in-memory.
- The same data for humans (UI) and coding agents (MCP).

## Process model

```
runlens <cmd>
  ├─ HTTP :4318  OTLP/HTTP  /v1/traces   (protobuf or JSON)
  │              UI         /            (embedded Vite build)
  │              API        /api/*       (JSON + SSE stream)
  │              MCP        /mcp         (streamable HTTP)
  ├─ gRPC :4317  OTLP/gRPC  TraceService/Export
  └─ child: <cmd> with OTEL_* env and a runtime hook
```

If 4318 or 4317 is taken, runlens binds a free port and passes it to the child. The chosen URL is written to `<user cache>/runlens/last-url` so `runlens mcp` finds it.

## Instrumentation injection

Detection is by command name (`npm`, `node`, `tsx`, `next`, ... vs `python*`, `uv`, `uvicorn`, `flask`, ...). `--runtime` overrides it.

**Node.** On first run runlens writes a pinned `package.json` and `register.mjs` into `<user cache>/runlens/node/<hash>` and runs `npm install` there. The child gets `NODE_OPTIONS=--import=file:///.../register.mjs`. The loader registers `@opentelemetry/instrumentation/hook.mjs` through `module.register` (ESM) and requires `@opentelemetry/auto-instrumentations-node/register` (CJS and the SDK). Package-manager processes (`npm`, `npx`, `pnpm`, `yarn`, `corepack`) are skipped so `npm run dev` does not trace npm itself. `fs`, `dns` and `net` instrumentations are disabled because they add noise without adding signal.

**Python.** runlens finds the interpreter the command will use (the command itself, `uv run python`, a sibling `python` next to the script, `.venv`, then `PATH`) and installs `opentelemetry-distro`, the OTLP/HTTP exporter and a fixed list of instrumentations into `<user cache>/runlens/python/<version>-<os>-<hash>/site` with `uv pip install --target` (falls back to `pip`). A boot directory holding `sitecustomize.py` goes first on `PYTHONPATH`. That file:

1. Runs the user's own `sitecustomize.py` if one exists further down `sys.path`.
2. Appends the cache site directory to the end of `sys.path`, so the app's own packages always win.
3. Sets `OTEL_EXPORTER_OTLP_ENDPOINT` from `RUNLENS_OTLP_ENDPOINT`, calls `opentelemetry.instrumentation.auto_instrumentation.initialize()`, and restores the old value. FastAPI 0.14x reads that variable to set up its own exporter; scoping it to SDK startup avoids a second exporter and its startup error.
4. Adds `execute`/`executemany` to the DB-API connection proxy. The upstream `sqlite3` instrumentation only traces `cursor().execute`, so `conn.execute(...)` would be invisible otherwise.

Nothing is written into the user's project or virtualenv.

## Storage

`store.Store` holds the last `--capacity` traces (default 2000) in a map plus an insertion-order slice. Spans for a trace can arrive across batches and are merged and deduplicated by span ID. Each `Add` notifies subscribers with the changed trace IDs, which drives the SSE stream.

## Analysis

All analysis is computed on read from spans; nothing is precomputed.

- **Classification** reads both legacy and stable semantic conventions (`db.statement` / `db.query.text`, `http.method` / `http.request.method`, `net.peer.name` / `server.address`, ...). DB spans without a statement (e.g. `pg-pool.connect`) count as internal.
- **Fingerprint**: string and numeric literals become `?`, `IN (...)` and `VALUES (...)` lists collapse. With redaction on (default) the fingerprint replaces the statement, DB parameter attributes and auth/cookie headers become `[redacted]`, and URL query values become `_`.
- **N+1**: a fingerprint executed `--n1` (default 5) or more times in one trace.
- **Hints**: N+1, single queries over `--slow-query`, DB time over 60% of the request, two or more outbound calls that never overlap, requests over `--slow`.

## UI

Vite + TypeScript, no framework. The service map is SVG (nodes, edges, labels) with a canvas overlay for particles and node halos. Columns are routes, services, then databases and external hosts. Edge width follows calls in the last 60 s. A request animates as particles from the left edge into its route, into the service, then to each dependency at its real relative start offset. Routes with N+1, slow p95 or errors are kept on screen first when space runs out. Reduced-motion users get no particles. The selected trace lives in the URL hash.

## MCP

Built on `github.com/modelcontextprotocol/go-sdk` (spec 2026-07-28). Tools take a `Source` function so the same handlers serve the in-process store (streamable HTTP at `/mcp`) and `runlens mcp` (stdio), which fetches `/api/export` from a running instance on every call. Tool outputs are plain JSON objects without output schemas.

## Security

- Listens on 127.0.0.1 only.
- Requests whose `Host` is not a loopback name get 403 (DNS rebinding).
- Go's `http.CrossOriginProtection` rejects cross-site state-changing browser requests.
- Redaction is on by default; `--show-values` turns it off.

## Known limits

- In-memory only; restarting runlens drops history.
- Python on Windows before 3.13 has a coarse clock, so sub-millisecond spans show as 0 ms.
- The Python boot `sitecustomize.py` runs the first user `sitecustomize.py` it finds; a `sitecustomize` package (directory) is not chained.
- Node needs `module.register` (Node 18.19+ / 20.6+).
- Bun and Deno are not injected; point their OTLP exporters at runlens instead.
