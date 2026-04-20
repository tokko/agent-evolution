package main

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Memory wraps the SQLite connection and all persistence helpers.
type Memory struct{ DB *sql.DB }

// Event is a single row from events, returned to the UI timeline.
type Event struct {
	ID, TaskID int64
	AttemptID  *int64
	Kind       string
	Payload    string
	CreatedAt  time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS roles (
  id            INTEGER PRIMARY KEY,
  name          TEXT    NOT NULL UNIQUE,
  system_prompt TEXT    NOT NULL,
  parent_id     INTEGER REFERENCES roles(id),
  created_at    INTEGER NOT NULL,
  active        INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS tasks (
  id            INTEGER PRIMARY KEY,
  title         TEXT    NOT NULL,
  body          TEXT    NOT NULL DEFAULT '',
  column_name   TEXT    NOT NULL CHECK(column_name IN ('todo','doing','review','done','failed')),
  assigned_role INTEGER REFERENCES roles(id),
  parent_task   INTEGER REFERENCES tasks(id),
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_col_updated ON tasks(column_name, updated_at);
CREATE TABLE IF NOT EXISTS attempts (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  role_id    INTEGER NOT NULL REFERENCES roles(id),
  started_at INTEGER NOT NULL,
  ended_at   INTEGER,
  outcome    TEXT
);
CREATE INDEX IF NOT EXISTS idx_attempts_task ON attempts(task_id);
CREATE TABLE IF NOT EXISTS events (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  attempt_id INTEGER REFERENCES attempts(id),
  kind       TEXT    NOT NULL,
  payload    TEXT    NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_task_created ON events(task_id, created_at);
`

// Open opens (or creates) the SQLite DB and applies the schema.
func Open(path string) (*Memory, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Memory{DB: db}, nil
}

// Close closes the underlying connection.
func (m *Memory) Close() error { return m.DB.Close() }

// LogEvent appends an event row. payload must be valid JSON.
func (m *Memory) LogEvent(taskID int64, attemptID *int64, kind, payload string) error {
	_, err := m.DB.Exec(
		`INSERT INTO events (task_id, attempt_id, kind, payload, created_at) VALUES (?,?,?,?,?)`,
		taskID, attemptID, kind, payload, time.Now().Unix(),
	)
	return err
}

// EventsByTask returns events for a task, oldest first.
func (m *Memory) EventsByTask(taskID int64) ([]Event, error) {
	rows, err := m.DB.Query(
		`SELECT id, task_id, attempt_id, kind, payload, created_at FROM events WHERE task_id = ? ORDER BY id ASC`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var created int64
		var attempt sql.NullInt64
		if err := rows.Scan(&e.ID, &e.TaskID, &attempt, &e.Kind, &e.Payload, &created); err != nil {
			return nil, err
		}
		if attempt.Valid {
			e.AttemptID = &attempt.Int64
		}
		e.CreatedAt = time.Unix(created, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// StartAttempt opens a new attempt row and returns its id.
func (m *Memory) StartAttempt(taskID, roleID int64) (int64, error) {
	res, err := m.DB.Exec(
		`INSERT INTO attempts (task_id, role_id, started_at) VALUES (?,?,?)`,
		taskID, roleID, time.Now().Unix(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EndAttempt stamps an attempt's outcome and end time.
func (m *Memory) EndAttempt(id int64, outcome string) error {
	_, err := m.DB.Exec(
		`UPDATE attempts SET ended_at = ?, outcome = ? WHERE id = ?`,
		time.Now().Unix(), outcome, id,
	)
	return err
}
