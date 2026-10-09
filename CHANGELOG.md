# Changelog

All notable changes to this project are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-10-10

### Added

- `runlens <cmd>` launches a dev server with OpenTelemetry auto-instrumentation injected for Node (`NODE_OPTIONS=--import`) and Python (`sitecustomize` boot dir), installed once into the user cache.
- OTLP receiver: HTTP protobuf, HTTP JSON and gRPC.
- In-memory trace buffer with SSE live stream and JSON API.
- Web UI: animated service map, request list with filters, span waterfall with SQL, errors and stacks, endpoint p50/p95 and hints, light and dark themes.
- N+1 detection, slow query, DB share and sequential outbound call hints.
- Redaction of SQL literals, DB parameters, auth headers and URL query values by default.
- MCP server over streamable HTTP (`/mcp`) and stdio (`runlens mcp`) with `list_requests`, `get_trace`, `slowest_endpoints`, `find_n_plus_one`, `errors_since` and `service_map`.
- Example apps: Express + Postgres + Redis, FastAPI + SQLite.
