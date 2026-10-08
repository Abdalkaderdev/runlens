package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const ms = int64(time.Millisecond)

// fixture builds one request trace: a server span, a list query, n lookups
// with the same shape, and the given number of sequential outbound calls.
func fixture(trace string, start int64, lookups, outbound int, status int) []*Span {
	root := &Span{TraceID: trace, SpanID: trace + "-root", Name: "GET /authors", Kind: KindServer, Service: "shop", Start: start, End: start + 40*ms,
		Attrs: map[string]any{"http.request.method": "GET", "http.route": "/authors", "url.path": "/authors", "http.response.status_code": int64(status)}}
	spans := []*Span{root}
	add := func(s *Span) {
		s.TraceID, s.ParentID, s.Service = trace, root.SpanID, "shop"
		s.SpanID = fmt.Sprintf("%s-%d", trace, len(spans))
		s.Classify(true)
		spans = append(spans, s)
	}
	at := start + ms
	add(&Span{Name: "pg.query", Kind: KindClient, Start: at, End: at + ms, Attrs: map[string]any{"db.system": "postgresql", "db.name": "shop", "db.statement": "SELECT id FROM authors"}})
	for i := range lookups {
		at += 2 * ms
		add(&Span{Name: "pg.query", Kind: KindClient, Start: at, End: at + ms, Attrs: map[string]any{"db.system": "postgresql", "db.name": "shop", "db.statement": fmt.Sprintf("SELECT * FROM books WHERE author_id = %d", i)}})
	}
	for range outbound {
		at += 2 * ms
		add(&Span{Name: "GET", Kind: KindClient, Start: at, End: at + ms, Attrs: map[string]any{"http.request.method": "GET", "url.full": "https://registry.npmjs.org/x"}})
	}
	root.Classify(true)
	return spans
}

func TestNPlusOne(t *testing.T) {
	o := Defaults
	got := o.NPlusOne(fixture("t1", 0, 12, 0, 200))
	if len(got) != 1 || got[0].Count != 12 || got[0].Statement != "SELECT * FROM books WHERE author_id = ?" {
		t.Fatalf("findings = %+v", got)
	}
	if len(got[0].SpanIDs) != 12 {
		t.Fatalf("span ids = %d", len(got[0].SpanIDs))
	}
	if got := o.NPlusOne(fixture("t2", 0, 4, 0, 200)); len(got) != 0 {
		t.Fatalf("4 lookups is under the threshold, got %+v", got)
	}
}

func TestSummarize(t *testing.T) {
	r := Defaults.Summarize(fixture("t", 5*ms, 6, 2, 503))
	if r.Endpoint() != "GET /authors" || r.Status != 503 || !r.Error || r.DBCalls != 7 || r.External != 2 {
		t.Fatalf("summary = %+v", r)
	}
	if len(r.Deps) != 2 || r.Deps[0].Label != "postgresql · shop" || r.Deps[0].Calls != 7 || r.Deps[1].Label != "registry.npmjs.org" {
		t.Fatalf("deps = %+v", r.Deps)
	}
	if len(r.NPlusOne) != 1 {
		t.Fatalf("n+1 = %+v", r.NPlusOne)
	}
}

func TestDetailTreeAndHints(t *testing.T) {
	d := Defaults.Detail(fixture("t", 0, 6, 3, 200))
	if d.Tree[0].Depth != 0 || d.Tree[1].Depth != 1 || len(d.Tree) != 1+1+6+3 {
		t.Fatalf("tree = %+v", d.Tree)
	}
	n1 := 0
	for _, s := range d.Tree {
		if s.NPlusOne {
			n1++
		}
	}
	if n1 != 6 {
		t.Fatalf("n+1 spans marked = %d", n1)
	}
	h := strings.Join(d.Hints, "\n")
	if !strings.Contains(h, "N+1") || !strings.Contains(h, "3 outbound calls (registry.npmjs.org) run one after another") {
		t.Fatalf("hints = %s", h)
	}
}

func TestEndpointsAndErrors(t *testing.T) {
	now := time.Now().UnixNano()
	traces := [][]*Span{fixture("a", now, 10, 0, 200), fixture("b", now, 2, 0, 500)}
	traces[1][0].Error = true
	traces[1][0].Events = []Event{{Name: "exception", Attrs: map[string]any{"exception.type": "Error", "exception.message": "boom", "exception.stacktrace": "at x"}}}
	eps := Defaults.Endpoints(traces, time.Time{})
	if len(eps) != 1 || eps[0].Count != 2 || eps[0].Errors != 1 || len(eps[0].NPlusOne) != 1 || eps[0].NPlusOne[0].Count != 10 {
		t.Fatalf("endpoints = %+v", eps)
	}
	errs := Defaults.Errors(traces, time.Unix(0, now-int64(time.Minute)))
	if len(errs) != 1 || errs[0].Message != "boom" || errs[0].Stack != "at x" || errs[0].Status != 500 {
		t.Fatalf("errors = %+v", errs)
	}
	if got := Defaults.Errors(traces, time.Unix(0, now+int64(time.Minute))); len(got) != 0 {
		t.Fatalf("errors after window = %+v", got)
	}
}

func TestServiceMap(t *testing.T) {
	edges := Defaults.ServiceMap([][]*Span{fixture("a", 0, 5, 1, 200), fixture("b", 0, 5, 0, 200)})
	if len(edges) != 2 || edges[0].Target != "postgresql · shop" || edges[0].Calls != 12 || edges[0].Requests != 2 || edges[1].Calls != 1 {
		t.Fatalf("edges = %+v", edges)
	}
}
