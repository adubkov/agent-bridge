// Command agy-mcp is a tiny MCP (Model Context Protocol) server that exposes
// the Antigravity CLI (`agy`) — i.e. a Gemini agent — as a single MCP tool.
//
// A parent agent (e.g. Claude Code) calls the `gemini_agent` tool with a task;
// this server shells out to `agy --print <task>`, lets Gemini perform the task,
// and returns Gemini's response. In effect it is a spawned Gemini sub-agent
// callable from inside another agent's session.
//
// Safety: tool-use (Gemini editing files / running commands) is DISABLED by
// default. In the default mode the server runs `agy --print` with no
// permission-bypass, so Gemini can reason/answer but cannot take unattended
// actions. To let the spawned agent actually edit files in working_dir, the
// caller sets `allow_tools: true` (passes --dangerously-skip-permissions);
// scope it with `working_dir`. The `--sandbox` flag is OFF by default because
// it confines edits to an isolated scratch dir (set `sandbox: true` only for a
// confined "compute but don't touch my files" run). The tool result header
// always reports which mode ran.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultTimeoutSeconds = 300
	maxTimeoutSeconds     = 1800
)

// resolveAgyBinary finds the `agy` executable. Priority: AGY_BIN env override,
// then PATH, then the known install location. Claude Code may spawn this server
// with a minimal PATH, so the explicit fallback matters.
func resolveAgyBinary() string {
	if v := strings.TrimSpace(os.Getenv("AGY_BIN")); v != "" {
		return v
	}
	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		fallback := filepath.Join(home, ".local", "bin", "agy")
		if _, statErr := os.Stat(fallback); statErr == nil {
			return fallback
		}
	}
	return "agy"
}

var agyBin = resolveAgyBinary()

func main() {
	s := server.NewMCPServer(
		"agy-mcp",
		"0.1.0",
		server.WithToolCapabilities(false),
	)

	tool := mcp.NewTool("gemini_agent",
		mcp.WithDescription(
			"Spawn a Gemini agent (via the Antigravity `agy` CLI) to perform a task and return its response. "+
				"Give it a self-contained task in `task`; it runs non-interactively and returns Gemini's full output. "+
				"By default the spawned agent can reason and answer but CANNOT take unattended actions (no file edits / "+
				"command execution) — set `allow_tools: true` to let it act, which disables Gemini's permission prompts "+
				"and (by default) runs it in a restricted sandbox. Use `add_dirs` to give it workspace context and "+
				"`working_dir` to set where it runs.",
		),
		mcp.WithString("task",
			mcp.Required(),
			mcp.Description("The complete, self-contained task/prompt for the Gemini agent to perform."),
		),
		mcp.WithArray("add_dirs",
			mcp.Description("Directories to add to the agent's workspace (absolute paths). Repeatable."),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithString("working_dir",
			mcp.Description("Directory the agent runs in (absolute path). Defaults to this server's working directory."),
		),
		mcp.WithNumber("timeout_seconds",
			mcp.Description(fmt.Sprintf("Max seconds to wait for the agent (default %d, max %d).", defaultTimeoutSeconds, maxTimeoutSeconds)),
		),
		mcp.WithBoolean("allow_tools",
			mcp.Description("Allow the spawned agent to take actions (edit files in working_dir, run commands) by "+
				"auto-approving its permission prompts (passes --dangerously-skip-permissions). Default false "+
				"(reason/answer only). Use with care — this is unattended execution; scope it with working_dir."),
		),
		mcp.WithBoolean("sandbox",
			mcp.Description("Confine the agent to an isolated scratch dir with terminal restrictions (--sandbox). "+
				"Default false. WARNING: when true, the agent's file edits go to a scratch dir, NOT working_dir — "+
				"use only for a confined 'compute but don't touch my files' run."),
		),
	)

	s.AddTool(tool, handleGeminiAgent)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "agy-mcp: server error: %v\n", err)
		os.Exit(1)
	}
}

func handleGeminiAgent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Check context cancellation before executing
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	task := strings.TrimSpace(req.GetString("task", ""))
	if task == "" {
		return mcp.NewToolResultError("`task` is required and must be a non-empty string"), nil
	}

	timeoutSeconds := defaultTimeoutSeconds
	if v := req.GetInt("timeout_seconds", 0); v > 0 {
		timeoutSeconds = v
	}
	if timeoutSeconds > maxTimeoutSeconds {
		timeoutSeconds = maxTimeoutSeconds
	}

	allowTools := req.GetBool("allow_tools", false)

	// sandbox defaults OFF. With --sandbox, agy confines the agent to an
	// isolated scratch dir, so its file edits do NOT land in working_dir —
	// useless for real project edits. Callers wanting a confined
	// "compute but don't touch my files" run set sandbox: true explicitly.
	sandbox := req.GetBool("sandbox", false)

	workingDir := req.GetString("working_dir", "")

	var addDirs []string
	for _, d := range req.GetStringSlice("add_dirs", nil) {
		if s := strings.TrimSpace(d); s != "" {
			addDirs = append(addDirs, s)
		}
	}

	// Build the agy invocation. CRITICAL: `--print` takes the prompt as its
	// VALUE — the task must come immediately after it, with all other flags
	// AFTER the prompt. (agy usage: `agy --print "<prompt>" --print-timeout 10m`.)
	// Putting --print-timeout between --print and the task makes agy treat
	// "--print-timeout" as the prompt.
	cmdArgs := []string{
		"--print", task,
		"--print-timeout", fmt.Sprintf("%ds", timeoutSeconds),
	}
	for _, d := range addDirs {
		cmdArgs = append(cmdArgs, "--add-dir", d)
	}
	if allowTools {
		cmdArgs = append(cmdArgs, "--dangerously-skip-permissions")
	}
	if sandbox {
		cmdArgs = append(cmdArgs, "--sandbox")
	}

	// Give the process a little headroom beyond agy's own print-timeout so we
	// surface agy's timeout message rather than killing it first.
	hardDeadline := time.Duration(timeoutSeconds+30) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, hardDeadline)
	defer cancel()

	cmd := exec.CommandContext(runCtx, agyBin, cmdArgs...)
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start).Round(time.Millisecond)

	modeNote := "tool-use: disabled (reason/answer only)"
	if allowTools {
		modeNote = "tool-use: ENABLED (--dangerously-skip-permissions)"
		if sandbox {
			modeNote += " in --sandbox"
		}
	}

	// If the parent context was canceled, return the cancellation error
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if runCtx.Err() == context.DeadlineExceeded {
		return mcp.NewToolResultError(fmt.Sprintf(
			"gemini_agent timed out after %s (%s).\nPartial stdout:\n%s\nstderr:\n%s",
			elapsed, modeNote, truncate(stdout.String(), 8000), truncate(stderr.String(), 2000),
		)), nil
	}

	if runErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"gemini_agent failed (%s): %v\nstderr:\n%s\nstdout:\n%s",
			modeNote, runErr, truncate(stderr.String(), 4000), truncate(stdout.String(), 8000),
		)), nil
	}

	out := strings.TrimRight(stdout.String(), "\n")
	if strings.TrimSpace(out) == "" {
		out = "(agy returned no stdout)"
		if se := strings.TrimSpace(stderr.String()); se != "" {
			out += "\nstderr:\n" + truncate(se, 2000)
		}
	}

	header := fmt.Sprintf("[gemini_agent | %s | %s]\n\n", modeNote, elapsed)
	return mcp.NewToolResultText(header + out), nil
}

// truncate returns a copy of s truncated to at most max bytes, without splitting UTF-8 runes.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Back up to a valid UTF-8 rune boundary.
	// Continuation bytes start with the bits 10xxxxxx, i.e., byte & 0xC0 == 0x80.
	i := max
	for i > 0 && (s[i]&0xC0 == 0x80) {
		i--
	}
	return s[:i] + fmt.Sprintf("\n…(truncated, %d bytes total)", len(s))
}
