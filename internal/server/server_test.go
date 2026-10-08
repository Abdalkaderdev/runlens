package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Abdalkaderdev/runlens/internal/mcpserver"
	"github.com/Abdalkaderdev/runlens/internal/otlp"
	"github.com/Abdalkaderdev/runlens/internal/store"
)

func newServer() (*Server, *store.Store) {
	st := store.New(10)
	root := &store.Span{TraceID: "t1", SpanID: "s1", Kind: store.KindServer, Service: "shop", Start: 1, End: 2e6,
		Attrs: map[string]any{"http.request.method": "GET", "http.route": "/x"}}
	root.Classify(true)
	st.Add([]*store.Span{root})
	return &Server{Store: st, Opt: store.Defaults, Receiver: &otlp.Receiver{Store: st}, MCP: mcpserver.New(nil, store.Defaults, "t"),
		UI: fstest.MapFS{"index.html": {Data: []byte("<html>ui</html>")}}}, st
}

func get(h http.Handler, method, url, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, nil)
	req.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAPI(t *testing.T) {
	s, _ := newServer()
	h := s.Handler()
	w := get(h, "GET", "/api/requests", "127.0.0.1:4318")
	var reqs []store.Request
	json.Unmarshal(w.Body.Bytes(), &reqs)
	if w.Code != 200 || len(reqs) != 1 || reqs[0].Endpoint() != "GET /x" {
		t.Fatalf("requests: %d %s", w.Code, w.Body)
	}
	if w := get(h, "GET", "/api/traces/t1", "localhost:4318"); w.Code != 200 || !strings.Contains(w.Body.String(), `"tree"`) {
		t.Fatalf("trace: %d", w.Code)
	}
	if w := get(h, "GET", "/api/traces/nope", "localhost"); w.Code != 404 {
		t.Fatalf("missing trace: %d", w.Code)
	}
	if w := get(h, "GET", "/some/route", "localhost"); w.Code != 200 || !strings.Contains(w.Body.String(), "ui") {
		t.Fatalf("spa fallback: %d", w.Code)
	}
}

func TestRejectsForeignHost(t *testing.T) {
	s, _ := newServer()
	if w := get(s.Handler(), "GET", "/api/requests", "evil.example.com"); w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
}

func TestRejectsCrossOriginClear(t *testing.T) {
	s, st := newServer()
	req := httptest.NewRequest("POST", "/api/clear", nil)
	req.Host = "127.0.0.1:4318"
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || st.Len() != 1 {
		t.Fatalf("status %d len %d", w.Code, st.Len())
	}
}
