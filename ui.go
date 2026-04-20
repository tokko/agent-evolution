package main

import (
	"embed"
	"html/template"
	"io"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

var tpl = template.Must(template.New("root").Funcs(template.FuncMap{
	"fmtTime":  func(t time.Time) string { return t.Format("Jan 02 15:04:05") },
	"derefInt": func(p *int64) int64 { if p == nil { return 0 }; return *p },
}).ParseFS(templatesFS, "templates/*.html"))

// ColumnView is the board page's per-column struct.
type ColumnView struct {
	Name  string
	Prev  string
	Next  string
	Tasks []Task
}

// BoardView is the board page data.
type BoardView struct{ Columns []ColumnView }

// TaskView is the task-detail page data.
type TaskView struct {
	Task   Task
	Events []Event
}

// RenderBoard writes board.html with the given column views.
func RenderBoard(w io.Writer, v BoardView) error {
	return tpl.ExecuteTemplate(w, "board.html", v)
}

// RenderTask writes task.html for a single task.
func RenderTask(w io.Writer, v TaskView) error {
	return tpl.ExecuteTemplate(w, "task.html", v)
}

// BuildBoardView constructs the 5-column view from the board, wiring prev/next
// so the ← / → move buttons know where to go.
func BuildBoardView(b *Board) (BoardView, error) {
	cols := AllColumns
	out := BoardView{Columns: make([]ColumnView, len(cols))}
	for i, name := range cols {
		tasks, err := b.ListByColumn(name)
		if err != nil {
			return BoardView{}, err
		}
		cv := ColumnView{Name: name, Tasks: tasks}
		if i > 0 {
			cv.Prev = cols[i-1]
		}
		if i < len(cols)-1 {
			cv.Next = cols[i+1]
		}
		out.Columns[i] = cv
	}
	return out, nil
}
