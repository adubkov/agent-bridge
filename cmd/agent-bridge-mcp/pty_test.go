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
		{"strip OSC title (ST-terminated)", "\x1b]0;my title\x1b\\done", "done"},
		{"strip colon-delimited truecolor SGR", "\x1b[38:2:255:128:0mc\x1b[0m", "c"},
		{"normalize CRLF", "line1\r\nline2", "line1\nline2"},
		{"drop bare CR", "abc\rdef", "abcdef"},
		{
			"JSON survives de-TTY",
			"\x1b[32m[{\"file\":\"x\"}]\x1b[0m\r\n",
			"[{\"file\":\"x\"}]\n",
		},
		{
			// agy under a pty/ConPTY animates a spinner by repainting at cursor-home, then
			// writes the real output after a final home+erase. Only the last screen survives,
			// so the spinner frames don't glue onto the first line (which would break model
			// matching). Mirrors real `agy models` output.
			"collapse home-repaint spinner",
			"\x1b[2J\x1b[m\x1b[H⠋ Fetching available models...\x1b[H⠙ Fetching available models..." +
				"\x1b[H\x1b[KGemini 3.5 Flash (Medium)\r\nGemini 3.1 Pro (High)\r\n",
			"Gemini 3.5 Flash (Medium)\nGemini 3.1 Pro (High)\n",
		},
		{
			// A plain answer printed after one clear+home survives intact.
			"keep answer after single clear+home",
			"\x1b[?25l\x1b[2J\x1b[m\x1b[HPONG\r\n\x1b]0;title\x07\x1b[?25h",
			"PONG\n",
		},
		{
			// Cursor-home with a single row param (ESC[1H) is still a home — collapse on it.
			"collapse on single-param home (ESC[1H)",
			"old-frame\x1b[1Hfinal\r\n",
			"final\n",
		},
		{
			// Non-home absolute positioning (ESC[5H) must NOT trigger a collapse.
			"non-home position does not collapse",
			"keep-this\x1b[5Hand-this",
			"keep-thisand-this",
		},
		{
			// A trailing clear AFTER the real output empties the post-reset slice; keep the
			// real answer (the previous painted screen), not nothing.
			"trailing reset keeps the prior screen",
			"the real answer\x1b[2J",
			"the real answer",
		},
		{
			// Spinner frames, then the answer, then a trailing reset with nothing after it:
			// must yield ONLY the answer — not the answer with the spinner frames glued back on.
			"trailing reset does not reintroduce spinner frames",
			"\x1b[Hspin-1\x1b[Hspin-2\x1b[HREAL-ANSWER\r\n\x1b[2J",
			"REAL-ANSWER\n",
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
