---
name: delegate
description: Use when the user EXPLICITLY asks to delegate coding work to a different model family via the agent-bridge MCP tools (`antigravity_agent`/Gemini, `claude_agent`, `codex_agent`) — triggers like "delegate this", "have Gemini Flash implement it", "use Codex for X", or "via agent-bridge". Two shapes — (1) hand ONE self-contained task to a fast/cheap cross-model agent while you keep orchestrating (mechanical bulk edits, a first-pass draft, an independent second opinion); or (2) a TIERED pipeline where a heavy model plans and a cheaper/faster model (e.g. Gemini Flash) implements, then you verify. Covers tier selection (deep/fast), the self-contained handoff spec, scoping/isolating an acting executor, concurrent fan-out via `parallel_agents`, and the verify loop. This is explicit CROSS-MODEL delegation via agent-bridge — NOT the host's native subagent/Task or workflow tools; do not trigger on a bare "subagent" or "run in parallel" request that does not name agent-bridge or a cross-model backend. Requires the agent-bridge MCP server. (For multi-model code review specifically, use the `multi-model-review` skill instead.)
---

# Delegating work to sub-agents (agent-bridge)

The `agent-bridge` plugin exposes three MCP tools that each spawn a coding agent in a
different model family and return its output:

- **`antigravity_agent`** — Antigravity (Google's `agy` CLI, **Gemini** models).
- **`claude_agent`** — the `claude` CLI.
- **`codex_agent`** — OpenAI's `codex` CLI.

Each spawn is a real sub-agent: **fresh context** (it never saw this conversation), runs
non-interactively, and — when allowed — edits files and runs commands in a `working_dir`
you give it. The tool descriptions in `cmd/agent-bridge-mcp/main.go` are the **source of
truth** for per-backend behavior; this skill is the playbook for *using* them well.

This skill has **two shapes**:

- **Shape A — one-shot delegation.** Hand off one self-contained task and keep working.
- **Shape B — tiered pipeline.** A heavy model **plans**, a cheaper/faster model
  **implements**, then you **verify**. This is the "Opus plans, Flash implements" case.

Both rest on the same core discipline (the **handoff spec**) and the same **verify gate**.

## When to delegate — and when not

**Delegate only when the user has explicitly asked for it — it is opt-in, never automatic.**
The trigger is the word **delegate**, or an explicit agent-bridge reference: a named backend
or cross-model intent ("use Gemini Flash / Codex / `claude_agent`", "via agent-bridge", "spawn
a Gemini/Codex agent to …"). That — for *that* task — is the request.

**Do not** treat the generic phrases "subagent", "spawn an agent", or "run these in parallel"
as triggers on their own: in Claude Code (and other hosts) those name the host's **native**
Task/subagent or workflow tools, **not** this cross-model bridge. If the user says one of them
*without* naming agent-bridge or a cross-model backend, it is a native request — leave it to
the host and do **not** trigger here.

For any task the user has *not* explicitly asked to delegate, **do the work yourself
in-session** — even when it looks like an ideal candidate. When something is a strong fit but
the user hasn't asked, you may briefly *offer* to delegate; do not act on it unprompted. (This
skill stays loaded in context across later turns once triggered — that persistence must **not**
turn into delegating subsequent tasks on your own; re-confirm an explicit request each time.)

Once the user *has* asked, these are the work shapes where a sub-agent's
speed/throughput/cost or its *independent* perspective is the win:

- **Mechanical bulk edits** — rename a symbol across a package, apply a repetitive
  refactor, regenerate boilerplate, reformat.
- **Broad parallel exploration** — "summarize what this package does", "find all call
  sites of X" — run it while you do something else.
- **Fast first-pass draft** — a doc/test/function you'll then review.
- **Well-specified implementation** — once *you* have planned it (Shape B).
- **Independent second opinion** — critique a design or diff; one input, not gospel.

**Keep doing yourself:** work that needs your accumulated session context, judgment calls,
anything where a wrong *unattended* edit is costly, and **the final verification** of
whatever the sub-agent produced (always verify — see below).

## Shape A — one-shot delegation

Pick a backend (from a Claude host, prefer the *other* families for a genuinely
independent take), write a self-contained `task`, and call it:

- **Reason/answer** (default `mode: "reason"`) — analysis, drafts-as-text, second
  opinions. ⚠️ For agy this is **not** a hard write-block (see Safety); for a truly
  hands-off run, scope `working_dir` to a throwaway dir.
- **Read-only** (`mode: "read"` on claude/codex) — repo exploration that must not edit.
- **Acting** (`mode: "act"`) — the agent edits files / runs commands in `working_dir`
  with its permission gates off. Always pass `working_dir` so it's scoped, and **verify
  the result** (read the diff, run the build/tests) afterward.

Then treat the output as a deliverable to check, not verified truth.

## Shape B — tiered: heavy plans, fast implements, you verify

The point is **capability-tiered routing**: send each phase to the cheapest model that can
do it well. A frontier model's judgment is wasted on mechanical edits; a fast model's speed
is wasted if it has to reverse-engineer the goal. So split the task:

```
PLAN      heavy / deep tier — usually the orchestrator ITSELF (it holds the
          conversation context). Output: a self-contained implementation spec.
   │              (the handoff artifact — see "The handoff spec")
   ▼
EXECUTE   fast / cheap tier — antigravity_agent tier:"fast" mode:"act"  (Gemini Flash),
          or claude/codex fast. Decompose into independent chunks and fan them out
          with parallel_agents when they don't touch the same files.
   │
   ▼
VERIFY    orchestrator reviews the diff + runs build / tests / typecheck ITSELF.
          On failure → send a corrective follow-up task and loop.
          (Optional deeper gate: the multi-model-review skill.)
```

**Who plans.** Usually *you*, the orchestrator — you already hold the context, so you write
the spec directly. Delegate the plan to a *deep-tier* spawn only when you want a fresh heavy
pass (e.g. `claude_agent tier:"deep"`, or a different family for an independent design), and
even then **you** still own the verify step.

**Why this works only with a real spec.** The executor starts cold. The plan is the *entire*
bridge between your context and its work — so the plan's quality is the ceiling on the
result. A vague plan to a fast model produces fast garbage. Invest in the spec (next
section); that is what makes a cheap executor safe.

## The handoff spec (the load-bearing part)

Whatever the shape, the `task` you hand a sub-agent must stand entirely on its own — it does
**not** share your context. A good spec states:

- **Goal** — what to build/change, in one or two sentences.
- **Exact files and paths** — name them; don't make it hunt. Use `add_dirs` for extra
  context dirs.
- **The precise changes** — for edits, the specific transformation; for new code, the
  signature/shape and where it goes.
- **Constraints / what NOT to touch** — out-of-scope files, APIs to preserve, style to
  match ("write code that reads like the surrounding code").
- **Acceptance criteria** — what "done" looks like, and the **build/test command** it
  should run to self-check before returning.
- **Return shape** — "make the edits and list the files you changed", or for analysis, the
  exact output format you want back.

A plan written for *you* to execute is rarely a sufficient spec for a *fresh* agent — make
the implicit explicit before handing it off.

## Model & tier selection (deep / fast)

Every backend tool takes a **`tier`** param (`deep` / `fast`) that the bridge resolves
server-side to a model + effort. Pass the tier and let the server resolve it; an explicit
`model` / `effort` overrides the tier per-field.

| Tier | `antigravity_agent` | `claude_agent` | `codex_agent` |
|---|---|---|---|
| **deep** | discovered `*Pro* (High)` | `model: opus`, `effort: xhigh` | `model:` *(omit → frontier)*, `effort: high` |
| **fast** | discovered `*Flash* (Medium)` | `model: sonnet`, `effort: medium` | `model:` *(omit)*, `effort: low` |

- **Gemini Flash** = `antigravity_agent` with `tier: "fast"` — the server matches it against
  `agy models` (a `*Flash* (Medium)` entry) at runtime, so it stays current without
  hardcoding a version. To pin a specific one, run `agy models` and pass the label as
  `model`.
- **Claude** — `opus`/`sonnet` are aliases that always resolve to the latest in that family;
  effort is the separate `effort` param (`low|medium|high|xhigh|max`).
- **Codex** — **omit `model`** (its default is the recommended frontier model); set the tier
  with `effort` only (`minimal|low|medium|high` — map a requested "max" to `high`).

For the **execute** phase, `fast` is the usual choice (that's the savings). For a **deep**
plan or a hard implementation, use `deep`. The spawn's result header echoes the resolved
`model=… effort=… tier=…` actually sent to the CLI — read it to confirm your tier applied.

## Execution safety (when the executor writes)

The execute phase needs **`mode: "act"`** (writes + commands). Contain it:

- **Always scope `working_dir`** to the repo (absolute path). Without it the agent runs in
  the *bridge server's own cwd* — often your project tree, but not guaranteed.
- **A throwaway `git worktree` is OPTIONAL when the changes are intended.** You *want* the
  writes here, so the worktree is **not** a safety requirement — it's a review/containment
  convenience. Pointing `working_dir` at the repo (ideally a **clean feature branch**) and
  reviewing `git diff` afterward is a perfectly good, simpler flow — but check `git status`
  for untracked debris too, not just `git diff`: an acting agy executor routinely drops
  scratch `git diff` dumps into `working_dir`, and those are **untracked**, so stage explicit
  paths (never `git add -A` blind) when you commit. Reach for a separate worktree when you
  (a) have unrelated uncommitted changes you don't want mixed with the agent's, (b) run
  **multiple** executors in parallel (they'd clobber a shared tree), or (c) want a clean
  review-then-merge gate / to bound a broad edit's blast radius:
  ```sh
  wt="$(mktemp -d)/wt"; git worktree add "$wt" -b feat/x HEAD   # point working_dir here
  # … executor runs in $wt … then review its diff and merge, finally:
  git worktree remove "$wt"
  ```
- **The agy "no write-safe tier / `--sandbox` isn't a guard" caveat is about *non-acting*
  runs, not this one.** When agy is *acting* you want the writes, so that caveat is moot —
  any worktree you use is for review/containment (above), not a write-guard. It bites only
  when you *don't* want writes: a `reason` agy run has no read-only tier and can still edit
  unattended in a writable `working_dir`, so there the throwaway dir is the actual guard (see
  the `multi-model-review` skill and Caveats).
- **Verify after, every time** — the executor's "done" is a claim, not proof.

## Parallel decomposition (`parallel_agents`)

If the plan splits into **independent** chunks, run them concurrently in **one
`parallel_agents` call** — a `jobs` array, one entry per chunk, each
`{"agent": "...", "task": "...", "mode": "act", "working_dir": "...", ...}`. The bridge runs
them as concurrent goroutines (wall-clock ≈ the slowest job). **Do not issue N separate tool
calls** — every MCP host dispatches tool calls **serially**, so N calls run back-to-back
(wall-clock = the sum).

> **Avoid write conflicts.** Parallel `act` agents on the *same* files will clobber each
> other. Only fan out chunks that touch **disjoint** file sets — or give each its **own
> worktree** and merge after. When in doubt, run them sequentially.

## Verify (the gate you never skip)

A delegated result is unverified until *you* check it:

- For `act` edits: review the diff, run `go build` / tests / typecheck / lint yourself.
- For analysis: weigh it as one input, not ground truth.
- **On failure, loop:** send a corrective follow-up `task` (quote the failing output /
  test) rather than fixing silently — that keeps the executor doing the mechanical work and
  you doing the judgment. Fix inline only for a trivial last-mile touch-up.
- For a high-stakes diff, escalate to the **`multi-model-review`** skill for a cross-model
  correctness pass before you trust it.

## Params reference

| Param | Use |
|---|---|
| `task` (required) | The **self-contained** spec (see "The handoff spec"). |
| `working_dir` | Absolute path the agent runs in. Set it for any file work; isolate with a worktree for acting executors. |
| `add_dirs` | Extra workspace dirs for context. |
| `mode` | `reason` (default) / `act` everywhere; `read` (read-only) on `claude_agent`/`codex_agent` (`codex` `reason`==`read`); `antigravity_agent` has **no** `read`. `act` edits + runs commands unattended. |
| `tier` | `deep`/`fast` → model+effort, server-resolved (see table). Overridden per-field by explicit `model`/`effort`. |
| `model` / `effort` | Pin a model / reasoning effort. `effort` applies to claude/codex only (agy bakes effort into the model name). |
| `sandbox` | **antigravity only**, false by default. agy *terminal* restrictions — **not** a file-write guard. |
| `timeout_seconds` | Default 300, max 1800. Raise for big tasks; agy latency is variable, give it ≥300. |

## Examples

**Shape A — one-shot mechanical edit (fast, acting):**

```
antigravity_agent({
  task: "In internal/foo/bar.go, rename the exported function `OldName` to `NewName` and \
update all call sites within the internal/foo package. Match the surrounding style. Make \
the edits, run `go build ./...`, and list the files you changed.",
  working_dir: "/abs/path/to/repo",
  mode: "act",
  tier: "fast",
  timeout_seconds: 600
})
```
Then review the diff and run the build yourself.

**Shape B — heavy plans (you), Flash implements, you verify:**

1. **Plan** (you, the orchestrator): write the spec — exact files, the change per file,
   constraints, and the acceptance command. (Or delegate a deep pass to
   `claude_agent tier:"deep"` if you want a fresh heavy design.)
2. **Execute** (Flash): hand the spec to `antigravity_agent` with `tier: "fast"`,
   `mode: "act"`, `working_dir` at the repo — a **clean feature branch**, or a throwaway
   worktree if you want a review-then-merge gate (see Execution safety). If the spec splits
   into independent files, fan the chunks out in one `parallel_agents` call (disjoint files
   or separate worktrees).
3. **Verify** (you): review the worktree diff, run the build/tests, loop with a corrective
   follow-up on any failure, then merge.

## Caveats

- **Cost & latency.** Each spawn is a full CLI process + model inference — far heavier than
  an in-process step. Tiering is the lever: pay deep-tier prices only where judgment is
  needed, fast-tier for the mechanical bulk.
- **agy runs under a pty and its latency is variable.** The bridge gives the Antigravity
  backend a pseudo-terminal (its agentic `--print` loop hangs on plain pipes); per-run
  latency is highly variable (≈75–270s for a repo-sized task), so set generous
  `timeout_seconds` rather than reading a slow run as a hang.
- **agy is never truly hands-off without a throwaway dir.** It has no tool-disable flag and
  doesn't gate writes, so even a `reason` agy run with a writable `working_dir` (or none —
  which uses the bridge server's cwd, often your tree) can edit files. Point it at a
  throwaway dir/worktree; `--sandbox` does **not** confine writes.
- **Delegation depth is bounded.** The bridge caps hop depth at `AGENT_HOP_MAX` (default 2),
  tracking the current depth in `AGENT_HOP_DEPTH` (starts at 0), and freezes further
  delegation for non-acting (`reason`/`read`) children. An `act` executor is *not* frozen
  (it's bounded only by the hop guard), so don't build deep act→act→act chains.
- **Tool descriptions are authoritative.** This skill summarizes per-backend `mode` / `tier`
  / `working_dir` / `sandbox` behavior; if it ever diverges from the bridge's own MCP tool
  descriptions (generated from `cmd/agent-bridge-mcp/main.go`), trust those.
- **For cross-model code review, use `multi-model-review`.** That skill is the specialized
  pipeline (fan-out finders → cross-verify with a *different* model → synthesize); don't
  reinvent it here.
