//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

// ptySupported is false where we have no pseudo-terminal support; runAgent then
// falls back to the plain-pipe path even for backends with needsPTY set.
const ptySupported = false

// runOnPTY is never called when ptySupported is false; it exists only so the
// package builds on non-unix platforms.
func runOnPTY(cmd *exec.Cmd) ([]byte, error) {
	return nil, errors.New("pty not supported on this platform")
}
