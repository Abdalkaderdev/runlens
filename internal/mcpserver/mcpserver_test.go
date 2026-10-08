package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Abdalkaderdev/runlens/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func traces() [][]*store.Span {
	now := time.Now().UnixNano()
	mk := func(id, route string, lookups int, status int, failed bool) []*store.Span {
		root := &store.Span{TraceID: id, SpanID: id + "r", Name: "GET " + route, Kind: store.KindServer, Service: "shop", Start: now, End: now + int64(50*time.Millisecond), Error: failed,
			Attrs: map[string]any{"http.request.method": "GET", "http.route": route, "http.response.status_code": int64(status)}}
		if failed {
			root.Events = []store.Event{{Name: "exception", Attrs: map[string]any{"exception.message": "card declined", "exception.stacktrace": "at checkout (server.js:73)"}}}
		}
		out := []*store.Span{root}
		for i := range lookups {
			s := &store.Span{TraceID: id, SpanID: fmt.Sprint(id, i), ParentID: root.SpanID, Name: "pg.query", Kind: store.KindClient, Service: "shop",
				Start: now + int64(i)*1e6, End: now + int64(i+1)*1e6, Attrs: map[string]any{"db.system": "postgresql", "db.statement": fmt.Sprintf("SELECT * FROM books WHERE author_id = %d", i)}}
			s.Classify(true)
			out = append(out, s)
		}
		root.Classify(true)
		return out
	}
	return [][]*store.Span{mk("aa01", "/checkout", 1, 500, true), mk("aa02", "/authors", 8, 200, false)}
}

func session(t *testing.T) *mcp.ClientSession {
	t.Helper()
	src := func(context.Context) ([][]*store.Span, error) { return traces(), nil }
	srvT, cliT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := New(src, store.Defaults, "test").Connect(ctx, srvT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, cliT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s: %v", name, res.Content[0].(*mcp.TextContent).Text)
	}
	var out map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &out)
	return out
}

func TestTools(t *testing.T) {
	cs := session(t)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "list_requests") || len(names) != 6 {
		t.Fatalf("tools = %s", got)
	}

	reqs := call(t, cs, "list_requests", map[string]any{"errors_only": true})["requests"].([]any)
	if len(reqs) != 1 || reqs[0].(map[string]any)["endpoint"] != "GET /checkout" {
		t.Fatalf("list_requests = %v", reqs)
	}

	tr := call(t, cs, "get_trace", map[string]any{"trace_id": "AA02"})
	if tr["endpoint"] != "GET /authors" || len(tr["spans"].([]any)) != 9 || len(tr["n_plus_one"].([]any)) != 1 {
		t.Fatalf("get_trace = %v", tr)
	}

	n1 := call(t, cs, "find_n_plus_one", nil)["findings"].([]any)
	f := n1[0].(map[string]any)
	if len(n1) != 1 || f["max_per_request"] != float64(8) || f["statement"] != "SELECT * FROM books WHERE author_id = ?" {
		t.Fatalf("find_n_plus_one = %v", n1)
	}

	eps := call(t, cs, "slowest_endpoints", map[string]any{"limit": 1})["endpoints"].([]any)
	if len(eps) != 1 {
		t.Fatalf("slowest_endpoints = %v", eps)
	}

	errs := call(t, cs, "errors_since", map[string]any{"since": "5m"})["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["message"] != "card declined" {
		t.Fatalf("errors_since = %v", errs)
	}

	edges := call(t, cs, "service_map", nil)["edges"].([]any)
	if len(edges) != 2 || edges[0].(map[string]any)["calls"] != float64(8) {
		t.Fatalf("service_map = %v", edges)
	}
}

func TestGetTraceUnknown(t *testing.T) {
	cs := session(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_trace", Arguments: map[string]any{"trace_id": "nope"}})
	if err != nil || !res.IsError {
		t.Fatalf("want tool error, got %v %v", res, err)
	}
}

func TestBadSince(t *testing.T) {
	cs := session(t)
	res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "errors_since", Arguments: map[string]any{"since": "yesterday"}})
	if !res.IsError {
		t.Fatal("want tool error for bad since")
	}
}
