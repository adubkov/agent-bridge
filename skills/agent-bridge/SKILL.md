---
name: agent-bridge
description: Use when you want to delegate a self-contained task to a fast Gemini agent (via the `gemini_agent` MCP tool from the agent-bridge plugin) — e.g. mechanical bulk edits, broad parallel exploration, a fast first-pass draft, or an independent second opinion — while you keep orchestrating. Requires the agent-bridge MCP server (tool name `gemini_agent`).
---

# Delegating to a Gemini agent (`gemini_agent`)

The `agent-bridge` plugin exposes three MCP tools: **`gemini_agent`** (Claude →
Gemini, via the Antigravity `agy` CLI), `claude_agent` (the reverse, Gemini →
Claude — for use from a Gemini session, not relevant here), and `codex_agent`
(spawns an OpenAI Codex agent via `codex exec`). From a Claude session you usually
delegate with **`gemini_agent`**, which spawns a Gemini agent to perform a task
and returns its output. It is a real sub-agent: it runs non-interactively and
can — when allowed — edit files and run commands in a working directory you give
it. `codex_agent` is an alternative target with the same `task` / `working_dir` /
`add_dirs` / `allow_tools` interface; note its `allow_tools: false` is a *read-only
sandbox* (Codex has no pure no-tools mode), and `allow_tools: true` grants full
unsandboxed access — otherwise call it the same way.

## When to use it

Reach for `gemini_agent` when the work is **self-contained and delegable**, and
Gemini's speed/throughput is the win:

- **Mechanical bulk edits** — rename a symbol across a package, apply a
  repetitive refactor, regenerate boilerplate, reformat.
- **Broad parallel exploration** — "summarize what this package does", "find all
  call sites of X", run while you work on something else.
- **Fast first-pass draft** — a draft doc/test/function you'll then review.
- **Independent second opinion** — ask Gemini to critique a design or diff; use
  its answer as one input, not gospel.

Keep doing yourself: work needing your accumulated session context, careful
judgment calls, anything where a wrong unattended edit is costly, and the final
review/verification of whatever Gemini produces (always verify its output).

## How to call it

| Param | Use |
|---|---|
| `task` (required) | A **complete, self-contained** prompt. Gemini does not share your context — spell out the goal, the files, and the acceptance criteria. |
| `working_dir` | Absolute path the agent runs in (set this for file work). |
| `add_dirs` | Extra workspace dirs for context. |
| `timeout_seconds` | Default 300, max 1800. Raise for big tasks. |
| `allow_tools` | **false by default** (reason/answer only). Set **true** to let it edit files in `working_dir` / run commands (auto-approves its permission prompts). |
| `sandbox` | **false by default.** When true, agy confines the agent to an isolated scratch dir, so its edits do NOT reach `working_dir` — only for a "compute but don't touch my files" run. Leave it off for real edits. |

### Two modes

1. **Reason/answer (default, `allow_tools` omitted)** — safe. Gemini analyzes
   and returns text; it cannot touch the filesystem unattended. Use for
   analysis, drafts-as-text, second opinions.
2. **Acting (`allow_tools: true`, `sandbox` left off)** — Gemini edits files /
   runs commands in `working_dir` with its permission gates off. Use for the
   mechanical-edit cases. **Always pass `working_dir` so it's scoped, leave
   `sandbox` off (or its edits go to a throwaway scratch dir), and verify the
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

- For edits made with `allow_tools: true`: review the diff, run `go build` /
  tests / typecheck yourself before trusting it.
- For analysis: weigh it as one input.

## Example

Delegate a scoped mechanical edit:

```
gemini_agent({
  task: "In the Go file internal/foo/bar.go, rename the exported function `OldName` to `NewName` and update all call sites within the internal/foo package. Make the edits and list the files you changed.",
  working_dir: "/abs/path/to/repo",
  add_dirs: ["/abs/path/to/repo/internal/foo"],
  allow_tools: true,
  timeout_seconds: 600
})
```

Then review the diff and run the build yourself.
