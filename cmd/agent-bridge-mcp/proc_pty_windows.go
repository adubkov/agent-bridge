//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/UserExistsError/conpty"
)

// ptySupported reports whether runOnPTY can allocate a pseudo-terminal here. Windows
// 10 1809+ exposes the ConPTY API (CreatePseudoConsole); older builds return false so
// the dispatch refuses a needsPTY backend up front instead of hanging.
var ptySupported = conpty.IsConPtyAvailable()

// ptyCols/ptyRows size the pseudo-console. The width is deliberately very wide so a
// CLI's long output lines are NOT hard-wrapped by the console (wrapping would corrupt
// JSON and split model-list entries); the height only needs to be tall enough that the
// final screen isn't a tiny scroll region.
const (
	ptyCols = 8192
	ptyRows = 100

	// maxWindowsCmdline caps the command line we hand ConPTY's CreateProcess, which fails
	// near 32767 UTF-16 code units (incl. the terminating null). A little margin under that.
	maxWindowsCmdline = 32760

	// ptyExitDrain is how long we let the console keep draining after the child has
	// exited. ConPTY does not EOF the reader until Close, so on a clean exit we cannot
	// detect "fully drained" — instead we give the final bytes a brief settle (the child
	// is already gone; its output was emitted as it ran), then Close to flush + EOF. Much
	// shorter than childWaitDelay, which would otherwise tax every successful run.
	ptyExitDrain = 250 * time.Millisecond
)

// runOnPTY starts cmd attached to a Windows pseudo-console (ConPTY) and returns its
// combined stdout+stderr — the console is one stream, so the two merge, matching the
// unix pty path. Some CLIs (agy) only run their agentic --print loop to completion with
// a real terminal; spawned with plain pipes they hang, and `agy models` prints nothing.
//
// ConPTY is driven by a command LINE, not an exec.Cmd, so we take cmd only for its
// argv/dir/env (the context carries the deadline instead of cmd.Cancel). conpty.Close
// closes the pseudo-console, which terminates the attached process (and its console-
// sharing grandchildren) — that is our kill-on-timeout and the force-unblock for the
// drain, mirroring the unix path's group-kill + master close.
func runOnPTY(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	// exec.Command stashes a LookPath failure (not found, or an ErrDot relative path it
	// refuses to run) in cmd.Err; the pipe path surfaces it instead of spawning, so honor it
	// here too rather than letting ConPTY re-resolve and run something os/exec rejected.
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	// ConPTY launches the resolved image directly via CreateProcess, which — unlike os/exec's
	// pipe path — does NOT run a .bat/.cmd shim through cmd.exe. The only needsPTY backend
	// (agy) ships as a real agy.exe so this never fires for it; guard anyway so a future pty
	// backend that resolves to a batch shim fails with a clear message, not a cryptic
	// CreateProcess error.
	if ext := strings.ToLower(filepath.Ext(cmd.Path)); ext == ".bat" || ext == ".cmd" {
		return nil, fmt.Errorf("pty path cannot launch a %s shim (%s); it needs a real executable", ext, cmd.Path)
	}
	// CreateProcess caps the command line near 32 KB UTF-16 code units. Measure the actual
	// EscapeArg-expanded line (quotes/backslashes doubled, args quoted) plus the flags — what
	// a raw task-byte check upstream can't see — and fail clearly instead of letting
	// conpty.Start die with a cryptic "filename or extension is too long". Only agy is an
	// argv-prompt backend; claude/codex feed the prompt on stdin and never reach this path.
	cmdline := windowsCommandLine(cmd.Path, cmd.Args[1:])
	if n := len(utf16.Encode([]rune(cmdline))); n > maxWindowsCmdline {
		return nil, fmt.Errorf("command line is %d UTF-16 chars, over the Windows ~%d-char limit — shorten the task", n, maxWindowsCmdline)
	}

	opts := []conpty.ConPtyOption{conpty.ConPtyDimensions(ptyCols, ptyRows)}
	if strings.TrimSpace(cmd.Dir) != "" {
		opts = append(opts, conpty.ConPtyWorkDir(cmd.Dir))
	}
	if cmd.Env != nil {
		opts = append(opts, conpty.ConPtyEnv(cmd.Env))
	}

	cpty, err := conpty.Start(cmdline, opts...)
	if err != nil {
		return nil, err
	}

	// Drain the console output in a goroutine; it returns EOF once every process
	// attached to the pseudo-console has exited (or once Close releases the handle).
	var buf bytes.Buffer
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, cpty)
		close(copyDone)
	}()

	code, werr := cpty.Wait(ctx)

	if ctx.Err() != nil {
		// Deadline/cancel: conpty.Wait does NOT kill, so Close now to terminate the
		// child (and console-sharing grandchildren) and unblock the drain. runAgent maps
		// the context error to its timeout/cancel result.
		_ = cpty.Close()
		<-copyDone
		return buf.Bytes(), ctx.Err()
	}

	// Clean exit: let the final bytes settle (see ptyExitDrain), then Close to flush +
	// EOF the reader and reap any console-sharing grandchild that outlived the child.
	select {
	case <-copyDone:
	case <-time.After(ptyExitDrain):
	}
	_ = cpty.Close()
	<-copyDone

	if werr != nil {
		return buf.Bytes(), werr
	}
	if code != 0 {
		return buf.Bytes(), fmt.Errorf("exit status %d", code)
	}
	return buf.Bytes(), nil
}

// windowsCommandLine joins the resolved executable path and its arguments into a single
// command line with Windows quoting rules (the same escaping os/exec applies), since
// ConPTY takes a command line, not argv. The first token is cmd.Path — the path os/exec
// already resolved (via LookPath) and would run on the pipe path — NOT cmd.Args[0], which
// is the (possibly bare) name passed to exec.Command; using Path keeps ConPTY from
// re-resolving and launching a different executable.
func windowsCommandLine(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, syscall.EscapeArg(path))
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}
