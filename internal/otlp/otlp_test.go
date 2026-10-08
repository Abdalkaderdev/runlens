package otlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Abdalkaderdev/runlens/internal/store"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func str(k, v string) *common.KeyValue {
	return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}}
}

func request() *coltrace.ExportTraceServiceRequest {
	tid := bytes.Repeat([]byte{0xab}, 16)
	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
		Resource: &resource.Resource{Attributes: []*common.KeyValue{str("service.name", "shop")}},
		ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{
			{TraceId: tid, SpanId: []byte{1, 1, 1, 1, 1, 1, 1, 1}, Name: "GET /x", Kind: trace.Span_SPAN_KIND_SERVER, StartTimeUnixNano: 1e9, EndTimeUnixNano: 2e9,
				Attributes: []*common.KeyValue{str("http.request.method", "GET"), str("http.route", "/x")}},
			{TraceId: tid, SpanId: []byte{2, 2, 2, 2, 2, 2, 2, 2}, ParentSpanId: []byte{1, 1, 1, 1, 1, 1, 1, 1}, Name: "query", Kind: trace.Span_SPAN_KIND_CLIENT,
				StartTimeUnixNano: 1.1e9, EndTimeUnixNano: 1.2e9, Status: &trace.Status{Code: trace.Status_STATUS_CODE_ERROR, Message: "deadlock"},
				Attributes: []*common.KeyValue{str("db.system", "postgresql"), str("db.statement", "SELECT 1 FROM t WHERE a = 'x'")}},
		}}},
	}}}
}

func check(t *testing.T, st *store.Store) {
	t.Helper()
	spans := st.Trace("abababababababababababababababab")
	if len(spans) != 2 {
		t.Fatalf("got %d spans", len(spans))
	}
	root, db := spans[0], spans[1]
	if root.Service != "shop" || root.Kind != store.KindServer || root.Type != store.TypeHTTP {
		t.Fatalf("root = %+v", root)
	}
	if db.ParentID != "0101010101010101" || db.Type != store.TypeDB || !db.Error || db.StatusMsg != "deadlock" || db.Statement != "SELECT ? FROM t WHERE a = ?" {
		t.Fatalf("db = %+v", db)
	}
}

func TestHTTPProtobuf(t *testing.T) {
	st := store.New(10)
	srv := httptest.NewServer(&Receiver{Store: st, Redact: true})
	defer srv.Close()
	b, _ := proto.Marshal(request())
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(b)
	w.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, &gz)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("status %v err %v", res.StatusCode, err)
	}
	check(t, st)
}

func TestHTTPJSON(t *testing.T) {
	st := store.New(10)
	srv := httptest.NewServer(&Receiver{Store: st, Redact: true})
	defer srv.Close()
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"shop"}}]},
	  "scopeSpans":[{"spans":[
	    {"traceId":"abababababababababababababababab","spanId":"0101010101010101","name":"GET /x","kind":2,
	     "startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000",
	     "attributes":[{"key":"http.request.method","value":{"stringValue":"GET"}}]},
	    {"traceId":"abababababababababababababababab","spanId":"0202020202020202","parentSpanId":"0101010101010101","name":"query","kind":3,
	     "startTimeUnixNano":"1100000000","endTimeUnixNano":"1200000000","status":{"code":2,"message":"deadlock"},
	     "attributes":[{"key":"db.system","value":{"stringValue":"postgresql"}},{"key":"db.statement","value":{"stringValue":"SELECT 1 FROM t WHERE a = 'x'"}}]}
	  ]}]}]}`
	res, err := http.Post(srv.URL, "application/json", bytes.NewBufferString(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("status %v err %v", res.StatusCode, err)
	}
	check(t, st)

	res, _ = http.Post(srv.URL, "application/json", bytes.NewBufferString(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"zz"}]}]}]}`))
	if res.StatusCode != 400 {
		t.Fatalf("bad hex id: status %d, want 400", res.StatusCode)
	}
}

func TestRejectsUnknownContentType(t *testing.T) {
	srv := httptest.NewServer(&Receiver{Store: store.New(1)})
	defer srv.Close()
	res, _ := http.Post(srv.URL, "text/plain", bytes.NewBufferString("x"))
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestGRPC(t *testing.T) {
	st := store.New(10)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go (&Receiver{Store: st, Redact: true}).ServeGRPC(l)
	conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := coltrace.NewTraceServiceClient(conn).Export(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	check(t, st)
}
