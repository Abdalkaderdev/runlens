package store

import (
	"sort"
	"sync"
)

type Trace struct {
	ID    string
	Spans []*Span
	seen  map[string]bool
}

// Store keeps the most recent traces in memory. When more than Cap traces
// are held the oldest is evicted.
type Store struct {
	Cap int

	mu     sync.RWMutex
	traces map[string]*Trace
	order  []string
	subs   map[chan []string]struct{}
}

func New(capacity int) *Store {
	return &Store{Cap: capacity, traces: map[string]*Trace{}, subs: map[chan []string]struct{}{}}
}

// Add merges spans into their traces and notifies subscribers with the IDs
// of the traces that changed.
func (s *Store) Add(spans []*Span) {
	if len(spans) == 0 {
		return
	}
	s.mu.Lock()
	var changed []string
	touched := map[string]bool{}
	for _, sp := range spans {
		t := s.traces[sp.TraceID]
		if t == nil {
			t = &Trace{ID: sp.TraceID, seen: map[string]bool{}}
			s.traces[sp.TraceID] = t
			s.order = append(s.order, sp.TraceID)
		}
		if t.seen[sp.SpanID] {
			continue
		}
		t.seen[sp.SpanID] = true
		t.Spans = append(t.Spans, sp)
		if !touched[sp.TraceID] {
			touched[sp.TraceID] = true
			changed = append(changed, sp.TraceID)
		}
	}
	for len(s.order) > s.Cap {
		delete(s.traces, s.order[0])
		s.order = s.order[1:]
	}
	for _, t := range changed {
		sort.Slice(s.traces[t].Spans, func(i, j int) bool { return s.traces[t].Spans[i].Start < s.traces[t].Spans[j].Start })
	}
	subs := make([]chan []string, 0, len(s.subs))
	for c := range s.subs {
		subs = append(subs, c)
	}
	s.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- changed:
		default:
		}
	}
}

func (s *Store) Clear() {
	s.mu.Lock()
	s.traces = map[string]*Trace{}
	s.order = nil
	s.mu.Unlock()
}

func (s *Store) Subscribe() (<-chan []string, func()) {
	c := make(chan []string, 256)
	s.mu.Lock()
	s.subs[c] = struct{}{}
	s.mu.Unlock()
	return c, func() {
		s.mu.Lock()
		delete(s.subs, c)
		s.mu.Unlock()
	}
}

// Trace returns a copy of the span list for one trace.
func (s *Store) Trace(id string) []*Span {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.traces[id]
	if t == nil {
		return nil
	}
	return append([]*Span(nil), t.Spans...)
}

// Traces returns all traces, newest first.
func (s *Store) Traces() [][]*Span {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([][]*Span, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		out = append(out, append([]*Span(nil), s.traces[s.order[i]].Spans...))
	}
	return out
}

// Spans returns every span, oldest trace first, so that adding them to an
// empty store reproduces the same trace order.
func (s *Store) Spans() []*Span {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Span
	for _, id := range s.order {
		out = append(out, s.traces[id].Spans...)
	}
	return out
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.order)
}
