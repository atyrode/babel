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
  machine: `scan`, `archive`, `prepare`, `verify`, and nothing else
  (`babel/manifest.json`; `babel/machine/main.ts`). It reads
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

| Operation | Reads                                         | Writes                                                                      | Network  | Ceilings                                       |
| --------- | --------------------------------------------- | --------------------------------------------------------------------------- | -------- | ---------------------------------------------- |
| `scan`    | `~/.omp`, `~/.codex`, `~/.claude`, read-only  | the sealed output lease                                                     | **none** | 10 min, 1 GiB, 64 processes, 64 MiB of output  |
| `prepare` | the same three, read-only                     | the sealed output lease                                                     | **none** | 30 min, 1 GiB, 64 processes, 512 MiB of output |
| `archive` | the same three, read-only                     | the sealed output lease, and the restic repository                          | **host** | 10 min, 1 GiB, 64 processes                    |
| `verify`  | the restic repository, and none of the corpus | the sealed output lease, and a restore target inside a location it declares | **host** | 60 min, 1 GiB, 64 processes                    |

Five properties of that table carry the weight:

- **No operation writes into the corpus.** The session directories are bound read-only where they
  are bound at all, and `verify` does not mount them: it reads a repository, not a harness. A job
  cannot edit the evidence it read.
- **Two of the four have no network at all, and they are the two that read the most hostile
  bytes.** Reading and preparing the corpus have no route out, so a log that persuades something
  to exfiltrate has nothing to exfiltrate through. What the other two reach is one restic
  repository.
- **`archive` and `verify` have host network because restic must reach the repository**, and their
  one secret arrives through a loopback service proxy the engine opens for that job alone: a
  capability minted per job, not the operator's upstream credential. The repository password
  reaches restic as `RESTIC_PASSWORD` in the **child's** environment only — never argv, never the
  parent's environment, never a receipt — and each child gets a minimal environment, so no ambient
  `RESTIC_*` variable can redirect an archive or aim a verification at another repository
  (`babel/machine/restic.ts`).
- **A restore writes corpus bytes onto the machine, and the sandbox is what bounds where.**
  `verify` restores a catalogued session to prove it comes back byte for byte, so those bytes
  land outside the repository: under the job's own managed cache location, or in the target the
  caller named. Either path must be inside a location the operation declares writable, or the
  sandbox refuses the write — what stops a restore landing somewhere else is the manifest and the
  hub's enforcement of it, not the operation's good behaviour. The scratch directory is removed
  once the comparison is made, and that is cleanup rather than a boundary: an operation killed
  between the restore and the removal leaves the bytes where they fell (residual 4).
- **Nothing deletes, and the absence is enforced rather than incidental.** The verbs restic may be
  asked for are a closed set of eight — `cat`, `init`, `backup`, `snapshots`, `check`, `ls`,
  `dump`, `restore` — and every invocation is built by the one function that admits a verb or
  throws (`RESTIC_VERBS` and `resticArgv`, `babel/machine/restic.ts`), with a test that
  pins the refusal of `forget`, `prune`, `repair` and `unlock`. A compromised `archive` could
  write a snapshot and a compromised `verify` could read one; neither has a verb that removes one,
  and a fifth destructive verb costs a deliberate edit to a named list and a failing test rather
  than a moment's inattention.

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
side of the line (`SPEC.md` §2.6; `babel/machine/prepare.ts`):

- A run reads `/inputs/material`, an immutable sealed selection bound read-only. It is not a live
  view of the corpus: the bytes were fixed when the preparation was sealed, and a session still
  being written is excluded outright, because a scope whose bytes move has no stable identity.
- Babel's own run transcripts are excluded unless a preset asks for them, so Babel does not read
  itself by accident.
- The selection is scanned for likely credentials before it is sealed, and a matched span is
  replaced by a marker naming its class and the locator of the original
  (`babel/machine/preflight.ts`). The scan is deterministic — patterns and an entropy
  heuristic, no model and no network — so the same bytes redact the same way on any machine, and
  the locator resolves only against the log this machine holds. A preparation may refuse the
  scope instead, and either way the receipt carries what was found by class and never by value.
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
   what goes into the selection and who the provider is, and both are the operator's. The secret
   preflight narrows this for one class of content — a likely credential does not travel — but it
   is a credential scanner rather than a filter on meaning: private source, personal data and
   anything else in the selection still reaches the provider by design.
2. **Prompt injection from the corpus is the live attack.** Archive content is attacker-influenced
   by contract, and it is read by a session with whatever tools Code gives it. Babel's mitigations
   are narrow and worth naming precisely: the answer must be one fenced block matching a strict
   schema, every citation must resolve against the material's index, and a run's output becomes
   records only after that check. What Babel cannot mitigate is what the session does with its own
   tools while it reads; that boundary is Code's.
3. **The provider credential is Code's, and Babel never sees it.** This removes a residual rather
   than adding one, but it relocates it: a compromise of the session reaches whatever credential
   Code placed there, and Babel's receipt cannot tell a reviewer what that was.
4. **The material is a second copy of sensitive bytes, and a restore is a third.** A preparation
   seals real session logs into a job output lease, which is then bound into a session's sandbox.
   The corpus's sensitivity travels with it, and a machine that may run `prepare` is a machine
   that may read every session the selection names. A `verify` that restores a session writes
   those bytes into the job's own cache location, and removes them when the comparison is made —
   an operation killed in between leaves them until the job's state is reclaimed. A restore that
   named a target meant to be kept is a copy the operator now owns, in the open, with none of the
   repository's encryption around it.
5. **A credential the scan does not recognize still travels.** The preflight is deterministic, so
   it catches what its rules describe and nothing else: a credential in a format no rule names, or
   one the entropy heuristic reads as prose, reaches the provider like any other bytes. The rule
   table is the claim, class by class (`babel/machine/preflight.ts`), and the receipt
   names the rule set that ran so a corpus scanned by an older one is identifiable rather than
   assumed clean.
6. **The trust base is the hub, the machine and Code.** The hub constructs the job sandbox, holds
   the store and mints the service capability; the machine's owner binds `restic` and the native
   closure; Code confines the session. Babel audits none of the three and depends on all of them.
7. **A shared kernel shares microarchitecture.** No claim is made against timing or
   speculative-execution side channels, and none is implied.

## 6. What the operator is asked to accept, in one sentence each

- Running an analysis means sending selected archived conversations to a model provider.
- Running `archive` means a job on that machine can write to the repository and cannot delete from
  it.
- Running `verify` means a job on that machine can read the whole repository, and can write a
  session restored out of it back onto that machine.
- Enabling a machine for `prepare` means that machine can read every session the selection names.
- Running an analysis means accepting that a credential in a format the preflight's rules do not
  name travels with the material.

## 7. What would invalidate this document

- **Babel launching a process that reaches a model.** The declaration boundary in §4 exists because
  it does not. A launcher of Babel's own puts the containment question back on this side of the
  line and requires a declared backend with measured properties.
- **A machine operation gaining network, or write access to the corpus.** Both are manifest facts
  today, and §3's table is the claim; either change rewrites it.
- **A machine operation this table does not name**, or one whose secret arrives by any path other
  than the job's own service binding. The table is the claim operation by operation, so an
  operation added without a row here is one running with no stated boundary at all.
- **Restored bytes reaching a path the sandbox does not bound.** The fourth property above rests
  on every write going to a location the operation declares; a restore that escaped that — an
  absolute target the hub does not check, a write outside the job's own filesystem — is a
  different document.
- **A detector class leaving the preflight's rule table, or the scan ceasing to run before the
  material is sealed.** §4's third property and residual 5 are written against that table and
  that ordering; a rule removed or a scan moved after the seal is a different disclosure boundary.
- **Citations ceasing to be checked against the material index**, which is the only mechanism that
  makes a model's claim about the corpus verifiable.
