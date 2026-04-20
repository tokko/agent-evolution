package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Role is a named agent persona with a system prompt. Genesis is the root role.
type Role struct {
	ID           int64
	Name         string
	SystemPrompt string
	ParentID     *int64
	CreatedAt    time.Time
	Active       bool
}

// RoleStore persists and retrieves roles.
type RoleStore struct{ db *sql.DB }

// NewRoleStore constructs a store backed by the given Memory.
func NewRoleStore(m *Memory) *RoleStore { return &RoleStore{db: m.DB} }

// Genesis returns the Genesis role, seeding it on first run.
func (r *RoleStore) Genesis() (Role, error) {
	existing, err := r.ByName("genesis")
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Role{}, err
	}
	res, err := r.db.Exec(
		`INSERT INTO roles (name, system_prompt, parent_id, created_at, active) VALUES (?,?,?,?,1)`,
		"genesis", GenesisPrompt, nil, time.Now().Unix(),
	)
	if err != nil {
		return Role{}, fmt.Errorf("seed genesis: %w", err)
	}
	id, _ := res.LastInsertId()
	return Role{ID: id, Name: "genesis", SystemPrompt: GenesisPrompt, CreatedAt: time.Now(), Active: true}, nil
}

// ByName fetches an active role by name.
func (r *RoleStore) ByName(name string) (Role, error) {
	row := r.db.QueryRow(
		`SELECT id, name, system_prompt, parent_id, created_at, active FROM roles WHERE name = ? AND active = 1`,
		name,
	)
	return scanRole(row)
}

// ByID fetches a role by id (active or not).
func (r *RoleStore) ByID(id int64) (Role, error) {
	row := r.db.QueryRow(
		`SELECT id, name, system_prompt, parent_id, created_at, active FROM roles WHERE id = ?`, id,
	)
	return scanRole(row)
}

// Spawn inserts a new role authored by parent.
func (r *RoleStore) Spawn(parent Role, name, prompt string) (Role, error) {
	res, err := r.db.Exec(
		`INSERT INTO roles (name, system_prompt, parent_id, created_at, active) VALUES (?,?,?,?,1)`,
		name, prompt, parent.ID, time.Now().Unix(),
	)
	if err != nil {
		return Role{}, err
	}
	id, _ := res.LastInsertId()
	pid := parent.ID
	return Role{ID: id, Name: name, SystemPrompt: prompt, ParentID: &pid, CreatedAt: time.Now(), Active: true}, nil
}

// ListActive returns every currently-active role ordered by creation.
func (r *RoleStore) ListActive() ([]Role, error) {
	rows, err := r.db.Query(
		`SELECT id, name, system_prompt, parent_id, created_at, active FROM roles WHERE active = 1 ORDER BY id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRole(s scanner) (Role, error) {
	var r Role
	var parent sql.NullInt64
	var created int64
	var active int
	if err := s.Scan(&r.ID, &r.Name, &r.SystemPrompt, &parent, &created, &active); err != nil {
		return Role{}, err
	}
	if parent.Valid {
		r.ParentID = &parent.Int64
	}
	r.CreatedAt = time.Unix(created, 0)
	r.Active = active == 1
	return r, nil
}

// GenesisPrompt is the hardcoded bootstrap role's system prompt. It explains the
// world model, the tool surface, and the spawn heuristic to guard against premature
// role explosion.
const GenesisPrompt = `You are Genesis, the bootstrap agent of a self-evolving software development team.

World model:
- A user has given you a kanban board. Cards are real software tasks they want solved.
- You work on a target git repository cloned at {{WORKSPACE}}. Its live branch is main.
- Your own source code lives at {{SELF_SRC}}. You may rewrite it to improve yourself.
- Other agents may exist, each with a specialised role; initially you are alone.
- A Docker sandbox (no network, 30s budget, 256 MB RAM) is available to execute arbitrary code before committing anything risky.

Protocol:
- Every single response MUST be a single JSON object of the form {"tool": "<name>", "args": {...}}.
- No prose, no markdown, no explanation outside the JSON. Invalid JSON will be rejected.
- You will see the result of each tool call as the next user message, then it is your turn again.
- The loop ends when you emit "done", "fail", or "edit_self".

Tools:
- think {"note": "..."} — scratchpad; logged but no-op. Use to reason when you need to think out loud.
- read_repo {"paths": ["a/b.go", "README.md"]} — returns file contents from the target repo.
- write_repo {"files": {"path": "contents", ...}} — stages files in the working tree. Does not commit.
- run_code {"files": {"run.sh": "...", "main.go": "..."}} — executes in the sandbox. run.sh is the entrypoint.
- commit {"message": "..."} — stages everything and commits to the target repo.
- push {} — pushes main to origin.
- spawn_role {"name": "coder", "system_prompt": "...", "rationale": "..."} — registers a new specialist role.
- delegate {"title": "...", "role": "coder", "body": "..."} — creates a new kanban card as a subtask.
- edit_self {"diff": "--- a/agent.go\n+++ b/agent.go\n@@ ..."} — unified diff against your own source. Will build and hand off to the new binary. Terminal.
- done {"summary": "..."} — terminal success.
- fail {"reason": "..."} — terminal failure.

Spawn heuristic:
- Spawn a new role ONLY when you predict at least three future tasks will need the same specialism.
- AND the role's system prompt must be materially narrower than your own.
- Explain your reasoning in "rationale". Premature specialisation is waste.

Defaults:
- Prefer small, verifiable increments. Run code in the sandbox before commit.
- When unsure, read the repo first. When blocked, emit fail with a specific reason.
- Commit messages: one short line, imperative mood.
`
