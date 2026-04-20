package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Task is a single kanban card.
type Task struct {
	ID           int64
	Title        string
	Body         string
	Column       string
	AssignedRole *int64
	ParentTask   *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Board is the set of task-CRUD operations, backed by SQLite.
type Board struct{ db *sql.DB }

// NewBoard constructs a Board from a Memory.
func NewBoard(m *Memory) *Board { return &Board{db: m.DB} }

// AllColumns in display order.
var AllColumns = []string{"todo", "doing", "review", "done", "failed"}

// AddTask inserts a new card in the "todo" column and returns it.
func (b *Board) AddTask(title, body string) (Task, error) {
	now := time.Now().Unix()
	res, err := b.db.Exec(
		`INSERT INTO tasks (title, body, column_name, created_at, updated_at) VALUES (?,?,?,?,?)`,
		title, body, "todo", now, now,
	)
	if err != nil {
		return Task{}, err
	}
	id, _ := res.LastInsertId()
	return b.Get(id)
}

// AddSubtask inserts a child card linked to parent.
func (b *Board) AddSubtask(parentID int64, title, body string, assignedRole *int64) (Task, error) {
	now := time.Now().Unix()
	res, err := b.db.Exec(
		`INSERT INTO tasks (title, body, column_name, assigned_role, parent_task, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		title, body, "todo", assignedRole, parentID, now, now,
	)
	if err != nil {
		return Task{}, err
	}
	id, _ := res.LastInsertId()
	return b.Get(id)
}

// MoveTask shifts a card to a different column.
func (b *Board) MoveTask(id int64, toCol string) error {
	if !validColumn(toCol) {
		return fmt.Errorf("invalid column %q", toCol)
	}
	_, err := b.db.Exec(
		`UPDATE tasks SET column_name = ?, updated_at = ? WHERE id = ?`,
		toCol, time.Now().Unix(), id,
	)
	return err
}

// AssignRole persists the scheduler's role decision for a task.
func (b *Board) AssignRole(id, roleID int64) error {
	_, err := b.db.Exec(
		`UPDATE tasks SET assigned_role = ?, updated_at = ? WHERE id = ?`,
		roleID, time.Now().Unix(), id,
	)
	return err
}

// ListByColumn returns every task in a column, oldest first.
func (b *Board) ListByColumn(col string) ([]Task, error) {
	rows, err := b.db.Query(
		`SELECT id, title, body, column_name, assigned_role, parent_task, created_at, updated_at
		   FROM tasks WHERE column_name = ? ORDER BY updated_at ASC`, col,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns a single task by id.
func (b *Board) Get(id int64) (Task, error) {
	row := b.db.QueryRow(
		`SELECT id, title, body, column_name, assigned_role, parent_task, created_at, updated_at FROM tasks WHERE id = ?`,
		id,
	)
	return scanTask(row)
}

// PickOldestTodo returns the longest-pending todo card, or (Task{}, sql.ErrNoRows).
func (b *Board) PickOldestTodo() (Task, error) {
	row := b.db.QueryRow(
		`SELECT id, title, body, column_name, assigned_role, parent_task, created_at, updated_at
		   FROM tasks WHERE column_name = 'todo' ORDER BY updated_at ASC LIMIT 1`,
	)
	return scanTask(row)
}

// FindResumable returns the first 'doing' task (used after edit_self handoff).
func (b *Board) FindResumable() (Task, error) {
	row := b.db.QueryRow(
		`SELECT id, title, body, column_name, assigned_role, parent_task, created_at, updated_at
		   FROM tasks WHERE column_name = 'doing' ORDER BY updated_at ASC LIMIT 1`,
	)
	return scanTask(row)
}

func scanTask(s scanner) (Task, error) {
	var t Task
	var assigned, parent sql.NullInt64
	var created, updated int64
	if err := s.Scan(&t.ID, &t.Title, &t.Body, &t.Column, &assigned, &parent, &created, &updated); err != nil {
		return Task{}, err
	}
	if assigned.Valid {
		t.AssignedRole = &assigned.Int64
	}
	if parent.Valid {
		t.ParentTask = &parent.Int64
	}
	t.CreatedAt = time.Unix(created, 0)
	t.UpdatedAt = time.Unix(updated, 0)
	return t, nil
}

func validColumn(c string) bool {
	for _, v := range AllColumns {
		if v == c {
			return true
		}
	}
	return false
}

// IsNoTask reports whether err means "no task found".
func IsNoTask(err error) bool { return errors.Is(err, sql.ErrNoRows) }
