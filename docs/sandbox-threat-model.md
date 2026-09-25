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
  the hub gives it. It has no open database handle, no port and no timer: a `batch` is its
  transaction. It holds no credential, and it reaches exactly one origin — the embedding service
  the operator installed, and only if he installed one. That call is the paragraph below.
- **The machine half** is a Bun bundle the hub installs and runs as a **job** on an enrolled
  machine: `catalog`, `archive`, `prepare`, `verify`, and nothing else
  (`babel/manifest.json`; `babel/machine/main.ts`). It reads archived captures out of restic —
  `archive` alone reads session logs on the machine that holds them — and writes sealed outputs.
  It never reaches a model.
- **A run that reaches a model is a Code session.** Babel composes a prompt and posts it through
  `atyrode.code.runSession`. It chooses no model, no thinking level and no account, holds no
  provider credential, and owns no process to reap. The session's containment is Code's, and §4
  says exactly what Babel may therefore assert about it.

**The server half reaches one origin, for one thing, and only when the operator installed it.** The
corpus index's meaning half needs a model, and Babel holds no model and no key, so the vector is
computed by a host service: the plugin names `atyrode.babel.embeddings` and an operation, the host
resolves the credential by reference against a source only the machine's owner holds, and writes it
into the outbound request. `babel/server/embed.ts` is the only module in the bundle that can make
that call, and its own order is the control — the size cap first, the roster second, the invocation
behind both. No service configured under that id means no call at all, so a deployment that
installs no policy makes no outbound request and is indistinguishable from one running a build
without this shape. What leaves is a record's own prose — its title and the claim fields the peel
shows — capped at 8 KiB, in one named field, with no identifier, no run, no session byte, no
locator and no instant beside it. The keyword half of the same index is SQLite FTS5 in the hub's
own file and reaches nothing ever, so search itself does not depend on this being installed.

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

| Operation | Reads                                                                          | Writes                                                                                | Network  | Ceilings                                     |
| --------- | ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------- | -------- | -------------------------------------------- |
| `catalog` | the restic repository's snapshot and tree metadata, and no transcript content  | the sealed output lease, and its memory of listed snapshots in the managed cache      | **host** | 30 min, 1 GiB, 64 processes, 1 GiB of output |
| `prepare` | the captures it is named, streamed out of the restic repository; no local file | the sealed output and material leases, and its redacted readings in the managed cache | **host** | 30 min, 1 GiB, 64 processes, 1 GiB of output |
| `archive` | `~/.omp`, `~/.codex`, `~/.claude`, read-only                                   | the sealed output lease, and the restic repository                                    | **host** | 10 min, 1 GiB, 64 processes, 1 GiB of output |
| `verify`  | the restic repository, and none of the corpus                                  | the sealed output lease, and a restore target inside a location it declares           | **host** | 60 min, 1 GiB, 64 processes, 1 GiB of output |

Six properties of that table carry the weight:

- **No operation writes into the corpus.** The session directories are bound read-only, by
  `archive` alone; `catalog`, `prepare` and `verify` do not mount them: they read a repository,
  not a harness. A job cannot edit the evidence it read.
- **Every operation has host network, including the two that read the most hostile bytes**
  (#453). Reading and preparing the corpus used to have no route out; they now read it out of the
  archive, and restic must reach the object store, while the protocol offers `none` or `host` and
  nothing between (manifold `packages/protocol/src/jobs.ts:292`). What runs there is deterministic
  Babel code — a listing, a parser, a digest and a scan — with no model, no tool and nothing that
  interprets transcript text, so a log that persuades something to exfiltrate has nothing in
  `prepare` to persuade. What it would take instead is a defect in that code, and residual 8 is
  what such a defect would reach.
- **The one secret arrives through a loopback service proxy the engine opens for that job
  alone**: a capability minted per job, not the operator's upstream credential. The repository
  password reaches restic as `RESTIC_PASSWORD` in the **child's** environment only — never argv,
  never the parent's environment, never a receipt — and each child gets a minimal environment, so
  no ambient `RESTIC_*` variable can redirect an archive or aim a reading at another repository
  (`babel/machine/restic.ts`).
- **A preparation's raw bytes exist only in process memory.** `prepare` streams each capture out
  of restic's stdout straight into the pass that digests, scans and normalizes it. What reaches a
  disk is the redacted normalized stream: the material lease, and the reading kept in the managed
  cache so a second preparation fetches nothing.
- **A restore writes corpus bytes onto the machine, and the sandbox is what bounds where.**
  `verify` restores a catalogued session to prove it comes back byte for byte, so those bytes
  land outside the repository: under the job's own managed cache location, or in the target the
  caller named. Either path must be inside a location the operation declares writable, or the
  sandbox refuses the write — what stops a restore landing somewhere else is the manifest and the
  hub's enforcement of it, not the operation's good behaviour. The scratch directory is removed
  once the comparison is made, and that is cleanup rather than a boundary: an operation killed
  between the restore and the removal leaves the bytes where they fell (residual 5).
- **Nothing deletes, reading takes no lock, and only `archive` writes.** The verbs restic may be
  asked for are a closed set of eight — `cat`, `init`, `backup`, `snapshots`, `check`, `ls`,
  `dump`, `restore` — and every invocation is built by the one function that admits a verb or
  throws (`RESTIC_VERBS` and `resticArgv`, `babel/machine/restic.ts`), with a test that pins the
  refusal of `forget`, `prune`, `repair` and `unlock`. Every read verb is built with `--no-lock`,
  `backup` is `archive`'s alone and `init` is called only by disposable test fixtures. A
  compromised `archive` could write a snapshot and a compromised reader could read one; none has
  a verb that removes one, and a destructive verb costs a deliberate edit to a named list and a
  failing test rather than a moment's inattention.

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
  view of the corpus: its bytes are archived captures, each fixed by snapshot and path, so a scope
  has a stable identity and a machine's local files are never read at all.
- Babel's own run transcripts are excluded unless a preset asks for them, so Babel does not read
  itself by accident.
- The selection is scanned for likely credentials before it is sealed, and a matched span is
  replaced by a marker naming its class and the locator of the original
  (`babel/machine/preflight.ts`). The scan is deterministic — patterns and an entropy
  heuristic, no model and no network — so the same bytes redact the same way on any machine, and
  the locator resolves only against the archived capture, by a job holding the archive binding. A
  preparation may refuse the scope instead, and either way the receipt carries what was found by
  class and never by value.
- The index records, per session, the selector, the file and the digest it was served at. Every
  citation is checked against it, and a path the material does not name or a digest that does not
  match refuses the whole answer as `unknown-reference`. The check is a whitelist of the exact
  spellings the index carries, never a resolver: a locator containing a `..` segment is refused
  even where resolving it would land back inside the material, and the bytes of a session are
  reached through the index entry rather than through the string the model wrote
  (`babel/server/engine/citations.ts`).
- A citation's quoted text is checked against the bytes at the line it names, and what was found
  is recorded on the record's own evidence: at that line, elsewhere in that session, or nowhere
  in it. This one MARKS and does not refuse — it is an accuracy finding about real bytes rather
  than a claim about bytes nobody served — and the hub reads the cited member of the sealed
  material back to make it, which is residual 10.

The first of those two is the one worth stating as security rather than as correctness: a model
cannot cite its way to a file it was not given, and it cannot quietly substitute different bytes
for the ones a reviewer will later read.

**Account preflight is not spend authority** (`babel/server/engine/session.ts`; #255).
`CodeEngine.checkProfile` asks Code about the selected profile revision. A changed revision or
absence from this caller's configured, readable roster is stale; a matching revision with
`resolved: true` and no accounts is refused as `engine_no_account`. Exploration and automatic
titling check this after local eligibility and before preparation, and the adapter checks every
session posting, including conductor reviews. Refused titling leaves its batch available for a
later cycle rather than recording the sessions as declined.

An unresolved observation is not evidence that no account exists. It passes through to Code,
which remains authoritative about the current profile, account selection and provider when it
posts a session. The adapter caches the roster only within its own instance, not across wakes;
the preflight neither authorizes a spend nor guarantees that a provider will honour it.

Babel names a container and revision, not a provider credential. Code composes account references
([plugins/atyrode.code/session.ts:43–62 at `c6a9c264`](https://github.com/atyrode/code/blob/c6a9c264bfcb37934b5814b0943989fa78f7dd05/plugins/atyrode.code/session.ts#L43-L62));
the machine broker owns the credentials. A revoked key, an exhausted window, a changed selection
or an unavailable broker can still make Code refuse after preflight succeeds.

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
3. **Model-account access remains outside Babel's boundary.** Code selects accounts by reference;
   the machine broker owns their credentials. A compromised session has whatever account access
   its runtime grants it, which Babel's receipt does not establish or constrain.
4. **An installed embedding policy sends every record's prose to one more provider.** The corpus
   index's meaning half is the second place Babel's content leaves the hub, and it differs from
   the first in scope rather than in kind: an analysis sends the selection an operator chose, and
   a backfill sends every record there is, once each, as the drain works through them. The prose
   is all that goes — no identifier, no locator, no session byte — and the origin, the credential
   and whether any of it happens at all are the operator's, because an uninstalled policy makes
   no call. The secret preflight does not stand in front of this path: it scans session logs on
   the machine half, and a record's own text is a model's summary of one rather than a copy of it,
   so a credential a run copied into a claim would travel here as prose. What bounds it is the
   8 KiB cap and the expression that selects the text, both of which name fields rather than
   forwarding a payload.
5. **The material is a second copy of sensitive bytes, a kept reading a third, and a restore a
   fourth.** A preparation seals the redacted normalized stream of real session logs into a job
   output lease, which is then bound into a session's sandbox, and keeps the same stream in the
   machine's managed cache so a second preparation fetches nothing. The corpus's sensitivity
   travels with both — redaction removes the credentials its rules recognize and nothing else —
   and any job that holds the archive binding may read every archived session of every machine. A
   `verify` that restores a session writes the raw bytes into the job's own cache location, and
   removes them when the comparison is made — an operation killed in between leaves them until
   the job's state is reclaimed. A restore that named a target meant to be kept is a copy the
   operator now owns, in the open, with none of the repository's encryption around it.
6. **A credential the scan does not recognize still travels.** The preflight is deterministic, so
   it catches what its rules describe and nothing else: a credential in a format no rule names, or
   one the entropy heuristic reads as prose, reaches the provider like any other bytes. The rule
   table is the claim, class by class (`babel/machine/preflight.ts`), and the receipt
   names the rule set that ran so a corpus scanned by an older one is identifiable rather than
   assumed clean.
7. **The trust base is the hub, the machine and Code.** The hub constructs the job sandbox, holds
   the store and mints the service capability; the machine's owner binds `restic` and the native
   closure; Code confines the session. Babel audits none of the three and depends on all of them.
8. **A defect in the code that parses the corpus would run with a route out.** `catalog` and
   `prepare` hold host network because restic must reach the object store (§3), and `prepare` is
   where the most hostile bytes are parsed. No model and nothing that interprets transcript text
   runs there, so the path is a defect in Babel's parser or in restic, not persuasion; but such a
   defect would hold the network, the corpus bytes in memory and the storage document — the
   repository password, and the object-store key the deployment serves. What bounds it is the
   job sandbox, a per-job capability rather than the upstream credential, and the key the
   operator chooses to serve: a read-only one on a machine that only analyses limits it to
   reading. A per-operation egress allowlist is the upstream hardening this asks for, and the
   protocol does not offer one today.
9. **A shared kernel shares microarchitecture.** No claim is made against timing or
   speculative-execution side channels, and none is implied.
10. **Checking a quote puts corpus bytes in the hub's own memory.** Verifying that a citation's
    quoted text is where it says it is cannot be done from an index — a digest covers a whole
    file and says nothing about a span inside it — so the settlement reads the cited member of
    the sealed material back through the hub. It is transient, bounded by the ceiling the hub
    already reads every sealed output under, decoded only for the sessions a quote actually
    names, and it happens only where an answer carried a quote. It is still one more place the
    corpus exists, in a process that also holds the store, so it belongs here: residual 5 counts
    the copies, and this is the fifth.

## 6. What the operator is asked to accept, in one sentence each

- Running an analysis means sending selected archived conversations to a model provider.
- Running `archive` means a job on that machine can write to the repository and cannot delete from
  it.
- Running `verify` means a job on that machine can read the whole repository, and can write a
  session restored out of it back onto that machine.
- Giving a machine the archive binding means a job on it can read every archived session of
  every machine, and running `catalog` or `prepare` there means code that parses those
  transcripts runs with host network and the storage document in memory.
- Installing an embedding policy means every record's own prose reaches that provider once, and
  installing none means the corpus index answers by keyword and reaches nothing.
- Running an analysis means accepting that a credential in a format the preflight's rules do not
  name travels with the material.
- A profile Code positively resolves with no account is refused before preparation or posting.
  An unresolved observation leaves that decision to Code; Babel supplies no credential fallback.

## 7. What would invalidate this document

- **Babel launching a process that reaches a model.** The declaration boundary in §4 exists because
  it does not. A launcher of Babel's own puts the containment question back on this side of the
  line and requires a declared backend with measured properties.
- **A machine operation gaining write access to the corpus, a repository write other than
  `archive`'s `backup`, or a read that takes a lock.** These are manifest and code facts today,
  and §3's table and its properties are the claim; any of them rewrites it. So is the reason every
  operation holds network — restic reaching the object store: an operation that used the network
  for anything else is a different boundary, and a per-operation egress allowlist, once Manifold
  offers one, narrows §3 and residual 8 rather than invalidating them.
- **The server half reaching a second origin, or reaching one without a policy the operator
  installed.** §1's paragraph is the claim, and it is narrow on purpose: one service id, one
  operation, one module able to call it, one field on the wire, and no call at all where nothing
  is configured. A second binding, a call from a path other than `babel/server/embed.ts`, a field
  added to that request, or a fallback that reads an origin or a key out of the environment is
  each a different boundary — and the last of those is the shape the condition exists to catch,
  because it would look like a convenience.
- **The text an embedding is computed from ceasing to be the expression that selects it.** The
  claim that no identifier leaves rests on `recordTextSql` naming prose fields, so a change that
  sent a payload, a row or a locator instead moves this from "one field of prose" to "whatever
  the caller had".
- **A machine operation this table does not name**, or one whose secret arrives by any path other
  than the job's own service binding. The table is the claim operation by operation, so an
  operation added without a row here is one running with no stated boundary at all.
- **Restored bytes reaching a path the sandbox does not bound.** §3's fifth property rests
  on every write going to a location the operation declares; a restore that escaped that — an
  absolute target the hub does not check, a write outside the job's own filesystem — is a
  different document.
- **A detector class leaving the preflight's rule table, or the scan ceasing to run before the
  material is sealed.** §4's third property and residual 6 are written against that table and
  that ordering; a rule removed or a scan moved after the seal is a different disclosure boundary.
- **Citations ceasing to be checked against the material index**, which is the mechanism that
  makes a model's claim about the corpus verifiable at all. Two shapes of loosening count as
  the same condition: admitting a path by resolving it rather than by matching the index, which
  turns a whitelist into a traversal question; and letting the quote check refuse a claim,
  which would make the accuracy finding into a second gate and put the all-or-nothing waste
  back where #231 and #311 found it.
- **Babel binding or resolving an inference provider credential of its own** — an environment
  entry, an argv literal, a model service binding or credential configuration passed to
  `runSession`. §4's account boundary depends on Code and the machine broker owning that
  configuration. This is distinct from a credential appearing in corpus material, which
  residuals 1 and 6 already cover.
