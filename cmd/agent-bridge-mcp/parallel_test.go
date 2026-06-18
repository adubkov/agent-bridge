package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestBackendByTool(t *testing.T) {
	for _, name := range []string{"antigravity_agent", "claude_agent", "codex_agent"} {
		if b, ok := backendByTool(name); !ok || b.tool != name {
			t.Errorf("backendByTool(%q) = (%q, %v), want it found", name, b.tool, ok)
		}
	}
	if _, ok := backendByTool("nope_agent"); ok {
		t.Errorf("backendByTool(nope_agent) found, want not found")
	}
}

func TestToolResultText(t *testing.T) {
	if got := toolResultText(nil); got != "" {
		t.Errorf("toolResultText(nil) = %q, want empty", got)
	}
	if got := toolResultText(mcp.NewToolResultText("hello")); got != "hello" {
		t.Errorf("toolResultText(text) = %q, want hello", got)
	}
	if got := toolResultText(mcp.NewToolResultError("boom")); !strings.Contains(got, "boom") {
		t.Errorf("toolResultText(error) = %q, want it to contain boom", got)
	}
}

func TestJobAccessors(t *testing.T) {
	m := map[string]any{
		"s":     "hi",
		"b":     true,
		"f":     float64(7),
		"i":     int(9),
		"slice": []any{"a", "b", 3},
	}
	if got := jobStr(m, "s"); got != "hi" {
		t.Errorf("jobStr = %q", got)
	}
	if got := jobStr(m, "missing"); got != "" {
		t.Errorf("jobStr(missing) = %q, want empty", got)
	}
	if !jobBool(m, "b") || jobBool(m, "missing") {
		t.Errorf("jobBool wrong")
	}
	if got := jobInt(m, "f"); got != 7 {
		t.Errorf("jobInt(float64) = %d, want 7", got)
	}
	if got := jobInt(m, "i"); got != 9 {
		t.Errorf("jobInt(int) = %d, want 9", got)
	}
	if got := jobInt(m, "missing"); got != 0 {
		t.Errorf("jobInt(missing) = %d, want 0", got)
	}
	// jobStrSlice drops only non-strings; trimming/empty-dropping is buildRunOpts' job.
	if got := jobStrSlice(m, "slice"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("jobStrSlice = %v, want [a b] (non-string 3 dropped)", got)
	}
	if got := jobStrSlice(m, "missing"); got != nil {
		t.Errorf("jobStrSlice(missing) = %v, want nil", got)
	}
}

// clearDelegationEnv neutralizes the delegation freeze / hop env so buildRunOpts'
// tier path (which calls delegationGuard) is deterministic in tests.
func clearDelegationEnv(t *testing.T) {
	t.Helper()
	t.Setenv(noDelegateEnv, "")
	t.Setenv(hopDepthEnv, "")
	t.Setenv(hopMaxEnv, "")
}

func TestBuildRunOpts(t *testing.T) {
	ctx := context.Background()
	claude, _ := backendByTool("claude_agent")
	agy, _ := backendByTool("antigravity_agent")

	t.Run("empty task is rejected", func(t *testing.T) {
		_, res := buildRunOpts(ctx, claude, jobInput{task: "   "})
		if res == nil || !res.IsError || !strings.Contains(toolResultText(res), "task` is required") {
			t.Fatalf("want task-required error, got %v", toolResultText(res))
		}
	})

	t.Run("invalid mode is rejected", func(t *testing.T) {
		_, res := buildRunOpts(ctx, claude, jobInput{task: "x", mode: "bogus"})
		if res == nil || !strings.Contains(toolResultText(res), "invalid mode") {
			t.Fatalf("want invalid-mode error, got %v", toolResultText(res))
		}
	})

	t.Run("read mode rejected for backend without a read-only tier", func(t *testing.T) {
		_, res := buildRunOpts(ctx, agy, jobInput{task: "x", mode: "read"})
		if res == nil || !strings.Contains(toolResultText(res), "no read-only mode") {
			t.Fatalf("want no-read-only error for agy, got %v", toolResultText(res))
		}
	})

	t.Run("modes map to capability axes", func(t *testing.T) {
		o, res := buildRunOpts(ctx, claude, jobInput{task: "x"}) // default reason
		if res != nil || o.allowTools || o.readOnly {
			t.Fatalf("reason: want both false, got allow=%v ro=%v res=%v", o.allowTools, o.readOnly, toolResultText(res))
		}
		o, res = buildRunOpts(ctx, claude, jobInput{task: "x", mode: "read"})
		if res != nil || !o.readOnly || o.allowTools {
			t.Fatalf("read: want readOnly only, got allow=%v ro=%v", o.allowTools, o.readOnly)
		}
		o, res = buildRunOpts(ctx, claude, jobInput{task: "x", mode: "act"})
		if res != nil || !o.allowTools {
			t.Fatalf("act: want allowTools, got allow=%v", o.allowTools)
		}
	})

	t.Run("timeout default and clamp", func(t *testing.T) {
		o, _ := buildRunOpts(ctx, claude, jobInput{task: "x"})
		if o.timeoutSeconds != defaultTimeoutSeconds {
			t.Errorf("default timeout = %d, want %d", o.timeoutSeconds, defaultTimeoutSeconds)
		}
		o, _ = buildRunOpts(ctx, claude, jobInput{task: "x", timeoutSeconds: maxTimeoutSeconds + 1000})
		if o.timeoutSeconds != maxTimeoutSeconds {
			t.Errorf("clamped timeout = %d, want %d", o.timeoutSeconds, maxTimeoutSeconds)
		}
	})

	t.Run("tier resolves to model+effort", func(t *testing.T) {
		clearDelegationEnv(t)
		o, res := buildRunOpts(ctx, claude, jobInput{task: "x", tier: "deep"})
		if res != nil {
			t.Fatalf("tier deep errored: %v", toolResultText(res))
		}
		if o.model != "opus" || o.effort != "xhigh" || o.tier != "deep" {
			t.Errorf("tier deep → model=%q effort=%q tier=%q, want opus/xhigh/deep", o.model, o.effort, o.tier)
		}
	})

	t.Run("canceled context during tier discovery surfaces ctx error, not resolution failure", func(t *testing.T) {
		clearDelegationEnv(t)
		// A discovery-based tier backend with a unique cliName so it can't pick up a
		// model another test cached. With the request context already canceled, the
		// `<cli> models` probe fails and resolveTier would report "could not resolve a
		// model" — buildRunOpts must surface the cancellation as the real cause instead.
		probe := backend{
			tool:         "cancel_probe_agent",
			cliName:      "cancel-probe-nonexistent-cli",
			modelListCmd: []string{"models"},
			tiers:        map[string]tierSpec{"deep": {modelMatch: []string{"Pro"}}},
		}
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, res := buildRunOpts(cctx, probe, jobInput{task: "x", tier: "deep"})
		if res == nil || !res.IsError {
			t.Fatalf("want an error result for a canceled request, got %v", toolResultText(res))
		}
		msg := toolResultText(res)
		if strings.Contains(msg, "could not resolve a model") {
			t.Fatalf("cancellation misreported as a resolution failure: %q", msg)
		}
		if !strings.Contains(msg, context.Canceled.Error()) {
			t.Fatalf("want the context-canceled cause surfaced, got %q", msg)
		}
	})

	t.Run("tier rejected when backend has no presets", func(t *testing.T) {
		clearDelegationEnv(t)
		none := backend{tool: "x_agent"} // no tiers, no read-only, no sandbox
		_, res := buildRunOpts(ctx, none, jobInput{task: "x", tier: "deep"})
		if res == nil || !strings.Contains(toolResultText(res), "no tier presets") {
			t.Fatalf("want no-tier-presets error, got %v", toolResultText(res))
		}
	})

	t.Run("sandbox honored only for backends that support it", func(t *testing.T) {
		o, _ := buildRunOpts(ctx, agy, jobInput{task: "x", sandbox: true})
		if !o.sandbox {
			t.Errorf("agy: sandbox=true not applied")
		}
		o, _ = buildRunOpts(ctx, claude, jobInput{task: "x", sandbox: true})
		if o.sandbox {
			t.Errorf("claude: sandbox should be ignored (no sandbox concept)")
		}
	})

	t.Run("add_dirs are trimmed and empties dropped", func(t *testing.T) {
		o, _ := buildRunOpts(ctx, claude, jobInput{task: "x", addDirs: []string{" /a ", "", "  ", "/b"}})
		if len(o.addDirs) != 2 || o.addDirs[0] != "/a" || o.addDirs[1] != "/b" {
			t.Errorf("addDirs = %v, want [/a /b]", o.addDirs)
		}
	})
}
