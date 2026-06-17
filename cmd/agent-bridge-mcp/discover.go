package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// Probe depth levels for list_agents (the `probe` param), cheapest first.
const (
	probeInstalled = "installed" // CLI resolvable on PATH/binEnv only — no spawn
	probeAuth      = "auth"      // + the CLI's own cheap auth/health command
	probeServe     = "serve"     // + a real PONG round-trip (authoritative, costs an inference)
)

const (
	defaultAuthTimeoutSeconds  = 15
	defaultServeTimeoutSeconds = 120

	listAgentsToolDescription = "Discover which agent-bridge backends (antigravity_agent / claude_agent / " +
		"codex_agent) are actually usable on this host, so a caller can pick its reviewer/sub-agent set before " +
		"fanning out. Returns a JSON array, one entry per backend. The `probe` param controls depth: `installed` " +
		"(default; CHEAP — only resolves the CLI via binEnv/PATH, no process spawn), `auth` (also runs each CLI's own " +
		"cheap auth/health command — claude `auth status`, codex `doctor`; agy has none, so its auth is reported " +
		"\"unknown\" rather than guessed), or `serve` (AUTHORITATIVE — a real PONG round-trip per backend, which proves " +
		"it can serve requests but costs a model call and can be slow, especially agy). installed never spawns; auth " +
		"and serve shell out and are refused for a reason-only (AGENT_NO_DELEGATE) child."

	listAgentsProbeDescription = "Probe depth: `installed` (default) | `auth` | `serve`. installed = CLI resolvable " +
		"only (no spawn). auth = + the CLI's cheap auth/health command (agy → \"unknown\", it has no such command). " +
		"serve = + a real PONG round-trip (authoritative but costs an inference; agy can take minutes — raise " +
		"timeout_seconds)."

	listAgentsTimeoutDescription = "Per-backend timeout in seconds for the auth/serve probes (default 15 for auth, " +
		"120 for serve; max 1800). Ignored for probe=installed. agy's serve round-trip can need ≥300."
)

// agentStatus is one backend's discovery result. Fields beyond the installed set are
// populated only at the matching probe depth and omitted otherwise.
type agentStatus struct {
	Tool      string `json:"tool"`
	CLI       string `json:"cli"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"` // env | path | local-bin

	Authed string `json:"authed,omitempty"` // "yes" | "no" | "unknown" (auth/serve probes)

	Ready     *bool `json:"ready,omitempty"`      // serve probe: did it answer PONG?
	LatencyMS int64 `json:"latency_ms,omitempty"` // serve probe round-trip

	Detail string `json:"detail,omitempty"` // short note for a non-trivial auth/serve outcome
}

// listAgentsTool is the discovery tool's schema. It takes no task — it inspects the
// host, it does not spawn an agent to do work.
func listAgentsTool() mcp.Tool {
	return mcp.NewTool("list_agents",
		mcp.WithDescription(listAgentsToolDescription),
		mcp.WithString("probe", mcp.Description(listAgentsProbeDescription)),
		mcp.WithNumber("timeout_seconds", mcp.Description(listAgentsTimeoutDescription)),
	)
}

// listAgentsHandler reports per-backend availability. `installed` is a pure lookup;
// `auth`/`serve` shell out, so they honor the reason-only delegation freeze and run
// the backends in parallel (serve is slow).
func listAgentsHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	probe := strings.ToLower(strings.TrimSpace(req.GetString("probe", probeInstalled)))
	switch probe {
	case "", probeInstalled:
		probe = probeInstalled
	case probeAuth, probeServe:
	default:
		return mcp.NewToolResultError(fmt.Sprintf(
			"invalid probe %q — valid: %s | %s | %s", probe, probeInstalled, probeAuth, probeServe)), nil
	}

	// auth/serve spawn the CLIs, so a reason-only child (frozen via AGENT_NO_DELEGATE)
	// must not run them; installed is a pure lookup and is always allowed.
	if probe != probeInstalled && delegationDisabled(os.Getenv) {
		return mcp.NewToolResultError(fmt.Sprintf(
			"list_agents: probe %q shells out to the agent CLIs, but delegation is disabled (%s=1) for this "+
				"reason-only child. Use probe \"installed\" (no spawn).", probe, noDelegateEnv)), nil
	}

	timeoutSeconds := req.GetInt("timeout_seconds", 0)
	if timeoutSeconds <= 0 {
		if probe == probeServe {
			timeoutSeconds = defaultServeTimeoutSeconds
		} else {
			timeoutSeconds = defaultAuthTimeoutSeconds
		}
	}
	if timeoutSeconds > maxTimeoutSeconds {
		timeoutSeconds = maxTimeoutSeconds
	}
	timeout := time.Duration(timeoutSeconds) * time.Second

	results := make([]agentStatus, len(backends))
	var wg sync.WaitGroup
	for i := range backends {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = probeBackend(ctx, backends[i], probe, timeout)
		}()
	}
	wg.Wait()

	out, err := json.MarshalIndent(results, "", "  ")
	if err != nil { // should not happen for this struct
		return mcp.NewToolResultError(fmt.Sprintf("list_agents: failed to encode results: %v", err)), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

// probeBackend builds one backend's status to the requested depth.
func probeBackend(ctx context.Context, b backend, probe string, timeout time.Duration) agentStatus {
	path, source, found := b.locate()
	st := agentStatus{Tool: b.tool, CLI: b.cliName, Installed: found}
	if found {
		st.Path, st.Source = path, source
	}
	if !found || probe == probeInstalled {
		return st
	}
	switch probe {
	case probeAuth:
		st.Authed, st.Detail = b.checkAuth(ctx, timeout)
	case probeServe:
		ready, latencyMS, detail := b.checkServe(ctx, timeout)
		st.Ready, st.LatencyMS, st.Detail = &ready, latencyMS, detail
	}
	return st
}

// checkAuth runs the backend's authCheck command (cheap, no inference) and lets the
// backend's authParse interpret the output. A backend with no such command (agy)
// reports "unknown" — we never claim auth we cannot verify.
func (b backend) checkAuth(ctx context.Context, timeout time.Duration) (authed, detail string) {
	if len(b.authCheck) == 0 || b.authParse == nil {
		return "unknown", "no auth/status command for " + b.cliName
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, b.resolveBin(), b.authCheck...).CombinedOutput()
	if cctx.Err() == context.DeadlineExceeded {
		return "unknown", fmt.Sprintf("auth check timed out after %s", timeout)
	}
	return b.authParse(out, err)
}

// parseClaudeAuth reads `claude auth status` JSON ({"loggedIn":bool,"email":...}).
// Exit code alone is insufficient (claude exits 0 even when logged out), so the
// verdict comes from loggedIn. Pure — table-testable.
func parseClaudeAuth(out []byte, runErr error) (authed, detail string) {
	var v struct {
		LoggedIn   bool   `json:"loggedIn"`
		Email      string `json:"email"`
		AuthMethod string `json:"authMethod"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		if runErr != nil {
			return "no", firstLine(string(out))
		}
		return "unknown", "unparseable claude auth status output"
	}
	if v.LoggedIn {
		detail = strings.TrimSpace(v.Email)
		if v.AuthMethod != "" {
			detail = strings.TrimSpace(strings.TrimSpace(v.Email + " (" + v.AuthMethod + ")"))
		}
		return "yes", detail
	}
	return "no", "not logged in"
}

// parseCodexAuth reads `codex doctor --json` and searches its checks for any node
// with category "auth"; reports "yes" only if every such check has status "ok"
// (codex doctor exits 0 even with problems, so the status fields are the signal).
// Pure — table-testable.
func parseCodexAuth(out []byte, runErr error) (authed, detail string) {
	var doc any
	if err := json.Unmarshal(out, &doc); err != nil {
		if runErr != nil {
			return "no", firstLine(string(out))
		}
		return "unknown", "unparseable codex doctor output"
	}
	var statuses []string
	var summary string
	var walk func(n any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			if cat, _ := t["category"].(string); cat == "auth" {
				st, _ := t["status"].(string)
				statuses = append(statuses, st)
				if summary == "" {
					if s, ok := t["summary"].(string); ok {
						summary = s
					}
				}
			}
			for _, v := range t {
				walk(v)
			}
		case []any:
			for _, v := range t {
				walk(v)
			}
		}
	}
	walk(doc)
	if len(statuses) == 0 {
		return "unknown", "no auth check in codex doctor output"
	}
	for _, st := range statuses {
		if st != "ok" {
			return "no", summary
		}
	}
	return "yes", summary
}

// checkServe sends a trivial PONG round-trip through the normal run path (so it
// inherits the hop guard and reason-only freeze) and reports whether the model
// actually answered. Authoritative but costs a real inference — opt-in only. Uses
// read-only mode where available, else reason (agy); never act.
func (b backend) checkServe(ctx context.Context, timeout time.Duration) (ready bool, latencyMS int64, detail string) {
	o := runOpts{
		task:           "Reply with exactly the word: PONG",
		timeoutSeconds: int(timeout / time.Second),
	}
	if b.supportsReadOnly() {
		o.readOnly = true
	}
	start := time.Now()
	res, err := runAgent(ctx, b, o)
	latencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return false, latencyMS, firstLine(err.Error())
	}
	txt := toolResultText(res)
	if res != nil && res.IsError {
		return false, latencyMS, firstLine(txt)
	}
	if strings.Contains(strings.ToUpper(txt), "PONG") {
		return true, latencyMS, ""
	}
	return false, latencyMS, "no PONG in reply"
}

// toolResultText concatenates the text content of a tool result (production helper;
// the test file has its own *testing.T-flavored variant).
func toolResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// firstLine returns the first non-empty trimmed line of s (for compact detail notes).
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
