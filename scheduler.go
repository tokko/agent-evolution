package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// Scheduler picks the next todo card and runs an Agent against it, one at a time.
type Scheduler struct {
	Deps AgentDeps
	Poll time.Duration
	log  *slog.Logger

	// HandoffBin is set when an agent's edit_self tool succeeded; main.go reads
	// this after Loop returns to decide whether to syscall.Exec the new binary.
	HandoffBin string
	// HandoffTaskID is the task that triggered the pending handoff, so we can
	// resume it under the new binary.
	HandoffTaskID int64
}

// NewScheduler constructs a Scheduler.
func NewScheduler(deps AgentDeps, poll time.Duration) *Scheduler {
	return &Scheduler{Deps: deps, Poll: poll, log: slog.Default().With("component", "scheduler")}
}

// Loop runs until ctx is cancelled or an edit_self handoff is requested.
// On handoff it returns nil with HandoffBin set.
func (s *Scheduler) Loop(ctx context.Context, resumeTaskID int64) error {
	// Resume path: if we booted with --resume-task, pick up that task directly.
	if resumeTaskID > 0 {
		t, err := s.Deps.Board.Get(resumeTaskID)
		if err == nil {
			s.log.Info("resuming task after handoff", "task", resumeTaskID)
			_ = s.Deps.Mem.LogEvent(t.ID, nil, "resumed", `{}`)
			_ = s.Deps.Board.MoveTask(t.ID, "todo") // put it back in todo for fresh attempt
		}
	}

	ticker := time.NewTicker(s.Poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		t, err := s.Deps.Board.PickOldestTodo()
		if err != nil {
			if IsNoTask(err) {
				continue
			}
			s.log.Warn("pickNext", "err", err)
			continue
		}
		if err := s.runTask(ctx, t); err != nil {
			s.log.Warn("runTask", "err", err, "task", t.ID)
		}
		if s.HandoffBin != "" {
			return nil
		}
	}
}

func (s *Scheduler) runTask(ctx context.Context, t Task) error {
	if err := s.Deps.Board.MoveTask(t.ID, "doing"); err != nil {
		return err
	}
	role, err := s.assignRole(ctx, t)
	if err != nil {
		_ = s.Deps.Board.MoveTask(t.ID, "failed")
		return err
	}
	_ = s.Deps.Board.AssignRole(t.ID, role.ID)
	payload, _ := json.Marshal(map[string]any{"role": role.Name, "role_id": role.ID})
	_ = s.Deps.Mem.LogEvent(t.ID, nil, "route", string(payload))

	ag := NewAgent(role, t, s.Deps)
	outcome, err := ag.Run(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = s.Deps.Board.MoveTask(t.ID, "todo") // put back for next run
			return err
		}
		_ = s.Deps.Board.MoveTask(t.ID, "failed")
		return err
	}
	switch outcome {
	case "done":
		_ = s.Deps.Board.MoveTask(t.ID, "done")
	case "handoff":
		s.HandoffBin = ag.HandoffBin()
		s.HandoffTaskID = t.ID
		// Leave task in "doing" so resume picks it up.
	default:
		_ = s.Deps.Board.MoveTask(t.ID, "failed")
	}
	return nil
}

// assignRole asks the LLM to pick a role for the task. Short-circuits to
// Genesis when only Genesis exists.
func (s *Scheduler) assignRole(ctx context.Context, t Task) (Role, error) {
	if t.AssignedRole != nil {
		return s.Deps.Roles.ByID(*t.AssignedRole)
	}
	active, err := s.Deps.Roles.ListActive()
	if err != nil {
		return Role{}, err
	}
	if len(active) == 1 {
		return active[0], nil
	}
	sums := make([]RoleSummary, 0, len(active))
	for _, r := range active {
		sums = append(sums, RoleSummary{Name: r.Name, Summary: truncate(r.SystemPrompt, 120)})
	}
	prompt, err := RenderRouter(RouterInput{Roles: sums, Title: t.Title, Body: t.Body})
	if err != nil {
		return Role{}, err
	}
	call, _, err := s.Deps.LLM.ChatTool(ctx, []Message{
		{Role: "system", Content: "You are a router. Reply with {\"role\":\"<name>\"} only."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return s.Deps.Roles.Genesis() // fail open to genesis
	}
	var args struct{ Role string `json:"role"` }
	if json.Unmarshal(call.Args, &args) != nil || args.Role == "" {
		args.Role = call.Tool // tolerate {"role": "x"} or {"tool": "x"}
	}
	if role, err := s.Deps.Roles.ByName(args.Role); err == nil {
		return role, nil
	}
	return s.Deps.Roles.Genesis()
}
