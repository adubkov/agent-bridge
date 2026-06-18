# CLAUDE.md — working notes for agents in this repo

`agent-bridge` is an MCP server (`cmd/agent-bridge-mcp`) that exposes other coding-agent
CLIs as spawnable sub-agent tools: `antigravity_agent` (Antigravity/`agy`, Gemini),
`claude_agent` (`claude`), `codex_agent` (`codex`). Backends are declared in one in-code
registry (`backends` in [main.go](cmd/agent-bridge-mcp/main.go)); the tool descriptions
there are the **source of truth** for per-backend behavior. See [README.md](README.md) for
the user-facing docs and [skills/](skills/) for the playbooks.

The server also registers a **`list_agents`** discovery tool (takes no `task` — it
never spawns a worker; but probe depth scales cost: `installed` no-spawn,
`auth`/`serve` shell out; see [discover.go](cmd/agent-bridge-mcp/discover.go)) and a
**`tier`** param (`deep`/`fast`) that resolves model+effort per backend as registry data
(`tiers`/`tierSpec`) — for agy the model is discovered from `agy models` at runtime
(`discoverModel`, process-cached), claude/codex carry explicit presets. The pure
`resolveTier`/`pickModel` keep `buildArgs` subprocess-free; auth uses per-CLI parsers
(`parseClaudeAuth`/`parseCodexAuth`, since exit codes alone are unreliable).

## Keep this file in sync (rule)

**This file is an agent's first source of truth here — keep it current in the SAME change
that makes it stale, never as a follow-up.** When a change touches any of:

- the backend registry or tool/param behavior — `backends`, `tierSpec`/`tiers`, `authCheck`/
  `authParse`, a new/removed tool, or any tool / `mode` / `effort` / `tier` description — in
  [main.go](cmd/agent-bridge-mcp/main.go) / [discover.go](cmd/agent-bridge-mcp/discover.go),
- the load-bearing backend gotchas below (pty handling, agy sandbox/worktree, latency),
- the build / test / install / smoke targets in the [Makefile](Makefile), or
- the repo conventions (gitignore anchoring, the `proc_*.go` build-tag split),

reconcile the matching section here in the same commit. The MCP tool descriptions in
`main.go` stay the **source of truth** for per-backend behavior; this file summarizes and
points at them, so update the summary whenever they move. If a change leaves nothing here
stale, no edit is needed — don't pad it.

## Build & test

```sh
make build              # compile ./agent-bridge-mcp (the canonical binary)
go test ./...           # unit tests (table-driven; no network/CLIs needed)
make smoke-antigravity  # live PONG round-trip through agy (needs agy authed)
make smoke-list-agents  # list_agents discovery smoke (probe=installed; needs no CLIs authed)
make install-claude     # install the plugin into Claude Code (frozen cache copy)
make install-all        # install into every host whose CLI is on PATH (claude/agy/codex)
```

Installs are **frozen snapshots** — `make install-*` copies the freshly built binary into
each host's own plugin dir, so editing this checkout never changes an already-installed
agent. Re-run an `install-*` target to push a new build.

## Backend gotchas (load-bearing — verified the hard way)

**`agy` (antigravity) must run under a pseudo-terminal.** agy's agentic `--print` loop only
runs to completion with a controlling TTY; spawned with plain pipes it **hangs** until the
hard kill, burning the whole timeout. The bridge therefore runs the agy backend under a pty
(`needsPTY` + `runOnPTY` in [proc_pty_unix.go](cmd/agent-bridge-mcp/proc_pty_unix.go)).
`claude`/`codex` are built for headless `--print`/`exec` and use plain pipes — do **not** add
a pty for them. On a build without pty support (non-unix), the dispatch **refuses** a
`needsPTY` backend up front instead of falling through to pipes and hanging — keep that
guard. When touching `runOnPTY`: keep the goroutine drain + grace-period
force-close of the master (a synchronous `io.Copy` before `Wait` deadlocks if a grandchild
escapes the process-group kill and holds the slave open), and keep `cleanPTYOutput` (strip
ANSI/CR so results stay parseable). Under a pty stdout+stderr **merge**, so error/timeout
paths tail-truncate the merged stream for pty backends (`backend.failureStdout`).

**`agy --sandbox` is NOT a write guard.** It enables agy's *terminal* restrictions only; a
file write under `sandbox: true` still lands in `working_dir` (verified). agy also has **no
write-safe tier** (unlike `claude --tools ""` / `codex --sandbox read-only`) and routinely
saves a scratch `git diff` dump into `working_dir` while reviewing. To keep agy off your
tree, point `working_dir` at a **throwaway `git worktree`** — not `--sandbox`, and not by
omitting `working_dir` (that runs agy in the bridge server's own cwd, often your project tree).

**agy latency is highly variable** (~75–270s observed for the same Pro-tier repo review), so
give agy generous `timeout_seconds` (≥300) rather than reading a slow run as a hang.

**Don't `git add -A` blind.** Sub-agents pointed at the repo write scratch `git diff` dumps
into the working tree (e.g. `diff_pr8.txt`, `diff.txt`); `.gitignore` covers the common
patterns, but stage explicit paths so debris can't slip into a commit.

## Repo conventions

- The built binary `agent-bridge-mcp` lives at the **repo root** and is gitignored as
  `/agent-bridge-mcp` — **anchored** with a leading slash on purpose, so the pattern does not
  also match the `cmd/agent-bridge-mcp/` source dir and silently ignore new `.go` files
  there.
- `cmd/agent-bridge-mcp/proc_*.go` are platform-split via build tags (`unix` vs `!unix`);
  add new OS-specific helpers the same way and keep a non-unix fallback so the package builds
  everywhere.
