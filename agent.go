package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// AgentDeps bundles the everything-pointer passed to each agent.
type AgentDeps struct {
	LLM       *LLM
	Sandbox   *Sandbox
	Repo      *Repo
	Mem       *Memory
	Roles     *RoleStore
	Board     *Board
	SelfSrc   string // path to the daemon's own source tree
	Workspace string // path to the target repo root
	MaxSteps  int
	SelfMod   bool
}

// Tool is a single named capability exposed to the LLM.
type Tool struct {
	Name string
	Fn   func(ctx context.Context, args json.RawMessage) (result string, terminal bool, err error)
}

// Agent runs one task-attempt end to end for a single Role.
type Agent struct {
	role             Role
	task             Task
	deps             AgentDeps
	tools            map[string]Tool
	log              *slog.Logger
	attemptID        int64
	pendingHandoff   string // binary path, set by edit_self on success
	outcome          string // "done" | "fail" | "handoff"
}

// NewAgent constructs an Agent.
func NewAgent(role Role, task Task, deps AgentDeps) *Agent {
	a := &Agent{role: role, task: task, deps: deps, log: slog.Default().With("task", task.ID, "role", role.Name)}
	a.registerTools()
	return a
}

// HandoffBin returns the new binary path set by a successful edit_self.
func (a *Agent) HandoffBin() string { return a.pendingHandoff }

// Run executes the agent loop until a terminal tool, step-cap, or ctx cancel.
func (a *Agent) Run(ctx context.Context) (string, error) {
	attemptID, err := a.deps.Mem.StartAttempt(a.task.ID, a.role.ID)
	if err != nil {
		return "", fmt.Errorf("start attempt: %w", err)
	}
	a.attemptID = attemptID

	sys := RenderGenesisSystem(a.deps.Workspace, a.deps.SelfSrc)
	if a.role.Name != "genesis" {
		sys = a.role.SystemPrompt
	}
	intro, err := RenderTaskIntro(TaskIntroInput{ID: a.task.ID, Title: a.task.Title, Body: a.task.Body, Role: a.role.Name})
	if err != nil {
		return "", err
	}
	msgs := []Message{{Role: "system", Content: sys}, {Role: "user", Content: intro}}

	for step := 0; step < a.deps.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			_ = a.deps.Mem.EndAttempt(attemptID, "crash")
			return "", err
		}
		a.logEvent("step_start", map[string]any{"step": step})

		call, raw, err := a.deps.LLM.ChatTool(ctx, msgs)
		if err != nil {
			a.logEvent("llm_error", map[string]any{"error": err.Error()})
			_ = a.deps.Mem.EndAttempt(attemptID, "crash")
			return "", err
		}
		a.logEvent("llm_response", map[string]any{"raw": raw, "tool": call.Tool})

		tool, ok := a.tools[call.Tool]
		if !ok {
			toolResult := fmt.Sprintf("unknown tool %q; valid tools: %s", call.Tool, a.toolNames())
			msgs = append(msgs,
				Message{Role: "assistant", Content: raw},
				Message{Role: "user", Content: toolResult},
			)
			continue
		}
		a.logEvent("tool_call", map[string]any{"tool": call.Tool, "args": json.RawMessage(call.Args)})

		result, terminal, terr := tool.Fn(ctx, call.Args)
		if terr != nil {
			result = "error: " + terr.Error()
		}
		a.logEvent("tool_result", map[string]any{"tool": call.Tool, "ok": terr == nil, "result": truncate(result, 2000)})

		msgs = append(msgs,
			Message{Role: "assistant", Content: raw},
			Message{Role: "user", Content: result},
		)

		if terminal {
			a.outcome = terminalOutcome(call.Tool)
			_ = a.deps.Mem.EndAttempt(attemptID, a.outcome)
			return a.outcome, nil
		}
	}
	a.logEvent("step_cap", map[string]any{"max": a.deps.MaxSteps})
	a.outcome = "fail"
	_ = a.deps.Mem.EndAttempt(attemptID, "fail")
	return "fail", nil
}

func terminalOutcome(tool string) string {
	switch tool {
	case "done":
		return "done"
	case "fail":
		return "fail"
	case "edit_self":
		return "handoff"
	}
	return "fail"
}

func (a *Agent) logEvent(kind string, payload any) {
	body, _ := json.Marshal(payload)
	if err := a.deps.Mem.LogEvent(a.task.ID, &a.attemptID, kind, string(body)); err != nil {
		a.log.Warn("log event failed", "err", err)
	}
}

func (a *Agent) toolNames() string {
	out := ""
	for name := range a.tools {
		if out != "" {
			out += ", "
		}
		out += name
	}
	return out
}
