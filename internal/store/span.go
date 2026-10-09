package store

import (
	"net/url"
	"strings"
)

type Kind string

const (
	KindServer   Kind = "server"
	KindClient   Kind = "client"
	KindInternal Kind = "internal"
	KindProducer Kind = "producer"
	KindConsumer Kind = "consumer"
)

type Type string

const (
	TypeHTTP     Type = "http"
	TypeDB       Type = "db"
	TypeExternal Type = "external"
	TypeInternal Type = "internal"
)

type Event struct {
	Name  string         `json:"name"`
	Time  int64          `json:"time"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

type Span struct {
	TraceID   string         `json:"traceId"`
	SpanID    string         `json:"spanId"`
	ParentID  string         `json:"parentId,omitempty"`
	Name      string         `json:"name"`
	Kind      Kind           `json:"kind"`
	Service   string         `json:"service"`
	Start     int64          `json:"start"`
	End       int64          `json:"end"`
	Error     bool           `json:"error,omitempty"`
	StatusMsg string         `json:"statusMessage,omitempty"`
	Attrs     map[string]any `json:"attrs,omitempty"`
	Events    []Event        `json:"events,omitempty"`

	Type        Type   `json:"type"`
	DBSystem    string `json:"dbSystem,omitempty"`
	DBName      string `json:"dbName,omitempty"`
	Statement   string `json:"statement,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Peer        string `json:"peer,omitempty"`
}

func (s *Span) DurationMs() float64 { return float64(s.End-s.Start) / 1e6 }

func (s *Span) Str(keys ...string) string {
	for _, k := range keys {
		if v, ok := s.Attrs[k]; ok {
			switch t := v.(type) {
			case string:
				if t != "" {
					return t
				}
			default:
				return toString(v)
			}
		}
	}
	return ""
}

func (s *Span) Int(keys ...string) int {
	for _, k := range keys {
		switch t := s.Attrs[k].(type) {
		case int64:
			return int(t)
		case int:
			return t
		case float64:
			return int(t)
		}
	}
	return 0
}

func (s *Span) Classify(redact bool) {
	s.DBSystem = s.Str("db.system.name", "db.system")
	switch {
	case s.DBSystem != "":
		s.Type = TypeDB
		s.DBName = s.Str("db.namespace", "db.name", "db.redis.database_index")
		raw := s.Str("db.query.text", "db.statement")
		if raw == "" {
			raw = strings.TrimSpace(s.Str("db.operation.name", "db.operation") + " " + s.Str("db.collection.name", "db.mongodb.collection", "db.sql.table"))
		}
		if raw == "" {
			s.Type = TypeInternal
			return
		}
		s.Fingerprint = Normalize(raw)
		s.Statement = raw
		if redact {
			s.Statement = s.Fingerprint
		}
		s.Peer = s.Str("server.address", "net.peer.name")
	case s.Kind == KindClient && s.Str("http.request.method", "http.method") != "":
		s.Type = TypeExternal
		s.Peer = s.Str("server.address", "net.peer.name", "http.host")
		if s.Peer == "" {
			if u, err := url.Parse(s.Str("url.full", "http.url")); err == nil {
				s.Peer = u.Hostname()
			}
		}
		if p := s.Int("server.port", "net.peer.port"); p != 0 && p != 80 && p != 443 {
			s.Peer += ":" + toString(int64(p))
		}
	case s.Kind == KindServer:
		s.Type = TypeHTTP
	default:
		s.Type = TypeInternal
	}
	if redact {
		s.scrub()
	}
}

func (s *Span) scrub() {
	for k, v := range s.Attrs {
		lk := strings.ToLower(k)
		switch {
		case k == "db.statement" || k == "db.query.text":
			s.Attrs[k] = s.Fingerprint
		case strings.HasPrefix(lk, "db.") && strings.Contains(lk, "param"):
			s.Attrs[k] = "[redacted]"
		case strings.Contains(lk, "header") && (strings.Contains(lk, "authorization") || strings.Contains(lk, "cookie") || strings.Contains(lk, "api-key") || strings.Contains(lk, "token")):
			s.Attrs[k] = "[redacted]"
		case k == "url.full" || k == "http.url" || k == "url.query" || k == "http.target":
			if str, ok := v.(string); ok {
				s.Attrs[k] = redactQuery(str)
			}
		}
	}
}

func redactQuery(raw string) string {
	i := strings.IndexByte(raw, '?')
	if i < 0 {
		if strings.Contains(raw, "=") && !strings.Contains(raw, "/") {
			i = -1
		} else {
			return raw
		}
	}
	q, err := url.ParseQuery(raw[i+1:])
	if err != nil {
		return raw[:i+1] + "[redacted]"
	}
	parts := make([]string, 0, len(q))
	for k := range q {
		parts = append(parts, k+"=_")
	}
	sortStrings(parts)
	return raw[:i+1] + strings.Join(parts, "&")
}
