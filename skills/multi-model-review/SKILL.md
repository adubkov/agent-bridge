---
name: multi-model-review
description: Use when the user wants a CROSS-MODEL / multi-model code review of a diff — fan the change out to several different models (Gemini, Claude, Codex) as independent reviewers via the agent-bridge MCP tools, then cross-verify each finding with a DIFFERENT model before reporting. Good for high-stakes diffs where you want uncorrelated model perspectives, not just one model's opinion. Host-agnostic: the orchestrator can be a Claude Code, Antigravity (Gemini), or Codex session. Requires the agent-bridge MCP server (tools `gemini_agent` / `claude_agent` / `codex_agent`).
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
the cross-model verdicts (a finding two *other* models CONFIRM is hard to dismiss)
and report findings faithfully even when you disagree with them.

## Pipeline

1. **Gather the diff** — yourself, inline.
2. **Fan out finders** — one reason-only call per reviewer model, diff embedded.
3. **Cross-verify** — each candidate checked by a *different* model.
4. **Synthesize** — dedup, rank, report with provenance.
5. **(optional) Fix** — only if asked.

### 1. Gather the diff

Run it yourself (`git diff @{upstream}...HEAD`, `git diff main...HEAD`, or a PR/
path the user named) and **embed the diff text inline** in each finder's `task`.
Do NOT tell the sub-agents to run git or read files — that would force
`allow_tools: true` (unattended execution). Inline keeps every finder
**reason-only and safe**. If the diff is very large, scope to the files in review.

### 2. Fan out finders (reason-only, in parallel)

For each reviewer model, call its tool with the finder prompt below, the diff
embedded. **Send them in a single message (parallel tool calls)** so they run
concurrently.

| Param | Value |
|---|---|
| `task` | The finder prompt + the embedded diff (see template). |
| `allow_tools` | **omit it** (false) — finders only reason over the inline diff. |
| `timeout_seconds` | 300–600 depending on diff size. |
| `working_dir` / `add_dirs` | not needed (pure reasoning over inline text). |

Give every model the **same brief** so the diversity comes from the model, not the
prompt. (You can layer distinct angles later; start uniform.)

**Finder prompt template:**

> You are an independent senior reviewer. Review the unified diff below for
> CORRECTNESS bugs (logic errors, wrong conditions, off-by-one, nil/undefined,
> missing error handling, concurrency hazards, broken call sites). Be specific:
> name the trigger and the wrong result. Do not nitpick style.
> Return **ONLY** a JSON array (max 8) of objects:
> `{"file": "...", "line": "...", "severity": "HIGH|MEDIUM|LOW", "summary": "...", "why": "concrete inputs/state → wrong result"}`.
> No prose outside the JSON. If you find nothing, return `[]`.
> === DIFF ===
> &lt;embed the unified diff here&gt;

Tag each returned finding with the **finder model** that produced it.

### 3. Cross-verify (each finding by a *different* model)

Pool all candidates and assign each a **verifier model ≠ the finder model**
(round-robin across the participating models: e.g. gemini→claude, claude→codex,
codex→gemini; with two models, just use the other). Then **dispatch every verifier
call in a single message (parallel tool calls)**, each reason-only with the diff
embedded — just like the finder wave, the verifiers run concurrently.

This is a **two-wave** pipeline: finders all run in parallel, then verifiers all run
in parallel. The one unavoidable wait is *between* the waves — a finding can't be
verified before it exists — so total time ≈ slowest finder + slowest verifier, not
the sum of all calls.

**Verifier prompt template:**

> Another reviewer flagged this finding in the diff below. Decide if it is real.
> Answer with ONLY one JSON object:
> `{"verdict": "CONFIRMED|PLAUSIBLE|REFUTED", "reason": "quote the line that proves it"}`.
> CONFIRMED = you can name the trigger and wrong result. PLAUSIBLE = mechanism is
> real but the trigger is uncertain. REFUTED = the code doesn't say that, or it's
> guarded elsewhere (quote the guard).
> === FINDING ===
> &lt;the candidate&gt;
> === DIFF ===
> &lt;embed the unified diff here&gt;

Keep **CONFIRMED** and **PLAUSIBLE**; drop **REFUTED**. (Prototype: one cross-vote
per finding. For higher confidence, send to BOTH other models and require a
majority — note the extra cost.)

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

## Fast mode (single parallel pass)

For low-stakes diffs, skip cross-verification entirely: run the finder wave (step 2)
in parallel, then dedup and synthesize directly. This is **one wave — the fastest
possible run**, but you lose the adversarial cross-check (a finding only earns
confidence by surviving a *different* model's refutation), so expect more false
positives. Use it for a quick multi-model sanity sweep; use the full two-wave
pipeline when correctness matters. **Say in the report that cross-verification was
skipped.**

## Output format

A ranked list (table or JSON), each row: `file:line · SEVERITY · summary ·
found-by:<model> · verified-by:<model>:<verdict>`. Lead with a one-line note of
which models participated.

## Using this from Claude Code, Antigravity, or Codex

The bridge tools and this pipeline are **host-agnostic**. Running it from any host
needs two things in place:

**1. The bridge MCP tools are connected**, so `gemini_agent` / `claude_agent` /
`codex_agent` are callable from your session:

- **Claude Code:** `make install-claude` (tools only) or `make plugin-install`
  (tools + skills).
- **Antigravity (Gemini, via the `agy` CLI):** `make install-agy` (or
  `agy plugin install <repo>`) — imports the MCP server *and* this skill.
- **Codex:** `codex mcp add agent-bridge -- /abs/path/to/agent-bridge-mcp`
  (Codex supports external stdio MCP servers).

Then call the tools for the models you want as reviewers — for diversity, prefer
families other than your host (from a Codex host lean on `gemini_agent` +
`claude_agent`; from a Claude Code host on `gemini_agent` + `codex_agent`; from an
Antigravity host on `claude_agent` + `codex_agent`).

**2. The orchestrator can see this playbook.** How a host surfaces it differs:

- **Claude Code / Antigravity:** loaded as a skill by the plugin install above —
  it triggers from the `description`.
- **Codex:** Codex has a plugin/marketplace system (`codex plugin marketplace add`
  + `codex plugin add`) that mirrors this repo's local marketplace, but it does not
  surface Claude-format *skills* the way Claude Code / Antigravity do. The reliable
  path is
  to drop this playbook into Codex's standing-instructions file (`AGENTS.md`, per
  project or `~/.codex/AGENTS.md`), or to paste the steps as the task prompt.
  Nothing host-specific is required to *follow* the pipeline — it is just the steps
  above plus the bridge tools.

## Caveats

- **Cost & latency.** Each finder and verifier is a full CLI spawn + model
  inference. Three finders + cross-verification = several heavyweight calls — much
  slower and pricier than in-process review. Scale the finder count to the stakes;
  for a small diff, two models may be plenty.
- **JSON robustness.** Models sometimes wrap JSON in prose or ``` fences. Instruct
  "ONLY JSON" (the templates do) and parse defensively — extract the largest JSON
  array/object from the reply rather than assuming the whole reply is JSON.
- **Inline-only context.** Reason-only finders see only the embedded diff, not the
  surrounding repo. If a finding clearly needs wider context, you can re-run that
  one finder with `allow_tools: true` and a scoped `working_dir` for a repo-aware
  pass — heavier and opt-in, not the default.
- **Delegation depth.** Fanning out spawns child agents; the bridge's hop guard
  (`AGENT_HOP_MAX`) caps nesting. Reason-only finders cannot spawn further, so a
  single review round stays shallow.
- **Diversity is the point.** Prefer different families. If only one CLI is
  connected, this is a single-model review; report it as such.
