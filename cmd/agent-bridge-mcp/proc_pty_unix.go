//go:build unix

package main

import (
	"bytes"
	"io"
	"os/exec"
	"syscall"
	"time"

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

	// Drain the master in a goroutine. On a clean exit every writer on the slave goes
	// away (child + grandchildren exit) and io.Copy returns EOF (macOS) / EIO (Linux);
	// we discard that read error — it is the expected end-of-stream.
	var buf bytes.Buffer
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, ptmx)
		close(copyDone)
	}()

	werr := cmd.Wait()

	// cmd.Wait returns once the child is reaped (on timeout the Cancel above SIGKILLs
	// the whole group first). Normally the slave then closes and the copy drains the
	// last buffered bytes and finishes on its own — so wait a short grace period for a
	// clean EOF (no truncation). But a grandchild that escaped the group (its own
	// session) could hold the slave open and wedge io.Copy forever; cmd.WaitDelay does
	// NOT cover this read, since the pty master is not one of exec.Cmd's managed pipes.
	// So after the grace period, force the copy to unblock by closing the master,
	// guaranteeing runOnPTY returns even when the deadline alone would not free it.
	select {
	case <-copyDone:
	case <-time.After(childWaitDelay):
	}
	_ = ptmx.Close()
	<-copyDone
	return buf.Bytes(), werr
}
