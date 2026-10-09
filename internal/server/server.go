package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abdalkaderdev/runlens/internal/otlp"
	"github.com/Abdalkaderdev/runlens/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type App struct {
	Command  string `json:"command,omitempty"`
	Runtime  string `json:"runtime,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Running  bool   `json:"running"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

type Server struct {
	Store    *store.Store
	Opt      store.Options
	Receiver *otlp.Receiver
	MCP      *mcp.Server
	UI       fs.FS
	Version  string

	mu  sync.Mutex
	app App
}

func (s *Server) SetApp(a App) {
	s.mu.Lock()
	s.app = a
	s.mu.Unlock()
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.Handle("POST /v1/traces", s.Receiver)
	m.HandleFunc("POST /v1/metrics", otlp.Discard)
	m.HandleFunc("POST /v1/logs", otlp.Discard)
	m.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.MCP }, nil))
	m.HandleFunc("GET /api/meta", s.meta)
	m.HandleFunc("GET /api/requests", s.requests)
	m.HandleFunc("GET /api/traces/{id}", s.trace)
	m.HandleFunc("GET /api/endpoints", s.endpoints)
	m.HandleFunc("GET /api/export", s.export)
	m.HandleFunc("GET /api/stream", s.stream)
	m.HandleFunc("POST /api/clear", func(w http.ResponseWriter, r *http.Request) {
		s.Store.Clear()
		w.WriteHeader(http.StatusNoContent)
	})
	if s.UI != nil {
		m.Handle("/", spa(s.UI))
	}
	return localOnly(http.NewCrossOriginProtection().Handler(m))
}

func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		host = strings.Trim(host, "[]")
		if host != "localhost" && !strings.HasSuffix(host, ".localhost") {
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				http.Error(w, "runlens only answers on loopback hosts", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func spa(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(ui, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, ui, "index.html")
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	app := s.app
	s.mu.Unlock()
	writeJSON(w, map[string]any{
		"version": s.Version, "app": app, "traces": s.Store.Len(), "capacity": s.Store.Cap,
		"slowMs": s.Opt.SlowMs, "slowQueryMs": s.Opt.SlowQueryMs, "nPlusOneMin": s.Opt.NPlusOneMin,
	})
}

func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	out := []store.Request{}
	for _, t := range s.Store.Traces() {
		if req := s.Opt.Summarize(t); req.Method != "" {
			out = append(out, req)
			if len(out) == limit {
				break
			}
		}
	}
	writeJSON(w, out)
}

func (s *Server) trace(w http.ResponseWriter, r *http.Request) {
	spans := s.Store.Trace(r.PathValue("id"))
	if spans == nil {
		http.Error(w, "trace not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.Opt.Detail(spans))
}

func (s *Server) endpoints(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Opt.Endpoints(s.Store.Traces(), time.Time{}))
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	spans := s.Store.Spans()
	if spans == nil {
		spans = []*store.Span{}
	}
	writeJSON(w, spans)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := s.Store.Subscribe()
	defer cancel()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
		case ids := <-ch:
			for _, id := range ids {
				spans := s.Store.Trace(id)
				if spans == nil {
					continue
				}
				req := s.Opt.Summarize(spans)
				if req.Method == "" {
					continue
				}
				b, _ := json.Marshal(req)
				fmt.Fprintf(w, "event: request\ndata: %s\n\n", b)
			}
		}
		fl.Flush()
	}
}
