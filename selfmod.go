package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ApplyAndBuild applies a unified diff to srcDir via `git apply`, then runs
// `go build -o bin/daemon.new .` in srcDir. On build failure it reverses the
// patch so the daemon's source tree is left unchanged. Returns the absolute
// path of the newly built binary on success.
func ApplyAndBuild(ctx context.Context, srcDir, diff string) (string, error) {
	abs, err := filepath.Abs(srcDir)
	if err != nil {
		return "", err
	}

	// Dry-run first: git apply --check rejects broken patches early.
	if out, err := runDiff(ctx, abs, []string{"apply", "--check", "-"}, diff); err != nil {
		return "", fmt.Errorf("git apply --check: %w (%s)", err, out)
	}
	if out, err := runDiff(ctx, abs, []string{"apply", "-"}, diff); err != nil {
		return "", fmt.Errorf("git apply: %w (%s)", err, out)
	}

	binDir := filepath.Join(abs, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	newBin := filepath.Join(binDir, "daemon.new")
	build := exec.CommandContext(ctx, "go", "build", "-o", newBin, ".")
	build.Dir = abs
	if out, err := build.CombinedOutput(); err != nil {
		// Revert the patch; best-effort.
		if out2, rerr := runDiff(ctx, abs, []string{"apply", "-R", "-"}, diff); rerr != nil {
			return "", fmt.Errorf("go build failed: %w (%s); REVERT ALSO FAILED: %v (%s)", err, string(out), rerr, out2)
		}
		return "", fmt.Errorf("go build failed: %w (%s)", err, string(out))
	}
	return newBin, nil
}

// runDiff runs `git <args>` in dir with diff piped on stdin.
func runDiff(ctx context.Context, dir string, args []string, diff string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewBufferString(diff)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
