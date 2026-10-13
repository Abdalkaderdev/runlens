package agents

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type StatusIn struct {
	Status string `json:"status,omitempty" jsonschema:"only agents with this status: running, idle, stalled or done"`
}

type AgentRow struct {
	Session     string  `json:"session"`
	Agent       string  `json:"agent"`
	Description string  `json:"description,omitempty"`
	Status      string  `json:"status"`
	LastAction  string  `json:"last_action,omitempty"`
	LastAt      string  `json:"last_at"`
	ToolCalls   int     `json:"tool_calls"`
	Errors      int     `json:"errors"`
	Edits       int     `json:"edits"`
	Tokens      int     `json:"billable_tokens"`
	PerHour     float64 `json:"tokens_per_hour_last_10m"`
}

type DetailIn struct {
	Session string `json:"session" jsonschema:"session id from agents_status"`
	Agent   string `json:"agent" jsonschema:"agent id from agents_status, or main"`
	Events  int    `json:"events,omitempty" jsonschema:"how many of the latest events to return, default 30"`
}

func Register(s *mcp.Server, w *Watcher) {
	no := false
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}

	mcp.AddTool(s, &mcp.Tool{Name: "agents_status", Annotations: ro, Description: "Coding agents running on this machine (Claude Code sessions and their subagents): status, last action, tool calls, errors, edits and token burn, plus files edited by more than one agent."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in StatusIn) (*mcp.CallToolResult, any, error) {
			w.Poll()
			snap := w.Snapshot()
			rows := []AgentRow{}
			for _, sess := range snap.Sessions {
				for _, a := range sess.Agents {
					if in.Status != "" && a.Status != in.Status {
						continue
					}
					rows = append(rows, AgentRow{
						Session: sess.ID, Agent: a.ID, Description: a.Description, Status: a.Status, LastAction: a.LastAction,
						LastAt: a.LastAt.Format("2006-01-02T15:04:05Z07:00"), ToolCalls: a.ToolCalls, Errors: a.Errors, Edits: a.Edits,
						Tokens: a.Usage.Billable(), PerHour: a.TokensHour,
					})
				}
			}
			return nil, map[string]any{"agents": rows, "file_collisions": snap.Collisions}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "agent_detail", Annotations: ro, Description: "One agent's recent tool calls with commands, edited files and results, and a claim check of its final report: each claim such as tests pass or pushed is marked supported, stale, contradicted or unverified against the commands it actually ran."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in DetailIn) (*mcp.CallToolResult, any, error) {
			w.Poll()
			d, ok := w.Detail(in.Session, in.Agent)
			if !ok {
				return nil, nil, fmt.Errorf("agent %s/%s not found; call agents_status for current ids", in.Session, in.Agent)
			}
			n := in.Events
			if n <= 0 {
				n = 30
			}
			ev := d.Events
			if len(ev) > n {
				ev = ev[len(ev)-n:]
			}
			for i := range ev {
				ev[i].Old, ev[i].New = clip(ev[i].Old, 400), clip(ev[i].New, 400)
				ev[i].Output = clip(ev[i].Output, 400)
			}
			return nil, map[string]any{
				"agent": d.ID, "description": d.Description, "status": d.Status, "final_report": clip(d.Final, 1500),
				"claims": d.Claims, "files_edited": d.Files, "events": ev,
			}, nil
		})
}
