package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// SandboxResult captures the outcome of one sandbox run.
type SandboxResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// Sandbox runs LLM-generated code inside a network-isolated Docker container.
type Sandbox struct {
	Image      string
	Timeout    time.Duration
	Dockerfile string // path to sandbox.Dockerfile, for lazy build
}

// NewSandbox constructs a Sandbox.
func NewSandbox(image string, timeout time.Duration, dockerfile string) *Sandbox {
	return &Sandbox{Image: image, Timeout: timeout, Dockerfile: dockerfile}
}

// EnsureImage builds the sandbox image if it isn't already present.
func (s *Sandbox) EnsureImage(ctx context.Context) error {
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", s.Image).Run(); err == nil {
		return nil
	}
	dir := "."
	if s.Dockerfile != "" {
		dir = filepath.Dir(s.Dockerfile)
	}
	args := []string{"build", "-t", s.Image}
	if s.Dockerfile != "" {
		args = append(args, "-f", s.Dockerfile)
	}
	args = append(args, dir)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build sandbox: %w", err)
	}
	return nil
}

// Execute writes files into a fresh tempdir and runs `bash /work/run.sh` inside
// the sandbox container with no network and a hard timeout.
func (s *Sandbox) Execute(ctx context.Context, files map[string]string) (SandboxResult, error) {
	if _, ok := files["run.sh"]; !ok {
		return SandboxResult{}, errors.New("files must include run.sh entrypoint")
	}
	tmp, err := os.MkdirTemp("", "sandbox-*")
	if err != nil {
		return SandboxResult{}, err
	}
	defer os.RemoveAll(tmp)

	for name, body := range files {
		path := filepath.Join(tmp, filepath.Clean(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return SandboxResult{}, err
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			return SandboxResult{}, err
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "docker", "run", "--rm",
		"--network", "none",
		"--memory", "256m",
		"--cpus", "1.0",
		"-v", tmp+":/work",
		"-w", "/work",
		s.Image,
		"bash", "/work/run.sh",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	res := SandboxResult{
		Stdout:   truncate(stdout.String(), 8000),
		Stderr:   truncate(stderr.String(), 8000),
		ExitCode: cmd.ProcessState.ExitCode(),
		TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
	}
	if res.TimedOut {
		err = nil // timing out is an in-band result, not a caller error
	} else if err != nil {
		// exit code already captured; treat non-zero as in-band too
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = nil
		}
	}
	return res, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...[truncated]"
}
