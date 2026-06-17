package main

import (
	"strings"
	"testing"
)

// TestBackendNeedsPTY pins which backends run on a pseudo-terminal. agy's agentic
// --print hangs on plain pipes, so antigravity MUST be pty-run; claude/codex are
// built for headless --print/exec and must NOT be (a stray pty would only add TTY
// control noise to their output).
func TestBackendNeedsPTY(t *testing.T) {
	cases := []struct {
		b    backend
		want bool
	}{
		{antigravityBackend, true},
		{claudeBackend, false},
		{codexBackend, false},
	}
	for _, c := range cases {
		if c.b.needsPTY != c.want {
			t.Errorf("%s needsPTY = %v, want %v", c.b.tool, c.b.needsPTY, c.want)
		}
	}
}

// TestFailureStdout: a pty backend's error/timeout output must keep the TAIL (where a
// merged-stream error lands); a pipe backend head-truncates (its error is in stderr).
func TestFailureStdout(t *testing.T) {
	long := strings.Repeat("A", 100) + "TAIL-ERROR"
	if got := antigravityBackend.failureStdout(long, 30); !strings.Contains(got, "TAIL-ERROR") {
		t.Errorf("pty backend must keep the tail error; got %q", got)
	}
	if got := codexBackend.failureStdout(long, 30); strings.Contains(got, "TAIL-ERROR") {
		t.Errorf("pipe backend should head-truncate; unexpectedly kept tail: %q", got)
	}
}

func TestCleanPTYOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "hello world", "hello world"},
		{"strip SGR color", "\x1b[31mred\x1b[0m", "red"},
		{"strip cursor/clear", "a\x1b[2K\x1b[1Gb", "ab"},
		{"strip OSC title (BEL-terminated)", "\x1b]0;my title\x07done", "done"},
		{"normalize CRLF", "line1\r\nline2", "line1\nline2"},
		{"drop bare CR", "abc\rdef", "abcdef"},
		{
			"JSON survives de-TTY",
			"\x1b[32m[{\"file\":\"x\"}]\x1b[0m\r\n",
			"[{\"file\":\"x\"}]\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanPTYOutput([]byte(tt.in)); got != tt.want {
				t.Errorf("cleanPTYOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
