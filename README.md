# agent-bridge

> A bridge between coding agents — expose each agent's CLI as a spawnable
> sub-agent tool. Today it ships an MCP server (the `agent-bridge-mcp` binary,
> under [`cmd/agent-bridge-mcp`](cmd/agent-bridge-mcp)) with bidirectional
> **Claude ↔ Gemini** delegation plus an OpenAI **Codex** sub-agent.
> _(Formerly `agy-mcp` / `agy-gemini`.)_

A tiny [MCP](https://modelcontextprotocol.io) server that bridges coding agents,
exposing each as a spawnable sub-agent tool. One binary registers three tools:

- **`gemini_agent`** — shells out to the Antigravity CLI (`agy --print <task>`),
  i.e. spawns a **Gemini** sub-agent. Intended to be called from a Claude session.
- **`claude_agent`** — shells out to the Claude CLI (`claude --print <task>`),
  i.e. spawns a **Claude** sub-agent. Intended to be called from a Gemini session.
- **`codex_agent`** — shells out to the OpenAI Codex CLI (`codex exec <task>`),
  i.e. spawns a **Codex** sub-agent. Callable from any parent session.

A parent agent calls a tool with a self-contained task; this server shells out to
the corresponding CLI, lets the child agent perform it, and returns the child's
full output. In effect each tool is a **spawned sub-agent callable from inside
another agent's session**.

All three tools share one backend adapter, so the run / timeout / truncation /
result header / context-cancel / loop-guard behavior is identical; they differ
only in which CLI they invoke and which CLI-specific flags they support. Adding a
CLI agent is a single entry in the in-code backend registry, not new code.

## Tool: `gemini_agent`

Spawns a Gemini agent via the Antigravity `agy` CLI.

| Param | Type | Default | Notes |
|---|---|---|---|
| `task` | string (required) | — | The complete, self-contained task/prompt for Gemini. |
| `add_dirs` | string[] | — | Directories to add to the agent's workspace (absolute paths). Repeated as `--add-dir`. |
| `working_dir` | string | server cwd | Directory the agent runs in (sets `cmd.Dir`). |
| `timeout_seconds` | number | 300 (max 1800) | Maps to `agy --print-timeout`. |
| `model` | string | CLI default | Optional; `--model <model>` when non-empty. agy has **no family alias** and bakes effort into the model *name* (e.g. `Gemini 3.1 Pro (High)`) — list current names with `agy models`. No separate `effort` param. |
| `mode` | string | `reason` | Access tier: `reason` (no tools) · `act` (edit files in `working_dir` + run commands via `--dangerously-skip-permissions`, unattended). **No `read` tier** for gemini. |
| `sandbox` | bool | **false** | Confine the agent to an isolated scratch dir (`--sandbox`). **Warning:** when true, its edits go to the scratch dir, NOT `working_dir`. Leave off for real edits. **Gemini-only** — `claude_agent` has no `sandbox` param. |

## Tool: `claude_agent`

Spawns a **Claude** agent via the `claude` CLI. This is the reverse direction:
intended to be called *from a Gemini session* so Gemini can delegate to Claude.
It mirrors `gemini_agent`'s semantics. **Note:** every run shells out to the
`claude` CLI and therefore **consumes Claude credits** — even reason-only runs.

| Param | Type | Default | Notes |
|---|---|---|---|
| `task` | string (required) | — | The complete, self-contained task/prompt for Claude. Passed as the value of `--print` (`claude --print <task>`). |
| `add_dirs` | string[] | — | Directories to add to the agent's workspace (absolute paths). Repeated as `--add-dir`. |
| `working_dir` | string | server cwd | Directory the agent runs in (sets `cmd.Dir`). |
| `timeout_seconds` | number | 300 (max 1800) | The `claude` CLI has **no** `--print-timeout`; the timeout is enforced purely by the process context deadline (no timeout flag is passed to `claude`). |
| `model` | string | CLI default | Optional; `--model <model>` when non-empty. Accepts **family aliases** `opus`/`sonnet`/`haiku` (always resolve to the latest) or a full model name. |
| `effort` | string | model default | Optional reasoning effort; `--effort <level>` when non-empty. Accepts `low\|medium\|high\|xhigh\|max`. |
| `mode` | string | `reason` | Access tier: `reason` (no tools) · `read` (read-only exploration — read/grep files, no edits/exec, via `--permission-mode plan`) · `act` (full edit/run via `--dangerously-skip-permissions`, unattended — **consumes Claude credits**). |

There is **no `sandbox` param** on `claude_agent` — sandboxing is Gemini-only and
`--sandbox` is never passed to `claude`.

## Tool: `codex_agent`

Spawns an **OpenAI Codex** agent via the `codex` CLI (`codex exec`). Codex's
permission model differs from the others: it has **no pure "no tools" mode** — it
always runs as an autonomous agent — so `mode` toggles between a *read-only
sandbox* (`reason`/`read`) and *full, unsandboxed access* (`act`) rather than off/on.

| Param | Type | Default | Notes |
|---|---|---|---|
| `task` | string (required) | — | The complete, self-contained task/prompt for Codex. Passed as the trailing positional argument to `codex exec`. |
| `add_dirs` | string[] | — | Additional writable directories (absolute paths). Repeated as `--add-dir`. |
| `working_dir` | string | server cwd | Directory the agent runs in (sets `cmd.Dir`). |
| `timeout_seconds` | number | 300 (max 1800) | Codex `exec` has **no** internal timeout flag; the timeout is enforced purely by the process context deadline. |
| `model` | string | provider default | Optional; `--model <model>` when non-empty. **Omit** to use Codex's recommended *frontier* model (most-capable, auto-current). |
| `effort` | string | model default | Optional reasoning effort; passed as `-c model_reasoning_effort=<level>` when non-empty (e.g. `minimal\|low\|medium\|high`). |
| `mode` | string | `reason` | Access tier: `reason`/`read` are **both** read-only (`--sandbox read-only`) — Codex reads/reasons but cannot edit or run effectful commands; `act` → `--dangerously-bypass-approvals-and-sandbox`: fully **unattended, unsandboxed** file/command access. |

There is **no `sandbox` param** on `codex_agent` — `mode` already selects
read-only vs. full access. Codex always runs with `--skip-git-repo-check` (so it
works outside a Git repo). On success the tool returns Codex's final message;
Codex's session banner and step-by-step transcript go to **stderr** and are
surfaced only if the run fails or times out (so a successful result is as clean as
the other backends, not noisier).

### Safety model (all tools)

By default (`mode: "reason"`) the spawned agent is **reason/answer only** — it runs
`--print` with no permission bypass, so it can analyze, draft, and answer but cannot
take unattended actions. `claude_agent` also offers `mode: "read"` — read-only
exploration (read/grep files via `--permission-mode plan`, no edits or commands). To
let an agent actually act on your files/system, set `mode: "act"`, which passes
`--dangerously-skip-permissions` to the underlying CLI (the child's approval gates are
off — this is unattended execution). Scope it with `working_dir`; the agent's edits
land there.

For `gemini_agent`, `--sandbox` is **off by default**: with it on, `agy` confines
the agent to an isolated scratch dir, so edits would *not* reach `working_dir`.
Set `sandbox: true` only for a confined "compute but don't touch my files" run.
`claude_agent` has no sandbox concept.

**`codex_agent` differs:** Codex has no pure no-tools mode, so `mode: "reason"` and
`mode: "read"` both run it in a **read-only sandbox** (`--sandbox read-only`) — it can
read and reason but not write — and `mode: "act"` passes
`--dangerously-bypass-approvals-and-sandbox` (full access, no sandbox). Its
result-header mode note reflects this (`tool-use: read-only (--sandbox read-only)`).

The tool result header always reports which tool ran, the mode, the model/effort
requested, and the elapsed time: `[<tool> | <modeNote> | <model/effort> | <elapsed>]`.

### Loop guard (`AGENT_HOP_DEPTH` / `AGENT_HOP_MAX`)

Because these tools can call each other (Claude → Gemini → Claude → …), the
shared run path enforces a delegation-depth limit to prevent runaway A→B→A→B
chains. It reads two environment variables:

| Env var | Default | Meaning |
|---|---|---|
| `AGENT_HOP_DEPTH` | `0` | Current delegation depth. |
| `AGENT_HOP_MAX` | `2` | Maximum allowed depth. |

On each call:

- If the current depth has **reached the max** (`depth >= max`), the tool returns
  an **MCP error result** explaining the delegation-depth limit was reached and
  does **not** spawn a child. The parent agent should perform the task itself.
- Otherwise the child is spawned with `AGENT_HOP_DEPTH` set to `depth + 1` (the
  server rebuilds the child's environment from its own, removing any existing
  `AGENT_HOP_DEPTH` entry so there are no duplicate keys).

Set `AGENT_HOP_MAX` in the MCP server's environment to allow deeper (or shallower)
delegation chains. Invalid/missing values fall back to the defaults above.

In addition, every **non-acting** child (`mode: "reason"` or `"read"`) is spawned with
`AGENT_NO_DELEGATE=1`, and the run path refuses to spawn from any process carrying that
flag. This is a hard "no further delegation" stop, independent of the depth counter: a
non-acting agent (which should only reason/read, not act) cannot re-enter the bridge to
spawn a child — including `codex_agent`'s read-only sandbox, which can still run
read-only commands. Acting children (`mode: "act"`) do not carry the flag and may
delegate, bounded by the hop guard above.

## Build

```sh
go build -o agent-bridge-mcp ./cmd/agent-bridge-mcp          # local binary
# or
go install github.com/adubkov/agent-bridge/cmd/agent-bridge-mcp@latest
```

Each tool requires its CLI:

- `gemini_agent` needs `agy` on `PATH` (or set `AGY_BIN=/path/to/agy`); the server
  falls back to `~/.local/bin/agy`, then `agy`.
- `claude_agent` needs `claude` on `PATH` (or set `CLAUDE_BIN=/path/to/claude`);
  the server falls back to `~/.local/bin/claude`, then `claude`.
- `codex_agent` needs `codex` on `PATH` (or set `CODEX_BIN=/path/to/codex`); the
  server falls back to `~/.local/bin/codex`, then `codex`.

You only need the CLI for the tool you actually call.

## Install into Claude Code

Use this when the **parent** is Claude Code (so Claude can delegate to Gemini via
`gemini_agent`). **Requires `agy` authenticated** (`agy` login once) and on `PATH`
(or set `AGY_BIN`; the server also falls back to `~/.local/bin/agy`). Restart Claude
Code afterward (MCP loads at session start); run `/mcp` and `/plugin` to confirm. The
tools appear as `gemini_agent`, `claude_agent`, and `codex_agent`.

This repo is a Claude Code **plugin** (`agent-bridge`): installing it wires the MCP
server *and* ships the skills (`skills/agent-bridge` for delegating to Gemini, and
`skills/multi-model-review` for cross-model reviews). Claude discovers plugins through
**marketplaces**, so the repo carries a single-plugin local marketplace
(`.claude-plugin/marketplace.json`); `make install-claude` registers it and installs
the plugin:

```sh
make install-claude     # build + marketplace add (this repo) + plugin install
# then restart Claude Code; run /plugin and /mcp to confirm
# remove later with:
make uninstall-claude
```

Equivalent manual commands:

```sh
claude plugin marketplace add "$(pwd)"
claude plugin install agent-bridge@agent-bridge-local
```

`claude plugin install` copies the plugin — **binary included** — into a versioned
cache (`~/.claude/plugins/cache/.../agent-bridge-mcp`), referenced via
`${CLAUDE_PLUGIN_ROOT}`. So the install is a **frozen snapshot**: rebuilding this
checkout doesn't change an already-installed agent — re-run `make install-claude` to
push a new build.

> The marketplace records this repo's **absolute path** in your user settings, so this
> is a local-dev install tied to your checkout location. To share it, point a
> marketplace at the GitHub repo instead of the local path.

**Just the tools, no skills?** Register the MCP server by hand — this also lets you pick
a non-user scope (e.g. project-local):

```sh
claude mcp add agent-bridge --scope user -- "$(pwd)/agent-bridge-mcp"
```

This references the path you give (not a frozen copy), so rebuilding the checkout
changes what the agent runs — fine for active development, not for a stable install.

## Install into Antigravity (agy)

Use this when the **parent** is Antigravity/Gemini (so Gemini can delegate to Claude
via `claude_agent`). The Antigravity CLI manages plugins with `agy plugin` (run
`agy plugin help` for the subcommands). Because this repo is a Claude-format plugin
(`.claude-plugin/plugin.json`), `agy plugin install <plugin-dir>` reads the manifest
and imports its skills + MCP server. **Requires `claude` authenticated** and on `PATH`
(or set `CLAUDE_BIN`; the server also falls back to `~/.local/bin/claude`).

```sh
make install-agy        # build + agy plugin install + copy frozen binary + repoint
agy plugin list         # confirm it's imported (source: claude-code)
# remove later with:
make uninstall-agy
```

Use the make target, not a bare `agy plugin install "$(pwd)"`: `agy` imports the
manifests but **not** the binary, and has no `${CLAUDE_PLUGIN_ROOT}` support, so the
imported `mcp_config.json` would point at an unexpanded
`${CLAUDE_PLUGIN_ROOT}/agent-bridge-mcp` and fail to launch. `make install-agy` copies a
**frozen** binary into agy's own plugin dir (`~/.gemini/config/plugins/agent-bridge/`) and
repoints `mcp_config.json` at it — so the install is self-contained and doesn't track your
checkout (re-run to update). `gemini_agent`, `claude_agent`, and `codex_agent` all become
available inside agy; from a Gemini session you'll typically call `claude_agent`.

> **Alternatives (documented agy subcommands):**
> - `agy plugin import [gemini|claude]` imports plugins *already installed* in the Gemini
>   CLI or Claude Code into agy — e.g. after `make install-claude`, `agy plugin import
>   claude` pulls it in. With nothing installed it prints `No claude extensions found.`
> - `agy plugin install <plugin@marketplace>` is supported, but it resolves from **agy's**
>   registered marketplaces — and there is **no** `agy plugin marketplace add` to register a
>   local one, so use the plugin-dir path (`make install-agy`), not `agent-bridge@agent-bridge-local`.
>
> If your `agy` version behaves differently, run `agy plugin help`.

The Claude-format plugin bundles:

- `.claude-plugin/plugin.json` — plugin manifest.
- `.claude-plugin/marketplace.json` — single-plugin local marketplace (`agent-bridge-local`).
- `.mcp.json` — registers the `agent-bridge` MCP server (`${CLAUDE_PLUGIN_ROOT}/agent-bridge-mcp`).
- `skills/agent-bridge/` + `skills/multi-model-review/` — the delegation and cross-model-review skills.

## Install into Codex

Use this when the **parent** is OpenAI Codex (so Codex can delegate to Gemini/Claude via
`gemini_agent` / `claude_agent`). `make install-codex` installs the plugin — skill **and**
bundled MCP server — from a local Codex marketplace:

```sh
make install-codex      # build + copy skills/binary into the plugin + codex plugin marketplace add + codex plugin add
codex plugin list       # confirm agent-bridge@agent-bridge-local is installed/enabled
codex mcp list          # confirm the bundled agent-bridge MCP server (./agent-bridge-mcp)
# remove later with:
make uninstall-codex
```

Codex **cannot** consume this repo's Claude-format `.claude-plugin/` marketplace, so the
target ships a Codex-format plugin:

- `.agents/plugins/marketplace.json` — single-plugin Codex marketplace (`agent-bridge-local`).
- `plugins/agent-bridge/.codex-plugin/plugin.json` — Codex plugin manifest (`skills` + `mcpServers`).
- `plugins/agent-bridge/.mcp.json` — declares the `agent-bridge` MCP server with a
  plugin-relative command (`./agent-bridge-mcp`).

Codex requires a plugin's skills **and** any bundled MCP binary to live *inside* the plugin
root (its validator forbids `..`/symlink escapes), so `make install-codex` copies the
canonical `./skills` and the built binary into `plugins/agent-bridge/` (both gitignored)
before install. `codex plugin add` then snapshots the whole plugin — skills + `.mcp.json` +
binary — into a **frozen** cache (`~/.codex/plugins/cache/.../`) and wires up the MCP server
itself: **no separate `codex mcp add`**, and the install doesn't track your checkout.

> **Use `make install-codex`, not a bare `codex plugin add`.** Only the Codex manifests
> (`.codex-plugin/plugin.json`, `.mcp.json`) are committed; the `skills/` and the
> `agent-bridge-mcp` binary they reference are **gitignored** and materialized by the make
> target. Running `codex plugin marketplace add . && codex plugin add …` by hand on a fresh
> clone (without `make install-codex` first) registers a plugin whose declared skills and MCP
> binary are missing.

## Build (Makefile)

```sh
make build         # compile ./agent-bridge-mcp (referenced by .mcp.json)
make install-all   # install into every host whose CLI is on PATH (claude / agy / codex)
make uninstall-all # remove from every host whose CLI is on PATH
make install       # OPTIONAL standalone `go install` into $GOBIN (unrelated to install-all)
make vet           # static checks
make smoke         # reason-only round-trip against ALL tools (needs agy + claude + codex authed)
make smoke-gemini  # round-trip against gemini_agent only (needs agy authed)
make smoke-claude  # round-trip against claude_agent only (needs claude authed)
make smoke-codex   # round-trip against codex_agent only (needs codex authed)
make help          # list targets
```

Per-host targets (`install-claude` / `install-agy` / `install-codex` and their
`uninstall-*`) install into one host; `make install-all` runs whichever of the three
CLIs are present and skips the rest.

## Example calls

### `gemini_agent`

Reason-only (safe default):

```json
{ "task": "Review this Go error-handling pattern and suggest improvements: ..." }
```

Acting mode — let Gemini edit files (auto-approves its permission prompts; scope
it with `working_dir` and verify the diff afterward):

```json
{
  "task": "Rename the symbol Foo to Bar across this package and update callers. Make the edits and list the files you changed.",
  "working_dir": "/path/to/project",
  "add_dirs": ["/path/to/project"],
  "mode": "act"
}
```

### `claude_agent`

Reason-only (safe default — still consumes Claude credits):

```json
{ "task": "Review this Go error-handling pattern and suggest improvements: ..." }
```

Acting mode — let Claude edit files (unattended; scope it with `working_dir`):

```json
{
  "task": "Rename the symbol Foo to Bar across this package and update callers. Make the edits and list the files you changed.",
  "working_dir": "/path/to/project",
  "add_dirs": ["/path/to/project"],
  "model": "sonnet",
  "mode": "act"
}
```

### `codex_agent`

Read-only (safe default — `--sandbox read-only`, no writes):

```json
{ "task": "Review this Go error-handling pattern and suggest improvements: ..." }
```

Acting mode — full, unsandboxed access (`--dangerously-bypass-approvals-and-sandbox`;
scope it with `working_dir` and verify the diff afterward):

```json
{
  "task": "Rename the symbol Foo to Bar across this package and update callers. Make the edits and list the files you changed.",
  "working_dir": "/path/to/project",
  "add_dirs": ["/path/to/project"],
  "mode": "act"
}
```
