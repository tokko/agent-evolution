package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ToolResult is what dispatch returns for one tool call.
//
// Terminal ends the loop: "" (continue), "done", "fail", "handoff". On
// "handoff", NewBin holds the absolute path of the freshly built binary.
type ToolResult struct {
	Result   string
	Terminal string
	NewBin   string
	Err      error
}

// dispatch runs one tool call against the running process's own source tree.
// No file I/O ever escapes selfSrc: paths are resolved via safeJoin.
func dispatch(ctx context.Context, call ToolCall, cfg loopConfig, logger *EventLog) ToolResult {
	switch call.Tool {
	case "think":
		return toolThink(call.Args)
	case "list_self":
		return toolListSelf(cfg.selfSrc)
	case "read_self":
		return toolReadSelf(cfg.selfSrc, call.Args)
	case "edit_self":
		if !cfg.selfMod {
			return ToolResult{Err: errors.New("edit_self disabled (set SELF_MOD_ENABLED=true)")}
		}
		return toolEditSelf(ctx, cfg.selfSrc, call.Args, logger)
	case "done":
		return toolTerminal("done", call.Args, "summary")
	case "fail":
		return toolTerminal("fail", call.Args, "reason")
	default:
		return ToolResult{Err: fmt.Errorf("unknown tool %q (available: think, list_self, read_self, edit_self, done, fail)", call.Tool)}
	}
}

// ---- think ---------------------------------------------------------------

func toolThink(args json.RawMessage) ToolResult {
	var in struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Err: fmt.Errorf("think: %w", err)}
	}
	if in.Note == "" {
		return ToolResult{Result: "noted (empty)"}
	}
	return ToolResult{Result: "noted: " + truncate(in.Note, 1000)}
}

// ---- list_self -----------------------------------------------------------

// listable file extensions / names. Keeps the listing useful for an LLM
// without dumping the whole tree (no bin/, no node_modules, no .git/).
var listableExts = map[string]bool{
	".go": true, ".mod": true, ".sum": true, ".md": true, ".sh": true,
	".html": true, ".yaml": true, ".yml": true, ".toml": true, ".json": true,
	".txt": true, ".env": true, ".example": true,
}
var listableNames = map[string]bool{
	"Dockerfile": true, ".env": true, ".env.example": true, ".gitignore": true,
	"sandbox.Dockerfile": true,
}

func toolListSelf(root string) ToolResult {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			name := d.Name()
			if path == root {
				return nil
			}
			if name == ".git" || name == "bin" || name == "workspace" || name == "node_modules" || name == "tmp" || strings.HasPrefix(name, ".") && name != "." {
				return fs.SkipDir
			}
			return nil
		}
		base := d.Name()
		ext := strings.ToLower(filepath.Ext(base))
		if !listableExts[ext] && !listableNames[base] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, fmt.Sprintf("%8d  %s", info.Size(), filepath.ToSlash(rel)))
		return nil
	})
	if err != nil {
		return ToolResult{Err: fmt.Errorf("list_self: %w", err)}
	}
	sort.Strings(out)
	return ToolResult{Result: strings.Join(out, "\n")}
}

// ---- read_self -----------------------------------------------------------

const maxReadFiles = 20
const maxReadBytesPerFile = 64 * 1024

func toolReadSelf(root string, args json.RawMessage) ToolResult {
	var in struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Err: fmt.Errorf("read_self: %w", err)}
	}
	if len(in.Paths) == 0 {
		return ToolResult{Err: errors.New("read_self: paths is required (array of relative file paths)")}
	}
	if len(in.Paths) > maxReadFiles {
		return ToolResult{Err: fmt.Errorf("read_self: too many paths (%d); max %d", len(in.Paths), maxReadFiles)}
	}
	var b strings.Builder
	for _, p := range in.Paths {
		abs, err := safeJoin(root, p)
		if err != nil {
			fmt.Fprintf(&b, "---- %s ----\nerror: %v\n\n", p, err)
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			fmt.Fprintf(&b, "---- %s ----\nerror: %v\n\n", p, err)
			continue
		}
		fmt.Fprintf(&b, "---- %s (%d bytes) ----\n", filepath.ToSlash(p), len(data))
		if len(data) > maxReadBytesPerFile {
			b.Write(data[:maxReadBytesPerFile])
			fmt.Fprintf(&b, "\n...[+%d bytes truncated]\n\n", len(data)-maxReadBytesPerFile)
		} else {
			b.Write(data)
			b.WriteByte('\n')
			b.WriteByte('\n')
		}
	}
	return ToolResult{Result: b.String()}
}

// ---- edit_self -----------------------------------------------------------

func toolEditSelf(ctx context.Context, root string, args json.RawMessage, logger *EventLog) ToolResult {
	var in struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Err: fmt.Errorf("edit_self: %w", err)}
	}
	if strings.TrimSpace(in.Diff) == "" {
		return ToolResult{Err: errors.New("edit_self: diff is required (a unified diff rooted at the source tree)")}
	}
	logger.Write("edit_self_attempt", map[string]any{"diff_bytes": len(in.Diff)})
	newBin, err := ApplyAndBuild(ctx, root, in.Diff)
	if err != nil {
		logger.Write("edit_self_failed", map[string]any{"err": err.Error()})
		return ToolResult{Err: err}
	}
	logger.Write("edit_self_built", map[string]any{"new_bin": newBin})
	return ToolResult{
		Result:   "built new binary at " + newBin + "; process will hand off to it now",
		Terminal: "handoff",
		NewBin:   newBin,
	}
}

// ---- done / fail ---------------------------------------------------------

func toolTerminal(kind string, args json.RawMessage, field string) ToolResult {
	var in map[string]any
	_ = json.Unmarshal(args, &in)
	msg, _ := in[field].(string)
	return ToolResult{
		Result:   kind + ": " + msg,
		Terminal: kind,
	}
}

// ---- helpers -------------------------------------------------------------

// safeJoin resolves rel against root and returns the absolute path, rejecting
// any rel that escapes root (via .., absolute paths, or symlink tricks).
func safeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path not allowed: %s", rel)
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("path escapes root: %s", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, clean)
	// Final containment check.
	relCheck, err := filepath.Rel(absRoot, joined)
	if err != nil || strings.HasPrefix(relCheck, "..") {
		return "", fmt.Errorf("path escapes root: %s", rel)
	}
	return joined, nil
}
