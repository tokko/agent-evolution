package main

import (
	"net/http"
	"strconv"
	"strings"
)

// Server exposes the kanban UI + minimal REST.
type Server struct {
	Board *Board
	Mem   *Memory
}

// NewServer constructs a Server.
func NewServer(b *Board, m *Memory) *Server { return &Server{Board: b, Mem: m} }

// Handler returns the registered http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleBoard)
	mux.HandleFunc("POST /tasks", s.handleCreate)
	mux.HandleFunc("POST /tasks/{id}/move", s.handleMove)
	mux.HandleFunc("GET /tasks/{id}", s.handleTask)
	return mux
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	view, err := BuildBoardView(s.Board)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderBoard(w, view); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	body := r.FormValue("body")
	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	if _, err := s.Board.AddTask(title, body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	to := r.FormValue("to")
	if err := s.Board.MoveTask(id, to); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	t, err := s.Board.Get(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	events, err := s.Mem.EventsByTask(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderTask(w, TaskView{Task: t, Events: events}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
