# Sandbox threat model

This document exists so that a reviewer holding a run receipt can check the boundary it declares
against a written one. It states what contains Babel's work, what does not, who owns each control,
and which residuals are accepted rather than mitigated. `SPEC.md` §2.6 and §2.8 state the same
boundary as product rules; this is the reasoning under them.

Section 7 names the changes that make this document wrong. Whoever makes one of them updates it in
the same change.

## 1. What runs, and where

Babel executes in three places and holds a process in none of them.

- **The server half** runs in the hub, answering doors and ticking the conductor on whatever wake
  the hub gives it. It has no open database handle, no port, no timer and no credential: a `batch`
  is its transaction and a service binding is its secret.
- **The machine half** is a Bun bundle the hub installs and runs as a **job** on an enrolled
  machine: `scan`, `archive`, `prepare`, and nothing else
  (`atyrode.babel/manifest.json`; `atyrode.babel/machine/main.ts`). It reads
  session logs, runs restic, and writes sealed outputs. It never reaches a model.
- **A run that reaches a model is a Code session.** Babel composes a prompt and posts it through
  `atyrode.code.runSession`. It chooses no model, no thinking level and no account, holds no
  provider credential, and owns no process to reap. The session's containment is Code's, and §4
  says exactly what Babel may therefore assert about it.

## 2. What is being contained, and why

**The corpus is untrusted data.** Babel's subject is the operator's complete archived conversation
history across three harnesses, and `SPEC.md` §3 classifies all of it as evidence to analyze,
never instructions to follow: a transcript can carry malicious text copied from issues, web pages,
repositories, tool output or prior agents.

That is the threat, and it is not principally a malicious model. It is an ordinary, cooperative
model that read attacker-authored text inside the corpus and then acted on it — the more likely
failure of the two, and the one no amount of model quality removes.

Two things follow. The reading of the corpus (the machine half) must be confined so that a hostile
log cannot become host effects on the operator's machine. And the analysis of the corpus (the Code
session) must be confined by whoever gives that session its tools, which is not Babel.

## 3. The machine half's boundary

Every job runs in the hub's own job sandbox: a filesystem built from `/proc`, `/dev`, the job's own
tmpfs home and the artifacts the operation declares — so it carries no libc unless the operation
names the machine's native closure, and what is not declared does not exist inside. The manifest
is the whole of what each operation may touch, and it is committed, reviewed code.

| Operation | Reads | Writes | Network | Ceilings |
| --- | --- | --- | --- | --- |
| `scan` | `~/.omp`, `~/.codex`, `~/.claude`, read-only | the sealed output lease | **none** | 10 min, 1 GiB, 64 processes, 64 MiB of output |
| `prepare` | the same three, read-only | the sealed output lease | **none** | 30 min, 1 GiB, 64 processes, 512 MiB of output |
| `archive` | the same three, read-only | the sealed output lease, and the restic repository | **host** | 10 min, 1 GiB, 64 processes |

Four properties of that table carry the weight:

- **No operation writes into the corpus.** The session directories are bound read-only. A job
  cannot edit the evidence it read.
- **Two of the three have no network at all.** Reading and preparing the corpus — the two
  operations that touch the most hostile bytes — have no route out, so a log that persuades
  something to exfiltrate has nothing to exfiltrate through.
- **`archive` has host network because restic must reach the repository**, and its one secret
  arrives through a loopback service proxy the engine opens for that job alone: a capability minted
  per job, not the operator's upstream credential. The repository password reaches restic as
  `RESTIC_PASSWORD` in the **child's** environment only — never argv, never the parent's
  environment, never a receipt — and each child gets a minimal environment, so no ambient
  `RESTIC_*` variable can redirect an archive (`atyrode.babel/machine/restic.ts`).
- **Nothing deletes.** There is no `forget`, no `prune` and no `repair` code path. A compromised
  `archive` could write a snapshot; it has no verb that removes one.

The residual here is the hub's own sandbox: Babel declares what it needs and the hub constructs the
confinement. A defect in that construction is outside this document and inside Manifold's.

## 4. The model session's boundary, and why Babel declares nothing about it

Code owns the disposable sandbox and the credential isolation, because Code owns the profile, the
provider credential and the omp controller. A second sandbox in Babel would duplicate the boundary
while splitting responsibility for it.

So **Babel makes no containment declaration and no receipt of Babel's asserts one.** What a receipt
records is the container that answered, the model, the usage and the cost — facts Code reported —
and a reviewer who needs to know how that session was confined reads Code's declaration, not this
document. Claiming otherwise would be Babel describing a boundary it neither builds nor observes.

What Babel does own about that session is the **material**, and it is the one real control on this
side of the line (`SPEC.md` §2.6; `atyrode.babel/machine/prepare.ts`):

- A run reads `/inputs/material`, an immutable sealed selection bound read-only. It is not a live
  view of the corpus: the bytes were fixed when the preparation was sealed, and a session still
  being written is excluded outright, because a scope whose bytes move has no stable identity.
- Babel's own run transcripts are excluded unless a preset asks for them, so Babel does not read
  itself by accident.
- The index records, per session, the selector, the file and the digest it was served at. Every
  citation is checked against it, and a path the material does not name or a digest that does not
  match refuses the whole answer as `unknown-reference`.

That last property is the one worth stating as security rather than as correctness: a model cannot
cite its way to a file it was not given, and it cannot quietly substitute different bytes for the
ones a reviewer will later read.

## 5. Residuals, ranked by reachability

Most reachable first. A residual discovered in the system belongs in this list in the same change.

1. **Exfiltration through the model is possible and accepted.** The material is sent to a provider
   as prompt content; that is what analysis is. Anything in the selected corpus reaches the
   provider by design, and nothing distinguishes an analysis prompt from a payload. The levers are
   what goes into the selection and who the provider is — not a filter, and both are the operator's.
2. **Prompt injection from the corpus is the live attack.** Archive content is attacker-influenced
   by contract, and it is read by a session with whatever tools Code gives it. Babel's mitigations
   are narrow and worth naming precisely: the answer must be one fenced block matching a strict
   schema, every citation must resolve against the material's index, and a run's output becomes
   records only after that check. What Babel cannot mitigate is what the session does with its own
   tools while it reads; that boundary is Code's.
3. **The provider credential is Code's, and Babel never sees it.** This removes a residual rather
   than adding one, but it relocates it: a compromise of the session reaches whatever credential
   Code placed there, and Babel's receipt cannot tell a reviewer what that was.
4. **The material is a second copy of sensitive bytes.** A preparation seals real session logs into
   a job output lease, which is then bound into a session's sandbox. The corpus's sensitivity
   travels with it, and a machine that may run `prepare` is a machine that may read every session
   the selection names.
5. **Nothing scans a preparation before a model reads it.** The retired product had a deterministic
   secret preflight; the plugin has none, which `docs/parity.md` records. A credential pasted into
   a transcript years ago is sent to the provider along with everything else.
6. **The trust base is the hub, the machine and Code.** The hub constructs the job sandbox, holds
   the store and mints the service capability; the machine's owner binds `restic` and the native
   closure; Code confines the session. Babel audits none of the three and depends on all of them.
7. **A shared kernel shares microarchitecture.** No claim is made against timing or
   speculative-execution side channels, and none is implied.

## 6. What the operator is asked to accept, in one sentence each

- Running an analysis means sending selected archived conversations to a model provider.
- Running `archive` means a job on that machine can write to the repository and cannot delete from
  it.
- Enabling a machine for `prepare` means that machine can read every session the selection names.
- Nothing Babel runs scans that material for secrets first.

## 7. What would invalidate this document

- **Babel launching a process that reaches a model.** The declaration boundary in §4 exists because
  it does not. A launcher of Babel's own puts the containment question back on this side of the
  line and requires a declared backend with measured properties.
- **A machine operation gaining network, or write access to the corpus.** Both are manifest facts
  today, and §3's table is the claim; either change rewrites it.
- **A fourth machine operation**, or one whose secret arrives by any path other than the job's own
  service binding.
- **A secret preflight landing.** Residual 5 disappears and the accepted-risk list in §6 shrinks by
  one line.
- **Citations ceasing to be checked against the material index**, which is the only mechanism that
  makes a model's claim about the corpus verifiable.
