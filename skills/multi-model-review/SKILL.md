---
name: multi-model-review
description: Use when the user wants a CROSS-MODEL / multi-model code review of a diff — fan the change out to several different models (Gemini, Claude, Codex) as independent reviewers via the agent-bridge MCP tools, then cross-verify each finding with a DIFFERENT model before reporting. Good for high-stakes diffs where you want uncorrelated model perspectives, not just one model's opinion. Host-agnostic — the orchestrator can be a Claude Code, Antigravity (Gemini), or Codex session. Requires the agent-bridge MCP server (tools `gemini_agent` / `claude_agent` / `codex_agent`).
---

# Multi-model code review (via agent-bridge)

Run a code review by fanning a diff out to **multiple different models** as
independent finders, then **cross-verifying** each finding with a model that did
NOT raise it, and synthesizing a ranked list. The point is **model diversity**:
different model families (Gemini, Claude, Codex) have uncorrelated blind spots, so
one model's miss is another's catch, and a finding survives only if a *second*
model can't refute it.

Use this when a diff is high-stakes enough to want genuinely independent
cross-family perspectives, or when you want a second/third opinion from outside
your own host's model family. (For fast, deep, *same*-model review, a host's
native reviewer — e.g. Claude Code's `/code-review` — is the cheaper choice.)

**You are the orchestrator, not a reviewer** — whichever agent is running this
skill (a Claude Code, Antigravity/Gemini, or Codex session). You gather the diff,
call the bridge tools, run the verification round, and synthesize. The reviewers
are the *spawned* bridge agents; **you do not add your own findings on a diff you
authored** — that reintroduces the very author bias this skill exists to avoid (see
"Independence" below). This pipeline is the same regardless of which host you run
from; see "Using this from Claude Code, Antigravity, or Codex" below for per-host
setup.

## Prerequisites

- The agent-bridge MCP server is connected in your host. Check which of
  `gemini_agent`, `claude_agent`, `codex_agent` are actually available (and their
  CLIs authed).
- **Use whichever subset is connected.** With all three you get full cross-family
  diversity; with two it still works; with one this degrades to a single-model
  review — **say so** in the report rather than implying multi-model coverage.
- **Reviewers are fresh-context spawned agents — never the author-orchestrator.**
  Prefer reviewer families OTHER than your host; that is where the diversity comes
  from (see Independence).

## Model & effort selection

Each finder/verifier spawn takes a `model` and — for `claude_agent` / `codex_agent` — an
`effort` param. Default to the **most capable** model per family, named so it stays current,
and **honor any user override** from the orchestrator prompt. (This tier controls
model/effort only; whether you cross-verify is the separate Fast-mode choice below — the two
compose.)

### Default tiers — drive selection from a tier, default `deep`

| Tier | `claude_agent` | `codex_agent` | `gemini_agent` |
|---|---|---|---|
| **deep** (default) | `model: opus`, `effort: xhigh` | `model:` *(omit)*, `effort: high` | `model:` discovered `*Pro* (High)` |
| **fast** | `model: sonnet`, `effort: medium` | `model:` *(omit)*, `effort: low` | `model:` discovered `*Flash* (Medium)` |

Why these stay current (no hardcoded version strings):

- **Claude** — `opus`/`sonnet` are *aliases* that always resolve to the latest model in that
  family. Pass them verbatim; effort is the separate `effort` param.
- **Codex** — **omit `model`**: Codex defaults to its *recommended frontier* model, which
  OpenAI updates, so "no model" already means most-capable-and-current. Express the tier with
  `effort` only. (Do **not** use `codex-auto-review` — it's an approval-gating model, not a
  code reviewer.)
- **Gemini** — agy has no alias and bakes effort into the model *name*, so **discover** at
  review start: run `agy models`, pick the line matching the tier (deep → a `*Pro* (High)`
  entry; fast → a `*Flash* (Medium)` entry), and pass that label as `model`. Resolve once and
  reuse across the wave. If agy rejects the label, fall back to its default by omitting `model`.

Effort vocab differs across families — Claude takes `low|medium|high|xhigh|max`, Codex tops out
at `high`. Use the per-family value above; if a user asks for "max" on Codex, map it to `high`.

### User overrides (from the orchestrator prompt) — honor them

Resolve each reviewer's `model`/`effort` with this precedence, highest first:

1. **Explicit per-agent override** the user stated — e.g. "run Claude on `opus` `max`", "use
   `gpt-5.4-mini` for codex", "gemini on low effort". It wins for that agent, **per field**
   (overriding only Claude's effort leaves its model at the tier default). Pass the user's
   values **verbatim** to `model`/`effort` — don't "correct" them; the CLI validates (Claude
   warns and falls back on a bad effort). The user may also drop a reviewer ("skip codex") or
   add one not in the default mix.
2. **Explicit tier** the user named — "fast review" / "deep review" → apply the table above.
3. **Default** — `deep`.

Each spawn's result header echoes `model=… effort=…` actually used, so verify your override
landed; carry the resolved model + effort into the synthesis report per reviewer (and flag
when it came from a user override), so the user can confirm their choice took effect.

## Independence — who should review

The whole value is *independent* perspectives, so guard two separate biases:

- **Author bias** — sharing the reasoning that produced the code. The orchestrator
  is often the same session that just *wrote* the diff; it has already "decided" the
  code is correct, so it rationalizes its own choices and misses the assumptions it
  baked in. **A fresh-context spawned agent is the cure** — it never had your
  conversation, so it judges the diff cold.
- **Model bias** — the shared training blind spots of a model family. **The cure is
  a different family** (Gemini/Codex vs. Claude).

Independence ladder, best → worst reviewer:

1. **Different family, fresh context** (e.g. `gemini_agent` / `codex_agent` from a
   Claude host) — removes *both* biases.
2. **Same family, fresh context** (e.g. `claude_agent` from a Claude host) — removes
   author bias; still shares model blind spots. A useful supplement, not a
   substitute for (1).
3. **The author-orchestrator itself** — removes neither. **Do not use it as a
   reviewer.**

So keep the orchestrator a **coordinator**, and get every perspective — including
your host's own family — from a *spawned* agent. Want a Claude opinion on changes a
Claude session wrote? Spawn `claude_agent` (fresh context); don't let the author
session self-review.

**Exception:** if the orchestrator did **not** author the diff (e.g. reviewing a
teammate's PR it is seeing fresh), there is no author bias — it may contribute its
own findings as just another independent reviewer.

**Residual:** even as a pure coordinator, an author-orchestrator can still bias the
*synthesis* — waving off a real finding as "intended." Mitigate structurally: trust
the cross-model verdict (a finding another model CONFIRMs is hard to dismiss — and
the two-vote variant in step 3 makes it harder still) and report findings faithfully
even when you disagree with them.

## Pipeline

1. **Gather the diff** — yourself, inline.
2. **Fan out finders** — one reason-only call per reviewer model, diff embedded.
3. **Cross-verify** — each candidate checked by a *different* model.
4. **Synthesize** — dedup, rank, report with provenance.
5. **(optional) Fix** — only if asked.

### 1. Gather the diff

Run it yourself — and gather it with **`git diff --function-context`** (`-W`) so each hunk
carries its *whole enclosing function*, not just the default ±3 lines (e.g. `git diff
--function-context @{upstream}...HEAD`, `git diff -W main...HEAD`, or a PR/path the user
named). Then **embed the diff text inline, verbatim** in each finder's
`task` — do **not** summarize, paraphrase, or truncate it. A lossy diff makes
finders flag phantom issues (e.g. a section that only *looks* missing because you
trimmed it) and miss real ones. Embedding keeps the review self-contained and
reproducible; what actually keeps each finder **reason-only — no file writes, no
state-changing commands** is leaving `mode` at its default `reason` (with `codex_agent`
falling back to a `--sandbox read-only` mode). (Per-backend nuance: as spawned by the finder step —
reason-only, no `working_dir`/`add_dirs` — `gemini_agent` / `claude_agent` finders
have nothing but the inline diff to go on: reason-only blocks unattended file edits
and command execution, and nothing wires the repo in. `codex_agent` reason-only is a
`--sandbox read-only` mode that technically *could* read the repo, but you still
hand it the diff inline so all finders judge the same scoped input.) If the diff is
very large, narrow scope by dropping *whole files* — never by compressing the diff
text.

**How much context to embed — the ladder.** A bare `git diff` (±3 lines) catches *local*
bugs but is blind to anything outside the hunk; widen the context to fit the stakes:

1. **`git diff --function-context` (the default above)** — whole changed functions, no repo
   access needed. Kills most "I can't see the rest of the function" misses at near-zero cost.
   Start here.
2. **Full changed *files*** — embed the entire files the diff touches when same-file
   callers/helpers matter, or to check *completeness* (e.g. a new struct field whose handling
   `switch` is unchanged, and therefore invisible in a hunk).
3. **Repo-reading reviewer** — a `codex_agent` (its default read-only sandbox) **or** a
   `claude_agent` with `mode: "read"` (read-only plan mode), each with `working_dir` set —
   the only way to see *cross-file* callers, type definitions, and guards. Most powerful, but
   it is an agentic read-only run that can wander the tree (and, for codex, burn quota), so
   use it as a **targeted escalation** for a *specific* finding that needs it (see
   "Diff-scoped reviewers" in step 3), not the default pass.

Two different blind spots, two different fixes. Missing **code** context — callers, guards,
the rest of a function, type defs, completeness — is fixed by climbing this ladder. Missing
**external/runtime** knowledge — how a CLI, library, or framework actually behaves — is *not*
fixed by any amount of context; only a reviewer that already has that knowledge catches it,
which is why cross-family **model diversity** matters as much as context depth.

### 2. Fan out finders (reason-only, in parallel)

For each reviewer model, call its tool with the finder prompt below, the diff
embedded. **Issue them in a single message** — a host that runs independent tool
calls concurrently (e.g. Claude Code) then fans them out in parallel; a host that
serializes tool calls still runs them all, just one after another.

| Param | Value |
|---|---|
| `task` | The finder prompt + the embedded diff (see template). |
| `mode` | **omit it** (default `reason`) — keep finders reason-only (step 1 has the per-backend nuance). |
| `model` / `effort` | per **Model & effort selection** above (default `deep`; honor user overrides). |
| `timeout_seconds` | 300–600 depending on diff size. |
| `working_dir` / `add_dirs` | leave unset — the embedded diff is the intended input. |

> **Keep `codex_agent` scoped.** Its reason-only mode is an *agentic* `--sandbox read-only`
> run, not a no-tools mode (unlike `gemini_agent` / `claude_agent`, which genuinely cannot
> read files): if you set `working_dir`/`add_dirs` or tell it to "consult the repo," it will
> read files and can wander a large tree — slow, and it can burn its usage quota mid-review.
> For the finder/verifier passes leave `working_dir` unset and keep the "reason only over the
> inline diff" line in the prompt. Route to a repo-reading `codex_agent` (with `working_dir`
> set) only for a *specific* finding that genuinely needs out-of-diff context — see
> "Diff-scoped reviewers" below.

Give every model the **same brief** so the diversity comes from the model, not the
prompt. (You can layer distinct angles later; start uniform.)

**Finder prompt template:**

> You are an independent senior reviewer. Review the unified diff below for
> CORRECTNESS bugs (logic errors, wrong conditions, off-by-one, nil/undefined,
> missing error handling, concurrency hazards, broken call sites). Be specific:
> name the trigger and the wrong result. Do not nitpick style.
> **Reason only over the inline diff — do NOT read files, list directories, or run
> commands; everything you need is below.**
> Return **ONLY** a JSON array (max 8) of objects:
> `{"file": "...", "line": "...", "severity": "HIGH|MEDIUM|LOW", "summary": "...", "why": "concrete inputs/state → wrong result"}`.
> No prose outside the JSON. If you find nothing, return `[]`.
> === DIFF ===
> &lt;embed the unified diff here&gt;

Tag each returned finding with the **finder model** that produced it.

### 3. Cross-verify (each finding by a *different* model)

Pool all candidates and assign each a **verifier model ≠ the finder model**
(round-robin across the participating models: e.g. gemini→claude, claude→codex,
codex→gemini; with two models, just use the other; with only one model connected you
cannot cross-verify at all — use Fast mode and report it as a single-model review).
Then **dispatch every verifier call in a single message**, each reason-only with the
diff embedded — just like the finder wave.

This is a **two-wave** pipeline: all finders, then all verifiers. The one
unavoidable wait is *between* the waves — a finding can't be verified before it
exists. On a host that dispatches tool calls concurrently (e.g. Claude Code) each
wave runs in parallel, so total time ≈ slowest finder + slowest verifier; on a host
that serializes tool calls the two waves still hold but wall-clock is the sum.

**Verifier prompt template:**

> Another reviewer flagged this finding in the diff below. Decide if it is real.
> **Reason only over the inline diff — do NOT read files or run commands.**
> Answer with ONLY one JSON object:
> `{"verdict": "CONFIRMED|PLAUSIBLE|REFUTED", "reason": "quote the line that proves it"}`.
> CONFIRMED = you can name the trigger and wrong result. PLAUSIBLE = mechanism is
> real but the trigger is uncertain (use this too when a guard might exist outside
> what you can see). REFUTED = what you can see visibly contradicts the finding, or
> you can quote a guard that defuses it.
> === FINDING ===
> &lt;the candidate&gt;
> === DIFF ===
> &lt;embed the unified diff here&gt;

Keep **CONFIRMED** and **PLAUSIBLE**; drop **REFUTED**. (Prototype: one cross-vote
per finding. For higher confidence, send to BOTH other models and require a
majority — note the extra cost.)

**Diff-scoped reviewers.** `gemini_agent` / `claude_agent` finders and verifiers see
only the inline diff, so they can't check call sites or guards that live outside it.
Read the templates' "broken call sites" and "guarded elsewhere (quote the guard)" as
scoped to what the diff shows: a diff-only verifier that can't find a guard should
answer **PLAUSIBLE**, not REFUTED — a guard it can't see is not proof there is none.
When out-of-diff context is essential to a verdict, route that finding to a
repo-reading reviewer — `codex_agent` in reason-only (`--sandbox read-only`) mode can
read the repo, but **only if you point it there**: set `working_dir` to the repo root
(and `add_dirs` for any extra trees). Left unset, the spawned `codex` inherits the
bridge server's own working directory — not the repo under review — so it may not see
the files at all.

### 4. Synthesize

- **Dedup** candidates that point at the same file+line+mechanism; keep the one
  with the most concrete `why`.
- **Rank** by severity (HIGH → LOW), correctness over style.
- Report each finding with **provenance**: which model found it, which verified it,
  and the verdict.
- State **which models actually ran** and any skipped (CLI unavailable) — diversity
  is the whole value, so be honest when it was reduced.

### 5. Optional fix

Only if the user asked. You (the orchestrator) apply CONFIRMED findings directly,
then run the project's build/tests yourself. Do not delegate the fix unattended in
the same pass — review-then-fix keeps a human-auditable step.

## Fast mode (finders only, no cross-verify)

Skip cross-verification entirely: run the finder wave (step 2) — in parallel if more
than one model is connected — then dedup and synthesize directly. This is **one wave
— the fastest possible run**, but you lose the adversarial cross-check (a finding
only earns confidence by surviving a *different* model's refutation), so expect more
false positives. Reach for it for a low-stakes multi-model sanity sweep, or as the
unavoidable fallback when only one model is connected (nothing to cross-verify
against); use the full two-wave pipeline when correctness matters. **Say in the
report that cross-verification was skipped** — and, if only one model ran, that it
was a single-model review.

## Output format

A ranked list (table or JSON), each row: `file:line · SEVERITY · summary ·
found-by:<model> · verified-by:<model>:<verdict>`. Lead with a one-line note of which
models participated and at what tier/effort (and any user overrides) — each spawn's result
header reports the `model=… effort=…` it actually ran, so report that, not your intent.

## Using this from Claude Code, Antigravity, or Codex

The bridge tools and this pipeline are **host-agnostic**. Running it from any host
needs two things in place:

**1. The bridge MCP tools are connected**, so `gemini_agent` / `claude_agent` /
`codex_agent` are callable from your session:

- **Claude Code:** `make install-claude` — installs the plugin (tools + skills) from a
  local marketplace; `claude plugin install` copies the binary into a frozen, versioned
  cache, so the install doesn't track your checkout. (Tools only, no skills? `claude mcp
  add agent-bridge --scope user -- /abs/path/to/agent-bridge-mcp` by hand — not frozen.)
- **Antigravity (Gemini, via the `agy` CLI):** `make install-agy` — imports the MCP
  server *and* this skill. Use the make target, not a bare `agy plugin install
  <repo>`: `agy` copies the plugin manifests but not the built binary and does not
  expand Claude's `${CLAUDE_PLUGIN_ROOT}`, so the make target installs a frozen copy of
  the binary into agy's own plugin dir and repoints the imported MCP command at it.
- **Codex:** `make install-codex` — installs the plugin (tools + skill) from a local
  Codex marketplace. The MCP server is **bundled in the plugin** (its `.mcp.json` resolves
  `./agent-bridge-mcp` relative to the install), so `codex plugin add` copies the binary
  into Codex's frozen cache and wires up the tools — no separate `codex mcp add`.

Then call the tools for the models you want as reviewers — for diversity, prefer
families other than your host (from a Codex host lean on `gemini_agent` +
`claude_agent`; from a Claude Code host on `gemini_agent` + `codex_agent`; from an
Antigravity host on `claude_agent` + `codex_agent`).

**2. The orchestrator can see this playbook.** How a host surfaces it differs:

- **Claude Code / Antigravity:** loaded as a skill by the plugin install above —
  it triggers from the `description`.
- **Codex:** `make install-codex` installs this repo as a **Codex-format** plugin
  (`.agents/plugins/marketplace.json` + `plugins/agent-bridge/.codex-plugin/plugin.json`),
  so Codex surfaces the skill from its `description` just like Claude/Antigravity. Codex
  **cannot** consume this repo's Claude-format `.claude-plugin/` marketplace, and it
  requires a plugin's skills (and bundled MCP binary) to live *inside* the plugin root, so
  the make target ships a Codex marketplace and copies the canonical `./skills` and the
  built binary into the plugin dir for you.
  (Prefer not to install a plugin? You can still carry the playbook by hand — drop it into
  Codex's standing-instructions file `AGENTS.md` (per project or `~/.codex/AGENTS.md`), or
  paste the steps as the task prompt; nothing host-specific is required to *follow* the
  pipeline.)

## Caveats

- **Cost & latency.** Each finder and verifier is a full CLI spawn + model
  inference. Three finders + cross-verification = several heavyweight calls — much
  slower and pricier than in-process review. Scale the finder count to the stakes;
  for a small diff, two models may be plenty.
- **JSON robustness.** Models sometimes wrap JSON in prose or ``` fences. Instruct
  "ONLY JSON" (the templates do) and parse defensively — extract the largest JSON
  array/object from the reply rather than assuming the whole reply is JSON.
- **Inline-only context, and what it takes to widen it.** As spawned by the finder
  step (reason-only, no `working_dir`/`add_dirs`), `gemini_agent` / `claude_agent`
  finders have only the embedded diff to reason over — reason-only blocks unattended
  file edits and command execution, and nothing wires the repo in. `codex_agent`
  reason-only is a `--sandbox read-only` mode, so it *can* already read the repo and
  run read-only commands; you still give it the diff inline for a uniform, scoped
  input. If a finding genuinely needs wider context: `codex_agent` (default read-only) and
  `claude_agent` with `mode: "read"` (plan mode) both permit repo reads — point them at the
  repo with `working_dir` (else they read the bridge server's own directory). `gemini_agent`
  has **no read-only tier**, so `mode: "act"` is the only way to let it touch the repo — and
  that grants *full unattended execution* (`--dangerously-skip-permissions`: file writes +
  arbitrary commands), so reach for it sparingly, scope it with `working_dir`, and contain it
  further with `sandbox: true` (edits land in an isolated scratch dir instead of `working_dir`).
- **Delegation depth.** Fanning out spawns child agents; two independent safeguards keep
  a review round shallow. (1) The **hop guard** caps *depth*: the bridge reads
  `AGENT_HOP_DEPTH`, refuses to spawn once it reaches `AGENT_HOP_MAX` (default 2), and
  increments it for each child — bounding any A→B→A chain regardless of what an acting
  agent does. (2) Every reason-only child (the finders/verifiers here) is spawned with
  `AGENT_NO_DELEGATE=1`, and the bridge refuses to spawn from a process carrying that
  flag — so a reason-only reviewer genuinely **cannot** delegate further, including
  `codex_agent`'s read-only sandbox (which can still run read-only commands). A single
  review round, being all reason-only, therefore can't nest at all.
- **Tool behavior is authoritative in the tool descriptions.** This skill summarizes what
  each backend's `mode` (reason/read/act) / `working_dir` / `sandbox` options do, but the
  bridge's own MCP tool descriptions (generated from `cmd/agent-bridge-mcp/main.go`) are
  the source of truth — if they ever diverge from this summary, trust them.
- **Diversity is the point.** Prefer different families. If only one CLI is
  connected, this is a single-model review; report it as such.
