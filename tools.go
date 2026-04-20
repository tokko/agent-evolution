package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// registerTools wires the Agent's tool map. Each tool closes over the agent's
// deps. Arg decoding happens inside the tool; arg-schema errors are returned
// to the LLM as in-band results rather than Go errors so the loop continues.
func (a *Agent) registerTools() {
	a.tools = map[string]Tool{
		"think": {Name: "think", Fn: a.toolThink},
		"read_repo": {Name: "read_repo", Fn: a.toolReadRepo},
		"write_repo": {Name: "write_repo", Fn: a.toolWriteRepo},
		"run_code": {Name: "run_code", Fn: a.toolRunCode},
		"commit": {Name: "commit", Fn: a.toolCommit},
		"push": {Name: "push", Fn: a.toolPush},
		"spawn_role": {Name: "spawn_role", Fn: a.toolSpawnRole},
		"delegate": {Name: "delegate", Fn: a.toolDelegate},
		"edit_self": {Name: "edit_self", Fn: a.toolEditSelf},
		"done": {Name: "done", Fn: a.toolDone},
		"fail": {Name: "fail", Fn: a.toolFail},
	}
}

func (a *Agent) toolThink(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Note string `json:"note"` }
	_ = json.Unmarshal(raw, &args)
	return "thought logged: " + truncate(args.Note, 200), false, nil
}

func (a *Agent) toolReadRepo(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Paths []string `json:"paths"` }
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, err
	}
	files, err := a.deps.Repo.Read(args.Paths)
	if err != nil {
		return "", false, err
	}
	body, _ := json.Marshal(files)
	a.logEvent("repo_read", map[string]any{"paths": args.Paths})
	return string(body), false, nil
}

func (a *Agent) toolWriteRepo(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Files map[string]string `json:"files"` }
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, err
	}
	if err := a.deps.Repo.Write(args.Files); err != nil {
		return "", false, err
	}
	names := make([]string, 0, len(args.Files))
	for k := range args.Files {
		names = append(names, k)
	}
	a.logEvent("repo_write", map[string]any{"paths": names})
	return fmt.Sprintf("wrote %d file(s): %v", len(args.Files), names), false, nil
}

func (a *Agent) toolRunCode(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Files map[string]string `json:"files"` }
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, err
	}
	res, err := a.deps.Sandbox.Execute(ctx, args.Files)
	if err != nil {
		return "", false, err
	}
	a.logEvent("sandbox_run", map[string]any{
		"exit": res.ExitCode, "timed_out": res.TimedOut,
		"stdout": truncate(res.Stdout, 2000), "stderr": truncate(res.Stderr, 2000),
	})
	body, _ := json.Marshal(res)
	return string(body), false, nil
}

func (a *Agent) toolCommit(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Message string `json:"message"` }
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, err
	}
	if args.Message == "" {
		return "commit message required", false, nil
	}
	sha, err := a.deps.Repo.Commit(ctx, args.Message)
	if err != nil {
		return "", false, err
	}
	a.logEvent("commit", map[string]any{"sha": sha, "message": args.Message})
	return "committed " + sha, false, nil
}

func (a *Agent) toolPush(ctx context.Context, _ json.RawMessage) (string, bool, error) {
	if err := a.deps.Repo.Push(ctx); err != nil {
		return "", false, err
	}
	a.logEvent("push", map[string]any{"branch": a.deps.Repo.Branch})
	return "pushed", false, nil
}

func (a *Agent) toolSpawnRole(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct {
		Name, SystemPrompt, Rationale string
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, err
	}
	if args.Name == "" || args.SystemPrompt == "" {
		return "spawn_role requires name and system_prompt", false, nil
	}
	role, err := a.deps.Roles.Spawn(a.role, args.Name, args.SystemPrompt)
	if err != nil {
		return "", false, err
	}
	a.logEvent("role_spawned", map[string]any{"name": args.Name, "id": role.ID, "rationale": args.Rationale})
	return fmt.Sprintf("spawned role %q (id=%d)", args.Name, role.ID), false, nil
}

func (a *Agent) toolDelegate(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Title, Role, Body string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, err
	}
	if args.Title == "" {
		return "delegate requires title", false, nil
	}
	var roleID *int64
	if args.Role != "" {
		r, err := a.deps.Roles.ByName(args.Role)
		if err == nil {
			roleID = &r.ID
		}
	}
	sub, err := a.deps.Board.AddSubtask(a.task.ID, args.Title, args.Body, roleID)
	if err != nil {
		return "", false, err
	}
	a.logEvent("delegated", map[string]any{"child_task": sub.ID, "role": args.Role})
	return fmt.Sprintf("delegated as task #%d", sub.ID), false, nil
}

func (a *Agent) toolEditSelf(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	if !a.deps.SelfMod {
		return "self-mod is disabled (SELF_MOD_ENABLED=false)", false, nil
	}
	var args struct{ Diff string `json:"diff"` }
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false, err
	}
	if args.Diff == "" {
		return "diff required", false, nil
	}
	newBin, err := ApplyAndBuild(ctx, a.deps.SelfSrc, args.Diff)
	if err != nil {
		a.logEvent("selfmod_failed", map[string]any{"error": err.Error()})
		return "self-mod failed: " + err.Error(), false, nil
	}
	a.pendingHandoff = newBin
	a.logEvent("handoff_pending", map[string]any{"new_bin": newBin})
	return "self-mod applied; handoff to " + newBin, true, nil
}

func (a *Agent) toolDone(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Summary string `json:"summary"` }
	_ = json.Unmarshal(raw, &args)
	a.logEvent("done", map[string]any{"summary": args.Summary})
	return "task marked done: " + args.Summary, true, nil
}

func (a *Agent) toolFail(ctx context.Context, raw json.RawMessage) (string, bool, error) {
	var args struct{ Reason string `json:"reason"` }
	_ = json.Unmarshal(raw, &args)
	a.logEvent("failed", map[string]any{"reason": args.Reason})
	return "task marked failed: " + args.Reason, true, nil
}
