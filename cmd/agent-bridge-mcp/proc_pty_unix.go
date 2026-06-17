//go:build unix

package main

import (
	"bytes"
	"io"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// ptySupported reports whether runOnPTY can allocate a pseudo-terminal here.
const ptySupported = true

// runOnPTY starts cmd attached to a pseudo-terminal and returns its combined
// stdout+stderr (the slave is a single stream, so the two merge). Some CLIs — agy —
// only run their agentic loop to completion with a controlling TTY; spawned with
// plain pipes they hang. pty.Start gives the child a new session (Setsid) and makes
// the pty its controlling terminal.
//
// Because Setsid makes the child a session AND process-group leader (pgid == pid),
// we install a Cancel that SIGKILLs the whole group (negative pid) on
// context timeout — matching the pipe path's setupProcessGroup so grandchildren die
// too. We must NOT also set Setpgid: setpgid() on a session leader fails with EPERM,
// which would break the child's launch. cmd.WaitDelay (set by the caller) bounds how
// long Wait can block afterward.
func runOnPTY(cmd *exec.Cmd) ([]byte, error) {
	// pty.Start preserves any existing SysProcAttr and adds Setsid/Setctty/Ctty. Reset
	// it so a Setpgid left over from a caller can't collide with Setsid (EPERM).
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid → the whole process group led by the child.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill() // fall back to the direct child
		}
		return nil
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	defer func() { _ = ptmx.Close() }()

	// Drain the master until every writer on the slave is gone (child + any
	// grandchildren exited, or the group was killed on timeout). On Linux that final
	// read surfaces as EIO and on macOS as EOF; either way io.Copy stops. We discard
	// that read error — it is the expected end-of-stream, not a failure — and report
	// only the child's own exit status from Wait.
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, ptmx)
	return buf.Bytes(), cmd.Wait()
}
