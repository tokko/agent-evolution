//go:build linux || darwin

package main

import (
	"os"
	"syscall"
)

// Handoff replaces the current process with newBin via syscall.Exec. argv[0]
// is set to newBin; env is inherited. Only returns on failure.
func Handoff(newBin string, args []string) error {
	return syscall.Exec(newBin, append([]string{newBin}, args...), os.Environ())
}
