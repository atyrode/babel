# Triage: the issue lifecycle

## Ownership

This document owns the tracker: what a label means, how an issue enters, how a hold is written and
answered, what makes work pickable, and how an issue leaves. [`../AGENTS.md`](../AGENTS.md) owns
delivery — the gate, the boundaries, the changelog, what proves a change — and this document never
restates or relaxes it. A contradiction between the two is a finding, not a choice.

The model is [atyrode/manifold's](https://github.com/atyrode/manifold/blob/main/docs/TRIAGE.md),
adapted rather than copied. Babel is one plugin family, not a monorepo, so its areas are the
family's halves; it has one gate rather than four CI boundaries, so the CI policy stays in
`AGENTS.md` where it already lives. The label semantics, the six rules and the priority rubric are
deliberately identical, because an operator moving between the two repositories should not have to
learn a second vocabulary.

| Command            | What it proves                                                                               |
| ------------------ | -------------------------------------------------------------------------------------------- |
| `bun run triage`   | Rules T1–T6 below; `bun run triage:fix` applies the two that are bookkeeping                 |
| `bun run labels`   | The live labels match [`../.github/labels.yml`](../.github/labels.yml); `--apply` makes them |
| `bun run dispatch` | What an agent may pick up right now, in pick order                                           |

[`../.github/workflows/triage-policy.yml`](../.github/workflows/triage-policy.yml) runs the first
two hourly and on issue events. Nothing else writes to the tracker automatically, and **nothing
automatic ever closes an issue.**

## Label model

Four dimensions and two signals. [`../.github/labels.yml`](../.github/labels.yml) is the
inventory; this section is the meaning.

- **State** — exactly one on every open issue that is not a `tracking` umbrella:
  - `needs-triage` — the default. Not yet classified; nobody should pick it up.
  - `agent-ready` — scoped, prioritized and settled; an agent may claim it and open a pull request
    **without asking anyone**.
  - `needs-operator` — held for a decision only the operator can make, written as a decision block
    (§Holds).
  - `blocked` — waits on another issue or pull request named in the body.
- **Type** — `bug`, `enhancement`, `documentation`, `process`, `design`, `prerequisite`,
  `tracking`, `security`, `accessibility`.
- **Area** — where a code change lands: `area:store`, `area:doors`, `area:server`, `area:machine`,
  `area:feed`, `area:watch`, `area:jev`, `area:infra`. Required on `agent-ready` code work;
  `documentation` and `process` issues carry none, because they land nowhere in particular.
- **Priority** — `p0`–`p3` (§Priority rubric). Required on `agent-ready`.
- **Signals** — `aging`, applied and removed by the policy script from human activity, never by
  hand and never a reason to close anything; and `parked`, applied by hand to something kept
  deliberately with no current claim on attention. `parked` is not a state: a parked issue still
  carries one.
- **`cross-repo`** marks work needing a coordinated change in atyrode/manifold, atyrode/code or
  atyrode/dotfiles. It is a warning about sequencing, not a state.

The machine-checkable half, one rule per report row:

| Rule | Invariant                                                                        | `--fix`             |
| ---- | -------------------------------------------------------------------------------- | ------------------- |
| T1   | Exactly one state label on every open non-`tracking` issue                       | adds `needs-triage` |
| T2   | `agent-ready` carries one priority, and an area unless `documentation`/`process` | no                  |
| T3   | `blocked` names an issue or pull request, in its body or a comment               | no                  |
| T4   | `needs-operator` carries a `## Decision` block in its body or a comment          | no                  |
| T5   | `aging` is present exactly when the last human activity is over 14 days old      | adds/removes        |
| T6   | At most one priority label                                                       | no                  |

T1 and T5 are bookkeeping, so the script does them. The rest are judgement — choosing a state,
naming a blocker, writing a question — so the script reports them and a person or an agent running
§Runbooks fixes them. "Human activity" means the issue's creation or a comment on it; **a label
edit is not activity**, which is why a relabelled issue does not look fresh, and why the script
reads comment timestamps rather than `updatedAt`.

[`../scripts/triage-policy.test.ts`](../scripts/triage-policy.test.ts) proves all six against
constructed issues, including both sides of T5's fourteen-day boundary — the live tracker cannot
exercise that on demand, because nothing on it is fourteen days quiet at the moment you want to
check.

## Priority rubric

| Label | Meaning                                                                                                                                         |
| ----- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `p0`  | Confirmed security exposure reachable on a deployed hub, data loss, the hub down, or a blocked release. Work starts now.                        |
| `p1`  | A contract violation or a bug on a documented path; a `prerequisite` for p0/p1 work; a security design risk whose direction is already decided. |
| `p2`  | The default: an accepted bug, enhancement or finding.                                                                                           |
| `p3`  | Hygiene, consistency, nice-to-have.                                                                                                             |

Priority orders the ready queue. It is not a promise of a date, and `p3` work is not less correct,
only later.

## Intake

Every issue states a **Problem** and **Acceptance criteria**. An acceptance criterion is something
a reviewer can observe, not a description of the intended patch. Every new issue starts
`needs-triage`.

**An issue whose premise no longer exists is not a bug to fix.** Babel deleted a whole product
this month, and an issue written against the Go binary, the React shell, PostgreSQL or the fleet
may describe work that cannot be done and need not be. Check the premise before the priority:
[`parity.md`](parity.md) records what the retired product did and whether the plugin does it. Such
an issue is a `needs-operator` hold asking whether it still applies, or a close with a
`Disposition:` — never an `agent-ready` with a plausible-looking priority.

## Holds (`needs-operator`)

A hold is for a decision only the operator can make: the meaning of a contract, security posture,
a live or production action, scope and product direction, or spend. Everything else is a decision
the person or agent doing the work is expected to make. **Technical uncertainty is work to
diagnose, not a hold.**

Check recorded standing grants before raising one; an already-approved action does not need a
second approval. Every hold carries a decision block, in the body or a comment:

```
## Decision
Question: <one sentence>
Options:
- A — <option and consequence>
- B — <option and consequence>
Recommended: <letter> — <why>
Unblocks: <labels/state to apply and what work starts when decided>
```

Holds never resolve by silence, by deadline, or by a second agent's opinion. The triage runbook
presents every open hold to the operator on each run and records the technical outcome as a
`## Decision recorded (<date>)` comment — the selected option, its scope and constraints — then
applies `Unblocks`.

Two rules about what goes in that comment. An agent **never** writes `## Decision recorded`
without an explicit operator answer in its own session or a comment the operator authored. And it
records the technical outcome only: **permission to act is not permission to publish the exchange
that granted it**, so no public record quotes, paraphrases or narrates a private conversation as
evidence of authorization.

## Claims and dispatch

Claim before the first substantive commit, with a comment on the issue:

```
Claim: <branch> — <one-line scope>
```

Release it, when you stop, with `Release: <reason>`. Both are plain comments.

`bun run dispatch` lists the ready queue in pick order: `p0` → `p3`, then oldest first. It refuses
to list new work while an open non-draft pull request needs review, correction or merge, and it
skips an issue that already has an open pull request or a claim from someone else newer than 24
hours with no later `Release:`. Take at most **two** concurrent claims per session.

Branch names are `<prefix>/<issue>-<slug>`, where the prefix is the commit prefix for the work:
`feat`, `fix`, `docs`, `chore`, `test` or `ci`. Work in your own worktree.

## Pull requests

`AGENTS.md` Delivery owns the rest — conventional-commit titles, the changelog bullet, the gate.
What the tracker adds:

- Use `Closes #N` only when merging resolves all of the issue's acceptance. Otherwise `Refs #N`,
  and name what remains.
- Keep a pull request draft while its issue is `needs-operator`.
- An issue being `agent-ready` is not authority for anything outside its acceptance criteria.

## Exit

Closing as not planned requires a comment beginning `Disposition:` and naming one of:

- `duplicate of #N`
- `out of scope` — with the contract or the boundary that puts it outside
- `superseded by #N` — after preserving anything unique to the closed issue
- `premise removed` — the thing it describes no longer exists; name what deleted it
- `invalid` — with the reason

`aging` is a quiet-issue signal and never grounds for closure. Closing is reversible; a lost
requirement is not, so when a close is uncertain, hold it and ask.

## Runbooks

### triage

1. `bun run triage:fix`, then `bun run triage`. Repair by hand every violation the fix pass left:
   choose the state, name the blocker, write the decision block.
2. For each `needs-triage` issue, oldest first: check for duplicates; check the premise still
   exists (§Intake); ensure `## Problem` and `## Acceptance` are there; assign type, area and
   priority, then exactly one state — or close with a `Disposition:`.
3. Holds pass. Write a decision block for every `needs-operator` issue lacking one, researching
   the code so the options are concrete rather than "what should we do". With the operator
   present, ask; then record each outcome and apply `Unblocks`. With no operator in the session,
   write the blocks and stop — never continue held implementation while waiting.
4. `bun run triage` once more; end with the digest.

### dispatch

1. `bun run dispatch`. If it reports open pull requests needing attention, deal with those first;
   do not claim new work.
2. Read the issue's latest claim before starting, and post a `Claim:` for the phase you are
   resuming.
3. Respect the two-claim limit.
