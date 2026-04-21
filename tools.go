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
	"time"
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
		return toolDone(ctx, cfg, logger, call.Args)
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

// verifyPollInterval is how often toolDone checks for approve/reject files.
var verifyPollInterval = 10 * time.Second

// toolDone is NOT an immediate terminal. The agent uses it to declare "I
// believe I've reached the target system." Before the loop actually stops,
// a human must confirm by creating verify.approved in the source dir (or
// reject by creating verify.rejected, optionally with a reason). This
// prevents the model from halting evolution prematurely.
//
// The agent must provide:
//   - summary:       one-line claim of what was achieved.
//   - motivation:    why every target-system item is believed satisfied.
//   - evidence:      concrete pointers (file paths, behaviors, log lines)
//                    backing the motivation.
//   - verification:  step-by-step instructions a human can execute to
//                    check the claim. Each item should be a concrete
//                    shell command or observation, NOT a vague
//                    "verify the X works."
//
// While waiting, the process is idle: no LLM calls, no CPU. The poll
// checks a sidecar file every verifyPollInterval and honors ctx
// cancellation (Ctrl-C / SIGTERM) so the user can always abort.
//
//   APPROVE: terminal "done" — loop ends.
//   REJECT:  terminal ""     — loop continues, with the rejection reason
//            injected into the next user message so the model sees it.
func toolDone(ctx context.Context, cfg loopConfig, logger *EventLog, args json.RawMessage) ToolResult {
	var in struct {
		Summary      string   `json:"summary"`
		Motivation   string   `json:"motivation"`
		Evidence     []string `json:"evidence"`
		Verification []string `json:"verification"`
	}
	_ = json.Unmarshal(args, &in)
	summary := strings.TrimSpace(in.Summary)
	motivation := strings.TrimSpace(in.Motivation)
	if summary == "" {
		return ToolResult{Err: errors.New("done: summary is required (one short line describing what was achieved)")}
	}
	if motivation == "" {
		return ToolResult{Err: errors.New("done: motivation is required (a paragraph explaining why you believe every target-system item is satisfied). Without it, a human can't verify.")}
	}
	if len(in.Verification) == 0 {
		return ToolResult{Err: errors.New("done: verification is required (a non-empty array of concrete, human-executable steps. Each item should be a shell command to run or a specific observation to make. Example: [\"curl -s localhost:8080/ | grep -q 'todo'\", \"sqlite3 memory.db '.tables' should list tasks, roles, attempts, events\"])")}
	}

	root := cfg.selfSrc
	pendingPath := filepath.Join(root, "verify.pending")
	approvedPath := filepath.Join(root, "verify.approved")
	rejectedPath := filepath.Join(root, "verify.rejected")

	// Clear any stale markers from a previous round.
	_ = os.Remove(approvedPath)
	_ = os.Remove(rejectedPath)

	evidenceBlock := "(none provided)"
	if len(in.Evidence) > 0 {
		var b strings.Builder
		for i, e := range in.Evidence {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, strings.TrimSpace(e))
		}
		evidenceBlock = strings.TrimRight(b.String(), "\n")
	}

	var vb strings.Builder
	for i, v := range in.Verification {
		fmt.Fprintf(&vb, "  %d. %s\n", i+1, strings.TrimSpace(v))
	}
	verificationBlock := strings.TrimRight(vb.String(), "\n")

	pendingBody := fmt.Sprintf(`# Agent believes it has reached the target system.
#
# 1. Run through the HOW TO VERIFY steps below.
# 2. If they all pass:    touch %s
#    If something fails:  echo "what's missing" > %s
#
# Poll interval: %s. Ctrl-C on the daemon also aborts.
#
# Proposed at: %s

SUMMARY:
%s

MOTIVATION:
%s

EVIDENCE:
%s

HOW TO VERIFY (run each step in order):
%s
`,
		approvedPath,
		rejectedPath,
		verifyPollInterval,
		time.Now().UTC().Format(time.RFC3339),
		summary,
		motivation,
		evidenceBlock,
		verificationBlock,
	)
	if err := os.WriteFile(pendingPath, []byte(pendingBody), 0o644); err != nil {
		return ToolResult{Err: fmt.Errorf("done: write %s: %w", pendingPath, err)}
	}
	logger.Write("verification_pending", map[string]any{
		"summary":      summary,
		"motivation":   motivation,
		"evidence":     in.Evidence,
		"verification": in.Verification,
		"pending_path": pendingPath,
	})
	fmt.Fprintf(os.Stderr,
		"\n[VERIFY] Agent claims it has reached the target system. Review:\n  cat %s\nThen APPROVE:\n  touch %s\nOr REJECT with a reason:\n  echo 'why not' > %s\nPolling every %s. Ctrl-C to abort.\n\n",
		pendingPath, approvedPath, rejectedPath, verifyPollInterval,
	)

	ticker := time.NewTicker(verifyPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Write("verification_canceled", map[string]any{"err": ctx.Err().Error()})
			_ = os.Remove(pendingPath)
			return ToolResult{Err: ctx.Err()}
		case <-ticker.C:
			if _, err := os.Stat(approvedPath); err == nil {
				_ = os.Remove(pendingPath)
				_ = os.Remove(approvedPath)
				logger.Write("verification_approved", map[string]any{"summary": summary})
				fmt.Fprintf(os.Stderr, "[VERIFY] approved.\n")
				return ToolResult{
					Result:   "human approved: " + summary,
					Terminal: "done",
				}
			}
			if data, err := os.ReadFile(rejectedPath); err == nil {
				reason := strings.TrimSpace(string(data))
				if reason == "" {
					reason = "(no reason given)"
				}
				_ = os.Remove(pendingPath)
				_ = os.Remove(rejectedPath)
				logger.Write("verification_rejected", map[string]any{"reason": reason})
				fmt.Fprintf(os.Stderr, "[VERIFY] rejected: %s\n", truncate(reason, 200))
				return ToolResult{
					Result: "HUMAN REJECTED your done claim. Reason: " + reason +
						". Do NOT call done again until the rejection is addressed. Keep evolving.",
					// Terminal "" → loop continues.
				}
			}
		}
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
