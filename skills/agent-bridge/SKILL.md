---
name: agent-bridge
description: Use when you want to delegate a self-contained task to a fast Antigravity (Gemini) agent (via the `antigravity_agent` MCP tool from the agent-bridge plugin) — e.g. mechanical bulk edits, broad parallel exploration, a fast first-pass draft, or an independent second opinion — while you keep orchestrating. Requires the agent-bridge MCP server (tool name `antigravity_agent`).
---

# Delegating to an Antigravity agent (`antigravity_agent`)

The `agent-bridge` plugin exposes three MCP tools: **`antigravity_agent`** (Claude →
Antigravity, Google's `agy` CLI, which runs Gemini models), `claude_agent` (the
reverse, Antigravity → Claude — for use from an Antigravity session, not relevant
here), and `codex_agent` (spawns an OpenAI Codex agent via `codex exec`). From a
Claude session you usually delegate with **`antigravity_agent`**, which spawns an
Antigravity agent to perform a task
and returns its output. It is a real sub-agent: it runs non-interactively and
can — when allowed — edit files and run commands in a working directory you give
it. `codex_agent` is an alternative target with the same `task` / `working_dir` /
`add_dirs` / `mode` interface; note its `mode: "reason"`/`"read"` are a *read-only
sandbox* (Codex has no pure no-tools mode) and `mode: "act"` grants full unsandboxed
access — otherwise call it the same way. (`claude_agent` additionally has `mode: "read"`
for read-only repo exploration; `antigravity_agent` has only `reason`/`act`.) `claude_agent`
and `codex_agent` also take an `effort` param (reasoning effort: claude
`low|medium|high|xhigh|max`, codex `minimal|low|medium|high`); `antigravity_agent` has
none — it selects effort through the model name.

## When to use it

Reach for `antigravity_agent` when the work is **self-contained and delegable**, and
Antigravity's speed/throughput is the win:

- **Mechanical bulk edits** — rename a symbol across a package, apply a
  repetitive refactor, regenerate boilerplate, reformat.
- **Broad parallel exploration** — "summarize what this package does", "find all
  call sites of X", run while you work on something else.
- **Fast first-pass draft** — a draft doc/test/function you'll then review.
- **Independent second opinion** — ask Antigravity to critique a design or diff; use
  its answer as one input, not gospel.

Keep doing yourself: work needing your accumulated session context, careful
judgment calls, anything where a wrong unattended edit is costly, and the final
review/verification of whatever Antigravity produces (always verify its output).

## How to call it

| Param | Use |
|---|---|
| `task` (required) | A **complete, self-contained** prompt. The agent does not share your context — spell out the goal, the files, and the acceptance criteria. |
| `working_dir` | Absolute path the agent runs in (set this for file work). |
| `add_dirs` | Extra workspace dirs for context. |
| `model` | Optional; the CLI default is fine. Pass a name for a specific model (`agy models` lists Gemini's). |
| `timeout_seconds` | Default 300, max 1800. Raise for big tasks. |
| `mode` | **`reason`** by default — reason/answer, but **not** a hard write-block: agy has no tool-disable flag and doesn't gate writes, so a `reason` agent with a writable `working_dir` can still edit files unattended (point `working_dir` at a throwaway dir to prevent that — omitting `working_dir` is **not** a guard, it just runs agy in the bridge server's own cwd, often your project tree; `--sandbox` does **not** confine writes). `mode: "act"` lets it edit files in `working_dir` / run commands (auto-approves permission prompts). `antigravity_agent` has no `read` tier. |
| `sandbox` | **false by default.** Enables agy's *terminal* restrictions only — despite the name it does **not** confine the agent's file edits (a write under it still lands in `working_dir`, verified), so it is not a "don't touch my files" guard; use a throwaway `working_dir` for that. |

### Two modes (antigravity)

1. **Reason/answer (default, `mode: "reason"`)** — for analysis, drafts-as-text,
   second opinions. ⚠️ Not a hard write-block: agy has no tool-disable flag and
   doesn't gate writes, so a `reason` agent pointed at a writable `working_dir` can
   still edit files unattended. For a truly hands-off run, point `working_dir` at a
   throwaway dir (omitting it just runs agy in the bridge server's own cwd, often your
   project tree — not a guard); `--sandbox` does **not** confine writes.
2. **Acting (`mode: "act"`)** — Antigravity edits files /
   runs commands in `working_dir` with its permission gates off. Use for the
   mechanical-edit cases. **Always pass `working_dir` so it's scoped, and verify the
   result afterward** (read the diff / run the build/tests yourself). The tool
   result header reports which mode ran.

## Writing a good `task`

- State the goal, the exact files/paths, and what "done" looks like.
- For edits: name the files and the precise change; ask it to make the edit and
  report what it changed.
- For analysis: ask for the specific output shape you want back.
- Don't assume it knows anything from this conversation — it starts fresh.

## After it returns

Treat the output as a sub-agent's deliverable, not verified truth:

- For edits made with `mode: "act"`: review the diff, run `go build` /
  tests / typecheck yourself before trusting it.
- For analysis: weigh it as one input.

## Example

Delegate a scoped mechanical edit:

```
antigravity_agent({
  task: "In the Go file internal/foo/bar.go, rename the exported function `OldName` to `NewName` and update all call sites within the internal/foo package. Make the edits and list the files you changed.",
  working_dir: "/abs/path/to/repo",
  add_dirs: ["/abs/path/to/repo/internal/foo"],
  mode: "act",
  timeout_seconds: 600
})
```

Then review the diff and run the build yourself.
