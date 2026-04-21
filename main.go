package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Minimal self-improving agent.
//
// The only thing this binary does is: read its own source, ask MiniMax for a
// single unified-diff improvement toward the target system described in the
// system prompt, apply the diff, build, and hand off to the new binary. Then
// do it again.
//
// Everything else — a kanban board, a sandbox, a target-repo pipeline,
// specialist roles — is the GOAL, not the starting point. The agent has to
// build those itself via repeated edit_self steps.

func main() {
	var (
		selfSrc   string
		resume    bool
		maxSteps  int
		logPath   string
		oneShot   bool
		sleepSecs int
	)
	flag.StringVar(&selfSrc, "self-src", "", "path to the agent's own source tree (default: dir of the running binary, or cwd if unknown)")
	flag.BoolVar(&resume, "resume", false, "internal: set across self-mod handoffs so the new binary knows it woke up from an edit_self")
	flag.IntVar(&maxSteps, "max-steps", 0, "cap on agent-loop iterations in one process lifetime (0 = use MAX_STEPS env or default)")
	flag.StringVar(&logPath, "log", "", "path to the append-only JSONL event log (default: ./events.jsonl)")
	flag.BoolVar(&oneShot, "once", false, "run a single step then exit (useful for debugging)")
	flag.IntVar(&sleepSecs, "sleep", 0, "seconds to wait between steps (default: 0; useful when running on a loop)")
	flag.Parse()

	_ = loadDotenv(".env") // non-fatal
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if selfSrc == "" {
		selfSrc = guessSelfSrc()
	}
	abs, err := filepath.Abs(selfSrc)
	if err != nil {
		die("resolve --self-src: %v", err)
	}
	selfSrc = abs

	if maxSteps <= 0 {
		maxSteps, _ = strconv.Atoi(getenvDefault("MAX_STEPS", "25"))
	}
	if maxSteps <= 0 {
		maxSteps = 25
	}

	if logPath == "" {
		logPath = getenvDefault("EVENT_LOG", "./events.jsonl")
	}

	apiKey := os.Getenv("MINIMAX_API_KEY")
	if apiKey == "" || apiKey == "REPLACE_ME" {
		die("MINIMAX_API_KEY is not set (edit .env or export it)")
	}
	model := getenvDefault("MODEL", "MiniMax-M2.7")
	groupID := os.Getenv("MINIMAX_GROUP_ID")
	selfModEnabled := strings.EqualFold(getenvDefault("SELF_MOD_ENABLED", "true"), "true")

	llm := NewLLM(apiKey, groupID, model)

	logger, err := NewEventLog(logPath)
	if err != nil {
		die("open event log %q: %v", logPath, err)
	}
	defer logger.Close()

	bootKind := "boot"
	if resume {
		bootKind = "resumed"
	}
	logger.Write(bootKind, map[string]any{
		"self_src":   selfSrc,
		"model":      model,
		"max_steps":  maxSteps,
		"self_mod":   selfModEnabled,
		"goos":       runtime.GOOS,
		"at":         time.Now().UTC().Format(time.RFC3339),
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := loopConfig{
		selfSrc:  selfSrc,
		maxSteps: maxSteps,
		sleep:    time.Duration(sleepSecs) * time.Second,
		oneShot:  oneShot,
		selfMod:  selfModEnabled,
	}

	outcome, newBin, err := runLoop(ctx, llm, logger, cfg)
	if err != nil {
		slog.Error("loop ended with error", "err", err)
		logger.Write("fatal", map[string]any{"err": err.Error()})
	}

	if outcome == "handoff" && newBin != "" {
		logger.Write("handoff_exec", map[string]any{"new_bin": newBin})
		_ = logger.Close()
		if err := Handoff(newBin, []string{"--resume", "--self-src", selfSrc, "--log", logPath}); err != nil {
			die("handoff failed: %v", err)
		}
		return
	}

	logger.Write("exit", map[string]any{"outcome": outcome})
}

// loadDotenv parses a trivial KEY=VALUE file. Blank lines and #-comments are
// skipped. Already-set env vars are NOT overridden, so shell env wins.
func loadDotenv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, `"'`)
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func getenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// guessSelfSrc picks the directory of the running binary, unless that looks
// like a temp-install location (e.g. ~/.local/bin); otherwise cwd.
func guessSelfSrc() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		// If the binary sits in bin/ of a source tree, prefer the parent.
		if filepath.Base(dir) == "bin" {
			return filepath.Dir(dir)
		}
		// Heuristic: if there's a go.mod next to the binary, use that dir.
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fatal: "+format+"\n", args...)
	os.Exit(1)
}
