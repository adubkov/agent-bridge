---
name: delegate
description: Use when the user EXPLICITLY asks to delegate coding work to a different model family via the agent-bridge MCP tools (`antigravity_agent`/Gemini, `claude_agent`, `codex_agent`) — triggers like "delegate this to Gemini/Codex", "have Gemini Flash implement it", "use Codex for X", "get a second opinion from another model", "cross-check this design/decision across models", or "via agent-bridge". Two shapes — (1) hand ONE self-contained task to a fast/cheap cross-model agent while you keep orchestrating (mechanical bulk edits, a first-pass draft, an independent second opinion — a diverse multi-model panel by default); or (2) a TIERED pipeline where a heavy model plans and a cheaper/faster model (e.g. Gemini Flash) implements, then you verify. Covers tier selection (deep/fast), the self-contained handoff spec, scoping/isolating an acting executor, concurrent fan-out via `parallel_agents`, and the verify loop. This is explicit CROSS-MODEL delegation via agent-bridge — NOT the host's native subagent/Task or workflow tools; do not trigger on a bare "subagent" or "run in parallel" request that does not name agent-bridge or a cross-model backend. Requires the agent-bridge MCP server. (For multi-model code review specifically, use the `multi-model-review` skill instead.)
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
The trigger is an explicit **cross-model / agent-bridge** signal — a named backend or
another-model intent, optionally introduced by the word *delegate*: "delegate to / use Gemini
Flash / Codex / `claude_agent`", "via agent-bridge", "spawn a Gemini/Codex agent to …", "get a
**second opinion** from another model", "cross-check this design/decision across models" (a
*diff* goes to `multi-model-review`). The bare word "delegate" is **necessary but not
sufficient**: without a cross-model signal it's ambiguous with the host's native delegation, so
see the tie-breaker below. That — for *that* task — is the request.

**Do not** treat the generic phrases "subagent", "spawn an agent", or "run these in parallel"
as triggers on their own: in Claude Code (and other hosts) those name the host's **native**
Task/subagent or workflow tools, **not** this cross-model bridge. If the user says one of them
*without* naming agent-bridge or a cross-model backend, it is a native request — leave it to
the host and do **not** trigger here.

**Tie-breaker when a request mixes signals** (e.g. "delegate to a subagent", "delegate this in
parallel"): the native-tool vocabulary **wins** unless the user *also* names agent-bridge or a
cross-model target (a backend, "another model", "Gemini / Codex / Claude"). So bare "delegate"
+ native words + no cross-model signal → the host's native delegation, not this skill;
"delegate to Gemini", "delegate … to another model" → here.

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
- **Independent second opinion** — critique a design or decision; a **diverse multi-model
  panel by default** (see *Second opinion* below), one input, not gospel. (For a *diff*, use
  `multi-model-review`.)

**Keep doing yourself:** work that needs your accumulated session context, judgment calls,
anything where a wrong *unattended* edit is costly, and **the final verification** of
whatever the sub-agent produced (always verify — see below).

**Mind the break-even.** Delegation has fixed overhead — a full CLI spawn + model inference +
a round-trip + your verify pass. It pays off when the work is **bulky** (many files / many
sites), **parallelizable**, **offloadable** (long enough that you do something else
meanwhile), or worth a **different model's** perspective. A single small, well-understood edit
clears none of those: by the time you've written a self-contained spec for it, you could have
made the change. So for small / quick / mechanical work, **prefer inline** — and when the user
asks to delegate something that small, say so and *offer* to just do it inline rather than
silently spawning, while honoring an explicit "no, delegate it anyway." Reserve delegation for
work whose size, parallelism, or independence amortizes the overhead. (This is a *don't-bother*
nudge, not an override: it never licenses delegating unasked, and it yields to an explicit
delegation request.)

## Shape A — one-shot delegation

Pick a backend (from a Claude host, prefer the *other* families for a genuinely
independent take), write a self-contained `task`, and call it:

- **Reason/answer** (default `mode: "reason"`) — analysis, drafts-as-text, second
  opinions. ⚠️ For agy this is **not** a hard write-block (see Caveats); for a truly
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
          conversation context). Output: a contract spec — interfaces + acceptance, not code.
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

**Why this works only with a real spec — at the right altitude.** The executor starts cold.
The plan is the *entire* bridge between your context and its work, so its quality is the
ceiling on the result. A vague plan to a fast model produces fast garbage — but the opposite
failure is just as real: spec it down to the exact bytes and you've done the work yourself, so
the spawn only adds latency. Invest in the spec at *contract* altitude (next section): tight on
interfaces/constraints/acceptance, open on the implementation.

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

**Spec the contract, not the content — get the altitude right.** State the goal, the
interfaces/signatures, the constraints, and the acceptance criteria, then **let the executor
implement**. Its job is to *implement*, not to *transcribe*. ⚠️ **If you catch yourself writing
the literal file contents or exact code into the `task`, stop** — you've already done the work,
and wrapping it in a spawn is slower and pricier than just doing it. Either raise the handoff to
the contract level (interfaces + acceptance test, implementation left open) so the executor adds
real value, or make the edit inline. Too vague → fast garbage; too low (the bytes) → pointless
round-trip. Aim for the middle: **tight on the contract, open on the implementation.**

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
`model=… effort=… tier=…` actually sent to the CLI (no `effort=` for agy — it has no effort
lever) — read it to confirm your tier applied.

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

## Second opinion (diverse panel by default)

When the user wants an independent take on a **design, decision, or piece of reasoning** (not a
diff — for a diff, route to `multi-model-review`), the value is *diversity*: different model
families have uncorrelated blind spots, so the **spread** of views — and especially the
disagreements — is the signal. A single other model is just one more blind-spot set, so
**default to a panel, not one model.**

- **Fan out to the other families.** Send the *same* prompt to each connected family OTHER
  than your host (e.g. `antigravity_agent` + `codex_agent` from a Claude host) in **one
  `parallel_agents` call**, so they run concurrently (wall-clock ≈ the slowest). Keep each
  read-only with the **same per-backend write-safety as everywhere in this skill** (don't
  assume "reason" means no writes): `claude_agent` / `codex_agent` are write-safe — use
  `mode: "read"` if a panelist must read code for context; **`antigravity_agent` has no
  read-only tier and `reason` is *not* write-safe** (with no `working_dir` it even runs in the
  server's own cwd), so always point it at a **throwaway `git worktree`/dir** (see *Caveats* —
  agy is never hands-off without one), never your live checkout. (A pure design/decision question needs no repo access at
  all — put the context in the prompt; a *diff* goes to `multi-model-review`, not here.) The
  fan-out's diversity comes from the families *other* than your host — you, the orchestrator,
  are already a host-family model, so that perspective is partly represented, but only
  *mediated* through your synthesis, not as a clean independent read. So add a fresh host-family
  spawn (`claude_agent` on a Claude host) for a direct, **author-unbiased** same-family take:
  most valuable when **you authored** the subject (your own view is then biased), though it adds
  signal even when you didn't. It removes author bias but still shares your model's blind spots,
  so it *supplements* rather than replaces the cross-family voices.
- **Single model is the narrowed case** — when the user names one ("ask Gemini"), only one
  other family is connected, or they want a quick, cheap gut-check.
- **Degrade honestly.** If only one other family is connected it is effectively a single
  opinion — say so in the synthesis; don't imply diversity you didn't get.
- **Synthesize consensus + divergence.** Lead with where the models agree, then surface
  outliers and disagreements explicitly, with attribution — don't average them into mush.
  Every take is one input; you still own the call.
- **Cost.** A panel is one spawn per model (parallel, so latency ≈ the slowest, but N× the
  tokens). The single-model path is the cheap escape hatch.

This is **lighter than `multi-model-review`**: N *independent* takes plus your synthesis, with
**no** adversarial cross-verification wave. Use `multi-model-review` when the subject is a diff
and you want each finding refuted by a different model; use this panel for diverse judgment on a
non-diff question.

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

1. **Plan** (you, the orchestrator): write the spec as a **contract** — the files, the
   *interfaces* (signatures / types / behavior), the constraints, and the **acceptance
   command** — and stop there. Leave the implementation to Flash; if you've already written the
   actual code, just `Write` it yourself instead of spawning. (Or delegate a deep design pass
   to `claude_agent tier:"deep"` for a fresh heavy take.)

   *Contract-style `task` (interfaces + acceptance, not code):* "Add a token-bucket
   `RateLimiter` to `internal/limit/limiter.go` exposing `New(rps int) *RateLimiter` and
   `(*RateLimiter) Allow() bool`, and call `Allow()` in `server.handle`
   (`internal/http/server.go`) to reject over-limit requests with 429. Add a table test in
   `limiter_test.go` for burst + refill. Match the surrounding style. Run `go test ./internal/...`
   and report the result." — it pins the interfaces and the test; Flash writes the bucket logic.
2. **Execute** (Flash): hand that spec to `antigravity_agent` with `tier: "fast"`,
   `mode: "act"`, `working_dir` at the repo — a **clean feature branch**, or a throwaway
   worktree if you want a review-then-merge gate (see Execution safety). If the spec splits
   into independent files, fan the chunks out in one `parallel_agents` call (disjoint files
   or separate worktrees).
3. **Verify** (you): review the diff, run the build/tests, loop with a corrective
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
