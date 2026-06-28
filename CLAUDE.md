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
**`parallel_agents`** fan-out tool (`parallelAgentsHandler`): one call runs a list of
`jobs` as concurrent goroutines and returns them in order — the host-agnostic way to get
real parallelism, since MCP host clients (Claude Code/Codex/agy, all verified) dispatch
individual tool calls **serially**. It shares per-job validation with `makeHandler` via
`buildRunOpts` (single source of truth for mode/tier/sandbox) and reuses `runAgent`'s
hop-guard + no-delegate freeze. The server also exposes a
**`tier`** param (`deep`/`fast`) that resolves model+effort per backend as registry data
(`tiers`/`tierSpec`) — for agy the model is discovered from `agy models` at runtime
(`discoverModel`, process-cached, run **under the pty** since agy lists nothing on a pipe),
claude/codex carry explicit presets. The pure
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

**`agy` (antigravity) must run under a pseudo-terminal — on every OS.** agy's agentic
`--print` loop only runs to completion with a controlling TTY (and `agy models` prints
**nothing** without one); spawned with plain pipes it **hangs** until the hard kill, burning
the whole timeout. The bridge runs the agy backend under a pty via `needsPTY` + `runOnPTY`,
which is platform-split: a unix pty ([proc_pty_unix.go](cmd/agent-bridge-mcp/proc_pty_unix.go),
`creack/pty`) and a **Windows ConPTY** ([proc_pty_windows.go](cmd/agent-bridge-mcp/proc_pty_windows.go),
`UserExistsError/conpty`). `claude`/`codex` are built for headless `--print`/`exec` and use
plain pipes — do **not** add a pty for them. Only a platform with NEITHER (the
[proc_pty_other.go](cmd/agent-bridge-mcp/proc_pty_other.go) `!unix && !windows` fallback,
`ptySupported=false`) **refuses** a `needsPTY` backend up front instead of hanging — keep that
guard. When touching the unix `runOnPTY`: keep the goroutine drain + grace-period force-close
of the master (a synchronous `io.Copy` before `Wait` deadlocks if a grandchild escapes the
process-group kill and holds the slave open). For the Windows path: ConPTY is driven by a
command LINE (not `exec.Cmd`), so `runOnPTY` takes the **ctx** explicitly (the deadline isn't
wired via `cmd.Cancel`); `conpty.Close` both kills the attached process (and console-sharing
grandchildren) and unblocks the drain; ConPTY never EOFs the reader until Close, so a clean
exit uses a short `ptyExitDrain` settle, not `childWaitDelay`. Keep `cleanPTYOutput`: it
strips ANSI/OSC, drops CR, AND collapses full-screen repaints by keeping only what follows the
last clear/home (`screenResetRE`) — agy animates a cursor-home spinner, which would otherwise
glue onto the first real line and corrupt model matching. `discoverModel` runs `agy models`
under the same pty for the same reason. Under a pty stdout+stderr **merge**, so error/timeout
paths tail-truncate the merged stream for pty backends (`backend.failureStdout`).

**claude/codex take the prompt on STDIN; agy takes it on the command line.** claude/codex
set `promptStdin` — `buildArgs` omits the task and `runAgent` wires `cmd.Stdin` (`claude
--print` / `codex exec` read the prompt from stdin when no prompt arg is given). This dodges
two Windows traps a prompt hits via argv: CreateProcess caps the command line near 32 KB, and
a `.cmd`/`.bat` shim (codex installs as `codex.cmd`) runs through cmd.exe, which mangles
prompt metacharacters like `|`. agy can't use stdin (under its pty, stdin IS the terminal),
so it stays on argv — and `runOnPTY` (Windows) bounds the **actual EscapeArg-expanded command
line** (`maxWindowsCmdline`, measured in UTF-16 units, not raw task bytes) so a too-large agy
task fails with a clear message, not a cryptic `CreateProcess` error. Don't put the task back
on agy-style argv for a `promptStdin` backend, and keep `buildArgs` omitting the prompt token
when `promptStdin` is set.

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
  `/agent-bridge-mcp` (plus the Windows `/agent-bridge-mcp.exe`) — **anchored** with a leading
  slash on purpose, so the pattern does not also match the `cmd/agent-bridge-mcp/` source dir
  and silently ignore new `.go` files there.
- **Windows needs the `.exe` suffix.** MCP hosts spawn the server via Node/`CreateProcess`,
  which (unlike Git Bash, which sniffs the PE header) refuses to exec an extensionless binary
  and dies with `ENOENT` — the server then shows as connected-but-toolless / failed. So the
  [Makefile](Makefile) splits `NAME` (base name: source dir + the **extensionless** command
  token in the committed `.mcp.json`) from `BINARY := $(NAME)$(shell go env GOEXE)` (the built
  file, `.exe` on Windows). The manifests stay extensionless on purpose — Unix matches the file
  directly, Windows resolves the token to the `.exe` — so only the built file's name varies by
  OS. Don't hardcode `.exe` into a manifest, and don't fold `BINARY` back into `CMD`/`NAME`.
- `cmd/agent-bridge-mcp/proc_*.go` are platform-split via build tags — **three ways** for the
  pty path: `unix` (`proc_pty_unix.go`), `windows` (`proc_pty_windows.go`, ConPTY), and the
  `!unix && !windows` fallback (`proc_pty_other.go`, `ptySupported=false`). The non-pty
  `proc_*.go` (process-group helpers) stay a `unix` / `!unix` split. Add new OS-specific
  helpers the same way and keep a fallback so the package builds everywhere (verified by
  cross-compiling tests for linux/darwin and `go build` for plan9).
- **Tests must run on Windows too** (`go test ./...` is green there). The spawn tests therefore
  use a CROSS-PLATFORM fake CLI, not `#!/bin/sh` scripts (Windows can't exec those): a `TestMain`
  in [fakecli_test.go](cmd/agent-bridge-mcp/fakecli_test.go) re-execs the test binary as the fake
  when `AGENT_BRIDGE_FAKECLI` is set — drive it via `fakeBin(t, fakeOpts{…})`, never a shell
  script. Other Windows gotchas baked into the tests: `exec.LookPath` only matches PATHEXT names
  (suffix on-PATH fixtures with `exeSuffix()`); `os.UserHomeDir()` reads `%USERPROFILE%` not
  `$HOME` (use `setHomeDir`); paths compare case-insensitively (`samePath`). agy is `needsPTY`,
  so on Windows its spawn-behavior tests run the fake CLI **through ConPTY** (cleaned by
  `cleanPTYOutput`); `ptyRefusedResult` only fires on the rare build with no pty at all
  (`!unix && !windows`). Arg-construction is checked purely (no spawn) over `buildArgs`, so it
  covers agy on every OS regardless of the pty.
