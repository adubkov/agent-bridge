# agy-mcp

A tiny [MCP](https://modelcontextprotocol.io) server that exposes the
Antigravity CLI (`agy`) — a Gemini agent — as a single tool. A parent agent
(Claude Code, etc.) calls the tool with a task; this server runs
`agy --print <task>`, lets Gemini perform it, and returns Gemini's response.
In effect: a **spawned Gemini sub-agent callable from inside another agent**.

## Tool: `gemini_agent`

| Param | Type | Default | Notes |
|---|---|---|---|
| `task` | string (required) | — | The complete, self-contained task/prompt for Gemini. |
| `add_dirs` | string[] | — | Directories to add to the agent's workspace (absolute paths). |
| `working_dir` | string | server cwd | Directory the agent runs in. |
| `timeout_seconds` | number | 300 (max 1800) | Maps to `agy --print-timeout`. |
| `allow_tools` | bool | **false** | Let the agent edit files in `working_dir` / run commands by auto-approving its permission prompts (`--dangerously-skip-permissions`). |
| `sandbox` | bool | **false** | Confine the agent to an isolated scratch dir (`--sandbox`). **Warning:** when true, its edits go to the scratch dir, NOT `working_dir`. Leave off for real edits. |

### Safety model

By default the spawned agent is **reason/answer only** — it runs `agy --print`
with no permission bypass, so it can analyze, draft, and answer but cannot take
unattended actions. To let it actually act on your files/system, the caller must
explicitly pass `allow_tools: true`, which passes `--dangerously-skip-permissions`
(Gemini's approval gates are off — this is unattended execution). Scope it with
`working_dir`; the agent's edits land there.

`--sandbox` is **off by default**: with it on, agy confines the agent to an
isolated scratch dir, so edits would *not* reach `working_dir`. Set
`sandbox: true` only for a confined "compute but don't touch my files" run.

The tool result header always reports which mode ran.

## Build

```sh
go build -o agy-mcp .          # local binary
# or
go install github.com/adubkov/agy-mcp@latest
```

Requires `agy` on `PATH` (or set `AGY_BIN=/path/to/agy`). The server falls back
to `~/.local/bin/agy`.

## Install into Claude Code

Two ways — pick one. **Either way, requires `agy` authenticated** (`agy` login
once) and on `PATH` (or set `AGY_BIN`; the server also falls back to
`~/.local/bin/agy`). Restart Claude Code afterward (MCP loads at session start);
run `/mcp` to confirm the `agy` server is connected. The tool appears as
`gemini_agent`.

### A) MCP server only — `make install-claude` (simplest)

Registers just the tool (user scope, available in every project):

```sh
make install-claude     # build + `claude mcp add agy --scope user -- <binary>`
# remove later with:
make uninstall-claude
```

Equivalent manual command:

```sh
claude mcp add agy --scope user -- "$(pwd)/agy-mcp"
```

Or project scope via `.mcp.json` in a repo root:

```json
{
  "mcpServers": {
    "agy": {
      "command": "/absolute/path/to/agy-mcp/agy-mcp",
      "env": { "AGY_BIN": "/Users/you/.local/bin/agy" }
    }
  }
}
```

### B) As a plugin — `make plugin-install` (tool + skill)

This repo is also a Claude Code **plugin** (`agy-gemini`): installing it wires the
MCP server *and* ships a skill (`skills/gemini-agent/SKILL.md`) that teaches Claude
when and how to delegate to `gemini_agent` (and to verify its output).

Claude Code discovers plugins through **marketplaces**, not by scanning a
directory — so this repo carries a single-plugin local marketplace
(`.claude-plugin/marketplace.json`). The target registers that marketplace and
installs the plugin from it:

```sh
make plugin-install     # build + marketplace add (this repo) + plugin install
# then restart Claude Code; run /plugin and /mcp to confirm
# remove later with:
make plugin-uninstall
```

Equivalent manual commands:

```sh
claude plugin marketplace add "$(pwd)"
claude plugin install agy-gemini@agy-gemini-local
```

> The marketplace records this repo's **absolute path** in your user settings, so
> this is a local-dev install tied to your checkout location. To share it, point a
> marketplace at the GitHub repo instead of the local path.

The plugin bundles:

- `.claude-plugin/plugin.json` — plugin manifest.
- `.claude-plugin/marketplace.json` — single-plugin local marketplace
  (`agy-gemini-local`) so `claude plugin marketplace add` can find it.
- `.mcp.json` — registers the `agy` MCP server (`${CLAUDE_PLUGIN_ROOT}/agy-mcp`).
- `skills/gemini-agent/SKILL.md` — guidance for Claude on delegating tasks
  (when to use it, the two modes, how to write a good `task`, and "always verify
  the output").

## Build (Makefile)

```sh
make build    # compile ./agy-mcp (referenced by .mcp.json)
make install  # go install into $GOBIN
make vet      # static checks
make smoke    # build + drive the stdio server through a reason-only round-trip
make help     # list targets
```

## Example calls

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
  "allow_tools": true
}
```

Reason-only (safe default):

```json
{ "task": "Review this Go error-handling pattern and suggest improvements: ..." }
```

Let it act, with workspace context:

```json
{
  "task": "Rename the symbol Foo to Bar across this package and update callers.",
  "working_dir": "/path/to/project",
  "add_dirs": ["/path/to/project"],
  "allow_tools": true
}
```
