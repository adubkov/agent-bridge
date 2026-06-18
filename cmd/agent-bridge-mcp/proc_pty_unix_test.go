//go:build unix

package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestRunOnPTYAllocatesTTY is the regression test for the agy headless-hang bug:
// agy timed out through the bridge because it was spawned with plain pipes and its
// agentic --print loop only runs with a controlling terminal. runOnPTY must give the
// child a real TTY. We assert that directly with `test -t`, and confirm the
// plain-pipe path does NOT — so the two execution paths are genuinely different and
// the assertion is meaningful.
func TestRunOnPTYAllocatesTTY(t *testing.T) {
	// `[ -t 1 ]` (stdout) and `[ -t 0 ]` (stdin) are true only on a terminal.
	const script = `if [ -t 1 ] && [ -t 0 ]; then echo TTY; else echo NOTTY; fi`

	// runAgent always builds the command with CommandContext (so runOnPTY may set
	// cmd.Cancel for its group-kill); mirror that here.
	out, err := runOnPTY(exec.CommandContext(context.Background(), "sh", "-c", script))
	if err != nil {
		t.Fatalf("runOnPTY error: %v", err)
	}
	if got := strings.TrimSpace(cleanPTYOutput(out)); got != "TTY" {
		t.Errorf("runOnPTY: child saw %q, want TTY — agy would hang without a controlling terminal", got)
	}

	pipeOut, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("pipe run error: %v", err)
	}
	if got := strings.TrimSpace(string(pipeOut)); got != "NOTTY" {
		t.Errorf("pipe path: child saw %q, want NOTTY (control)", got)
	}
}

// TestRunAgentPTYMergesStderr verifies the pty path's stderr handling: agy is
// pty-run, so the child's stderr and stdout share one TTY stream. A failing agy run
// must therefore still surface its error text (it lands in the merged output) even
// though the bridge's separate stderr buffer stays empty.
func TestRunAgentPTYMergesStderr(t *testing.T) {
	t.Setenv(hopDepthEnv, "0")
	t.Setenv(hopMaxEnv, "2")
	bin := writeFakeBin(t, "#!/bin/sh\nprintf 'BOOM-on-stderr\\n' 1>&2\nexit 1\n")
	tb := withBin(t, antigravityBackend, bin)

	res, err := runAgent(context.Background(), tb, runOpts{task: "x", timeoutSeconds: 300})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected an error result, got %+v", res)
	}
	if txt := resultText(t, res); !strings.Contains(txt, "BOOM-on-stderr") {
		t.Errorf("pty failure must surface the child's stderr via the merged stream; got:\n%s", txt)
	}
}

// TestRunAgentKillsGrandchildPTY is the pty-path twin of TestRunAgentKillsGrandchild
// (which covers the plain-pipe runner): the antigravity backend is pty-run, so a
// backgrounded grandchild inherits the pty SLAVE rather than a pipe. runOnPTY must
// still return promptly — its session-wide SIGKILL takes out the grandchild, and if
// one escaped the session the grace-period master close unblocks the io.Copy drain.
// Without that, the grandchild holds the slave open and the drain wedges past the
// deadline. This lives in the unix-tagged file because the pty path only exists here.
func TestRunAgentKillsGrandchildPTY(t *testing.T) {
	t.Setenv(hopDepthEnv, "0")
	t.Setenv(hopMaxEnv, "2")

	bin := writeFakeBin(t, "#!/bin/sh\nsleep 30 &\nsleep 30\n")
	tb := withBin(t, antigravityBackend, bin)
	tb.timeoutHeadroom = 150 * time.Millisecond // hardDeadline (timeoutSeconds 0) == 150ms

	type out struct {
		res *mcp.CallToolResult
		err error
	}
	ch := make(chan out, 1)
	go func() {
		res, err := runAgent(context.Background(), tb, runOpts{task: "x", timeoutSeconds: 0})
		ch <- out{res, err}
	}()

	// Comfortably under the 30s grandchild sleep but above the 150ms deadline + the
	// grace-period master close, so a real hang is unambiguous.
	select {
	case o := <-ch:
		if o.err != nil {
			t.Fatalf("expected a tool result, got Go error: %v", o.err)
		}
		if o.res == nil || !o.res.IsError || !strings.Contains(resultText(t, o.res), "timed out") {
			t.Fatalf("expected a timeout error result, got %+v", o.res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runAgent hung: a grandchild holding the pty slave blocked runOnPTY past the deadline")
	}
}
