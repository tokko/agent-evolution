package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventLog is an append-only JSONL log. One record per call to Write. The log
// intentionally uses a flat file instead of SQLite so the scaffold stays
// dependency-free — the agent can add SQLite itself when it evolves into the
// target system.
type EventLog struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// NewEventLog opens (creating if needed) an append-only file at path.
func NewEventLog(path string) (*EventLog, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &EventLog{f: f, path: abs}, nil
}

// Path returns the absolute path of the log file.
func (e *EventLog) Path() string { return e.path }

// Write appends one record. kind is a short tag (e.g. "tool_call"); payload is
// any JSON-marshalable value (usually a map[string]any).
func (e *EventLog) Write(kind string, payload any) {
	if e == nil || e.f == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		"kind":    kind,
		"payload": payload,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		// Fallback: log the error marker so the file isn't silently missing events.
		b = []byte(fmt.Sprintf(`{"ts":%q,"kind":"marshal_error","payload":{"inner_kind":%q,"err":%q}}`,
			time.Now().UTC().Format(time.RFC3339Nano), kind, err.Error()))
	}
	_, _ = e.f.Write(b)
	_, _ = e.f.Write([]byte("\n"))
}

// Close flushes and closes the underlying file.
func (e *EventLog) Close() error {
	if e == nil || e.f == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.f.Close()
	e.f = nil
	return err
}
