//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

// ptySupported is false where we have no pseudo-terminal support. runAgent runs the
// pty-less backends (claude/codex) on plain pipes as usual, but refuses a needsPTY
// backend (agy) up front rather than letting it fall through and hang on pipes.
const ptySupported = false

// runOnPTY is never called when ptySupported is false; it exists only so the
// package builds on non-unix platforms.
func runOnPTY(cmd *exec.Cmd) ([]byte, error) {
	return nil, errors.New("pty not supported on this platform")
}
