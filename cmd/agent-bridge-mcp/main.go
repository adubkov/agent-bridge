// Command agent-bridge-mcp is a tiny MCP (Model Context Protocol) server that
// bridges coding agents, exposing each as a spawnable sub-agent tool.
//
// Three tools are registered:
//
//   - antigravity_agent — shells out to the Antigravity CLI (`agy --print <task>`),
//     i.e. spawns an Antigravity (Gemini) sub-agent. Intended to be called from a Claude session.
//   - claude_agent — shells out to the Claude CLI (`claude --print <task>`),
//     i.e. spawns a Claude sub-agent. Intended to be called from an Antigravity session.
//   - codex_agent — shells out to the OpenAI Codex CLI (`codex exec <task>`),
//     i.e. spawns a Codex sub-agent. Callable from any parent session.
//
// A parent agent calls the tool with a self-contained task; this server shells
// out to the corresponding CLI, lets the child agent perform the task, and
// returns the child's full output. In effect each tool is a spawned sub-agent
// callable from inside another agent's session. Backends are declared in a small
// in-code registry (see `backends`), so adding another CLI coding-agent is one
// entry, not new code.
//
// Access mode: the `mode` param selects the access tier — `reason` (default; no
// permission-bypass — intended for reason/answer, but NOT a hard write-block for
// every backend; see the agy caveat below), `read` (read-only
// exploration), or `act` (full file edits + command execution, unattended). Acting
// passes a permission-bypass flag:
//   - antigravity_agent passes --dangerously-skip-permissions to `agy`.
//   - claude_agent passes --dangerously-skip-permissions to `claude`.
//   - codex_agent passes --dangerously-bypass-approvals-and-sandbox to `codex`.
//
// Read mode: claude_agent passes --permission-mode plan; codex_agent reuses its
// --sandbox read-only; antigravity_agent has NO read tier.
//
// Scope read/act runs with `working_dir`. For antigravity_agent the `--sandbox` flag is
// OFF by default; note it applies agy's "terminal restrictions" but does NOT keep file
// edits out of `working_dir` (verified: a write under --sandbox landed in working_dir), so
// it is not a "don't touch my files" guard — point `working_dir` at a throwaway dir to keep
// agy off your files. claude_agent has NO sandbox option. codex_agent has no pure no-tools mode, so its
// default (`mode: "reason"`/`"read"`) runs it in a read-only sandbox
// (--sandbox read-only) rather than fully disabling tools. The tool result header
// always reports which mode ran.
//
// Reason tier — what does it actually restrict? Omitting the skip-perms flag is
// MEANT to stop unattended writes/commands, and does for claude_agent (which also
// passes `--tools ""` to hard-disable ALL built-in tools — a true no-tools run) and
// codex_agent (whose `reason` is a `--sandbox read-only` sandbox). But it does NOT
// stop a CLI from reading: both `claude --print` and `agy --print` auto-allow read
// tools. And it does NOT stop agy from WRITING: agy has no tool-disabling flag and
// does not gate writes behind skip-perms, so a `reason` antigravity_agent pointed at
// a writable `working_dir` can still edit files unattended — and `--sandbox` does NOT
// confine those edits (it is terminal restrictions only; a write under it still lands in
// working_dir, verified). The only real guard is WHERE it runs: point `working_dir` at a
// throwaway dir/worktree. Omitting `working_dir` is NOT a guard for agy — it then runs in
// the bridge server's own cwd, which is often your project tree.
//
// Loop guard: to prevent runaway A→B→A→B delegation chains, the shared run path
// reads AGENT_HOP_DEPTH (current delegation depth, default 0) and AGENT_HOP_MAX
// (max allowed depth, default 2) from the environment. If the current depth has
// reached the max, the tool returns an error instead of spawning a child.
// Otherwise the child is spawned with AGENT_HOP_DEPTH incremented by one.
//
// Reason-only freeze: the hop guard bounds depth for AGENTS THAT ACT, but a
// non-acting child should not delegate at all. So every non-acting child (mode
// reason or read) is spawned with AGENT_NO_DELEGATE=1 in its environment;
// any bridge server that child itself launches sees the flag and refuses to
// spawn — a hard "no further delegation" stop that does not rely on the depth
// counter. This matters for codex_agent (reason-only is a read-only sandbox that
// can still run read-only commands) and antigravity_agent (reason-only still has
// read tools — agy has no tool-disable flag), either of which could otherwise
// re-enter the bridge; claude_agent reason-only has no tools at all (--tools "").
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultTimeoutSeconds = 300
	maxTimeoutSeconds     = 1800

	// hopDepthEnv tracks the current delegation depth; hopMaxEnv caps it.
	hopDepthEnv     = "AGENT_HOP_DEPTH"
	hopMaxEnv       = "AGENT_HOP_MAX"
	defaultHopMax   = 2
	defaultHopDepth = 0

	// noDelegateEnv, when "1" in the environment, hard-blocks this server from
	// spawning ANY child — independent of (and stricter than) the hop-depth
	// counter. It is set on every reason-only child's environment so a reason-only
	// agent, which should only reason, cannot re-enter the bridge to spawn a
	// grandchild — making "a reason-only finder can't delegate" a real guarantee
	// rather than a property of whichever sandbox the backend happens to use.
	noDelegateEnv = "AGENT_NO_DELEGATE"

	// antigravityTimeoutHeadroom is the extra wall-clock allowed beyond the requested
	// timeout before agy is hard-killed, so agy's own --print-timeout fires first
	// and surfaces its message. claude has no --print-timeout, so its backend uses
	// zero headroom (the context deadline IS the timeout).
	antigravityTimeoutHeadroom = 30 * time.Second

	// childWaitDelay bounds how long cmd.Run may block on stdout/stderr I/O after
	// the child is killed, so a grandchild that inherited the pipes cannot hang the
	// call past the deadline. Paired with process-group kill (setupProcessGroup).
	childWaitDelay = 5 * time.Second
)

// runOpts carries the fully-parsed, backend-agnostic parameters of a single
// tool invocation. It is a plain struct (no req access) so that buildArgs can
// be a pure, table-testable function.
type runOpts struct {
	task           string
	timeoutSeconds int
	// Access tier. The public `mode` param maps to these two capability axes
	// (allowTools implies the ability to write/run; readOnly is read-but-not-write):
	//   reason → both false   read → readOnly   act → allowTools.
	allowTools bool
	readOnly   bool
	sandbox    bool // antigravity-only boolean --sandbox; ignored by claude/codex
	model      string
	effort     string // reasoning effort; claude --effort / codex -c model_reasoning_effort; ignored by antigravity
	tier       string // resolved reviewer tier (deep/fast) for the result header; "" if none requested
	addDirs    []string
	workingDir string
}

// Access modes — the values of the `mode` param. reason = reason/answer only, no
// tools (the default); read = read-only exploration (read/grep files, no edits or
// effectful commands); act = full file edits + command execution (unattended).
// Per backend: antigravity_agent has no `read` tier; codex_agent's reason and read are
// both its `--sandbox read-only` (it has no pure no-tools mode).
const (
	modeReason = "reason"
	modeRead   = "read"
	modeAct    = "act"
)

// Model-facing tool descriptions. The prose differs per backend; the parameter
// set is shared (see commonToolOptions) so the tool schemas can't drift.
const (
	antigravityToolDescription = "Spawn an Antigravity agent (Google's `agy` CLI, which runs Gemini models) to perform a " +
		"task and return its response. Give it a self-contained task in `task`; it runs non-interactively and returns " +
		"the agent's full output. By default (`mode: \"reason\"`) it runs WITHOUT the permission-bypass flag and is meant " +
		"to reason/answer — but agy has no tool-disable flag and does NOT gate writes, so a `reason` agent pointed at a " +
		"writable `working_dir` can still read AND edit files unattended. `--sandbox` is terminal-restrictions only and does " +
		"NOT keep edits out of `working_dir`, so to keep agy off your files point `working_dir` at a throwaway dir " +
		"(omitting `working_dir` is NOT a guard — agy then runs in the bridge server's own cwd, often your project tree). " +
		"Set `mode: \"act\"` to let it act, which " +
		"disables agy's permission prompts and runs it UNATTENDED, with edits landing in `working_dir`. (antigravity_agent " +
		"has no `read` tier — only `reason` or `act`.) Sandboxing is OFF by default. Use `add_dirs` for workspace context " +
		"and `working_dir` to set where it runs."

	claudeToolDescription = "Spawn a Claude agent (via the `claude` CLI) to perform a task and return its response. " +
		"Give it a self-contained task in `task`; it runs non-interactively (`claude --print`) and returns Claude's " +
		"full output. By default (`mode: \"reason\"`) the spawned agent can reason and answer but CANNOT take " +
		"unattended actions (no file edits / command execution). Set `mode: \"read\"` for read-only exploration " +
		"(read/grep/glob the repo and run read-only commands like `git diff`, but no edits or effectful commands — " +
		"passes --permission-mode plan), or `mode: \"act\"` to let it " +
		"act, which passes --dangerously-skip-permissions so Claude auto-approves its own permission prompts and runs " +
		"UNATTENDED (it will edit files / run commands and consume Claude credits without further confirmation). Use " +
		"`add_dirs` for workspace context and `working_dir` to set where it runs. Set `effort` " +
		"(low|medium|high|xhigh|max) to control reasoning effort. Note: even reason-only runs consume Claude credits."

	antigravityModeDescription = "Access mode (default `reason`). `reason` = no permission-bypass flag — but agy has no " +
		"tool-disabling flag and does NOT gate writes, so a `reason` agent with a writable working_dir may still read AND " +
		"edit files unattended (`--sandbox` does NOT confine these writes — use a throwaway working_dir; omitting it just runs agy in the server's own cwd, often your project tree). `act` = edit files in working_dir + run commands " +
		"UNATTENDED (passes --dangerously-skip-permissions). antigravity_agent has NO read-only tier, so `read` is " +
		"rejected — use `reason` or `act`."

	claudeModeDescription = "Access mode (default `reason`). `reason` = no tools (reason/answer only). `read` = " +
		"read-only exploration: read/grep/glob files and run read-only commands like `git diff`, but no edits or " +
		"effectful commands (passes --permission-mode plan). " +
		"`act` = full edit + command execution UNATTENDED (passes --dangerously-skip-permissions; consumes credits). " +
		"Scope `read`/`act` with working_dir."

	codexToolDescription = "Spawn an OpenAI Codex agent (via the `codex` CLI, `codex exec`) to perform a task and return " +
		"its response. Give it a self-contained task in `task`; it runs non-interactively and returns Codex's full " +
		"output. By default (`mode: \"reason\"` or `\"read\"` — both the same here) the agent runs READ-ONLY " +
		"(--sandbox read-only): it can read files and reason but CANNOT edit files or run effectful commands — note " +
		"this is a read-only sandbox, not a pure no-tools mode. Set `mode: \"act\"` to let it act, which passes --dangerously-bypass-approvals-and-sandbox " +
		"so it runs UNATTENDED with full file/command access and NO sandbox, with edits landing in `working_dir`. Use " +
		"`add_dirs` for additional writable context and `working_dir` to set where it runs. Set `effort` (e.g. " +
		"low|medium|high) to control reasoning effort (passed as `-c model_reasoning_effort`). Codex runs even outside a " +
		"Git repo (--skip-git-repo-check is always passed). The tool returns Codex's final message; its session banner " +
		"and step-by-step transcript go to stderr and are surfaced only if the run fails or times out."

	codexModeDescription = "Access mode (default `reason`). `reason` and `read` are BOTH read-only (--sandbox " +
		"read-only): read files + run read-only commands, no writes — codex has no pure no-tools mode. `act` = full, " +
		"unsandboxed file/command access (passes --dangerously-bypass-approvals-and-sandbox). Use with care; scope it " +
		"with working_dir."

	sandboxDescription = "Enable agy's sandbox terminal restrictions (--sandbox). Default false. NOTE: despite the name " +
		"this does NOT confine the agent's FILE edits — a write under --sandbox still lands in working_dir (verified), so it " +
		"is not a 'don't touch my files' guard. To keep agy off your files, point working_dir at a throwaway dir (omitting it just runs agy in the server's own cwd, often your project tree — also not a guard). " +
		"Antigravity-only."

	claudeEffortDescription = "Optional reasoning effort for this run (passed as `--effort`). Accepts low | medium | " +
		"high | xhigh | max. Leave empty for the model's default effort."

	codexEffortDescription = "Optional reasoning effort for this run (passed as `-c model_reasoning_effort=<value>`). " +
		"Accepts minimal | low | medium | high (model-dependent). Leave empty for the default effort."
)

// tierSpec is the model + reasoning effort a reviewer tier ("deep"/"fast") resolves
// to for one backend. modelMatch is for backends with no stable model alias (agy):
// the substrings identifying the tier's model in `<cli> models` output, resolved at
// runtime (see discoverModel); when modelMatch is set, model is unused.
type tierSpec struct {
	model      string
	effort     string
	modelMatch []string
}

// backend declares one CLI adapter as DATA: the shared run/timeout/truncate/
// header/context-cancel/hop-guard logic lives in runAgent, so adding a CLI
// coding-agent is a single registry entry (see the registry below), not new code.
// Optional flags (timeoutFlag, sandboxFlag) are "" when the CLI lacks them.
type backend struct {
	tool    string // MCP tool name, e.g. "antigravity_agent"
	cliName string // CLI binary name, e.g. "agy"; used for PATH/fallback lookup and the "(<cli> returned no stdout)" note
	binEnv  string // env override for the CLI path, e.g. "AGY_BIN"

	// subcmd is emitted right after the binary, before any flags or the prompt
	// (e.g. ["exec"] for codex). nil for CLIs invoked as `<bin> [flags] <prompt>`.
	subcmd []string

	// Flag names. For flag-style CLIs (antigravity/claude) promptFlag carries the task
	// as its VALUE and is emitted FIRST; every other flag follows. "" means the CLI
	// does not support that flag. When promptPositional is set the task is a
	// trailing positional argument instead and promptFlag is unused.
	promptFlag    string // "--print" (flag-style) | "" (positional, see promptPositional)
	timeoutFlag   string // "--print-timeout" (antigravity) | "" (claude/codex: ctx deadline only)
	modelFlag     string // "--model"
	effortFlag    string // "--effort" (claude) | "" (codex uses effortConfigKey; antigravity has no effort lever)
	addDirFlag    string // "--add-dir"
	skipPermsFlag string // "--dangerously-skip-permissions" (antigravity/claude) | "--dangerously-bypass-approvals-and-sandbox" (codex)
	sandboxFlag   string // "--sandbox" (antigravity boolean) | "" (claude/codex)

	// effortConfigKey carries reasoning effort via codex's `-c <key>=<value>` config
	// form (effortConfigKey = "model_reasoning_effort") when the CLI has no dedicated
	// effort FLAG. "" for backends that use effortFlag (claude) or have no effort lever
	// (antigravity, where effort is selected through the model name instead).
	effortConfigKey string

	// promptPositional makes the task a trailing positional argument (emitted LAST,
	// after subcmd and every flag) instead of promptFlag's value — for CLIs like
	// codex whose non-interactive form is `<bin> exec [flags] <prompt>`.
	promptPositional bool

	// extraArgs are static flags always appended to the invocation (e.g. codex's
	// ["--skip-git-repo-check", "--color", "never"]). nil for CLIs that need none.
	extraArgs []string

	// reasonOnlyArgs are appended in the default `reason` mode — the restraint a
	// CLI needs to stay safe in the no-bypass tier. codex uses ["--sandbox",
	// "read-only"] (it has no true no-tools mode); claude uses ["--tools", ""] to
	// HARD-DISABLE all built-in tools, because omitting the skip-perms flag alone does
	// NOT stop `claude --print` from reading (its read tools are auto-allowed). antigravity
	// leaves this nil: agy has no tool-disabling flag, so its reason tier can still
	// read — see reasonOnlyNote.
	reasonOnlyArgs []string

	// readOnlyArgs are appended in `read` mode — the flags that grant read-only
	// exploration (claude: ["--permission-mode", "plan"]; codex: ["--sandbox",
	// "read-only"]). nil means the backend has no read-only tier (antigravity), so `read`
	// is rejected for it. supportsReadOnly() reports presence.
	readOnlyArgs []string

	// reasonOnlyNote overrides the result-header mode note for reason-only runs.
	// "" yields the default "tool-use: disabled (reason/answer only)"; codex sets it
	// to reflect that its reason-only run is a read-only sandbox, not pure no-tools.
	reasonOnlyNote string

	// timeoutHeadroom is extra wall-clock added to the requested timeout before the
	// child is hard-killed. Non-zero only for CLIs with their own internal timeout
	// (antigravity/agy); zero for claude/codex (the context deadline is the timeout).
	timeoutHeadroom time.Duration

	// needsPTY runs the CLI attached to a pseudo-terminal instead of plain pipes.
	// Required for agy: its agentic `--print` loop only runs to completion with a
	// controlling TTY — spawned headless (pipes) it hangs until killed, burning the
	// whole timeout. claude/codex are built for headless --print/exec and leave this
	// false. Only honored where ptySupported (unix); elsewhere it falls back to pipes.
	needsPTY bool

	// tiers maps a reviewer tier ("deep"/"fast") to the model+effort it resolves to,
	// so a caller can pass a stable tier instead of a CLI-specific model name. nil =
	// the backend exposes no tier presets (and no `tier` param). See resolveTier.
	tiers map[string]tierSpec

	// modelListCmd enumerates the CLI's models (e.g. ["models"] for agy) to resolve a
	// tierSpec.modelMatch at runtime. nil for backends whose tiers carry explicit model
	// names (claude) or omit the model (codex).
	modelListCmd []string

	// authCheck is the argv (after the binary) of a cheap, non-inference auth/health
	// command for list_agents' `auth` probe — claude ["auth","status"], codex
	// ["doctor","--json"]. nil when the CLI has none (agy) → auth reported "unknown".
	authCheck []string

	// authParse interprets authCheck's output (and run error) into an auth verdict
	// ("yes"/"no"/"unknown") + a short detail. Per-CLI because each emits a different
	// JSON shape (claude loggedIn; codex doctor checks). nil alongside a nil authCheck.
	authParse func(out []byte, runErr error) (authed, detail string)

	description string // model-facing tool description
	modeDesc    string // description for the `mode` param
	effortDesc  string // description for the effort param ("" if the backend has no effort lever)
}

// supportsSandbox reports whether the backend exposes the sandbox option.
func (b backend) supportsSandbox() bool { return b.sandboxFlag != "" }

// supportsReadOnly reports whether the backend has a `read` (read-only) tier.
// antigravity has none (only no-tools or full), so `mode: "read"` is rejected for it.
func (b backend) supportsReadOnly() bool { return len(b.readOnlyArgs) > 0 }

// appliesEffort reports whether the backend exposes a reasoning-effort lever (a
// dedicated flag or codex's config-key form). antigravity has none — its effort lives in
// the model name — so its tool omits the effort param and ignores any value passed.
func (b backend) appliesEffort() bool { return b.effortFlag != "" || b.effortConfigKey != "" }

// tierNames returns the backend's tier keys in stable (sorted) order, for the
// `tier` param description and unknown-tier error messages.
func (b backend) tierNames() []string {
	names := make([]string, 0, len(b.tiers))
	for k := range b.tiers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// tierDesc builds the `tier` param description, naming the backend's actual presets.
func (b backend) tierDesc() string {
	fields := "model"
	if b.appliesEffort() {
		fields = "model+effort"
	}
	return fmt.Sprintf(
		"Optional reviewer tier resolving %s for this backend: %s (deep = most capable, fast = cheaper/faster). "+
			"An explicit `model`/`effort` param overrides the tier per-field. Omit for the CLI/provider default.",
		fields, strings.Join(b.tierNames(), " | "))
}

// pickModel returns the first non-empty line of `<cli> models` output containing
// every substring in match (case-insensitive), trimmed; "" if none match or match
// is empty. Pure — table-testable.
func pickModel(modelsOutput string, match []string) string {
	if len(match) == 0 {
		return ""
	}
	for _, line := range strings.Split(modelsOutput, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		matched := true
		for _, sub := range match {
			if !strings.Contains(strings.ToLower(s), strings.ToLower(sub)) {
				matched = false
				break
			}
		}
		if matched {
			return s
		}
	}
	return ""
}

// resolveTier maps a tier to (model, effort) for backend b, honoring explicit
// overrides PER FIELD: an explicit reqModel/reqEffort wins over the tier preset,
// which beats the CLI default. discover resolves a tierSpec.modelMatch (agy) to a
// concrete model name; it is injected so tests need no real `agy models` call.
// Either returned value may be "" (→ CLI default). Returns an error for an unknown
// tier. Pure given a pure discover.
func resolveTier(b backend, tier, reqModel, reqEffort string,
	discover func(match []string) string) (model, effort string, err error) {
	model, effort = reqModel, reqEffort
	if strings.TrimSpace(tier) == "" {
		return model, effort, nil
	}
	spec, ok := b.tiers[tier]
	if !ok {
		return "", "", fmt.Errorf("invalid tier %q — valid tiers are %s", tier, strings.Join(b.tierNames(), " | "))
	}
	if strings.TrimSpace(model) == "" {
		if len(spec.modelMatch) > 0 {
			// A discovery-based tier (agy) MUST resolve to a concrete model. If it can't
			// — `<cli> models` failed, or its output no longer matches modelMatch — do NOT
			// silently fall through to the CLI default: the caller asked for a specific
			// tier (e.g. a "deep" Pro model) and would otherwise get an unflagged default
			// while the result header still reports tier=deep. Fail loud so they can pass
			// an explicit `model`, omit `tier`, or fix the match.
			model = discover(spec.modelMatch)
			if strings.TrimSpace(model) == "" {
				return "", "", fmt.Errorf(
					"tier %q: could not resolve a model for %s (no `%s` entry matching %q) — "+
						"pass an explicit `model`, or omit `tier`",
					tier, b.cliName, strings.Join(b.modelListCmd, " "), strings.Join(spec.modelMatch, " "))
			}
		} else {
			model = spec.model
		}
	}
	if strings.TrimSpace(effort) == "" && b.appliesEffort() {
		effort = spec.effort
	}
	return model, effort, nil
}

// modelCache memoizes discovered model names per (cli, match) for the process
// lifetime — `<cli> models` output rarely changes mid-run, and a restart re-resolves.
var modelCache sync.Map // key: cliName + "\x00" + join(match); val: string

// discoverModel runs the backend's modelListCmd and returns the model name matching
// `match` (see pickModel), memoized. Returns "" on any error/miss; resolveTier turns an
// empty result for a discovery-based tier into a clear error rather than silently using
// the CLI default. NOT pure (spawns a subprocess) — deliberately kept out of buildArgs
// so that stays table-testable.
func (b backend) discoverModel(ctx context.Context, match []string) string {
	if len(b.modelListCmd) == 0 || len(match) == 0 {
		return ""
	}
	key := b.cliName + "\x00" + strings.Join(match, "\x00")
	if v, ok := modelCache.Load(key); ok {
		return v.(string)
	}
	// Bound by the caller's context (so a canceled/timed-out request stops the probe)
	// under a short cap of its own — listing models should be near-instant. Harden the
	// spawn like runAgent: WaitDelay + a process-group kill so a stray grandchild that
	// inherits the pipes can't keep .Output() blocked past the deadline.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, b.resolveBin(), b.modelListCmd...)
	cmd.WaitDelay = childWaitDelay
	setupProcessGroup(cmd)
	out, err := cmd.Output()
	model := ""
	if err == nil {
		model = pickModel(string(out), match)
	}
	if model != "" {
		modelCache.Store(key, model) // cache successes only; don't poison on a transient failure
	}
	return model
}

// locate finds the backend's CLI executable and reports HOW it was found, for
// list_agents' availability probe: binEnv override → PATH → ~/.local/bin/<cliName>.
// source is "env" | "path" | "local-bin" when found, "" when not. The explicit
// fallback matters because a parent agent may spawn this server with a minimal PATH.
func (b backend) locate() (path, source string, found bool) {
	if v := strings.TrimSpace(os.Getenv(b.binEnv)); v != "" {
		if isExecutableFile(v) {
			return v, "env", true
		}
		return v, "env", false
	}
	if p, err := exec.LookPath(b.cliName); err == nil {
		return p, "path", true
	}
	if home, err := os.UserHomeDir(); err == nil {
		fallback := filepath.Join(home, ".local", "bin", b.cliName)
		if isExecutableFile(fallback) {
			return fallback, "local-bin", true
		}
	}
	return b.cliName, "", false
}

// isExecutableFile reports whether path is a regular file the OS can execute. locate's
// binEnv and local-bin branches use it so a directory or a non-executable file is NOT
// reported as an installed CLI (a later spawn would fail with is-a-directory/permission).
// The PATH branch relies on exec.LookPath, which already enforces this. Windows has no
// execute bit, so there a regular file is accepted (PATHEXT/LookPath governs PATH lookups).
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode()&0o111 != 0
}

// resolveBin returns the path to the backend's CLI — the resolved executable, or the
// bare cliName as a last resort so exec surfaces a clear "not found". See locate for
// the lookup order. A binEnv override is honored even if it does not exist (so the
// configured path appears verbatim in exec's error).
func (b backend) resolveBin() string {
	path, _, _ := b.locate()
	return path
}

// buildArgs builds the CLI invocation from the backend's flag spec. CRITICAL for
// flag-style CLIs: promptFlag takes the task as its VALUE and is emitted FIRST,
// with every other flag AFTER it — putting another flag between promptFlag and the
// task makes the CLI treat that flag as the prompt. Positional CLIs
// (promptPositional) instead carry the task as a TRAILING argument emitted last,
// after subcmd and every flag (e.g. `codex exec [flags] <prompt>`). An empty flag
// name means the CLI lacks that option, so it (and its value/loop) is skipped.
// Pure — table-testable.
func (b backend) buildArgs(o runOpts) []string {
	args := append([]string{}, b.subcmd...)
	if !b.promptPositional {
		args = append(args, b.promptFlag, o.task)
	}
	if b.timeoutFlag != "" {
		args = append(args, b.timeoutFlag, fmt.Sprintf("%ds", o.timeoutSeconds))
	}
	if b.modelFlag != "" && strings.TrimSpace(o.model) != "" {
		args = append(args, b.modelFlag, o.model)
	}
	// Reasoning effort: a dedicated flag (claude --effort) or codex's config-key form
	// (-c model_reasoning_effort=<value>). antigravity has neither, so its effort is dropped
	// here (it is selected through the model name instead).
	if strings.TrimSpace(o.effort) != "" {
		switch {
		case b.effortFlag != "":
			args = append(args, b.effortFlag, o.effort)
		case b.effortConfigKey != "":
			args = append(args, "-c", b.effortConfigKey+"="+o.effort)
		}
	}
	if b.addDirFlag != "" {
		for _, d := range o.addDirs {
			args = append(args, b.addDirFlag, d)
		}
	}
	if o.allowTools && b.skipPermsFlag != "" {
		args = append(args, b.skipPermsFlag)
	}
	if o.sandbox && b.sandboxFlag != "" {
		args = append(args, b.sandboxFlag)
	}
	args = append(args, b.extraArgs...)
	// Restraint args for the non-acting tiers: `read` mode emits readOnlyArgs
	// (claude --permission-mode plan / codex --sandbox read-only); the default
	// `reason` mode emits reasonOnlyArgs (codex's read-only sandbox; claude's
	// --tools "" no-tools lock; antigravity none — agy has no tool-disabling flag). The
	// act tier emitted its skip-perms flag above and adds neither.
	switch {
	case o.allowTools:
		// skip-perms flag already emitted; no restraint args.
	case o.readOnly:
		args = append(args, b.readOnlyArgs...)
	default:
		args = append(args, b.reasonOnlyArgs...)
	}
	// Positional prompt goes LAST, after subcmd and every flag, preceded by the "--"
	// end-of-options marker. Without it a task that starts with a dash (e.g.
	// "--fix the bug") or matches a subcommand name (codex exec's resume/review/help)
	// is parsed as a flag/subcommand instead of the prompt — codex itself rejects it
	// with "unexpected argument ... use '-- ...'".
	if b.promptPositional {
		args = append(args, "--", o.task)
	}
	return args
}

// modeNote describes the run mode for the result header. The strings are derived
// from the backend's own flags so they stay accurate per CLI: reason-only uses
// reasonOnlyNote when set (codex is read-only, not no-tools), and the enabled note
// names the backend's actual skip-perms / sandbox flags.
func (b backend) modeNote(o runOpts) string {
	switch {
	case o.allowTools:
		note := fmt.Sprintf("tool-use: ENABLED (%s)", b.skipPermsFlag)
		if o.sandbox && b.supportsSandbox() {
			note += " in " + b.sandboxFlag
		}
		return note
	case o.readOnly:
		return "tool-use: read-only (" + strings.Join(b.readOnlyArgs, " ") + ")"
	default:
		if b.reasonOnlyNote != "" {
			return b.reasonOnlyNote
		}
		return "tool-use: disabled (reason/answer only)"
	}
}

// selectionNote summarizes the model/effort actually requested, for the result
// header — so the caller (and the user) can confirm an override took effect. model
// is always shown ("default" when unset); effort only for backends that apply it.
func (b backend) selectionNote(o runOpts) string {
	model := strings.TrimSpace(o.model)
	if model == "" {
		model = "default"
	}
	note := "model=" + model
	if b.appliesEffort() && strings.TrimSpace(o.effort) != "" {
		note += " effort=" + o.effort
	}
	if strings.TrimSpace(o.tier) != "" {
		note += " tier=" + o.tier
	}
	return note
}

// backends is the registry and SINGLE SOURCE OF TRUTH: adding a CLI coding-agent
// is one entry here — no new code, and main() iterates it to register tools. The
// named vars below are derived from it purely for convenient test reference, so a
// new entry can never be silently forgotten.
var backends = []backend{
	{
		tool:            "antigravity_agent",
		cliName:         "agy",
		binEnv:          "AGY_BIN",
		promptFlag:      "--print",
		timeoutFlag:     "--print-timeout",
		modelFlag:       "--model",
		addDirFlag:      "--add-dir",
		skipPermsFlag:   "--dangerously-skip-permissions",
		sandboxFlag:     "--sandbox",
		timeoutHeadroom: antigravityTimeoutHeadroom,
		needsPTY:        true, // agy's agentic --print hangs without a controlling TTY
		modelListCmd:    []string{"models"},
		tiers: map[string]tierSpec{
			// agy has no model alias and bakes effort into the model NAME, so the tier's
			// model is discovered from `agy models` output at runtime (see discoverModel).
			"deep": {modelMatch: []string{"Pro", "(High)"}},
			"fast": {modelMatch: []string{"Flash", "(Medium)"}},
		},
		// no authCheck: agy exposes no auth/status/doctor subcommand, so list_agents
		// reports its auth as "unknown" rather than guessing.
		// no readOnlyArgs — antigravity has no read-only tier (only reason or act). agy also
		// has no tool-disabling flag and does not gate writes behind the bypass flag, so a
		// reason agent with a writable working_dir can still read AND edit unattended (use a
		// throwaway working_dir to keep it off your files — --sandbox does NOT confine writes);
		// reasonOnlyNote states the no-bypass tier honestly.
		reasonOnlyNote: "tool-use: reason-only (no permission-bypass; agy keeps read tools — no no-tools flag)",
		description:    antigravityToolDescription,
		modeDesc:       antigravityModeDescription,
	},
	{
		tool:          "claude_agent",
		cliName:       "claude",
		binEnv:        "CLAUDE_BIN",
		promptFlag:    "--print",
		modelFlag:     "--model",
		effortFlag:    "--effort",
		addDirFlag:    "--add-dir",
		skipPermsFlag: "--dangerously-skip-permissions",
		// reason tier: hard-disable ALL built-in tools. Omitting skip-perms is NOT
		// enough — `claude --print` auto-allows Read/Grep/Glob, so a reason run could
		// still read the filesystem. `--tools ""` (documented: "" disables all tools)
		// makes reason genuinely no-tools, matching the "reason/answer only" header.
		reasonOnlyArgs: []string{"--tools", ""},
		readOnlyArgs:   []string{"--permission-mode", "plan"}, // claude's read-only tier (plan mode)
		tiers: map[string]tierSpec{
			// opus/sonnet are family aliases that always resolve to the latest model.
			"deep": {model: "opus", effort: "xhigh"},
			"fast": {model: "sonnet", effort: "medium"},
		},
		authCheck: []string{"auth", "status"}, // cheap, non-inference auth check
		authParse: parseClaudeAuth,
		// timeoutFlag/sandboxFlag "" — claude has neither; timeoutHeadroom 0 — deadline is the timeout.
		description: claudeToolDescription,
		modeDesc:    claudeModeDescription,
		effortDesc:  claudeEffortDescription,
	},
	{
		tool:             "codex_agent",
		cliName:          "codex",
		binEnv:           "CODEX_BIN",
		subcmd:           []string{"exec"},
		promptPositional: true,
		modelFlag:        "--model",
		effortConfigKey:  "model_reasoning_effort",
		addDirFlag:       "--add-dir",
		skipPermsFlag:    "--dangerously-bypass-approvals-and-sandbox",
		extraArgs:        []string{"--skip-git-repo-check", "--color", "never"},
		reasonOnlyArgs:   []string{"--sandbox", "read-only"},
		readOnlyArgs:     []string{"--sandbox", "read-only"}, // codex's reason and read tiers are the same read-only sandbox
		reasonOnlyNote:   "tool-use: read-only (--sandbox read-only)",
		tiers: map[string]tierSpec{
			// model omitted (CLI default = recommended frontier); tier sets effort only.
			"deep": {effort: "high"},
			"fast": {effort: "low"},
		},
		authCheck: []string{"doctor", "--json"}, // diagnoses install/config/auth/runtime
		authParse: parseCodexAuth,
		// promptFlag/timeoutFlag/sandboxFlag "" — codex takes the prompt positionally
		// (`codex exec [flags] <prompt>`), has no internal print-timeout (timeoutHeadroom
		// 0 — the ctx deadline IS the timeout), and exposes no boolean sandbox param
		// (mode toggles read-only sandbox vs. the bypass flag instead).
		description: codexToolDescription,
		modeDesc:    codexModeDescription,
		effortDesc:  codexEffortDescription,
	},
}

// Named references into the registry, for tests. Derived from backends so they
// can't drift from what the server actually registers.
var (
	antigravityBackend = backends[0]
	claudeBackend      = backends[1]
	codexBackend       = backends[2]
)

// parseHopEnv reads the current delegation depth and max from a getenv-style
// lookup function. Invalid, missing, or out-of-range values fall back to the
// defaults: a negative depth → defaultHopDepth, and a max < 1 (e.g. a
// fat-fingered AGENT_HOP_MAX=0, which would otherwise refuse every call) →
// defaultHopMax. Pure function — table-testable (pass a map-backed getenv).
func parseHopEnv(getenv func(string) string) (depth, hopMax int) {
	depth = defaultHopDepth
	if v, err := strconv.Atoi(strings.TrimSpace(getenv(hopDepthEnv))); err == nil && v >= 0 {
		depth = v
	}
	hopMax = defaultHopMax
	if v, err := strconv.Atoi(strings.TrimSpace(getenv(hopMaxEnv))); err == nil && v >= 1 {
		hopMax = v
	}
	return depth, hopMax
}

// hopLimitReached reports whether the current delegation depth has reached the
// configured maximum. Pure function — table-testable.
func hopLimitReached(depth, hopMax int) bool {
	return depth >= hopMax
}

// childHopEnv returns a copy of env (an os.Environ()-style slice) with any
// existing AGENT_HOP_DEPTH entry REMOVED and a single
// "AGENT_HOP_DEPTH=<depth+1>" appended, so the spawned child sees the
// incremented depth with no duplicate keys. Pure function — table-testable.
func childHopEnv(env []string, depth int) []string {
	prefix := hopDepthEnv + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, fmt.Sprintf("%s=%d", hopDepthEnv, depth+1))
	return out
}

// delegationDisabled reports whether this server was spawned by a reason-only
// parent that forbids any further delegation (AGENT_NO_DELEGATE=1). Only an
// exact "1" counts, so a stray empty/other value never silently blocks calls.
// Pure function — table-testable (pass a map-backed getenv).
func delegationDisabled(getenv func(string) string) bool {
	return strings.TrimSpace(getenv(noDelegateEnv)) == "1"
}

// childDelegationEnv returns a copy of env with any existing AGENT_NO_DELEGATE
// entry REMOVED, then appends "AGENT_NO_DELEGATE=1" iff the spawned child is
// NON-ACTING (reason or read mode — i.e. !allowTools). A non-acting child must not
// delegate further, so the flag rides its environment to any bridge server the
// child itself launches, where delegationDisabled() makes runAgent refuse. An
// acting child (mode act) gets no flag and may still delegate, bounded by the hop
// guard. Pure function — table-testable.
func childDelegationEnv(env []string, frozen bool) []string {
	prefix := noDelegateEnv + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	if frozen {
		out = append(out, noDelegateEnv+"=1")
	}
	return out
}

func main() {
	s := server.NewMCPServer(
		"agent-bridge-mcp",
		"0.1.0",
		server.WithToolCapabilities(false),
	)

	for _, b := range backends {
		s.AddTool(newTool(b), makeHandler(b))
	}
	s.AddTool(listAgentsTool(), listAgentsHandler)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "agent-bridge-mcp: server error: %v\n", err)
		os.Exit(1)
	}
}

// commonToolOptions returns the tool options shared by every tool: the given
// description plus the task/add_dirs/working_dir/timeout_seconds/model/mode params.
// Per-tool extras (e.g. antigravity's sandbox, the effort param) are appended by the
// caller. Defining the shared params once keeps the tool schemas from drifting.
func commonToolOptions(description, modeDescription string) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithDescription(description),
		mcp.WithString("task",
			mcp.Required(),
			mcp.Description("The complete, self-contained task/prompt for the agent to perform."),
		),
		mcp.WithArray("add_dirs",
			mcp.Description("Directories to add to the agent's workspace (absolute paths). Repeatable."),
			mcp.Items(map[string]any{"type": "string"}),
		),
		mcp.WithString("working_dir",
			mcp.Description("Directory the agent runs in (absolute path). Defaults to this server's own working "+
				"directory — NOT necessarily the repo you want reviewed/edited — so set it explicitly when the agent "+
				"must read or write a specific project (e.g. codex_agent's read-only mode resolves its file reads against this dir)."),
		),
		mcp.WithNumber("timeout_seconds",
			mcp.Description(fmt.Sprintf("Max seconds to wait for the agent (default %d, max %d).", defaultTimeoutSeconds, maxTimeoutSeconds)),
		),
		mcp.WithString("model",
			mcp.Description("Optional model (passed as --model); leave empty for the CLI/provider default (Codex maps that "+
				"to its recommended frontier model). Claude takes family aliases like `opus`/`sonnet`/`haiku` that always "+
				"resolve to the latest; agy/codex take explicit names (list them with `agy models`). For reasoning effort "+
				"see the `effort` param (claude_agent / codex_agent)."),
		),
		mcp.WithString("mode",
			mcp.Description(modeDescription),
		),
	}
}

// newTool builds the MCP tool for a backend: the shared params, plus the sandbox
// option for backends that support it.
func newTool(b backend) mcp.Tool {
	opts := commonToolOptions(b.description, b.modeDesc)
	if b.appliesEffort() {
		opts = append(opts, mcp.WithString("effort", mcp.Description(b.effortDesc)))
	}
	if len(b.tiers) > 0 {
		opts = append(opts, mcp.WithString("tier", mcp.Description(b.tierDesc())))
	}
	if b.supportsSandbox() {
		opts = append(opts, mcp.WithBoolean("sandbox", mcp.Description(sandboxDescription)))
	}
	return mcp.NewTool(b.tool, opts...)
}

// makeHandler returns the MCP handler for a backend. The `sandbox` param is read
// from the request only for backends that support it (b.supportsSandbox()).
func makeHandler(b backend) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Check context cancellation before executing.
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

		o := runOpts{
			task:           task,
			timeoutSeconds: timeoutSeconds,
			model:          strings.TrimSpace(req.GetString("model", "")),
			effort:         strings.TrimSpace(req.GetString("effort", "")),
			workingDir:     req.GetString("working_dir", ""),
		}

		// Resolve the access tier from the `mode` enum (reason | read | act); an
		// omitted/blank mode means reason. `read` is rejected for backends with no
		// read-only tier (antigravity). Validated BEFORE the tier block below so an
		// invalid request (e.g. agy + read) is rejected up front, without first shelling
		// out to `<cli> models` for tier model discovery.
		mode := strings.ToLower(strings.TrimSpace(req.GetString("mode", "")))
		if mode == "" {
			mode = modeReason
		}
		switch mode {
		case modeReason:
			// defaults: allowTools=false, readOnly=false
		case modeRead:
			if !b.supportsReadOnly() {
				return mcp.NewToolResultError(fmt.Sprintf(
					"%s: no read-only mode — use mode \"reason\" (no unattended edits/commands) or \"act\" (full access). "+
						"Only claude_agent and codex_agent have a read-only tier.", b.tool)), nil
			}
			o.readOnly = true
		case modeAct:
			o.allowTools = true
		default:
			return mcp.NewToolResultError(fmt.Sprintf(
				"%s: invalid mode %q — valid modes are reason | read | act.", b.tool, mode)), nil
		}

		// Resolve an optional reviewer tier (deep/fast) → model+effort. Explicit
		// model/effort params override per-field; for agy the tier's model name is
		// discovered from `agy models` at runtime. Backends with no presets reject it.
		if tier := strings.ToLower(strings.TrimSpace(req.GetString("tier", ""))); tier != "" {
			if len(b.tiers) == 0 {
				return mcp.NewToolResultError(fmt.Sprintf(
					"%s: no tier presets — set `model`/`effort` explicitly instead.", b.tool)), nil
			}
			// Enforce the delegation freeze / hop limit BEFORE resolving the tier: agy's
			// tier model discovery shells out to `agy models`, and a child that will be
			// refused at spawn time must not run any subprocess first. (mode is already
			// validated above, so an invalid mode never reaches this discovery either.)
			if res := delegationGuard(b.tool); res != nil {
				return res, nil
			}
			discover := func(match []string) string { return b.discoverModel(ctx, match) }
			model, effort, terr := resolveTier(b, tier, o.model, o.effort, discover)
			if terr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("%s: %v", b.tool, terr)), nil
			}
			o.model, o.effort, o.tier = model, effort, tier
		}

		// sandbox defaults OFF and is antigravity-only. --sandbox applies agy's
		// "terminal restrictions" but does NOT confine file edits (a write under it
		// still lands in working_dir, verified), so it is not a write guard — to keep
		// agy off your files use a throwaway working_dir. claude has no sandbox
		// concept, so the param is not read.
		if b.supportsSandbox() {
			o.sandbox = req.GetBool("sandbox", false)
		}

		for _, d := range req.GetStringSlice("add_dirs", nil) {
			if s := strings.TrimSpace(d); s != "" {
				o.addDirs = append(o.addDirs, s)
			}
		}

		return runAgent(ctx, b, o)
	}
}

// delegationGuard returns a non-nil error result when this process must refuse to spawn
// a child: a reason-only parent froze it (AGENT_NO_DELEGATE=1), or it has reached the
// hop-depth limit (AGENT_HOP_DEPTH >= AGENT_HOP_MAX). makeHandler consults it BEFORE
// resolving a tier — so a child that will be refused never shells out to `<cli> models`
// for tier model discovery first — and runAgent re-checks it as the authoritative gate.
func delegationGuard(tool string) *mcp.CallToolResult {
	// Reason-only freeze: stricter than (and independent of) the hop-depth counter.
	if delegationDisabled(os.Getenv) {
		return mcp.NewToolResultError(fmt.Sprintf(
			"%s: delegation disabled (%s=1). This agent was spawned reason-only by a parent "+
				"agent and may not spawn further agents. Perform this task directly instead of delegating.",
			tool, noDelegateEnv,
		))
	}
	// Loop guard: refuse once the delegation depth limit is reached (runaway A→B→A→B).
	if depth, hopMax := parseHopEnv(os.Getenv); hopLimitReached(depth, hopMax) {
		return mcp.NewToolResultError(fmt.Sprintf(
			"%s: delegation-depth limit reached (%s=%d, %s=%d). "+
				"Refusing to spawn another agent to avoid a runaway delegation loop. "+
				"Perform this task directly instead of delegating further.",
			tool, hopDepthEnv, depth, hopMaxEnv, hopMax,
		))
	}
	return nil
}

// runAgent is the shared backend run path: hop guard, command construction,
// timeout/context handling, truncation, and header formatting. Tool-level
// failures (timeout, child error, hop limit) are encoded as MCP error results
// with a nil Go error; only parent-context cancellation returns a Go error,
// mirroring the original antigravity_agent behavior.
func runAgent(ctx context.Context, b backend, o runOpts) (*mcp.CallToolResult, error) {
	// Refuse to spawn if frozen reason-only or at the hop-depth limit. makeHandler also
	// checks this before tier resolution; runAgent re-checks for direct callers (e.g.
	// checkServe) and is the authoritative gate.
	if res := delegationGuard(b.tool); res != nil {
		return res, nil
	}
	depth, _ := parseHopEnv(os.Getenv)

	args := b.buildArgs(o)
	modeNoteStr := b.modeNote(o)

	// Give backends with their own internal timeout (antigravity/agy) a little headroom
	// beyond the requested timeout so they surface their own timeout message rather
	// than us killing them first. claude has no internal timeout (headroom 0), so
	// the context deadline IS the timeout. Guard against a negative timeout (a
	// direct runAgent caller bypassing makeHandler's clamp) collapsing the deadline.
	effectiveTimeout := o.timeoutSeconds
	if effectiveTimeout < 0 {
		effectiveTimeout = 0
	}
	hardDeadline := time.Duration(effectiveTimeout)*time.Second + b.timeoutHeadroom
	runCtx, cancel := context.WithTimeout(ctx, hardDeadline)
	defer cancel()

	cmd := exec.CommandContext(runCtx, b.resolveBin(), args...)
	if strings.TrimSpace(o.workingDir) != "" {
		cmd.Dir = o.workingDir
	}
	// Spawn the child with an incremented delegation depth (no duplicate keys), and
	// — when this run is NON-ACTING (reason or read mode) — with AGENT_NO_DELEGATE=1
	// so the child cannot re-enter the bridge to spawn a grandchild.
	cmd.Env = childDelegationEnv(childHopEnv(os.Environ(), depth), !o.allowTools)

	// Kill the whole process group on cancel/timeout (so grandchildren the child
	// spawned die too) and bound how long Run may block on I/O afterward. Without
	// this, a surviving grandchild that inherited the stdout/stderr pipes keeps
	// them open and cmd.Run() hangs past the deadline, leaking the goroutine/fds.
	cmd.WaitDelay = childWaitDelay

	// A pty-required backend (agy) hangs forever on plain pipes, so on a build with no
	// pty support refuse up front instead of falling through to the pipe path and
	// burning the entire timeout on a guaranteed hang.
	if b.needsPTY && !ptySupported {
		return mcp.NewToolResultError(fmt.Sprintf(
			"%s requires a pseudo-terminal, which this build does not support (GOOS=%s); refusing rather than hanging on plain pipes.",
			b.tool, runtime.GOOS,
		)), nil
	}

	var stdout, stderr bytes.Buffer
	start := time.Now()
	var runErr error
	if b.needsPTY && ptySupported {
		// Run on a pseudo-terminal: agy's agentic --print hangs on plain pipes.
		// runOnPTY installs its own session-based process-group kill (pty.Start adds
		// Setsid, so we must NOT also Setpgid — setpgid on a session leader is EPERM)
		// and returns combined stdout+stderr, which we de-TTY (strip ANSI/CR) into stdout.
		var out []byte
		out, runErr = runOnPTY(cmd)
		stdout.WriteString(cleanPTYOutput(out))
	} else {
		setupProcessGroup(cmd)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr = cmd.Run()
	}
	elapsed := time.Since(start).Round(time.Millisecond)

	// If the parent context was canceled, return the cancellation error
	// (mirrors the original antigravity_agent behavior: a Go error, not a result).
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if runCtx.Err() == context.DeadlineExceeded {
		return mcp.NewToolResultError(fmt.Sprintf(
			"%s timed out after %s (%s).\nPartial stdout:\n%s\nstderr:\n%s",
			b.tool, elapsed, modeNoteStr, b.failureStdout(stdout.String(), 8000), truncateTail(stderr.String(), 2000),
		)), nil
	}

	if runErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"%s failed (%s): %v\nstderr:\n%s\nstdout:\n%s",
			b.tool, modeNoteStr, runErr, truncateTail(stderr.String(), 4000), b.failureStdout(stdout.String(), 8000),
		)), nil
	}

	out := strings.TrimRight(stdout.String(), "\n")
	if strings.TrimSpace(out) == "" {
		out = fmt.Sprintf("(%s returned no stdout)", b.cliName)
		if se := strings.TrimSpace(stderr.String()); se != "" {
			out += "\nstderr:\n" + truncateTail(se, 2000)
		}
	}

	header := fmt.Sprintf("[%s | %s | %s | %s]\n\n", b.tool, modeNoteStr, b.selectionNote(o), elapsed)
	return mcp.NewToolResultText(header + out), nil
}

// ansiEscapeRE matches the terminal control sequences a CLI emits when it thinks
// it is driving a TTY: CSI sequences (colors, cursor moves, line clears — the
// parameter class is the full ECMA-48 range 0x30-0x3F, so colon-delimited truecolor
// SGR like ESC[38:2:r:g:bm is covered, not just the semicolon form), OSC sequences
// (window-title sets, terminated by either BEL or ST = ESC \), and the remaining
// two-byte escapes. A pty-run backend's output is laced with these; we strip them so
// the result the model receives is plain text (e.g. parseable JSON), matching what
// the pipe-run backends already return.
var ansiEscapeRE = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-_]")

// cleanPTYOutput turns raw pseudo-terminal output into plain text: it strips ANSI
// escape sequences and normalizes the TTY's CRLF / bare-CR line endings to LF.
func cleanPTYOutput(b []byte) string {
	s := ansiEscapeRE.ReplaceAllString(string(b), "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// failureStdout renders captured stdout for an error/timeout result. For a pty-run
// backend stdout is the MERGED stdout+stderr stream and the real error (a CLI's crash
// message, a timeout notice) lands at the TAIL, so keep the tail. Pipe backends carry
// the error in their separate (already tail-truncated) stderr and stdout holds normal
// output, so the HEAD is the useful part there.
func (b backend) failureStdout(s string, limit int) string {
	if b.needsPTY {
		return truncateTail(s, limit)
	}
	return truncate(s, limit)
}

// truncate returns a copy of s truncated to at most limit bytes, without
// splitting UTF-8 runes. A negative limit is treated as 0 (no content kept),
// guarding against an out-of-range slice panic.
func truncate(s string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if len(s) <= limit {
		return s
	}
	// Back up to a valid UTF-8 rune boundary.
	// Continuation bytes start with the bits 10xxxxxx, i.e., byte & 0xC0 == 0x80.
	i := limit
	for i > 0 && (s[i]&0xC0 == 0x80) {
		i--
	}
	return s[:i] + fmt.Sprintf("\n…(truncated, %d bytes total)", len(s))
}

// truncateTail returns a copy of s truncated to at most limit bytes by keeping the
// END of the string (without splitting UTF-8 runes). Use it for child stderr: a
// CLI's real error lands at the TAIL — e.g. codex echoes the whole prompt to stderr
// first and prints the actual error (a usage limit, an auth failure) last — so
// head-truncation would discard exactly the line you need. Negative limit → 0.
func truncateTail(s string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if len(s) <= limit {
		return s
	}
	// Keep the last `limit` bytes, advancing to a valid UTF-8 rune boundary so the
	// kept slice never starts mid-rune (continuation bytes are 10xxxxxx).
	start := len(s) - limit
	for start < len(s) && (s[start]&0xC0 == 0x80) {
		start++
	}
	return fmt.Sprintf("…(truncated, %d bytes total)\n", len(s)) + s[start:]
}
