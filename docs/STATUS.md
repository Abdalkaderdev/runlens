# Status

Paused WIP. Branch `wip`.

## Name

`xray` collides with XTLS/Xray-core (42k stars) and chaitin/xray (11k). Renamed to `runlens`; `Abdalkaderdev/runlens` and npm `runlens` are free. Repo lives at `C:\Users\max\abd\runlens`.

## Done (compiles, `go vet` clean, untested)

- `internal/store`: span model, semconv classification (legacy and stable attribute names), SQL normalizer/fingerprint, redaction (SQL literals, db params, auth headers, URL query values), in-memory trace ring buffer with pub/sub, analysis: request summaries, N+1 detection, endpoint p50/p95 stats and hints, errors with stacks, service map edges.
- `internal/otlp`: OTLP/HTTP protobuf receiver (gzip), gRPC trace service, metrics/logs discard.
- `internal/mcpserver`: MCP server on go-sdk v1.8.0 (spec 2026-07-28) with `list_requests`, `get_trace`, `slowest_endpoints`, `find_n_plus_one`, `errors_since`, `service_map`, backed by a `Source` func so stdio mode can pull from a running instance.
- `internal/server`: one HTTP handler for `/v1/traces`, `/mcp`, `/api/*` (meta, requests, traces/{id}, endpoints, export, SSE stream, clear) and SPA UI; loopback-host check and cross-origin protection.

## Verified

Nothing at runtime yet. Only build and vet.

## Next

1. `main.go`: CLI (`runlens <cmd>`, `runlens mcp` stdio bridge via `/api/export`, `runlens` standalone), port selection (4318 HTTP, 4317 gRPC, fall back to free ports), child launch with OTel env, browser open, `ui` embed package.
2. `internal/launch`: Node via cached `@opentelemetry/auto-instrumentations-node` + `--import` loader that calls `module.register('@opentelemetry/instrumentation/hook.mjs')` and requires `.../register`; skip npm/npx/pnpm/yarn processes. Python via `uv pip install --target <cache>/py<ver> --python <user python>` and a boot dir with `sitecustomize.py` that appends the cache to `sys.path` and calls `opentelemetry.instrumentation.auto_instrumentation.initialize()`. Env: `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_BSP_SCHEDULE_DELAY=200`, metrics/logs exporters `none`, Node resource detectors `env,process`.
3. UI (Vite + TS, SVG map + canvas particles, Geist + JetBrains Mono, #f44e00). Load `design-taste-frontend` and `web-design-guidelines` skills first.
4. Examples: Express + Postgres (N+1, outbound to registry.npmjs.org), FastAPI + SQLite (outbound to pypi.org).
5. Tests: receiver, store, N+1, MCP handlers (in-memory transport).
6. Repo: README, LICENSE, CHANGELOG, .gitignore, .gitattributes, docs/design.md, CI matrix, goreleaser.
7. End-to-end run, MCP check with a real client, Playwright screenshots and GIF (ffmpeg is installed).

## Open problems

- OTLP/JSON not accepted (protojson would mis-decode hex IDs); returns 415.
- MCP tool outputs use `Out=any` to skip output-schema validation (nil slices as null would fail).
- Python sitecustomize injection shadows a user's own `sitecustomize.py`.
- Repo dir is `runlens`, not `xray`; this file is at `runlens/docs/STATUS.md`.
