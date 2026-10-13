package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

func (u Usage) Billable() int { return u.Input + u.Output + u.CacheWrite }

type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Tool    string    `json:"tool,omitempty"`
	ID      string    `json:"id,omitempty"`
	Summary string    `json:"summary,omitempty"`
	File    string    `json:"file,omitempty"`
	Old     string    `json:"old,omitempty"`
	New     string    `json:"new,omitempty"`
	Output  string    `json:"output,omitempty"`
	IsError bool      `json:"isError,omitempty"`
	Edit    bool      `json:"edit,omitempty"`
}

type Agent struct {
	ID          string    `json:"id"`
	Session     string    `json:"session"`
	Description string    `json:"description,omitempty"`
	Type        string    `json:"type,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	LastAt      time.Time `json:"lastAt"`
	Usage       Usage     `json:"usage"`
	ToolCalls   int       `json:"toolCalls"`
	Errors      int       `json:"errors"`
	Edits       int       `json:"edits"`
	Final       string    `json:"final,omitempty"`
	ended       bool
	events      []Event
	pending     map[string]int
	usageAt     []usagePoint
}

type usagePoint struct {
	at     time.Time
	tokens int
}

type Summary struct {
	Agent
	Status     string   `json:"status"`
	LastAction string   `json:"lastAction,omitempty"`
	TokensHour float64  `json:"tokensPerHour"`
	Files      []string `json:"files"`
}

type Session struct {
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Cwd     string    `json:"cwd,omitempty"`
	LastAt  time.Time `json:"lastAt"`
	Agents  []Summary `json:"agents"`
	Usage   Usage     `json:"usage"`
}

type Collision struct {
	Session string    `json:"session"`
	File    string    `json:"file"`
	Agents  []string  `json:"agents"`
	LastAt  time.Time `json:"lastAt"`
}

type Snapshot struct {
	Sessions   []Session   `json:"sessions"`
	Collisions []Collision `json:"collisions"`
	Generated  time.Time   `json:"generatedAt"`
}

type Watcher struct {
	Root     string
	Since    time.Duration
	MaxEvent int
	Now      func() time.Time

	mu       sync.Mutex
	agents   map[string]*Agent
	sessions map[string]*sessionInfo
	offsets  map[string]int64
	partial  map[string][]byte
}

type sessionInfo struct {
	project string
	cwd     string
}

func New(root string, since time.Duration) *Watcher {
	return &Watcher{Root: root, Since: since, MaxEvent: 3000, Now: time.Now}
}

func DefaultRoot() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

func key(session, agent string) string { return session + "/" + agent }

func (w *Watcher) init() {
	if w.agents == nil {
		w.agents = map[string]*Agent{}
		w.sessions = map[string]*sessionInfo{}
		w.offsets = map[string]int64{}
		w.partial = map[string][]byte{}
	}
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.MaxEvent == 0 {
		w.MaxEvent = 3000
	}
}

func (w *Watcher) Poll() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	cutoff := w.Now().Add(-w.Since)
	projects, err := os.ReadDir(w.Root)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(w.Root, p.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			sid := strings.TrimSuffix(name, ".jsonl")
			path := filepath.Join(dir, name)
			if !w.recent(path, cutoff) && !w.recent(filepath.Join(dir, sid, "subagents"), cutoff) {
				continue
			}
			if w.sessions[sid] == nil {
				w.sessions[sid] = &sessionInfo{project: p.Name()}
			}
			w.read(path, sid, "main")
			subs, _ := os.ReadDir(filepath.Join(dir, sid, "subagents"))
			for _, s := range subs {
				n := s.Name()
				if !strings.HasSuffix(n, ".jsonl") || !strings.HasPrefix(n, "agent-") {
					continue
				}
				sp := filepath.Join(dir, sid, "subagents", n)
				if !w.recent(sp, cutoff) && w.offsets[sp] == 0 {
					continue
				}
				id := strings.TrimSuffix(strings.TrimPrefix(n, "agent-"), ".jsonl")
				w.read(sp, sid, id)
				w.meta(strings.TrimSuffix(sp, ".jsonl")+".meta.json", sid, id)
			}
		}
	}
	return nil
}

func (w *Watcher) recent(path string, cutoff time.Time) bool {
	st, err := os.Stat(path)
	return err == nil && st.ModTime().After(cutoff)
}

func (w *Watcher) meta(path, sid, id string) {
	a := w.agents[key(sid, id)]
	if a == nil || a.Description != "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
	}
	if json.Unmarshal(b, &m) == nil {
		a.Description, a.Type = m.Description, m.AgentType
	}
}

func (w *Watcher) read(path, sid, id string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	off := w.offsets[path]
	if st, err := f.Stat(); err == nil && st.Size() < off {
		off = 0
		w.partial[path] = nil
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(bufio.NewReader(f))
	if err != nil || len(data) == 0 {
		return
	}
	w.offsets[path] = off + int64(len(data))
	buf := append(w.partial[path], data...)
	last := bytes.LastIndexByte(buf, '\n')
	if last < 0 {
		w.partial[path] = buf
		return
	}
	w.partial[path] = append([]byte(nil), buf[last+1:]...)
	for _, line := range bytes.Split(buf[:last], []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			w.ingest(sid, id, line)
		}
	}
}

type rawLine struct {
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp"`
	Cwd          string    `json:"cwd"`
	ToolEndsTurn bool      `json:"toolEndsTurn"`
	Message      struct {
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (w *Watcher) agent(sid, id string, at time.Time) *Agent {
	k := key(sid, id)
	a := w.agents[k]
	if a == nil {
		a = &Agent{ID: id, Session: sid, StartedAt: at, pending: map[string]int{}}
		if id == "main" {
			a.Description = "main session"
		}
		w.agents[k] = a
	}
	return a
}

func (w *Watcher) ingest(sid, id string, line []byte) {
	var r rawLine
	if json.Unmarshal(line, &r) != nil || (r.Type != "assistant" && r.Type != "user") {
		return
	}
	a := w.agent(sid, id, r.Timestamp)
	if r.Timestamp.After(a.LastAt) {
		a.LastAt = r.Timestamp
	}
	if r.Cwd != "" {
		a.Cwd = r.Cwd
		if s := w.sessions[sid]; s != nil && id == "main" && s.cwd == "" {
			s.cwd = r.Cwd
		}
	}
	var blocks []block
	if json.Unmarshal(r.Message.Content, &blocks) != nil {
		var s string
		if json.Unmarshal(r.Message.Content, &s) == nil && r.Type == "user" {
			a.ended = false
		}
		return
	}
	if r.Type == "assistant" {
		if u := r.Message.Usage; u != nil {
			a.Usage.Input += u.Input
			a.Usage.Output += u.Output
			a.Usage.CacheRead += u.CacheRead
			a.Usage.CacheWrite += u.CacheWrite
			a.usageAt = append(a.usageAt, usagePoint{r.Timestamp, u.Input + u.Output + u.CacheWrite})
		}
		hasTool := false
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					a.Final = t
					w.push(a, Event{Time: r.Timestamp, Kind: "text", Summary: clip(t, 600)})
				}
			case "tool_use":
				if b.Name == "SubagentHandback" {
					var in struct {
						Message string `json:"message"`
					}
					if json.Unmarshal(b.Input, &in) == nil && strings.TrimSpace(in.Message) != "" {
						a.Final = strings.TrimSpace(in.Message)
						w.push(a, Event{Time: r.Timestamp, Kind: "text", Summary: clip(a.Final, 600)})
					}
					continue
				}
				hasTool = true
				a.ToolCalls++
				e := toolEvent(b)
				e.Time = r.Timestamp
				if e.Edit {
					a.Edits++
				}
				a.pending[b.ID] = len(a.events)
				w.push(a, e)
			}
		}
		a.ended = !hasTool
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		out := resultText(b.Content)
		if b.IsError {
			a.Errors++
		}
		if i, ok := a.pending[b.ToolUseID]; ok && i < len(a.events) && a.events[i].ID == b.ToolUseID {
			a.events[i].Output = clip(out, 2000)
			a.events[i].IsError = b.IsError
			delete(a.pending, b.ToolUseID)
		}
		a.ended = r.ToolEndsTurn
	}
}

func (w *Watcher) push(a *Agent, e Event) {
	a.events = append(a.events, e)
	if over := len(a.events) - w.MaxEvent; over > 0 {
		a.events = append([]Event(nil), a.events[over:]...)
		for k, i := range a.pending {
			if i < over {
				delete(a.pending, k)
			} else {
				a.pending[k] = i - over
			}
		}
	}
}

func toolEvent(b block) Event {
	e := Event{Kind: "tool", Tool: b.Name, ID: b.ID}
	var in map[string]any
	json.Unmarshal(b.Input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch b.Name {
	case "Bash", "PowerShell":
		e.Summary = str("command")
	case "Edit":
		e.File, e.Old, e.New, e.Edit = str("file_path"), clip(str("old_string"), 4000), clip(str("new_string"), 4000), true
		e.Summary = e.File
	case "MultiEdit":
		e.File, e.Edit = str("file_path"), true
		e.Summary = e.File
		if edits, ok := in["edits"].([]any); ok {
			var o, n []string
			for _, x := range edits {
				if m, ok := x.(map[string]any); ok {
					ov, _ := m["old_string"].(string)
					nv, _ := m["new_string"].(string)
					o, n = append(o, ov), append(n, nv)
				}
			}
			e.Old, e.New = clip(strings.Join(o, "\n…\n"), 4000), clip(strings.Join(n, "\n…\n"), 4000)
		}
	case "Write":
		e.File, e.New, e.Edit = str("file_path"), clip(str("content"), 4000), true
		e.Summary = e.File
	case "NotebookEdit":
		e.File, e.New, e.Edit = str("notebook_path"), clip(str("new_source"), 4000), true
		e.Summary = e.File
	case "Read":
		e.File, e.Summary = str("file_path"), str("file_path")
	case "Grep", "Glob":
		e.Summary = strings.TrimSpace(str("pattern") + " " + str("path"))
	case "Agent", "Task":
		e.Summary = str("description")
	case "WebFetch":
		e.Summary = str("url")
	case "WebSearch":
		e.Summary = str("query")
	default:
		keys := make([]string, 0, len(in))
		for k := range in {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := in[k].(string); ok && s != "" {
				e.Summary = clip(s, 200)
				break
			}
		}
	}
	e.Summary = clip(e.Summary, 400)
	return e
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
				b.WriteByte('\n')
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (w *Watcher) status(a *Agent, now time.Time) string {
	idle := now.Sub(a.LastAt)
	switch {
	case a.ended && a.ID != "main":
		return "done"
	case idle < 90*time.Second:
		return "running"
	case !a.ended && idle > 10*time.Minute:
		return "stalled"
	default:
		return "idle"
	}
}

func (w *Watcher) summarize(a *Agent, now time.Time) Summary {
	s := Summary{Agent: *a, Status: w.status(a, now), Files: []string{}}
	s.Final = clip(a.Final, 4000)
	seen := map[string]bool{}
	for i := len(a.events) - 1; i >= 0; i-- {
		e := a.events[i]
		if s.LastAction == "" && e.Kind == "tool" {
			s.LastAction = e.Tool + " " + e.Summary
		}
		if e.Edit && e.File != "" && !seen[e.File] {
			seen[e.File] = true
			s.Files = append(s.Files, e.File)
		}
	}
	tok := 0
	for _, p := range a.usageAt {
		if now.Sub(p.at) <= 10*time.Minute {
			tok += p.tokens
		}
	}
	s.TokensHour = float64(tok) * 6
	return s
}

func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	now := w.Now()
	bySession := map[string][]*Agent{}
	for _, a := range w.agents {
		bySession[a.Session] = append(bySession[a.Session], a)
	}
	snap := Snapshot{Sessions: []Session{}, Collisions: []Collision{}, Generated: now}
	for sid, list := range bySession {
		info := w.sessions[sid]
		s := Session{ID: sid, Agents: []Summary{}}
		if info != nil {
			s.Project, s.Cwd = info.project, info.cwd
		}
		files := map[string]map[string]time.Time{}
		for _, a := range list {
			sum := w.summarize(a, now)
			s.Agents = append(s.Agents, sum)
			if a.LastAt.After(s.LastAt) {
				s.LastAt = a.LastAt
			}
			s.Usage.Input += a.Usage.Input
			s.Usage.Output += a.Usage.Output
			s.Usage.CacheRead += a.Usage.CacheRead
			s.Usage.CacheWrite += a.Usage.CacheWrite
			for _, e := range a.events {
				if e.Edit && e.File != "" {
					norm := strings.ToLower(filepath.Clean(e.File))
					if files[norm] == nil {
						files[norm] = map[string]time.Time{}
					}
					if e.Time.After(files[norm][a.ID]) {
						files[norm][a.ID] = e.Time
					}
				}
			}
		}
		sort.Slice(s.Agents, func(i, j int) bool {
			if (s.Agents[i].ID == "main") != (s.Agents[j].ID == "main") {
				return s.Agents[i].ID == "main"
			}
			if !s.Agents[i].StartedAt.Equal(s.Agents[j].StartedAt) {
				return s.Agents[i].StartedAt.Before(s.Agents[j].StartedAt)
			}
			return s.Agents[i].ID < s.Agents[j].ID
		})
		for f, who := range files {
			if len(who) < 2 {
				continue
			}
			c := Collision{Session: sid, File: f}
			for id, t := range who {
				c.Agents = append(c.Agents, id)
				if t.After(c.LastAt) {
					c.LastAt = t
				}
			}
			sort.Strings(c.Agents)
			snap.Collisions = append(snap.Collisions, c)
		}
		snap.Sessions = append(snap.Sessions, s)
	}
	sort.Slice(snap.Sessions, func(i, j int) bool { return snap.Sessions[i].LastAt.After(snap.Sessions[j].LastAt) })
	sort.Slice(snap.Collisions, func(i, j int) bool { return snap.Collisions[i].LastAt.After(snap.Collisions[j].LastAt) })
	return snap
}

type Detail struct {
	Summary
	Events []Event `json:"events"`
	Claims []Claim `json:"claims"`
}

func (w *Watcher) Detail(session, id string) (Detail, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.init()
	a := w.agents[key(session, id)]
	if a == nil {
		return Detail{}, false
	}
	return Detail{Summary: w.summarize(a, w.Now()), Events: append([]Event(nil), a.events...), Claims: CheckClaims(a.Final, a.events)}, true
}

func (w *Watcher) Run(stop <-chan struct{}, every time.Duration) {
	w.Poll()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			w.Poll()
		}
	}
}
