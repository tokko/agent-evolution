package main

import (
	"bytes"
	"strings"
	"text/template"
)

// genesisSystemTmpl injects the runtime paths into the hardcoded Genesis prompt.
var genesisSystemTmpl = template.Must(template.New("genesis").Parse(GenesisPrompt))

// RenderGenesisSystem substitutes the {{WORKSPACE}} and {{SELF_SRC}} placeholders
// in the GenesisPrompt constant. We use plain string replace because the
// constant uses {{...}} syntax by convention but we do not want Go template
// semantics (no .Field lookups or escaping).
func RenderGenesisSystem(workspace, selfSrc string) string {
	_ = genesisSystemTmpl // reserved for future template use
	out := strings.ReplaceAll(GenesisPrompt, "{{WORKSPACE}}", workspace)
	out = strings.ReplaceAll(out, "{{SELF_SRC}}", selfSrc)
	return out
}

// routerTmpl asks the LLM to pick which role should handle a task.
var routerTmpl = template.Must(template.New("router").Parse(`You are the router of a self-evolving dev team.
Pick the role best suited to handle the task below. If no specialist fits, pick "genesis".

Active roles:
{{- range .Roles}}
- {{.Name}}: {{.Summary}}
{{- end}}

Task title: {{.Title}}
Task body:
{{.Body}}

Reply with EXACTLY one JSON object: {"role": "<name>"}. No prose.`))

// RouterInput feeds routerTmpl.
type RouterInput struct {
	Roles []RoleSummary
	Title string
	Body  string
}

// RoleSummary is a short role view for the router prompt.
type RoleSummary struct {
	Name    string
	Summary string
}

// RenderRouter builds the router prompt body.
func RenderRouter(in RouterInput) (string, error) {
	var buf bytes.Buffer
	if err := routerTmpl.Execute(&buf, in); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// taskIntroTmpl is the first user message the agent sees for a new task.
var taskIntroTmpl = template.Must(template.New("task").Parse(`Task #{{.ID}} — {{.Title}}

{{.Body}}

You are role "{{.Role}}". Begin by emitting exactly one JSON tool call.`))

// TaskIntroInput feeds taskIntroTmpl.
type TaskIntroInput struct {
	ID         int64
	Title      string
	Body       string
	Role       string
}

// RenderTaskIntro builds the task-introduction user message.
func RenderTaskIntro(in TaskIntroInput) (string, error) {
	var buf bytes.Buffer
	if err := taskIntroTmpl.Execute(&buf, in); err != nil {
		return "", err
	}
	return buf.String(), nil
}
