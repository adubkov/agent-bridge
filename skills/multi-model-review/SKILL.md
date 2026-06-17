---
name: multi-model-review
description: Use when the user wants a CROSS-MODEL / multi-model code review of a diff — fan the change out to several different models (Gemini, Claude, Codex) as independent reviewers via the agent-bridge MCP tools, then cross-verify each finding with a DIFFERENT model before reporting. Good for high-stakes diffs where you want uncorrelated model perspectives, not just one model's opinion. Host-agnostic — the orchestrator can be a Claude Code, Antigravity (Gemini), or Codex session. Requires the agent-bridge MCP server (tools `antigravity_agent` / `claude_agent` / `codex_agent`).
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
skill (a Claude Code, Antigravity/Gemini, or Codex session). You set up the change
(point reviewers at the repo, or gather the diff), call the bridge tools, run the
verification round, and synthesize. The reviewers
are the *spawned* bridge agents; **you do not add your own findings on a diff you
authored** — that reintroduces the very author bias this skill exists to avoid (see
"Independence" below). This pipeline is the same regardless of which host you run
from; see "Using this from Claude Code, Antigravity, or Codex" below for per-host
setup.

## Prerequisites

- The agent-bridge MCP server is connected in your host. Check which of
  `antigravity_agent`, `claude_agent`, `codex_agent` are actually available (and their
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

| Tier | `claude_agent` | `codex_agent` | `antigravity_agent` |
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

1. **Different family, fresh context** (e.g. `antigravity_agent` / `codex_agent` from a
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

1. **Set up the change** — decide how reviewers see it: point them at the repo
   (default) or embed the diff inline (fallback).
2. **Fan out finders** — one read-only call per reviewer model; each pulls the diff itself.
3. **Cross-verify** — each candidate checked by a *different* model.
4. **Synthesize** — dedup, rank, report with provenance.
5. **(optional) Fix** — only if asked.

### 1. How reviewers see the change

Two modes. **Default: point each reviewer at the repo and let it pull the diff itself.**
This removes the orchestrator as a lossy middleman — the failure mode where you
summarize or truncate a big diff into the `task` and finders flag phantoms or miss real
bugs — and it lets each reviewer read *around* the change (call sites, type defs, guards)
the way an inline hunk never can. **Fallback: embed the diff inline** ("Inline mode"
below) when the change isn't in a repo the reviewers can reach, or you want byte-identical
input across finders.

**Repo-reading, read-only (the default).** Give every reviewer:

- `working_dir` = the repo root (absolute path). Without it the spawned agent runs in the
  *bridge server's* own cwd, not your repo, and sees nothing.
- the **exact** diff command in the `task`, so every reviewer judges the *same* change —
  e.g. `git diff --function-context <base>...<head>` (a PR ref, `@{upstream}...HEAD`, or
  paths the user named). Tell it to run that, review those changes, and read surrounding
  files only as needed.
- the per-backend **read-only recipe** below. Each is verified to read the repo, run
  read-only `git`, stay write-safe, and keep the no-delegate freeze:

  | Backend | Read-only recipe | Why |
  |---|---|---|
  | `claude_agent` | `mode: "read"` | `--permission-mode plan`: read/grep/glob + read-only Bash (incl. `git diff`); no edits or commands. |
  | `codex_agent` | `mode: "read"` | `--sandbox read-only`: reads + read-only commands; no writes. |
  | `antigravity_agent` | `mode: "reason"` **+ `sandbox: true`** | agy has no read tier, and its `reason` mode **does not block writes** — it will edit files unattended if pointed at a writable `working_dir`. `sandbox: true` confines any write to a throwaway scratch dir while reads of `working_dir` and read-only `git` still work, and `reason` keeps the delegation freeze. (Do **not** use `mode: "act"`+`sandbox`: also write-safe, but `act` forfeits the freeze — see Delegation.) |

  Always add an **enforcing line** to the `task`: *"Inspect only — do not edit, create, or
  delete files, do not run state-changing commands, and do not delegate to other agents."*
  For agy the sandbox is the real guarantee and the line is defense-in-depth; for
  claude/codex the mode already enforces it.

First make sure the repo is in the state you want reviewed — the PR branch checked out and
the base ref present locally — since each reviewer reads the live working tree.

**Context is now (almost) free.** Because reviewers read the repo, the old "how much diff
to embed" ladder mostly dissolves: each can pull `--function-context`, open whole files,
and chase cross-file callers / guards / type-defs itself. Missing **code** context stops
being the orchestrator's problem. What model diversity *still* uniquely buys is
**external/runtime** knowledge — how a CLI, library, or framework actually behaves — which
no amount of context substitutes for; that is why cross-family reviewers matter as much as
ever.

**Inline mode (fallback).** Gather the diff yourself with `git diff --function-context`
(`-W`) and **embed it verbatim** in each finder's `task` — never summarize, paraphrase, or
truncate (a lossy diff makes finders flag phantom issues and miss real ones; if it is too
big, drop *whole files*, do not compress). Reach for this only when the change isn't in a
repo the reviewers can access (a pasted patch, a PR not fetched locally), or you need
strictly byte-identical input across finders. In inline mode keep finders reason-only
(omit `mode`) with no `working_dir`, and tell them to reason only over the inline diff.
(Per-backend reason-only nuance there: `claude_agent` passes `--tools ""` → genuinely no
tools; `codex_agent` is a read-only sandbox; `antigravity_agent` is the exception — it has
no tool-disable flag and, with no `working_dir`, runs in the **bridge server's own cwd**,
which can itself be a writable tree (the host launches the server in your project), so an
inline agy finder can still edit files unattended. Give agy `sandbox: true` even in inline
mode to confine any write to a throwaway scratch dir.)

### 2. Fan out finders (read-only, in parallel)

For each reviewer model, call its tool with the finder prompt below. **Issue them in a
single message** — a host that runs independent tool calls concurrently (e.g. Claude Code)
fans them out in parallel; a host that serializes still runs them all, one after another.

| Param | Repo-reading (default) | Inline (fallback) |
|---|---|---|
| `task` | finder prompt + the exact diff command + the "inspect only" enforcing line | finder prompt + the verbatim embedded diff |
| `mode` / `sandbox` | per the read-only recipe (claude/codex `read`; agy `reason` **+ `sandbox: true`**) | omit `mode` (reason-only); **agy still needs `sandbox: true`** (its cwd may be writable — see caveat) |
| `working_dir` | the repo root (absolute path) | leave unset |
| `model` / `effort` | per **Model & effort selection** above (default `deep`; honor overrides) | same |
| `timeout_seconds` | 300–600 (repo-reading is agentic — lean higher for big repos) | 300–600 |

> **Scope each reviewer tightly.** A repo-reading reviewer is agentic and can wander the
> tree (and, for codex, burn usage quota) if you let it. In the `task`: name the exact diff
> command and the files in scope, tell it to review *only* that change (reading around it as
> needed) and not to explore unrelated parts of the repo, and keep the enforcing "inspect
> only — no edits, state-changing commands, or delegation" line in every prompt. (That line
> is load-bearing for agy, whose sandbox confines writes but whose model is otherwise free
> to roam; for claude/codex the read mode already blocks writes.)

Give every model the **same brief** so the diversity comes from the model, not the prompt.
(You can layer distinct angles later; start uniform.)

**Finder prompt template (repo-reading default):**

> You are an independent senior reviewer with `working_dir` set to a git repo. Run
> `git diff --function-context <base>...<head>` to see the change under review, then review
> it for CORRECTNESS bugs (logic errors, wrong conditions, off-by-one, nil/undefined,
> missing error handling, concurrency hazards, broken call sites). You MAY read the
> surrounding files, callers, and type definitions for context, but **review only that
> diff's changes**. **Inspect only — do NOT edit/create/delete files, run state-changing
> commands, or delegate to other agents.** Be specific: name the trigger and the wrong
> result. Do not nitpick style.
> Return **ONLY** a JSON array (max 8) of objects:
> `{"file": "...", "line": "...", "severity": "HIGH|MEDIUM|LOW", "summary": "...", "why": "concrete inputs/state → wrong result"}`.
> No prose outside the JSON. If you find nothing, return `[]`.

For **inline mode**, replace the first two sentences with: *"Review the unified diff below
for CORRECTNESS bugs … Reason only over the inline diff — do NOT read files, list
directories, or run commands; everything you need is below,"* then append `=== DIFF ===`
and the verbatim diff.

Tag each returned finding with the **finder model** that produced it.

### 3. Cross-verify (each finding by a *different* model)

Pool all candidates and assign each a **verifier model ≠ the finder model**
(round-robin across the participating models: e.g. gemini→claude, claude→codex,
codex→gemini; with two models, just use the other; with only one model connected you
cannot cross-verify at all — use Fast mode and report it as a single-model review).
Then **dispatch every verifier call in a single message**, using the same read-only
repo-reading recipe as the finders (or inline, matching whatever the finders used).

This is a **two-wave** pipeline: all finders, then all verifiers. The one
unavoidable wait is *between* the waves — a finding can't be verified before it
exists. On a host that dispatches tool calls concurrently (e.g. Claude Code) each
wave runs in parallel, so total time ≈ slowest finder + slowest verifier; on a host
that serializes tool calls the two waves still hold but wall-clock is the sum.

**Verifier prompt template (repo-reading default):**

> Another reviewer flagged the finding below. With `working_dir` set to a git repo, run
> `git diff --function-context <base>...<head>` — and read call sites, guards, and type
> defs as needed — to decide if it is real. **Inspect only — no edits, state-changing
> commands, or delegation.** Answer with ONLY one JSON object:
> `{"verdict": "CONFIRMED|PLAUSIBLE|REFUTED", "reason": "quote the line or guard that proves it"}`.
> CONFIRMED = you can name the trigger and wrong result. PLAUSIBLE = mechanism is real but
> the trigger is uncertain. REFUTED = the code visibly contradicts the finding, or you can
> quote a guard that defuses it.
> === FINDING ===
> &lt;the candidate&gt;

For **inline mode**, swap the run-`git` sentence for *"Decide if it is real, reasoning only
over the inline diff — do NOT read files or run commands,"* and append `=== DIFF ===` plus
the verbatim diff.

Keep **CONFIRMED** and **PLAUSIBLE**; drop **REFUTED**. (Prototype: one cross-vote per
finding. For higher confidence, send to BOTH other models and require a majority — note the
extra cost.)

**Out-of-diff context.** A repo-reading verifier can chase call sites, guards, and type
defs across files, so a **REFUTED** backed by a quoted guard is now trustworthy — this is a
real advantage of the repo-reading default over inline. In **inline mode** a reviewer sees
only the diff and cannot prove a guard's *absence*: there, a verifier that can't find a
guard should answer **PLAUSIBLE**, not REFUTED. If an inline verdict hinges on out-of-diff
context, re-run that one finding in repo-reading mode (the recipe in step 1, `working_dir`
set) rather than trusting a blind REFUTED.

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

**1. The bridge MCP tools are connected**, so `antigravity_agent` / `claude_agent` /
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
families other than your host (from a Codex host lean on `antigravity_agent` +
`claude_agent`; from a Claude Code host on `antigravity_agent` + `codex_agent`; from an
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
- **Repo-reading default vs. inline fallback.** Reviewers read the repo themselves (the
  recipe in step 1), so the orchestrator never compresses a diff into the `task` — killing
  the failure mode where a trimmed/paraphrased diff produces phantom findings or hides real
  ones. The costs: a repo-reading run is *agentic* (more tool calls, slower; codex can burn
  usage quota), and reviewers can vary slightly in what they choose to read — so pin the
  **diff command** in every prompt so they all judge the same change. Use **inline mode**
  only when the change isn't in a reachable repo, or you need byte-identical input.
- **`antigravity_agent` `reason` is NOT write-safe.** Unlike `claude_agent` (`--tools ""` →
  no tools) and `codex_agent` (`--sandbox read-only`), agy's `reason` tier merely omits the
  permission-bypass flag — but agy still performs **unattended file writes** non-
  interactively when pointed at a writable `working_dir` (verified: it created a file). So a
  repo-reading agy reviewer **must** add `sandbox: true`; the sandbox (not the absence of the
  bypass flag) is what protects your tree. In *inline* mode no `working_dir` is wired in, so
  agy runs in the bridge server's own cwd — but that cwd can itself be a writable tree (the
  host typically launches the server in your project dir), so inline agy is **not** inherently
  safe either; give it `sandbox: true` in inline mode too for a hard guarantee.
- **Delegation depth.** Fanning out spawns child agents; two independent safeguards keep a
  review round shallow. (1) The **hop guard** caps *depth*: the bridge reads
  `AGENT_HOP_DEPTH`, refuses to spawn once it reaches `AGENT_HOP_MAX` (default 2), and
  increments it for each child — bounding any A→B→A chain. (2) Every **non-acting** child —
  `reason` or `read` mode, which is every reviewer in the recommended recipes (claude
  `read`, codex `read`, agy `reason`+`sandbox`) — is spawned with `AGENT_NO_DELEGATE=1`, and
  the bridge refuses to spawn from a process carrying that flag, so a reviewer genuinely
  **cannot** delegate further. A round built from those recipes therefore can't nest at all.
  The one way to lose this: agy in `mode: "act"` — an *acting* child gets no freeze and is
  bounded only by the hop guard, which is exactly why step 1 uses agy `reason`+`sandbox`,
  not `act`+`sandbox`.
- **Tool behavior is authoritative in the tool descriptions.** This skill summarizes what
  each backend's `mode` (reason/read/act) / `working_dir` / `sandbox` options do, but the
  bridge's own MCP tool descriptions (generated from `cmd/agent-bridge-mcp/main.go`) are
  the source of truth — if they ever diverge from this summary, trust them.
- **Diversity is the point.** Prefer different families. If only one CLI is
  connected, this is a single-model review; report it as such.
