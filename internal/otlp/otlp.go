// Package otlp receives OTLP trace exports over HTTP (protobuf) and gRPC.
package otlp

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"io"
	"net"
	"net/http"

	"github.com/Abdalkaderdev/runlens/internal/store"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type Receiver struct {
	Store  *store.Store
	Redact bool
}

func (rc *Receiver) Ingest(req *coltrace.ExportTraceServiceRequest) {
	rc.Store.Add(Convert(req, rc.Redact))
}

func Convert(req *coltrace.ExportTraceServiceRequest, redact bool) []*store.Span {
	var out []*store.Span
	for _, rs := range req.GetResourceSpans() {
		service := "unknown"
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.Key == "service.name" {
				service = kv.Value.GetStringValue()
			}
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, p := range ss.GetSpans() {
				s := &store.Span{
					TraceID:   hex.EncodeToString(p.TraceId),
					SpanID:    hex.EncodeToString(p.SpanId),
					ParentID:  hex.EncodeToString(p.ParentSpanId),
					Name:      p.Name,
					Kind:      kind(p.Kind),
					Service:   service,
					Start:     int64(p.StartTimeUnixNano),
					End:       int64(p.EndTimeUnixNano),
					Error:     p.GetStatus().GetCode() == trace.Status_STATUS_CODE_ERROR,
					StatusMsg: p.GetStatus().GetMessage(),
					Attrs:     attrs(p.Attributes),
				}
				for _, e := range p.Events {
					s.Events = append(s.Events, store.Event{Name: e.Name, Time: int64(e.TimeUnixNano), Attrs: attrs(e.Attributes)})
				}
				s.Classify(redact)
				out = append(out, s)
			}
		}
	}
	return out
}

func kind(k trace.Span_SpanKind) store.Kind {
	switch k {
	case trace.Span_SPAN_KIND_SERVER:
		return store.KindServer
	case trace.Span_SPAN_KIND_CLIENT:
		return store.KindClient
	case trace.Span_SPAN_KIND_PRODUCER:
		return store.KindProducer
	case trace.Span_SPAN_KIND_CONSUMER:
		return store.KindConsumer
	}
	return store.KindInternal
}

func attrs(kvs []*common.KeyValue) map[string]any {
	if len(kvs) == 0 {
		return nil
	}
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = value(kv.Value)
	}
	return m
}

func value(v *common.AnyValue) any {
	switch t := v.GetValue().(type) {
	case *common.AnyValue_StringValue:
		return t.StringValue
	case *common.AnyValue_IntValue:
		return t.IntValue
	case *common.AnyValue_DoubleValue:
		return t.DoubleValue
	case *common.AnyValue_BoolValue:
		return t.BoolValue
	case *common.AnyValue_BytesValue:
		return hex.EncodeToString(t.BytesValue)
	case *common.AnyValue_ArrayValue:
		arr := make([]any, 0, len(t.ArrayValue.Values))
		for _, x := range t.ArrayValue.Values {
			arr = append(arr, value(x))
		}
		return arr
	case *common.AnyValue_KvlistValue:
		return attrs(t.KvlistValue.Values)
	}
	return nil
}

const maxBody = 32 << 20

// ServeHTTP handles POST /v1/traces with an application/x-protobuf body.
func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && ct != "application/x-protobuf" {
		http.Error(w, "runlens accepts OTLP http/protobuf; set OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf", http.StatusUnsupportedMediaType)
		return
	}
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxBody)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = io.LimitReader(gz, maxBody)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req coltrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(b, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rc.Ingest(&req)
	out, _ := proto.Marshal(&coltrace.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(out)
}

// Discard accepts OTLP metrics and logs so exporters do not log errors.
func Discard(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, io.LimitReader(r.Body, maxBody))
	w.Header().Set("Content-Type", "application/x-protobuf")
}

type grpcService struct {
	coltrace.UnimplementedTraceServiceServer
	rc *Receiver
}

func (g grpcService) Export(_ context.Context, req *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	g.rc.Ingest(req)
	return &coltrace.ExportTraceServiceResponse{}, nil
}

// ServeGRPC serves the OTLP trace service on l until it is closed.
func (rc *Receiver) ServeGRPC(l net.Listener) error {
	s := grpc.NewServer(grpc.MaxRecvMsgSize(maxBody))
	coltrace.RegisterTraceServiceServer(s, grpcService{rc: rc})
	return s.Serve(l)
}
