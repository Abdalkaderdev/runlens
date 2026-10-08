package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM books WHERE author_id = 42":                 "SELECT * FROM books WHERE author_id = ?",
		"SELECT * FROM t WHERE name = 'O''Brien' AND x = 1.5":      "SELECT * FROM t WHERE name = ? AND x = ?",
		"SELECT * FROM t WHERE id = $1 AND c2 = 3":                 "SELECT * FROM t WHERE id = $1 AND c2 = ?",
		"SELECT * FROM t WHERE id IN (1, 2, 3)":                    "SELECT * FROM t WHERE id IN (?)",
		"INSERT INTO t (a, b) VALUES (1, 'x'), (2, 'y')":           "INSERT INTO t (a, b) VALUES (?)",
		`SELECT "col1" FROM   t2  WHERE   "x" = 'secret'`:          `SELECT "col1" FROM t2 WHERE "x" = ?`,
		"SELECT pg_sleep(0.25), count(*)::int FROM orders LIMIT 5": "SELECT pg_sleep(?), count(*)::int FROM orders LIMIT ?",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestClassifyAndRedact(t *testing.T) {
	db := &Span{Kind: KindClient, Attrs: map[string]any{
		"db.system": "postgresql", "db.name": "shop", "db.statement": "SELECT * FROM users WHERE email = 'a@b.c'",
		"db.statement.parameters": "[1]",
	}}
	db.Classify(true)
	if db.Type != TypeDB || db.DBName != "shop" || db.Statement != "SELECT * FROM users WHERE email = ?" {
		t.Fatalf("db span: %+v", db)
	}
	if db.Attrs["db.statement"] != db.Statement || db.Attrs["db.statement.parameters"] != "[redacted]" {
		t.Fatalf("db attrs not redacted: %v", db.Attrs)
	}

	ext := &Span{Kind: KindClient, Attrs: map[string]any{
		"http.request.method": "GET", "url.full": "https://api.example.com/v1/x?token=abc&page=2", "server.port": int64(8443),
		"http.request.header.authorization": "Bearer xyz",
	}}
	ext.Classify(true)
	if ext.Type != TypeExternal || ext.Peer != "api.example.com:8443" {
		t.Fatalf("external span: %+v", ext)
	}
	if ext.Attrs["url.full"] != "https://api.example.com/v1/x?page=_&token=_" || ext.Attrs["http.request.header.authorization"] != "[redacted]" {
		t.Fatalf("external attrs not redacted: %v", ext.Attrs)
	}

	raw := &Span{Kind: KindClient, Attrs: map[string]any{"db.system.name": "sqlite", "db.query.text": "SELECT 1 WHERE x = 'y'"}}
	raw.Classify(false)
	if raw.Statement != "SELECT 1 WHERE x = 'y'" || raw.Fingerprint != "SELECT ? WHERE x = ?" {
		t.Fatalf("unredacted span: %+v", raw)
	}

	pool := &Span{Kind: KindInternal, Attrs: map[string]any{"db.system": "postgresql"}}
	pool.Classify(true)
	if pool.Type != TypeInternal {
		t.Fatalf("statement-less db span should be internal, got %s", pool.Type)
	}
}

func TestStoreEvictsOldest(t *testing.T) {
	s := New(3)
	for i := range 5 {
		s.Add([]*Span{{TraceID: fmt.Sprint("t", i), SpanID: "a", Start: int64(i)}})
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want 3", s.Len())
	}
	if s.Trace("t0") != nil || s.Trace("t1") != nil || s.Trace("t4") == nil {
		t.Fatal("wrong traces evicted")
	}
	tr := s.Traces()
	if tr[0][0].TraceID != "t4" || tr[2][0].TraceID != "t2" {
		t.Fatalf("Traces not newest first: %s..%s", tr[0][0].TraceID, tr[2][0].TraceID)
	}
}

func TestStoreMergesAndDedupes(t *testing.T) {
	s := New(10)
	ch, cancel := s.Subscribe()
	defer cancel()
	s.Add([]*Span{{TraceID: "t", SpanID: "b", Start: 2}})
	s.Add([]*Span{{TraceID: "t", SpanID: "a", Start: 1}, {TraceID: "t", SpanID: "b", Start: 2}})
	spans := s.Trace("t")
	if len(spans) != 2 || spans[0].SpanID != "a" {
		t.Fatalf("spans = %+v", spans)
	}
	for range 2 {
		select {
		case ids := <-ch:
			if len(ids) != 1 || ids[0] != "t" {
				t.Fatalf("notified %v", ids)
			}
		case <-time.After(time.Second):
			t.Fatal("no notification")
		}
	}
}

func TestSpansRoundTripKeepsOrder(t *testing.T) {
	a := New(10)
	for i := range 4 {
		a.Add([]*Span{{TraceID: fmt.Sprint("t", i), SpanID: "x"}})
	}
	b := New(10)
	b.Add(a.Spans())
	var got []string
	for _, tr := range b.Traces() {
		got = append(got, tr[0].TraceID)
	}
	if strings.Join(got, ",") != "t3,t2,t1,t0" {
		t.Fatalf("order after round trip: %v", got)
	}
}
