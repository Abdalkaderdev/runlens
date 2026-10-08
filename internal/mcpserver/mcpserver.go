// Package mcpserver exposes captured traces to coding agents over MCP.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Abdalkaderdev/runlens/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Source returns the captured traces, newest first.
type Source func(context.Context) ([][]*store.Span, error)

type ListIn struct {
	Limit         int     `json:"limit,omitempty" jsonschema:"max rows to return, default 50"`
	Endpoint      string  `json:"endpoint,omitempty" jsonschema:"substring filter on 'METHOD /route' or path"`
	ErrorsOnly    bool    `json:"errors_only,omitempty" jsonschema:"only failed requests"`
	MinDurationMs float64 `json:"min_duration_ms,omitempty" jsonschema:"only requests slower than this"`
}

type Row struct {
	TraceID    string  `json:"trace_id"`
	Time       string  `json:"time"`
	Endpoint   string  `json:"endpoint"`
	Path       string  `json:"path,omitempty"`
	Status     int     `json:"status,omitempty"`
	DurationMs float64 `json:"duration_ms"`
	DBCalls    int     `json:"db_calls"`
	External   int     `json:"external_calls"`
	Error      bool    `json:"error"`
	NPlusOne   int     `json:"n_plus_one_patterns"`
}

type TraceIn struct {
	TraceID string `json:"trace_id" jsonschema:"trace id from list_requests"`
}

type SinceIn struct {
	Since string `json:"since,omitempty" jsonschema:"lookback window like 30s, 5m, 2h, or an RFC3339 time; default 15m"`
}

type SlowIn struct {
	Limit int    `json:"limit,omitempty" jsonschema:"max endpoints, default 10"`
	Since string `json:"since,omitempty" jsonschema:"lookback window like 5m or an RFC3339 time; default all captured"`
}

type N1In struct {
	Endpoint string `json:"endpoint,omitempty" jsonschema:"substring filter on 'METHOD /route'"`
}

type N1Row struct {
	Service      string  `json:"service"`
	Endpoint     string  `json:"endpoint"`
	Statement    string  `json:"statement"`
	DBSystem     string  `json:"db_system,omitempty"`
	MaxPerReq    int     `json:"max_per_request"`
	Requests     int     `json:"affected_requests"`
	TotalMs      float64 `json:"worst_total_ms"`
	ExampleTrace string  `json:"example_trace_id"`
}

func New(src Source, opt store.Options, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "runlens", Version: version}, &mcp.ServerOptions{
		Instructions: "runlens records live traces from the local dev server it launched. Use list_requests or slowest_endpoints first, then get_trace for span-level detail. SQL literals are redacted.",
	})
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "list_requests", Annotations: ro, Description: "Recent inbound HTTP requests, newest first, with status, duration, DB and outbound call counts."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ListIn) (*mcp.CallToolResult, any, error) {
			traces, err := src(ctx)
			if err != nil {
				return nil, nil, err
			}
			if in.Limit <= 0 {
				in.Limit = 50
			}
			rows := []Row{}
			for _, t := range traces {
				r := opt.Summarize(t)
				if r.Method == "" || (in.ErrorsOnly && !r.Error) || r.DurationMs < in.MinDurationMs {
					continue
				}
				if in.Endpoint != "" && !contains(r.Endpoint()+" "+r.Path, in.Endpoint) {
					continue
				}
				rows = append(rows, Row{
					TraceID: r.TraceID, Time: ms(r.StartMs), Endpoint: r.Endpoint(), Path: r.Path, Status: r.Status,
					DurationMs: roundMs(r.DurationMs), DBCalls: r.DBCalls, External: r.External, Error: r.Error, NPlusOne: len(r.NPlusOne),
				})
				if len(rows) == in.Limit {
					break
				}
			}
			return nil, map[string]any{"requests": rows, "traces_in_buffer": len(traces)}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_trace", Annotations: ro, Description: "Full span tree for one request: every span with offset and duration, SQL statements, outbound calls, errors with stack traces, N+1 findings and hints."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in TraceIn) (*mcp.CallToolResult, any, error) {
			traces, err := src(ctx)
			if err != nil {
				return nil, nil, err
			}
			id := strings.ToLower(strings.TrimSpace(in.TraceID))
			for _, t := range traces {
				if len(t) > 0 && t[0].TraceID == id {
					return nil, traceOut(opt.Detail(t)), nil
				}
			}
			return nil, nil, fmt.Errorf("trace %q not found; call list_requests for current ids", in.TraceID)
		})

	mcp.AddTool(s, &mcp.Tool{Name: "slowest_endpoints", Annotations: ro, Description: "Endpoints ranked by p95 latency, with p50/p95/max, error count, DB share, N+1 patterns and concrete hints."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SlowIn) (*mcp.CallToolResult, any, error) {
			traces, err := src(ctx)
			if err != nil {
				return nil, nil, err
			}
			since := time.Time{}
			if in.Since != "" {
				if since, err = parseSince(in.Since); err != nil {
					return nil, nil, err
				}
			}
			if in.Limit <= 0 {
				in.Limit = 10
			}
			eps := opt.Endpoints(traces, since)
			if len(eps) > in.Limit {
				eps = eps[:in.Limit]
			}
			return nil, map[string]any{"endpoints": eps, "slow_threshold_ms": opt.SlowMs}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "find_n_plus_one", Annotations: ro, Description: fmt.Sprintf("Query shapes that run %d or more times inside a single request (N+1), grouped by endpoint.", opt.NPlusOneMin)},
		func(ctx context.Context, _ *mcp.CallToolRequest, in N1In) (*mcp.CallToolResult, any, error) {
			traces, err := src(ctx)
			if err != nil {
				return nil, nil, err
			}
			idx := map[string]*N1Row{}
			var order []string
			for _, t := range traces {
				r := opt.Summarize(t)
				if in.Endpoint != "" && !contains(r.Endpoint(), in.Endpoint) {
					continue
				}
				for _, f := range r.NPlusOne {
					k := r.Service + "|" + r.Endpoint() + "|" + f.Fingerprint
					row := idx[k]
					if row == nil {
						row = &N1Row{Service: r.Service, Endpoint: r.Endpoint(), Statement: f.Statement, DBSystem: f.DBSystem}
						idx[k] = row
						order = append(order, k)
					}
					row.Requests++
					if f.Count > row.MaxPerReq {
						row.MaxPerReq, row.TotalMs, row.ExampleTrace = f.Count, f.TotalMs, r.TraceID
					}
				}
			}
			rows := []N1Row{}
			for _, k := range order {
				rows = append(rows, *idx[k])
			}
			return nil, map[string]any{"findings": rows, "threshold": opt.NPlusOneMin}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "errors_since", Annotations: ro, Description: "Failed spans (exceptions, error status, HTTP 5xx) in a time window, with messages and stack traces."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SinceIn) (*mcp.CallToolResult, any, error) {
			traces, err := src(ctx)
			if err != nil {
				return nil, nil, err
			}
			if in.Since == "" {
				in.Since = "15m"
			}
			since, err := parseSince(in.Since)
			if err != nil {
				return nil, nil, err
			}
			errs := opt.Errors(traces, since)
			if errs == nil {
				errs = []store.ErrorItem{}
			}
			return nil, map[string]any{"errors": errs, "since": since.Format(time.RFC3339)}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "service_map", Annotations: ro, Description: "Which endpoints call which databases and external hosts, with call counts, errors and average latency."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			traces, err := src(ctx)
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"edges": opt.ServiceMap(traces)}, nil
		})
	return s
}

type spanOut struct {
	Depth     int            `json:"depth"`
	Name      string         `json:"name"`
	Type      store.Type     `json:"type"`
	Service   string         `json:"service,omitempty"`
	OffsetMs  float64        `json:"offset_ms"`
	DurMs     float64        `json:"duration_ms"`
	Statement string         `json:"statement,omitempty"`
	DB        string         `json:"db,omitempty"`
	Peer      string         `json:"peer,omitempty"`
	Error     string         `json:"error,omitempty"`
	Stack     string         `json:"stack,omitempty"`
	Slow      bool           `json:"slow,omitempty"`
	NPlusOne  bool           `json:"n_plus_one,omitempty"`
	Attrs     map[string]any `json:"attributes,omitempty"`
}

func traceOut(d store.Detail) map[string]any {
	spans := make([]spanOut, 0, len(d.Tree))
	for _, v := range d.Tree {
		o := spanOut{Depth: v.Depth, Name: v.Name, Type: v.Type, OffsetMs: v.OffsetMs, DurMs: v.DurMs, Statement: v.Statement, DB: v.DBSystem, Peer: v.Peer, Slow: v.Slow, NPlusOne: v.NPlusOne}
		if v.Depth == 0 || v.Service != d.Service {
			o.Service = v.Service
		}
		if v.Error {
			o.Error = v.StatusMsg
			if o.Error == "" {
				o.Error = "error"
			}
		}
		for _, e := range v.Events {
			if e.Name == "exception" {
				if m, ok := e.Attrs["exception.message"].(string); ok {
					o.Error = m
				}
				o.Stack, _ = e.Attrs["exception.stacktrace"].(string)
			}
		}
		if v.Type == store.TypeHTTP || v.Type == store.TypeExternal {
			o.Attrs = pick(v.Attrs, "http.route", "url.full", "http.url", "url.path", "http.target", "http.response.status_code", "http.status_code")
		}
		spans = append(spans, o)
	}
	n1 := d.NPlusOne
	if n1 == nil {
		n1 = []store.Finding{}
	}
	for i := range n1 {
		n1[i].SpanIDs = nil
	}
	return map[string]any{
		"trace_id": d.TraceID, "service": d.Service, "endpoint": d.Endpoint(), "path": d.Path, "status": d.Status,
		"time": ms(d.StartMs), "duration_ms": roundMs(d.DurationMs), "error": d.Error, "db_calls": d.DBCalls, "db_ms": roundMs(d.DBMs),
		"external_calls": d.External, "n_plus_one": n1, "hints": d.Hints, "spans": spans,
	}
}

func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func contains(s, sub string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(sub)) }

func ms(unixMs float64) string {
	return time.UnixMilli(int64(unixMs)).Format(time.RFC3339Nano)
}

func roundMs(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func parseSince(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("since %q: use a duration like 5m or an RFC3339 time", s)
}
