//go:build !unix && !windows

package main

import (
	"context"
	"errors"
	"os/exec"
)

// ptySupported is false where we have no pseudo-terminal support (neither a unix pty nor
// Windows ConPTY). runAgent runs the pty-less backends (claude/codex) on plain pipes as
// usual, but refuses a needsPTY backend (agy) up front rather than letting it fall
// through and hang on pipes.
const ptySupported = false

// runOnPTY is never called when ptySupported is false; it exists only so the
// package builds on platforms with no pseudo-terminal support.
func runOnPTY(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	return nil, errors.New("pty not supported on this platform")
}
