package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	// Flags
	repoURL := flag.String("repo", "", "git URL of the target repo (required on first run)")
	selfSrc := flag.String("self-src", "", "path to daemon's own source tree (default: cwd)")
	resumeTask := flag.Int64("resume-task", 0, "internal: task id to resume after edit_self handoff")
	flag.Parse()

	_ = loadDotenv(".env") // non-fatal: env vars may already be set another way
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := loadConfig()
	if *selfSrc == "" {
		cwd, _ := os.Getwd()
		*selfSrc = cwd
	}

	// Open persistent state.
	mem, err := Open(cfg.DBPath)
	if err != nil {
		die("open db: %v", err)
	}
	defer mem.Close()

	roles := NewRoleStore(mem)
	if _, err := roles.Genesis(); err != nil {
		die("seed genesis: %v", err)
	}
	board := NewBoard(mem)

	// Target repo.
	var repo *Repo
	if *repoURL != "" || existingWorkspace(cfg.Workspace) != "" {
		url := *repoURL
		root := cfg.Workspace
		if url == "" {
			// Nothing new to clone; reuse existing workspace.
			root = existingWorkspace(cfg.Workspace)
		} else {
			root = filepath.Join(cfg.Workspace, repoNameFromURL(url))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		repo, err = Clone(ctx, url, root)
		cancel()
		if err != nil {
			slog.Warn("clone failed; running without repo", "err", err)
			repo = nil
		}
	}
	if repo == nil {
		slog.Warn("no target repo configured; UI works but task execution will fail until --repo is set")
	}

	sbx := NewSandbox(cfg.SandboxImage, 30*time.Second, "sandbox.Dockerfile")
	// Best-effort sandbox image build; don't fail startup if docker is missing.
	if err := sbx.EnsureImage(context.Background()); err != nil {
		slog.Warn("sandbox image not ready", "err", err)
	}

	llm := NewLLM(cfg.APIKey, cfg.GroupID, cfg.Model)

	workspacePath := ""
	if repo != nil {
		workspacePath = repo.Root
	}
	deps := AgentDeps{
		LLM:       llm,
		Sandbox:   sbx,
		Repo:      repo,
		Mem:       mem,
		Roles:     roles,
		Board:     board,
		SelfSrc:   *selfSrc,
		Workspace: workspacePath,
		MaxSteps:  cfg.MaxSteps,
		SelfMod:   cfg.SelfMod,
	}

	// Signal-rooted context.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// HTTP server.
	srv := &http.Server{Addr: ":" + cfg.Port, Handler: NewServer(board, mem).Handler()}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "err", err)
			cancel()
		}
	}()

	// Scheduler.
	sched := NewScheduler(deps, 2*time.Second)
	var schedErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		schedErr = sched.Loop(ctx, *resumeTask)
		// Scheduler exiting on its own (e.g. handoff) should trigger shutdown.
		cancel()
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = srv.Shutdown(shutdownCtx)
	shutdownCancel()
	wg.Wait()

	if sched.HandoffBin != "" {
		slog.Info("handing off to new binary", "bin", sched.HandoffBin, "task", sched.HandoffTaskID)
		_ = mem.Close()
		if err := Handoff(sched.HandoffBin, append([]string{"--resume-task", strconv.FormatInt(sched.HandoffTaskID, 10)}, passthroughArgs(*repoURL, *selfSrc)...)); err != nil {
			die("handoff failed: %v", err)
		}
	}
	if schedErr != nil && schedErr != context.Canceled {
		die("scheduler: %v", schedErr)
	}
}

// Config is the runtime env snapshot.
type Config struct {
	APIKey, GroupID, Model, Port, DBPath, Workspace, SandboxImage string
	MaxSteps                                                      int
	SelfMod                                                       bool
}

func loadConfig() Config {
	c := Config{
		APIKey:       os.Getenv("MINIMAX_API_KEY"),
		GroupID:      os.Getenv("MINIMAX_GROUP_ID"),
		Model:        getenvDefault("MODEL", "MiniMax-M2.7"),
		Port:         getenvDefault("PORT", "8080"),
		DBPath:       getenvDefault("DB_PATH", "./memory.db"),
		Workspace:    getenvDefault("WORKSPACE", "./workspace"),
		SandboxImage: getenvDefault("SANDBOX_IMAGE", "agent-sandbox:latest"),
		SelfMod:      strings.EqualFold(getenvDefault("SELF_MOD_ENABLED", "true"), "true"),
	}
	c.MaxSteps, _ = strconv.Atoi(getenvDefault("MAX_STEPS", "25"))
	if c.MaxSteps <= 0 {
		c.MaxSteps = 25
	}
	return c
}

// loadDotenv parses a trivial KEY=VALUE file. Lines starting with # and blank
// lines are skipped. Existing env vars are NOT overridden.
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

func repoNameFromURL(u string) string {
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	if u == "" {
		u = "repo"
	}
	return u
}

// existingWorkspace returns the first subdirectory of root that looks like a
// git working copy, so a restart without --repo still finds what was cloned.
func existingWorkspace(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), ".git")); err == nil {
			return filepath.Join(root, e.Name())
		}
	}
	return ""
}

func passthroughArgs(repo, selfSrc string) []string {
	var out []string
	if repo != "" {
		out = append(out, "--repo", repo)
	}
	if selfSrc != "" {
		out = append(out, "--self-src", selfSrc)
	}
	return out
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fatal: "+format+"\n", args...)
	os.Exit(1)
}
