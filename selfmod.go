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

// PromoteBinary atomically moves a freshly built binary (src, typically
// bin/daemon.new) into place as the canonical binary (dest, typically
// bin/daemon), backing up the previous dest to dest+".prev".
//
// On Linux/macOS this works even when dest is the currently-running
// executable: the rename is atomic at the directory-entry level and the
// kernel keeps the already-loaded old image mapped as long as this process
// lives. On Windows the running .exe is locked by the OS, so the rename
// fails; in that case we fall back to returning src unchanged so the caller
// can still exec into it — Windows self-mod was already advertised as
// compile-check-only.
//
// Returns the absolute path that subsequent callers should run.
func PromoteBinary(src, dest string) (string, error) {
	if src == "" {
		return "", fmt.Errorf("promote: empty src")
	}
	if dest == "" {
		return src, nil
	}
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("promote: stat src %q: %w", src, err)
	}
	absSrc, _ := filepath.Abs(src)
	absDest, _ := filepath.Abs(dest)
	if absSrc == absDest {
		return absSrc, nil
	}

	// Back up the previous dest (best-effort).
	if _, err := os.Stat(absDest); err == nil {
		backup := absDest + ".prev"
		_ = os.Remove(backup)
		if err := os.Rename(absDest, backup); err != nil {
			// On Windows this is where we land if the current binary is
			// locked. Fall back to running out of src.
			return absSrc, fmt.Errorf("promote: backup %q: %w (falling back to src)", absDest, err)
		}
	}
	if err := os.Rename(absSrc, absDest); err != nil {
		return absSrc, fmt.Errorf("promote: rename %q -> %q: %w (falling back to src)", absSrc, absDest, err)
	}
	return absDest, nil
}
