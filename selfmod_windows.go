//go:build windows

package main

import "errors"

// ErrHandoffUnsupported is returned by Handoff on platforms without syscall.Exec.
var ErrHandoffUnsupported = errors.New("self-mod handoff is not supported on Windows; deploy on linux/arm64 to use edit_self")

// Handoff on Windows is a no-op that signals the unsupported case. The caller
// should surface this back to the LLM so it can avoid edit_self during Windows
// local development.
func Handoff(newBin string, args []string) error {
	return ErrHandoffUnsupported
}
