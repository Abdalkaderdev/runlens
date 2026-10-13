package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

func line(t *testing.T, typ string, at time.Duration, content any, usage map[string]int) string {
	t.Helper()
	m := map[string]any{"type": typ, "timestamp": t0.Add(at).Format(time.RFC3339Nano), "cwd": "/work", "message": map[string]any{"content": content}}
	if usage != nil {
		m["message"].(map[string]any)["usage"] = usage
	}
	b, _ := json.Marshal(m)
	return string(b) + "\n"
}

func tool(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func result(id, out string, isErr bool) map[string]any {
	return map[string]any{"type": "tool_result", "tool_use_id": id, "content": out, "is_error": isErr}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func fixture(t *testing.T) (string, *Watcher) {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	subs := filepath.Join(proj, "s1", "subagents")
	os.MkdirAll(subs, 0o755)
	main := line(t, "user", 0, "build things", nil) +
		line(t, "assistant", time.Second, []any{tool("m1", "Agent", map[string]any{"description": "Build pack"})}, map[string]int{"input_tokens": 10, "output_tokens": 5})
	os.WriteFile(filepath.Join(proj, "s1.jsonl"), []byte(main), 0o644)

	a := line(t, "assistant", 2*time.Second, []any{tool("e1", "Edit", map[string]any{"file_path": "/work/src/app.ts", "old_string": "a", "new_string": "b"})}, map[string]int{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 1000, "cache_creation_input_tokens": 50}) +
		line(t, "user", 3*time.Second, []any{result("e1", "ok", false)}, nil) +
		line(t, "assistant", 4*time.Second, []any{tool("b1", "Bash", map[string]any{"command": "npm test"})}, nil) +
		line(t, "user", 5*time.Second, []any{result("b1", "ℹ pass 12\nℹ fail 0", false)}, nil) +
		line(t, "assistant", 6*time.Second, []any{text("Done. All 12 tests pass and the typecheck is clean. Committed on main.")}, nil)
	os.WriteFile(filepath.Join(subs, "agent-aaa.jsonl"), []byte(a), 0o644)
	os.WriteFile(filepath.Join(subs, "agent-aaa.meta.json"), []byte(`{"agentType":"general-purpose","description":"Build pack"}`), 0o644)

	b := line(t, "assistant", 2*time.Second, []any{tool("w1", "Write", map[string]any{"file_path": "/work/SRC/app.ts", "content": "x"})}, nil) +
		line(t, "user", 3*time.Second, []any{result("w1", "ok", false)}, nil)
	os.WriteFile(filepath.Join(subs, "agent-bbb.jsonl"), []byte(b), 0o644)

	w := New(root, 24*time.Hour)
	w.Now = func() time.Time { return t0.Add(10 * time.Second) }
	return root, w
}

func TestSnapshot(t *testing.T) {
	_, w := fixture(t)
	if err := w.Poll(); err != nil {
		t.Fatal(err)
	}
	snap := w.Snapshot()
	if len(snap.Sessions) != 1 || len(snap.Sessions[0].Agents) != 3 {
		t.Fatalf("sessions/agents = %+v", snap.Sessions)
	}
	ag := snap.Sessions[0].Agents
	if ag[0].ID != "main" {
		t.Fatalf("main should sort first, got %s", ag[0].ID)
	}
	var a Summary
	for _, x := range ag {
		if x.ID == "aaa" {
			a = x
		}
	}
	if a.Description != "Build pack" || a.Type != "general-purpose" {
		t.Fatalf("meta not read: %+v", a)
	}
	if a.Status != "done" {
		t.Fatalf("status = %s, want done", a.Status)
	}
	if a.ToolCalls != 2 || a.Edits != 1 || a.Usage.CacheRead != 1000 || a.Usage.Billable() != 170 {
		t.Fatalf("counts wrong: %+v", a)
	}
	if len(snap.Collisions) != 1 || strings.Join(snap.Collisions[0].Agents, ",") != "aaa,bbb" {
		t.Fatalf("collisions = %+v", snap.Collisions)
	}
}

func TestIncrementalAndPartialLines(t *testing.T) {
	root, w := fixture(t)
	w.Poll()
	p := filepath.Join(root, "proj", "s1", "subagents", "agent-bbb.jsonl")
	full := line(t, "assistant", 7*time.Second, []any{tool("b2", "Bash", map[string]any{"command": "go test ./..."})}, nil)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(full[:20])
	f.Close()
	w.Poll()
	d, _ := w.Detail("s1", "bbb")
	if d.ToolCalls != 1 {
		t.Fatalf("partial line must not be parsed yet, toolCalls=%d", d.ToolCalls)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(full[20:])
	f.Close()
	w.Poll()
	d, _ = w.Detail("s1", "bbb")
	if d.ToolCalls != 2 || d.Events[len(d.Events)-1].Summary != "go test ./..." {
		t.Fatalf("after completing line: %+v", d.Events)
	}
}

func TestClaims(t *testing.T) {
	_, w := fixture(t)
	w.Poll()
	d, ok := w.Detail("s1", "aaa")
	if !ok {
		t.Fatal("agent missing")
	}
	got := map[string]string{}
	for _, c := range d.Claims {
		got[c.Kind] = c.Verdict
	}
	if got["tests"] != "supported" || got["typecheck"] != "unverified" || got["committed"] != "unverified" {
		t.Fatalf("claims = %+v", d.Claims)
	}
}

func TestClaimVerdicts(t *testing.T) {
	ev := func(kind, tool, summary, out string, isErr, edit bool) Event {
		return Event{Kind: kind, Tool: tool, Summary: summary, Output: out, IsError: isErr, Edit: edit}
	}
	cases := []struct {
		name   string
		final  string
		events []Event
		want   string
	}{
		{"stale", "All tests pass.", []Event{ev("tool", "Bash", "npm test", "ok", false, false), ev("tool", "Edit", "a.ts", "", false, true)}, "stale"},
		{"failed output", "Tests pass.", []Event{ev("tool", "Bash", "node --test", "ℹ pass 3\nℹ fail 2", false, false)}, "contradicted"},
		{"later failure", "Tests pass.", []Event{ev("tool", "Bash", "go test ./...", "ok", false, false), ev("tool", "Bash", "go test ./...", "--- FAIL: TestX", false, false)}, "contradicted"},
		{"negated", "Tests did not pass yet.", []Event{}, ""},
		{"build green but failed", "The build is green.", []Event{ev("tool", "Bash", "npm run build", "Type error", true, false)}, "contradicted"},
		{"pushed ok", "Pushed to origin.", []Event{ev("tool", "Bash", "git push -q", "", false, false)}, "supported"},
	}
	for _, c := range cases {
		claims := CheckClaims(c.final, c.events)
		got := ""
		if len(claims) > 0 {
			got = claims[0].Verdict
		}
		if got != c.want {
			t.Errorf("%s: verdict %q, want %q (%+v)", c.name, got, c.want, claims)
		}
	}
}

func TestHandbackEndsAgent(t *testing.T) {
	root := t.TempDir()
	subs := filepath.Join(root, "p", "s2", "subagents")
	os.MkdirAll(subs, 0o755)
	os.WriteFile(filepath.Join(root, "p", "s2.jsonl"), []byte(line(t, "user", 0, "go", nil)), 0o644)
	end := map[string]any{"type": "user", "timestamp": t0.Add(3 * time.Second).Format(time.RFC3339Nano), "toolEndsTurn": true,
		"message": map[string]any{"content": []any{result("h1", "delivered", false)}}}
	eb, _ := json.Marshal(end)
	body := line(t, "assistant", time.Second, []any{tool("b1", "Bash", map[string]any{"command": "go test ./..."})}, nil) +
		line(t, "user", 2*time.Second, []any{result("b1", "ok", false)}, nil) +
		line(t, "assistant", 3*time.Second, []any{tool("h1", "SubagentHandback", map[string]any{"message": "All tests pass."})}, nil) +
		string(eb) + "\n"
	os.WriteFile(filepath.Join(subs, "agent-ccc.jsonl"), []byte(body), 0o644)
	w := New(root, 24*time.Hour)
	w.Now = func() time.Time { return t0.Add(20 * time.Minute) }
	w.Poll()
	d, _ := w.Detail("s2", "ccc")
	if d.Status != "done" || d.Final != "All tests pass." || d.ToolCalls != 1 {
		t.Fatalf("handback: status=%s final=%q calls=%d", d.Status, d.Final, d.ToolCalls)
	}
	if len(d.Claims) != 1 || d.Claims[0].Verdict != "supported" {
		t.Fatalf("claims = %+v", d.Claims)
	}
}

func TestStatuses(t *testing.T) {
	w := New("", time.Hour)
	now := t0
	a := &Agent{ID: "x", LastAt: now.Add(-30 * time.Second)}
	if s := w.status(a, now); s != "running" {
		t.Fatalf("recent = %s", s)
	}
	a.LastAt = now.Add(-20 * time.Minute)
	if s := w.status(a, now); s != "stalled" {
		t.Fatalf("old unfinished = %s", s)
	}
	a.ended = true
	if s := w.status(a, now); s != "done" {
		t.Fatalf("ended = %s", s)
	}
}
