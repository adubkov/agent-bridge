package main

import "testing"

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
