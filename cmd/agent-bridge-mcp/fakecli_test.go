package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// fakecli_test.go provides a CROSS-PLATFORM fake CLI for the spawn tests. The old
// helper wrote `#!/bin/sh` scripts, which Windows cannot exec (CreateProcess won't run
// an extensionless, shebang-only file — every spawn test then failed with ENOENT). The
// portable trick instead: re-exec the TEST BINARY itself (a real executable on every
// OS) and have it behave as the fake when AGENT_BRIDGE_FAKECLI is set in its env. The
// behavior is a JSON-encoded fakeOpts in that var, which the bridge inherits because it
// spawns children with os.Environ() (see childHopEnv/childDelegationEnv, which only
// touch the hop/no-delegate keys). t.Setenv restores the var after each test.

const fakeCLIEnv = "AGENT_BRIDGE_FAKECLI"

// fakeOpts drives the fake CLI's behavior. The zero value is a silent, exit-0 process.
type fakeOpts struct {
	Out        string `json:"out,omitempty"`        // write this line to stdout
	Err        string `json:"err,omitempty"`        // write this line to stderr
	Exit       int    `json:"exit,omitempty"`       // process exit code
	EchoND     bool   `json:"echoND,omitempty"`     // print ND=[<AGENT_NO_DELEGATE>] to stdout
	SleepMS    int    `json:"sleepMS,omitempty"`    // sleep this long, then exit (a killable long run)
	FillErrKB  int    `json:"fillErrKB,omitempty"`  // emit ~this many KB of filler to stderr BEFORE Err
	Touch      string `json:"touch,omitempty"`      // create this file in the cwd (proves working_dir)
	Grandchild bool   `json:"grandchild,omitempty"` // spawn a detached grandchild holding stdout, then sleep
	PidFile    string `json:"pidFile,omitempty"`    // if set, write the grandchild's PID here (so a test can kill it)
	EchoStdin  bool   `json:"echoStdin,omitempty"`  // read all of stdin and print STDIN[<it>] (proves promptStdin wiring)
}

// TestMain turns the test binary into the fake CLI when AGENT_BRIDGE_FAKECLI is set
// (a child spawned by the bridge), otherwise runs the tests normally. In fake mode we
// exit BEFORE m.Run(), so the bridge's argv (--print, the task, …) is never parsed as
// test flags and no test output pollutes the fake's stdout.
func TestMain(m *testing.M) {
	if os.Getenv(fakeCLIEnv) != "" {
		os.Exit(runFakeCLI())
	}
	os.Exit(m.Run())
}

func runFakeCLI() int {
	var o fakeOpts
	if err := json.Unmarshal([]byte(os.Getenv(fakeCLIEnv)), &o); err != nil {
		fmt.Fprintf(os.Stderr, "fakecli: bad directive: %v\n", err)
		return 2
	}

	if o.Grandchild {
		// Background a grandchild that INHERITS our stdout and outlives us (it re-execs our
		// own image in a plain sleep, so it never recurses into its own grandchild). It must
		// outlive the test's hang-detection window (>15s), so it sleeps 30s. We record its
		// PID so the test can kill it on cleanup — otherwise the 30s orphan would, on
		// Windows, keep the test binary locked and block `go test` from deleting it.
		gc := exec.Command(os.Args[0])
		gc.Env = fakeChildEnv(fakeOpts{SleepMS: 30000})
		gc.Stdout = os.Stdout
		gc.Stderr = os.Stderr
		if err := gc.Start(); err != nil {
			// Fail loudly instead of sleeping anyway: a swallowed spawn error would let the
			// grandchild-hang test pass with no grandchild, hiding a regression.
			fmt.Fprintf(os.Stderr, "fakecli: grandchild start: %v\n", err)
			return 1
		}
		if o.PidFile != "" {
			_ = os.WriteFile(o.PidFile, []byte(strconv.Itoa(gc.Process.Pid)), 0o644)
		}
		// Block until the parent (runAgent) kills us at its deadline; the grandchild keeps
		// the stream open until WaitDelay (pipe path) / the pty grace-close fires.
		time.Sleep(30 * time.Second)
		return 0
	}
	if o.SleepMS > 0 {
		time.Sleep(time.Duration(o.SleepMS) * time.Millisecond)
	}
	if o.Touch != "" {
		f, err := os.Create(o.Touch) // relative => lands in the child's cwd (the working_dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakecli: touch %q: %v\n", o.Touch, err)
			return 1
		}
		_ = f.Close()
	}
	if o.EchoStdin {
		in, _ := io.ReadAll(os.Stdin)
		fmt.Fprintf(os.Stdout, "STDIN[%s]\n", string(in))
	}
	if o.EchoND {
		fmt.Fprintf(os.Stdout, "ND=[%s]\n", os.Getenv(noDelegateEnv))
	}
	if o.FillErrKB > 0 {
		line := strings.Repeat("x", 63) + "\n" // 64 bytes/line
		for i := 0; i < o.FillErrKB*1024/64; i++ {
			fmt.Fprint(os.Stderr, line)
		}
	}
	if o.Out != "" {
		fmt.Fprintln(os.Stdout, o.Out)
	}
	if o.Err != "" {
		fmt.Fprintln(os.Stderr, o.Err)
	}
	return o.Exit
}

// fakeChildEnv returns os.Environ() with AGENT_BRIDGE_FAKECLI replaced by the JSON for o
// (existing copies stripped so a grandchild gets exactly one, recursion-free directive).
func fakeChildEnv(o fakeOpts) []string {
	b, _ := json.Marshal(o)
	prefix := fakeCLIEnv + "="
	out := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, fakeCLIEnv+"="+string(b))
}

// fakeBin points the next spawned child at this test binary running as the fake CLI with
// the given behavior, and returns its path (os.Args[0], a real executable on every OS).
// Pass it to withBin. For stat-only uses (locate/probe never spawn) the behavior is
// irrelevant — the path just needs to exist, which os.Args[0] does.
func fakeBin(t *testing.T, o fakeOpts) string {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal fakeOpts: %v", err)
	}
	t.Setenv(fakeCLIEnv, string(b))
	return os.Args[0]
}

// grandchildFakeBin is fakeBin for the grandchild-hang tests, whose backgrounded
// grandchild re-execs the test binary and must outlive the test's hang-detection window
// (>15s). On Windows a running process LOCKS its image, so that 30s orphan would keep
// go's compiled test binary locked and make `go test` fail to delete it (a non-zero
// exit). To avoid that — without a slow, cold binary copy that the child's kill deadline
// would race — the grandchild records its PID and we kill it on cleanup; by then the test
// has its result and the freed binary deletes cleanly. On unix this is harmless tidy-up
// (a running binary can be unlinked anyway).
func grandchildFakeBin(t *testing.T, o fakeOpts) string {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	o.PidFile = pidFile
	t.Cleanup(func() { killPIDFile(pidFile) })
	return fakeBin(t, o)
}

// killPIDFile reads a PID the fake CLI's grandchild recorded and terminates it. A missing
// file, an unparseable PID, or an already-exited process are all no-ops.
func killPIDFile(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// exeSuffix is ".exe" on Windows (where exec.LookPath only matches PATHEXT-suffixed
// names), "" elsewhere — used to name on-PATH fixtures so LookPath can find them.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// setHomeDir points os.UserHomeDir() at dir. UserHomeDir reads $HOME on unix but
// %USERPROFILE% on Windows, so a test that fakes the home dir must set both.
func setHomeDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	}
}

// samePath compares two filesystem paths, tolerating Windows' case-insensitivity and
// separator quirks so PATH/local-bin lookups can be asserted portably.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// ptyRefusedResult reports whether runAgent refused to spawn b because it needs a
// pseudo-terminal this build lacks (any non-unix build, e.g. Windows). On such builds
// agy is refused up front by design, so its spawn-behavior tests assert that refusal and
// the caller skips the rest. On unix (ptySupported) it always returns false, so the
// normal spawn assertions run.
func ptyRefusedResult(t *testing.T, b backend, res *mcp.CallToolResult, err error) bool {
	t.Helper()
	if !(b.needsPTY && !ptySupported) {
		return false
	}
	if err != nil {
		t.Fatalf("[%s] pty-less build should refuse with a result, got Go error: %v", b.tool, err)
	}
	if res == nil || !res.IsError || !strings.Contains(resultText(t, res), "requires a pseudo-terminal") {
		t.Fatalf("[%s] expected pty-refusal result on a build without pty support, got %+v", b.tool, res)
	}
	return true
}
