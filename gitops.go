package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo is a working copy of the target repository.
type Repo struct {
	Root   string // absolute path
	URL    string
	Branch string
}

// Clone clones url into root if missing; otherwise opens the existing working copy.
func Clone(ctx context.Context, url, root string) (*Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
		return &Repo{Root: abs, URL: url, Branch: "main"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", url, abs)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone %s: %w (%s)", url, err, string(out))
	}
	return &Repo{Root: abs, URL: url, Branch: "main"}, nil
}

// Pull fast-forwards the current branch from origin.
func (r *Repo) Pull(ctx context.Context) error {
	return r.run(ctx, "pull", "--ff-only")
}

// Read returns the contents of each requested path relative to the repo root.
// Paths that escape the root (via "..") are rejected.
func (r *Repo) Read(paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		full, err := r.safeJoin(p)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		out[p] = string(b)
	}
	return out, nil
}

// Write creates or overwrites files relative to the repo root, creating parents.
func (r *Repo) Write(files map[string]string) error {
	for p, content := range files {
		full, err := r.safeJoin(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Commit stages all working-tree changes and creates a commit with msg. If there
// is nothing to commit this returns the current HEAD sha.
func (r *Repo) Commit(ctx context.Context, msg string) (string, error) {
	if err := r.run(ctx, "add", "-A"); err != nil {
		return "", err
	}
	// Only commit if there's something staged.
	if err := r.run(ctx, "diff", "--cached", "--quiet"); err == nil {
		return r.head(ctx)
	}
	// Author identity. Use env-provided GIT_* if set, else fall back.
	env := os.Environ()
	if os.Getenv("GIT_AUTHOR_NAME") == "" {
		env = append(env, "GIT_AUTHOR_NAME=agent-evolution", "GIT_COMMITTER_NAME=agent-evolution")
	}
	if os.Getenv("GIT_AUTHOR_EMAIL") == "" {
		env = append(env, "GIT_AUTHOR_EMAIL=agent@local", "GIT_COMMITTER_EMAIL=agent@local")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", r.Root, "commit", "-m", msg)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit: %w (%s)", err, string(out))
	}
	return r.head(ctx)
}

// Push pushes the current branch to origin.
func (r *Repo) Push(ctx context.Context) error {
	return r.run(ctx, "push", "origin", "HEAD:"+r.Branch)
}

// Diff returns the unified diff of the working tree against HEAD.
func (r *Repo) Diff(ctx context.Context) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", r.Root, "diff", "HEAD")
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff: %w (%s)", err, buf.String())
	}
	return buf.String(), nil
}

func (r *Repo) head(ctx context.Context) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", r.Root, "rev-parse", "HEAD")
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func (r *Repo) run(ctx context.Context, args ...string) error {
	full := append([]string{"-C", r.Root}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, string(out))
	}
	return nil
}

func (r *Repo) safeJoin(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	full := filepath.Join(r.Root, clean)
	// Defend against symlink escapes by string-prefix check.
	if !strings.HasPrefix(full+string(filepath.Separator), r.Root+string(filepath.Separator)) && full != r.Root {
		return "", fmt.Errorf("path escapes root: %q", rel)
	}
	return full, nil
}
