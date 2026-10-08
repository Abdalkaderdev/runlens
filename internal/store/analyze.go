package store

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Options struct {
	NPlusOneMin int
	SlowMs      float64
	SlowQueryMs float64
}

var Defaults = Options{NPlusOneMin: 5, SlowMs: 500, SlowQueryMs: 100}

type Dep struct {
	Key     string    `json:"key"`
	Kind    Type      `json:"kind"`
	Label   string    `json:"label"`
	System  string    `json:"system,omitempty"`
	Calls   int       `json:"calls"`
	Errors  int       `json:"errors"`
	Ms      float64   `json:"ms"`
	Offsets []float64 `json:"offsets"`
}

type Finding struct {
	Fingerprint string   `json:"fingerprint"`
	Statement   string   `json:"statement"`
	DBSystem    string   `json:"dbSystem,omitempty"`
	Count       int      `json:"count"`
	TotalMs     float64  `json:"totalMs"`
	SpanIDs     []string `json:"spanIds,omitempty"`
}

type Request struct {
	TraceID    string    `json:"traceId"`
	Service    string    `json:"service"`
	Method     string    `json:"method,omitempty"`
	Route      string    `json:"route"`
	Path       string    `json:"path,omitempty"`
	Status     int       `json:"status,omitempty"`
	StartMs    float64   `json:"startMs"`
	DurationMs float64   `json:"durationMs"`
	Error      bool      `json:"error"`
	Spans      int       `json:"spans"`
	DBCalls    int       `json:"dbCalls"`
	DBMs       float64   `json:"dbMs"`
	External   int       `json:"external"`
	Slow       bool      `json:"slow"`
	NPlusOne   []Finding `json:"nPlusOne,omitempty"`
	Deps       []Dep     `json:"deps,omitempty"`
}

func (r Request) Endpoint() string {
	if r.Method == "" {
		return r.Route
	}
	return r.Method + " " + r.Route
}

type SpanView struct {
	*Span
	Depth    int     `json:"depth"`
	OffsetMs float64 `json:"offsetMs"`
	DurMs    float64 `json:"durMs"`
	Slow     bool    `json:"slow,omitempty"`
	NPlusOne bool    `json:"nPlusOne,omitempty"`
}

type Detail struct {
	Request
	Hints []string   `json:"hints"`
	Tree  []SpanView `json:"tree"`
}

func root(spans []*Span) *Span {
	ids := make(map[string]bool, len(spans))
	for _, s := range spans {
		ids[s.SpanID] = true
	}
	var best *Span
	for _, s := range spans {
		if s.ParentID != "" && ids[s.ParentID] {
			continue
		}
		if best == nil || (s.Kind == KindServer && best.Kind != KindServer) || (s.Kind == best.Kind && s.Start < best.Start) {
			best = s
		}
	}
	return best
}

func (o Options) Summarize(spans []*Span) Request {
	r := root(spans)
	if r == nil {
		return Request{}
	}
	req := Request{
		TraceID:    r.TraceID,
		Service:    r.Service,
		Method:     r.Str("http.request.method", "http.method"),
		Route:      r.Str("http.route"),
		Status:     r.Int("http.response.status_code", "http.status_code"),
		StartMs:    float64(r.Start) / 1e6,
		DurationMs: r.DurationMs(),
		Spans:      len(spans),
	}
	req.Path = r.Str("url.path", "http.target")
	if i := strings.IndexByte(req.Path, '?'); i >= 0 {
		req.Path = req.Path[:i]
	}
	if req.Path == "" {
		if u, err := url.Parse(r.Str("url.full", "http.url")); err == nil {
			req.Path = u.Path
		}
	}
	if req.Route == "" {
		req.Route = req.Path
	}
	if req.Route == "" {
		req.Route = r.Name
	}
	req.Error = r.Error || req.Status >= 500
	req.Slow = req.DurationMs > o.SlowMs
	dur := float64(r.End - r.Start)
	deps := map[string]*Dep{}
	var order []string
	for _, s := range spans {
		if s == r || (s.Type != TypeDB && s.Type != TypeExternal) {
			continue
		}
		key, label := depKey(s)
		d := deps[key]
		if d == nil {
			d = &Dep{Key: key, Kind: s.Type, Label: label, System: s.DBSystem}
			deps[key] = d
			order = append(order, key)
		}
		d.Calls++
		d.Ms += s.DurationMs()
		if s.Error {
			d.Errors++
		}
		off := 0.0
		if dur > 0 {
			off = math.Max(0, math.Min(1, float64(s.Start-r.Start)/dur))
		}
		d.Offsets = append(d.Offsets, math.Round(off*1000)/1000)
		if s.Type == TypeDB {
			req.DBCalls++
			req.DBMs += s.DurationMs()
		} else {
			req.External++
		}
	}
	for _, k := range order {
		req.Deps = append(req.Deps, *deps[k])
	}
	req.NPlusOne = o.NPlusOne(spans)
	return req
}

func depKey(s *Span) (string, string) {
	if s.Type == TypeDB {
		label := s.DBSystem
		if s.DBName != "" {
			label += " · " + s.DBName
		} else if s.Peer != "" {
			label += " · " + s.Peer
		}
		return "db:" + s.DBSystem + ":" + s.DBName, label
	}
	peer := s.Peer
	if peer == "" {
		peer = "unknown host"
	}
	return "http:" + peer, peer
}

// NPlusOne reports query shapes executed at least NPlusOneMin times in one
// trace.
func (o Options) NPlusOne(spans []*Span) []Finding {
	groups := map[string]*Finding{}
	var keys []string
	for _, s := range spans {
		if s.Type != TypeDB || s.Fingerprint == "" {
			continue
		}
		f := groups[s.Fingerprint]
		if f == nil {
			f = &Finding{Fingerprint: s.Fingerprint, Statement: s.Statement, DBSystem: s.DBSystem}
			groups[s.Fingerprint] = f
			keys = append(keys, s.Fingerprint)
		}
		f.Count++
		f.TotalMs += s.DurationMs()
		f.SpanIDs = append(f.SpanIDs, s.SpanID)
	}
	var out []Finding
	for _, k := range keys {
		if f := groups[k]; f.Count >= o.NPlusOneMin {
			f.TotalMs = round(f.TotalMs)
			out = append(out, *f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

func (o Options) Detail(spans []*Span) Detail {
	req := o.Summarize(spans)
	d := Detail{Request: req, Hints: o.traceHints(req, spans)}
	r := root(spans)
	if r == nil {
		return d
	}
	n1 := map[string]bool{}
	for _, f := range req.NPlusOne {
		for _, id := range f.SpanIDs {
			n1[id] = true
		}
	}
	children := map[string][]*Span{}
	ids := map[string]bool{}
	for _, s := range spans {
		ids[s.SpanID] = true
	}
	var orphans []*Span
	for _, s := range spans {
		if s == r {
			continue
		}
		if s.ParentID != "" && ids[s.ParentID] {
			children[s.ParentID] = append(children[s.ParentID], s)
		} else {
			orphans = append(orphans, s)
		}
	}
	var walk func(s *Span, depth int)
	walk = func(s *Span, depth int) {
		v := SpanView{Span: s, Depth: depth, OffsetMs: round(float64(s.Start-r.Start) / 1e6), DurMs: round(s.DurationMs()), NPlusOne: n1[s.SpanID]}
		v.Slow = (s.Type == TypeDB || s.Type == TypeExternal) && v.DurMs > o.SlowQueryMs
		d.Tree = append(d.Tree, v)
		for _, c := range children[s.SpanID] {
			walk(c, depth+1)
		}
	}
	walk(r, 0)
	for _, s := range orphans {
		walk(s, 1)
	}
	return d
}

func round(f float64) float64 { return math.Round(f*100) / 100 }

func short(stmt string) string {
	if len(stmt) > 90 {
		return stmt[:87] + "..."
	}
	return stmt
}

func (o Options) traceHints(req Request, spans []*Span) []string {
	h := []string{}
	for _, f := range req.NPlusOne {
		h = append(h, fmt.Sprintf("N+1: %q ran %d times (%.1f ms total). Batch it with one IN (...) query or a join.", short(f.Statement), f.Count, f.TotalMs))
	}
	for _, s := range spans {
		if s.Type == TypeDB && s.DurationMs() > o.SlowQueryMs {
			h = append(h, fmt.Sprintf("Slow query (%.0f ms): %q", s.DurationMs(), short(s.Statement)))
		}
	}
	if req.DurationMs > o.SlowQueryMs/2 && req.DBMs/req.DurationMs > 0.6 && req.DBCalls > 1 {
		h = append(h, fmt.Sprintf("%.0f%% of the request is spent in %d database calls.", 100*req.DBMs/req.DurationMs, req.DBCalls))
	}
	if n, hosts := sequentialExternal(spans); n >= 2 {
		h = append(h, fmt.Sprintf("%d outbound calls (%s) run one after another; they could run concurrently.", n, strings.Join(hosts, ", ")))
	}
	if req.Slow {
		h = append(h, fmt.Sprintf("Request took %.0f ms, over the %.0f ms slow threshold.", req.DurationMs, o.SlowMs))
	}
	return h
}

func sequentialExternal(spans []*Span) (int, []string) {
	var ext []*Span
	for _, s := range spans {
		if s.Type == TypeExternal {
			ext = append(ext, s)
		}
	}
	if len(ext) < 2 {
		return 0, nil
	}
	sort.Slice(ext, func(i, j int) bool { return ext[i].Start < ext[j].Start })
	seen := map[string]bool{}
	var hosts []string
	for i, s := range ext {
		if i > 0 && s.Start < ext[i-1].End {
			return 0, nil
		}
		if !seen[s.Peer] {
			seen[s.Peer] = true
			hosts = append(hosts, s.Peer)
		}
	}
	return len(ext), hosts
}

type Endpoint struct {
	Service        string    `json:"service"`
	Endpoint       string    `json:"endpoint"`
	Count          int       `json:"count"`
	Errors         int       `json:"errors"`
	P50Ms          float64   `json:"p50Ms"`
	P95Ms          float64   `json:"p95Ms"`
	MaxMs          float64   `json:"maxMs"`
	AvgDBCalls     float64   `json:"avgDbCalls"`
	DBShare        float64   `json:"dbShare"`
	Slow           bool      `json:"slow"`
	SlowestTraceID string    `json:"slowestTraceId"`
	NPlusOne       []Finding `json:"nPlusOne,omitempty"`
	Hints          []string  `json:"hints"`
}

// Endpoints aggregates request summaries per endpoint, slowest p95 first.
func (o Options) Endpoints(traces [][]*Span, since time.Time) []Endpoint {
	type acc struct {
		e     Endpoint
		durs  []float64
		db    float64
		total float64
		n1    map[string]Finding
		hints map[string]bool
	}
	groups := map[string]*acc{}
	var keys []string
	for _, t := range traces {
		r := o.Summarize(t)
		if r.TraceID == "" || r.Method == "" || r.StartMs < float64(since.UnixMilli()) {
			continue
		}
		k := r.Service + "|" + r.Endpoint()
		a := groups[k]
		if a == nil {
			a = &acc{e: Endpoint{Service: r.Service, Endpoint: r.Endpoint()}, n1: map[string]Finding{}, hints: map[string]bool{}}
			groups[k] = a
			keys = append(keys, k)
		}
		a.e.Count++
		if r.Error {
			a.e.Errors++
		}
		a.durs = append(a.durs, r.DurationMs)
		if r.DurationMs >= a.e.MaxMs {
			a.e.MaxMs = r.DurationMs
			a.e.SlowestTraceID = r.TraceID
		}
		a.e.AvgDBCalls += float64(r.DBCalls)
		a.db += r.DBMs
		a.total += r.DurationMs
		for _, f := range r.NPlusOne {
			if f.Count > a.n1[f.Fingerprint].Count {
				f.SpanIDs = nil
				a.n1[f.Fingerprint] = f
			}
		}
		if n, hosts := sequentialExternal(t); n >= 2 {
			a.hints[fmt.Sprintf("%d outbound calls (%s) run sequentially; run them concurrently.", n, strings.Join(hosts, ", "))] = true
		}
	}
	out := make([]Endpoint, 0, len(keys))
	for _, k := range keys {
		a := groups[k]
		e := a.e
		sort.Float64s(a.durs)
		e.P50Ms = round(pct(a.durs, 0.5))
		e.P95Ms = round(pct(a.durs, 0.95))
		e.MaxMs = round(e.MaxMs)
		e.AvgDBCalls = round(e.AvgDBCalls / float64(e.Count))
		if a.total > 0 {
			e.DBShare = round(a.db / a.total)
		}
		e.Slow = e.P95Ms > o.SlowMs
		for _, f := range a.n1 {
			e.NPlusOne = append(e.NPlusOne, f)
		}
		sort.Slice(e.NPlusOne, func(i, j int) bool { return e.NPlusOne[i].Count > e.NPlusOne[j].Count })
		e.Hints = []string{}
		for _, f := range e.NPlusOne {
			e.Hints = append(e.Hints, fmt.Sprintf("N+1: %q runs up to %d times per request.", short(f.Statement), f.Count))
		}
		if e.DBShare > 0.6 && (e.AvgDBCalls > 3 || e.P95Ms > o.SlowQueryMs) {
			e.Hints = append(e.Hints, fmt.Sprintf("%.0f%% of time is spent in the database (%.1f calls per request).", e.DBShare*100, e.AvgDBCalls))
		}
		hs := make([]string, 0, len(a.hints))
		for h := range a.hints {
			hs = append(hs, h)
		}
		sort.Strings(hs)
		e.Hints = append(e.Hints, hs...)
		if e.Slow {
			e.Hints = append(e.Hints, fmt.Sprintf("p95 %.0f ms is over the %.0f ms slow threshold.", e.P95Ms, o.SlowMs))
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].P95Ms > out[j].P95Ms })
	return out
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

type ErrorItem struct {
	TraceID  string  `json:"traceId"`
	TimeMs   float64 `json:"timeMs"`
	Endpoint string  `json:"endpoint"`
	Status   int     `json:"status,omitempty"`
	Span     string  `json:"span"`
	Type     string  `json:"type,omitempty"`
	Message  string  `json:"message"`
	Stack    string  `json:"stack,omitempty"`
}

// Errors lists failed spans that started after since, newest first.
func (o Options) Errors(traces [][]*Span, since time.Time) []ErrorItem {
	var out []ErrorItem
	for _, t := range traces {
		r := o.Summarize(t)
		for _, s := range t {
			if s.Start < since.UnixNano() {
				continue
			}
			code := 0
			if s.Kind == KindServer || s.Type == TypeExternal {
				code = s.Int("http.response.status_code", "http.status_code")
			}
			if !s.Error && code < 500 {
				continue
			}
			e := ErrorItem{TraceID: s.TraceID, TimeMs: float64(s.Start) / 1e6, Endpoint: r.Endpoint(), Status: code, Span: s.Name, Message: s.StatusMsg}
			for _, ev := range s.Events {
				if ev.Name != "exception" {
					continue
				}
				e.Type = toString(ev.Attrs["exception.type"])
				if m := toString(ev.Attrs["exception.message"]); m != "" {
					e.Message = m
				}
				e.Stack = toString(ev.Attrs["exception.stacktrace"])
			}
			if e.Message == "" && code >= 500 {
				e.Message = fmt.Sprintf("HTTP %d", code)
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMs > out[j].TimeMs })
	return out
}

type Edge struct {
	Service  string  `json:"service"`
	Endpoint string  `json:"endpoint"`
	Target   string  `json:"target"`
	Kind     Type    `json:"kind"`
	Requests int     `json:"requests"`
	Calls    int     `json:"calls"`
	Errors   int     `json:"errors"`
	AvgMs    float64 `json:"avgMs"`
}

// ServiceMap lists which endpoints call which databases and external hosts.
func (o Options) ServiceMap(traces [][]*Span) []Edge {
	idx := map[string]*Edge{}
	var keys []string
	for _, t := range traces {
		r := o.Summarize(t)
		for _, d := range r.Deps {
			k := r.Service + "|" + r.Endpoint() + "|" + d.Key
			e := idx[k]
			if e == nil {
				e = &Edge{Service: r.Service, Endpoint: r.Endpoint(), Target: d.Label, Kind: d.Kind}
				idx[k] = e
				keys = append(keys, k)
			}
			e.Requests++
			e.Calls += d.Calls
			e.Errors += d.Errors
			e.AvgMs += d.Ms
		}
	}
	out := make([]Edge, 0, len(keys))
	for _, k := range keys {
		e := *idx[k]
		e.AvgMs = round(e.AvgMs / float64(e.Calls))
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Calls > out[j].Calls })
	return out
}
