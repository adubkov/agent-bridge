package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestLocateSource(t *testing.T) {
	b := claudeBackend
	t.Run("binEnv override that exists -> env/found", func(t *testing.T) {
		p := writeFakeBin(t, "#!/bin/sh\n")
		t.Setenv(b.binEnv, p)
		path, source, found := b.locate()
		if !found || source != "env" || path != p {
			t.Errorf("got (%q,%q,%v); want (%q,env,true)", path, source, found, p)
		}
	})
	t.Run("binEnv override that is missing -> env/not found", func(t *testing.T) {
		miss := filepath.Join(t.TempDir(), "nope")
		t.Setenv(b.binEnv, miss)
		path, source, found := b.locate()
		if found || source != "env" || path != miss {
			t.Errorf("got (%q,%q,%v); want (%q,env,false)", path, source, found, miss)
		}
	})
	t.Run("binEnv override that is a directory -> not found", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(b.binEnv, dir)
		path, source, found := b.locate()
		if found || source != "env" || path != dir {
			t.Errorf("got (%q,%q,%v); want (%q,env,false)", path, source, found, dir)
		}
	})
	t.Run("binEnv override that is non-executable -> not found", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("no execute bit on windows")
		}
		p := filepath.Join(t.TempDir(), "noexec")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatalf("write non-exec: %v", err)
		}
		t.Setenv(b.binEnv, p)
		path, source, found := b.locate()
		if found || source != "env" || path != p {
			t.Errorf("got (%q,%q,%v); want (%q,env,false)", path, source, found, p)
		}
	})
	t.Run("PATH hit -> path/found", func(t *testing.T) {
		t.Setenv(b.binEnv, "")
		dir := t.TempDir()
		want := writeExec(t, dir, b.cliName)
		t.Setenv("PATH", dir)
		path, source, found := b.locate()
		if !found || source != "path" || path != want {
			t.Errorf("got (%q,%q,%v); want (%q,path,true)", path, source, found, want)
		}
	})
	t.Run("local-bin fallback", func(t *testing.T) {
		t.Setenv(b.binEnv, "")
		t.Setenv("PATH", t.TempDir())
		home := t.TempDir()
		t.Setenv("HOME", home)
		want := writeExec(t, filepath.Join(home, ".local", "bin"), b.cliName)
		path, source, found := b.locate()
		if !found || source != "local-bin" || path != want {
			t.Errorf("got (%q,%q,%v); want (%q,local-bin,true)", path, source, found, want)
		}
	})
	t.Run("not found -> bare name, empty source", func(t *testing.T) {
		t.Setenv(b.binEnv, "")
		t.Setenv("PATH", t.TempDir())
		t.Setenv("HOME", t.TempDir())
		path, source, found := b.locate()
		if found || source != "" || path != b.cliName {
			t.Errorf("got (%q,%q,%v); want (%q,\"\",false)", path, source, found, b.cliName)
		}
	})
}

func TestIsPongReply(t *testing.T) {
	cases := []struct {
		txt  string
		want bool
	}{
		{"PONG", true},
		{"pong", true},
		{"PONG\n", true},
		{"  PONG  ", true},
		{"PONG!", true},
		{"**PONG**", true},
		{"no PONG", false},
		{"I cannot output PONG", false},
		{"PONGO", false},
		{"", false},
		{"   ", false},
		// runAgent wraps the model reply with a "[<tool> | …]" header line; PONG on its
		// own body line is ready, a refusal in the body is not.
		{"[claude_agent | tool-use: read-only | model=opus | 0.1s]\n\nPONG", true},
		{"[claude_agent | tool-use: read-only | model=opus | 0.1s]\n\nI cannot output PONG", false},
	}
	for _, c := range cases {
		if got := isPongReply(c.txt); got != c.want {
			t.Errorf("isPongReply(%q) = %v; want %v", c.txt, got, c.want)
		}
	}
}

func TestParseClaudeAuth(t *testing.T) {
	t.Run("logged in", func(t *testing.T) {
		a, d := parseClaudeAuth([]byte(`{"loggedIn":true,"email":"me@x.com","authMethod":"claude.ai"}`), nil)
		if a != "yes" || !strings.Contains(d, "me@x.com") {
			t.Errorf("got (%q,%q); want yes + email", a, d)
		}
	})
	t.Run("logged out", func(t *testing.T) {
		if a, _ := parseClaudeAuth([]byte(`{"loggedIn":false}`), nil); a != "no" {
			t.Errorf("got %q; want no", a)
		}
	})
	t.Run("unparseable, no run error -> unknown", func(t *testing.T) {
		if a, _ := parseClaudeAuth([]byte(`not json`), nil); a != "unknown" {
			t.Errorf("got %q; want unknown", a)
		}
	})
	t.Run("unparseable with run error -> no", func(t *testing.T) {
		if a, _ := parseClaudeAuth([]byte(`command not found`), os.ErrNotExist); a != "no" {
			t.Errorf("got %q; want no", a)
		}
	})
}

func TestParseCodexAuth(t *testing.T) {
	const ok = `{"overallStatus":"ok","checks":{"auth":{"credentials":{"id":"auth.credentials","category":"auth","status":"ok","summary":"auth is configured"}}}}`
	const bad = `{"checks":{"auth":{"credentials":{"category":"auth","status":"error","summary":"not logged in"}}}}`
	const noAuth = `{"checks":{"net":{"x":{"category":"network","status":"ok"}}}}`
	t.Run("auth ok -> yes", func(t *testing.T) {
		a, d := parseCodexAuth([]byte(ok), nil)
		if a != "yes" || !strings.Contains(d, "configured") {
			t.Errorf("got (%q,%q); want yes + summary", a, d)
		}
	})
	t.Run("auth error -> no", func(t *testing.T) {
		if a, _ := parseCodexAuth([]byte(bad), nil); a != "no" {
			t.Errorf("got %q; want no", a)
		}
	})
	t.Run("mixed ok+fail -> no with the FAILING node's detail", func(t *testing.T) {
		// Two auth checks, one ok one error. Map values walk in randomized order, so the
		// "no" detail must come from a node that actually failed — never the ok node's
		// summary (which would contradict the verdict). Run repeatedly to defeat ordering luck.
		const mixed = `{"checks":{"auth":{"creds":{"category":"auth","status":"ok","summary":"auth is configured"},` +
			`"token":{"category":"auth","status":"error","summary":"token expired"}}}}`
		for i := 0; i < 50; i++ {
			a, d := parseCodexAuth([]byte(mixed), nil)
			if a != "no" || d != "token expired" {
				t.Fatalf("got (%q,%q); want (no, token expired) — detail must be the failing node's", a, d)
			}
		}
	})
	t.Run("no auth check -> unknown", func(t *testing.T) {
		if a, _ := parseCodexAuth([]byte(noAuth), nil); a != "unknown" {
			t.Errorf("got %q; want unknown", a)
		}
	})
	t.Run("garbage, no run error -> unknown", func(t *testing.T) {
		if a, _ := parseCodexAuth([]byte(`xxx`), nil); a != "unknown" {
			t.Errorf("got %q; want unknown", a)
		}
	})
}

func TestListAgentsInstalled(t *testing.T) {
	// Point every backend's binEnv at an existing fake file so locate is deterministic.
	for _, b := range backends {
		t.Setenv(b.binEnv, writeFakeBin(t, "#!/bin/sh\n"))
	}
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "list_agents", Arguments: map[string]any{"probe": "installed"}}}
	res, err := listAgentsHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("unexpected error result: %q", toolResultText(res))
	}
	var got []agentStatus
	if err := json.Unmarshal([]byte(toolResultText(res)), &got); err != nil {
		t.Fatalf("result not a JSON array: %v\n%s", err, toolResultText(res))
	}
	if len(got) != len(backends) {
		t.Fatalf("got %d statuses; want %d", len(got), len(backends))
	}
	for _, s := range got {
		if !s.Installed || s.Source != "env" {
			t.Errorf("%s: installed=%v source=%q; want installed via env", s.Tool, s.Installed, s.Source)
		}
		if s.Authed != "" || s.Ready != nil {
			t.Errorf("%s: installed probe must not set authed/ready (authed=%q ready=%v)", s.Tool, s.Authed, s.Ready)
		}
	}
}

func TestListAgentsProbeValidation(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "list_agents", Arguments: map[string]any{"probe": "bogus"}}}
	res, _ := listAgentsHandler(context.Background(), req)
	if res == nil || !res.IsError || !strings.Contains(toolResultText(res), "invalid probe") {
		t.Errorf("want invalid-probe error; got %q", toolResultText(res))
	}
}

func TestListAgentsAuthRefusedWhenFrozen(t *testing.T) {
	t.Setenv(noDelegateEnv, "1")
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "list_agents", Arguments: map[string]any{"probe": "auth"}}}
	res, _ := listAgentsHandler(context.Background(), req)
	if res == nil || !res.IsError || !strings.Contains(toolResultText(res), "delegation is disabled") {
		t.Errorf("want delegation-disabled refusal for auth; got %q", toolResultText(res))
	}
	// installed must still work even when frozen (it never spawns).
	for _, b := range backends {
		t.Setenv(b.binEnv, writeFakeBin(t, "#!/bin/sh\n"))
	}
	req2 := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "list_agents", Arguments: map[string]any{"probe": "installed"}}}
	res2, _ := listAgentsHandler(context.Background(), req2)
	if res2 == nil || res2.IsError {
		t.Errorf("installed probe must work when frozen; got %q", toolResultText(res2))
	}
}

func TestCheckServe(t *testing.T) {
	t.Setenv(hopDepthEnv, "0")
	t.Setenv(hopMaxEnv, "2")
	t.Setenv(noDelegateEnv, "")

	t.Run("PONG reply -> ready", func(t *testing.T) {
		b := withBin(t, claudeBackend, writeFakeBin(t, "#!/bin/sh\nprintf 'PONG\\n'\n"))
		ready, _, detail := b.checkServe(context.Background(), 10*time.Second)
		if !ready {
			t.Errorf("want ready; detail=%q", detail)
		}
	})
	t.Run("no PONG -> not ready", func(t *testing.T) {
		b := withBin(t, claudeBackend, writeFakeBin(t, "#!/bin/sh\nprintf 'nope\\n'\n"))
		ready, _, detail := b.checkServe(context.Background(), 10*time.Second)
		if ready || !strings.Contains(detail, "no PONG") {
			t.Errorf("want not-ready with no-PONG detail; got ready=%v detail=%q", ready, detail)
		}
	})
}
