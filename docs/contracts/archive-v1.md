# `babel/v1` archive contract

## 1. Status

**DRAFT — NOT FROZEN.**

This document is the artifact the pre-first-write freeze gate (SPEC.md §6.1,
decision 6) freezes. Until that gate passes with explicit operator approval,
**no durable `babel/v1` remote write may occur**: no bundle object, blob,
manifest segment, generation index, commit record, or `latest` hint may be
written to Cellar or any other shared remote. Phase A may implement the
module, adapters, the local-directory backend, synthetic fixtures, and golden
tests against local roots; that work is explicitly permitted before the gate.

Scope of the freeze: canonical bundle, manifest-segment, generation-index,
commit-record, and `latest` bytes; revision encodings and append-chain
reassembly; schemas, Go types, and null rules; the host-generation key and
total ordering; the SHA-256 domain and digest semantics over plaintext; path
and file-metadata rules; golden fixtures; compatibility and unknown-field
rules; and the direct-rclone recovery fixture.

Source of truth: the Go contract surface in `internal/archive` and
`internal/objectstore`. Where this document and that code disagree, the code
is normative and this document is a defect. SPEC.md is cited for intent.

Contract-relevant constants at the time of writing:

| Constant | Value | Source |
| --- | --- | --- |
| `ManifestSchemaVersion` | `1` | `types.go` |
| `SegmentSchemaVersion` | `1` | `types.go` |
| `IndexSchemaVersion` | `1` | `types.go` |
| `CommitSchemaVersion` | `1` | `types.go` |
| `HintSchemaVersion` | `1` | `types.go` |
| `MaxChainDepth` | `24` | `types.go` |

Every synthetic value in this document — host `example-host`, source IDs
`synthetic-session-0001`/`synthetic-session-0002`, workspace
`/synthetic/workspace/alpha`, remote `git@example.invalid:synthetic/alpha.git`
— is invented. No real host name, path, title, identifier, or transcript
content appears here or in any committed fixture (SPEC.md §3).

## 2. Key layout

Object keys are slash-separated paths relative to the archive root:
`archive:babel/v1/` on the rclone remote, the configured root directory for
the local-directory backend. Three key families exist and no other key is
written.

```text
cas/sha256/<aa>/<64 hex>                                   immutable content-addressed bytes
hosts/<host-id>/commits/g<gen 10>-<record digest 64>.json  immutable commit records
hosts/<host-id>/latest.json                                mutable, non-authoritative hint
```

`cas/` holds *every* immutable byte range: bundle payloads, append-delta
tails, artifacts, blobs, manifest segments, and generation indexes. `<aa>` is
the first two hex characters of the digest, matching `archive.CASKey`. Nothing
else about a CAS object is encoded in its key — no harness, host, session,
generation, or type — so shared bytes deduplicate globally and a CAS key
reveals nothing before decryption.

Host-scoped keys are produced only by `archive.HostPrefix`,
`archive.CommitPrefix`, `archive.LatestKey`, and `archive.CommitKey`. Host
IDs and harness names must satisfy `archive.ValidName`: 1–64 characters of
`[a-z0-9._-]`, first character alphanumeric.

### 2.1 Commit-key grammar

```text
hosts/<host-id>/commits/g<GGGGGGGGGG>-<DDDD…DDDD>.json
                        │ │            │
                        │ │            └── 64 lowercase hex, SHA-256 of the record's own canonical bytes
                        │ └─────────────── exactly 10 decimal digits, zero-padded generation
                        └───────────────── literal 'g'
```

The name is exactly `1 + 10 + 1 + 64 = 76` characters plus `.json`
(`commitNameLen` in `keys.go`). `archive.ParseCommitKey` accepts only that
form: `.json` suffix, correct length, leading `g`, `-` at index 11, ten ASCII
digits, and a digest that passes `Digest.Valid`. Anything else is a foreign or
malformed key and readers **skip** it rather than failing. Generation `0` is
representable; generations are `uint64` and the 10-digit field caps the
orderable range at `9999999999`.

**Write-once by construction.** The key embeds the full digest of the record's
own canonical bytes, so an identical key implies identical bytes, and
different bytes can never clobber one another. This rests on the same
SHA-256 collision-resistance assumption as the CAS itself; it introduces no
new cryptographic assumption. It is what makes correctness independent of the
object store's weak `Put` (§2.4).

**Lease relationship.** In shared mode, same-host publication is serialized by
PostgreSQL server-time fenced host leases (SPEC.md §9). The digest suffix is
*defense in depth for the lease-less recovery path*, which must not depend on
PostgreSQL being reachable. The two mechanisms are not redundant: the lease
prevents the anomaly, the key form makes the anomaly non-destructive.

**Total order.** The canonical total order of a host's commit records is
ascending lexicographic key order. The zero-padded generation dominates; the
record digest deterministically breaks same-generation ties. Readers select
the highest fully verified record in that order (§7). Because
`objectstore.Store.List` returns keys in ascending lexicographic order, the
store's native ordering *is* the contract ordering — no reader-side
generation parsing is needed to sort, only to validate.

**Same-generation anomaly.** Two records at one generation are anomalous:
they indicate concurrent publication without a lease (recovery or
misconfiguration). Semantics are fixed, not undefined:

- the lexicographically larger digest wins, deterministically, for every
  reader — no reader ever disagrees with another about the head;
- the shadowed writer's sessions are re-published by its next reconciling
  push, so no session is lost, only delayed;
- `archive verify` surfaces the duplicate as a **warning**, not an error; and
- neither record is ever rewritten or deleted. V1 is remote append-only
  (SPEC.md §6.1, decision 11).

`hosts/<host-id>/latest.json` is the only mutable key in the contract. It is
replaced through `objectstore.Store.ReplacePointer` after a durable commit and
is never authoritative (§6.4, §7).

### 2.2 Session, revision, and partition identity

| Form | Grammar | Producer |
| --- | --- | --- |
| Session key | `harness/host-id/source-id` | `archive.SessionKey.String` |
| Revision key | `harness/host-id/source-id@sha256:<64 hex>` | `archive.SessionKey.Revision` |
| Partition | first byte of `sha256(session_key)`, lowercase hex | `archive.PartitionOf` |

`source-id` is adapter-defined and must pass `archive.ValidSourceID`: one or
more `/`-separated segments of `[A-Za-z0-9._-]+`, no empty, `.`, or `..`
segment, at most 512 bytes total. Note the case asymmetry — source IDs allow
uppercase, host and harness names do not. `@` is reserved as the revision
separator and therefore excluded from every component. `ParseRevisionKey`
splits on the **last** `@`, so the separator remains unambiguous even if a
future grammar relaxes source IDs.

The digest in a revision key is always the *reassembled-plaintext* digest
(§3), never the stored object digest. A revision key is therefore stable and
reproducible regardless of whether the revision was stored as `full` or
`append-delta`, and re-encoding a revision cannot change its identity.

### 2.3 Digest-key relationship summary

| Referenced by | Key | Verified how |
| --- | --- | --- |
| `CommitRecord` (self) | `CommitKey(host, gen, digest)` | digest of read bytes vs. key (§7) |
| `CommitRecord.index` | `CASKey(index.digest)` | size + digest (`readVerified`) |
| `SegmentRef.object` | `CASKey(object.digest)` | size + digest on load; size-only at head selection |
| `ManifestEntry.object` | `CASKey(object.digest)` | size + digest on fetch |
| `FileRef` / `ObjectRef` closure | `CASKey(digest)` | size + digest on fetch |
| `LatestHint.commit` | `CommitKey(host, hint.generation, hint.commit.digest)` | advisory only |

### 2.4 Object-store obligations

`objectstore.Store` is deliberately narrow: `Put`, `Stat`, `Read`, `List`,
`ReplacePointer`. `Put` is **advisory no-clobber**: backends without a native
conditional write implement it as stat-then-write, which leaves a race window.
The archive layer is safe against that window by construction:

1. content-addressed keys bind key to content, so a racing duplicate write
   stores identical bytes and is idempotent;
2. non-content-addressed immutable keys — commit records — **must** be
   verified by the writer with a full read-back comparing the exact bytes
   written. A failed read-back means a concurrent writer won; the publication
   must be retried at a **later generation**, never rewritten in place; and
3. shared mode additionally serializes same-host publication with fenced
   leases.

`Put` returns `created=false` and leaves the object untouched when the key
already exists **with the same size**; a same-key different-size object
returns `ErrImmutableConflict`. Byte-level equality of an existing object is
not verified by `Put` (see the open item in §10). `Stat` and `Read` return
`ErrNotExist` for absent keys. Readers only trust digest-verified records, so
a torn or clobbered object is skipped, never silently exposed.

## 3. Digest semantics

A `Digest` is the string `sha256:` followed by exactly 64 lowercase hex
characters. `Digest.Valid` enforces prefix, total length, and the lowercase
`[0-9a-f]` alphabet; uppercase hex is invalid, not normalized.

Rules, all normative:

- **Plain SHA-256, no domain separation.** `sha256(bytes)` with no prefix,
  length framing, salt, or tree structure. `archive.DigestBytes` and
  `archive.ComputeDigest` are the only producers.
- **Over plaintext, before transport encryption.** Digests never describe
  ciphertext. This is what makes disaster recovery possible with standard
  tools: `rclone cat` decrypts through `crypt`, and the resulting bytes hash
  to the recorded digest.
- **`sha256sum`-compatible.** For any archived object,
  `rclone cat <remote>/<key> | sha256sum` prints exactly `Digest.Hex()`. This
  is a frozen compatibility promise, not an implementation detail; §9 depends
  on it.
- **Content digest vs. object digest.** `ManifestEntry.content` describes the
  reassembled session plaintext. `ManifestEntry.object` describes the bytes
  actually stored in the CAS. For `full` revisions the two are identical. For
  `append-delta` revisions `object` is the appended tail alone and `content`
  is the whole reassembled plaintext — which exists as bytes nowhere in the
  archive. Size follows digest: `content.size` is the reassembled length,
  `object.size` the stored length.

Two consequences worth stating explicitly: a `content` digest may name a byte
sequence that no single CAS key holds, so `CASKey(content.digest)` must never
be dereferenced for an `append-delta` revision; `object` digests are always
dereferenceable.

## 4. Revision encodings

`ManifestEntry.encoding` is a closed enumeration of two values in v1.
**Readers reject unknown encodings** rather than guessing; new encodings are
additive contract increments (SPEC.md decision 40).

| `encoding` | `object` holds | `parent_revision` | `chain_depth` |
| --- | --- | --- | --- |
| `full` | complete plaintext | absent | absent (`0`) |
| `append-delta` | appended tail only | required | required, `1…MaxChainDepth` |

### 4.1 Exact-byte-prefix validity

An `append-delta` revision is valid **only** when the parent revision's
reassembled plaintext is an exact byte prefix of the new content. This is a
byte-level test, not a line, record, or JSON-structure test. A writer proves
it before publishing; a reader can re-prove it after reassembly by comparing
the reassembled digest to `content.digest`.

`chain_depth` is the number of deltas above the nearest `full` ancestor: a
`full` revision has depth `0`, its immediate delta child depth `1`.

### 4.2 Fallback-to-full triggers

The writer publishes a `full` revision whenever any of the following holds:

- the parent's plaintext is not an exact byte prefix of the new content —
  which covers forks, in-place rewrites, truncations, re-orderings,
  compactions, and any prefix mismatch however small;
- there is no prior committed revision for the session (first publication);
- the parent revision's plaintext cannot be verified locally, so the prefix
  claim cannot be established;
- publishing a delta would make `chain_depth` exceed `MaxChainDepth`; or
- the parent revision declares an encoding this writer does not implement.

The bound is mechanical: because a delta may only be published while the
resulting depth stays within `MaxChainDepth = 24`, a `full` revision recurs at
least every 24 deltas. Restore therefore walks at most 25 objects, and a
single damaged object strands at most that much history — the newest verified
`full` revision below the damage remains fetchable (SPEC.md §11).

### 4.3 Reassembly by concatenation

Reassembly is byte concatenation, in root-to-leaf order, of the `full`
ancestor's object followed by each descendant's tail object. No framing,
delimiter, padding, alignment, or separator is inserted; no transformation is
applied. Consequences:

- reassembly is expressible as `cat a b c > out` with standard tools (§9);
- concatenation is associative, so partial reassembly may be cached and
  extended; and
- the reassembled bytes must hash to the leaf entry's `content.digest` and
  match `content.size`. A mismatch is corruption.

An `append-delta` revision whose parent chain cannot be fully verified is
reported as **incomplete** by `verify` and `fetch` rather than being silently
reassembled (SPEC.md §11).

## 5. Manifest model

### 5.1 Entry envelope

`archive.ManifestEntry` is the frozen envelope. Field order below is
declaration order, which is also canonical byte order (§5.4).

| JSON field | Go type | Presence | Meaning |
| --- | --- | --- | --- |
| `manifest_schema` | `int` | always | envelope version; `1` |
| `harness` | `string` | always | `omp`, `codex`, or `claude` |
| `adapter_schema` | `int` | always | producing adapter's version |
| `host_id` | `string` | always | stable host identity, `ValidName` |
| `source_id` | `string` | always | adapter identity, `ValidSourceID` |
| `session_key` | `string` | always | `harness/host_id/source_id` |
| `revision_key` | `string` | always | `session_key@content.digest` |
| `generation_added` | `uint64` | always | generation that first published this revision |
| `snapshot_time` | `time.Time` | always | UTC snapshot instant |
| `encoding` | `Encoding` | always | `full` or `append-delta` |
| `content` | `ObjectRef` | always | reassembled plaintext digest+size |
| `object` | `ObjectRef` | always | stored payload digest+size |
| `parent_revision` | `string` | delta only | parent revision key |
| `chain_depth` | `int` | delta only | deltas above nearest `full` |
| `title` | `*string` | nullable | display title |
| `workspace` | `*string` | nullable | workspace/project path |
| `created_at` | `*time.Time` | nullable | session creation |
| `modified_at` | `*time.Time` | nullable | last source modification |
| `lifecycle` | `*string` | nullable | adapter-defined opaque state string |
| `repo` | `*RepoFingerprint` | nullable | best-effort repository identity |
| `completeness` | `[]CompletenessReason` | when non-empty | one entry per absent nullable field |
| `artifacts` | `[]FileRef` | when non-empty | declared artifact closure |
| `blobs` | `[]ObjectRef` | when non-empty | referenced content-addressed blobs |
| `unresolved_blob_refs` | `[]string` | when non-empty | references the adapter could not resolve |
| `continuation_grade` | `bool` | always | complete closure guarantee |
| `adapter_metadata_schema` | `int` | when non-zero | version of `adapter_metadata` |
| `adapter_metadata` | `json.RawMessage` | when non-empty | namespaced adapter extension object |

`RepoFingerprint` fields are `remote`, `commit`, `branch` (all `omitempty`
strings) and `dirty` (`*bool`, omitted when unknown — distinct from `false`).

`FileRef` is `{path, digest, size}`; `path` is the **source-relative** path
inside the session's artifact tree, always slash-separated, never absolute and
never containing `.` or `..` segments.

`ObjectRef` is `{digest, size}`. `size` is bytes of the referenced object; a
size mismatch is corruption, and it is what the cheap verification tier checks
without downloading (§7).

`CompletenessReason` is `{field, reason}`; both are always present.

Note the fields with no `omitempty`: `harness`, `adapter_schema`, `host_id`,
`source_id`, `session_key`, `revision_key`, `generation_added`,
`snapshot_time`, `encoding`, `content`, `object`, and `continuation_grade` are
always present in canonical bytes, including at their zero values. A reader
finding `"harness":""` is looking at a malformed entry, not an absent field.

### 5.2 Nullability and completeness

Nullable means *omitted*, never synthesized. Rules:

1. A nullable field whose value is unknown is a Go `nil` pointer and is
   **absent** from the canonical bytes — `omitempty` on a pointer omits the
   key entirely rather than emitting `null`.
2. Every absent nullable field **must** be explained by exactly one
   `CompletenessReason` in `completeness`, naming the JSON field and a terse
   human reason. Adapters never invent a value merely to satisfy the shape
   (SPEC.md §3, `adapter.CommonMeta`).
3. Reasons carry no transcript content. They describe the *source format's*
   limitation, not the session's contents.
4. Inability to derive a title, workspace, lifecycle, or artifact closure
   **never** excludes the raw transcript from the archive. The transcript is
   the guarantee; metadata is best effort with declared gaps.
5. `continuation_grade` is `true` only when the adapter guarantees the
   complete artifact/blob closure required to continue the session. A
   non-empty `unresolved_blob_refs` forces `false`. This is a hard
   implication, not a heuristic — cross-machine continuation depends on it
   (SPEC.md §2.5).

V1 adapter guarantees are deliberately unequal (SPEC.md §3): OMP resolves the
full blob closure; Codex may lack title/workspace/lifecycle and attachment
closure; Claude Code may additionally lack project identity and timestamps
beyond filesystem observations.

### 5.3 Adapter metadata versioning

`adapter_metadata` is a namespaced JSON **object** — not an array, string, or
number — versioned by `adapter_metadata_schema` **independently of
`manifest_schema` and `adapter_schema`**. Three version axes exist and must
not be conflated:

- `manifest_schema` — the portable envelope's shape;
- `adapter_schema` — the adapter's extraction behavior; and
- `adapter_metadata_schema` — the shape of the extension object only.

An adapter may increment its metadata schema without any envelope change, and
readers that do not understand a metadata schema still read the entire
envelope, the transcript, and the closure. Metadata is never a precondition
for retrieval.

Adapter metadata must pass `archive.CanonicalRawMessage` before it enters an
entry: leading/trailing whitespace trimmed, first non-space byte `{` enforced,
then `json.Compact`. Empty or all-whitespace input yields `nil`, which
`omitempty` omits along with a zero `adapter_metadata_schema`.

`CanonicalRawMessage` compacts but does **not** reorder object keys. Key order
inside `adapter_metadata` is the adapter's authored order, preserved verbatim
into canonical bytes. Determinism of that region is therefore the adapter's
obligation: an adapter that emits keys in Go map-iteration order produces
different bytes on every run, breaking segment byte reuse (§5.5). Adapters
must emit a deterministic order — building metadata from a struct, or from a
`map` marshalled by `encoding/json` (which sorts map keys), both satisfy this.

### 5.4 Canonical JSON

Canonical bytes are `encoding/json` output of the Go contract structs, via
`archive.MarshalCanonical`. Producing a document any other way is a contract
violation. The rules that follow, all load-bearing because digests are taken
over these bytes:

- **struct fields in declaration order** — not alphabetical. Go struct
  marshalling is order-preserving; the declaration order in `types.go` is the
  contract.
- **Go map keys sorted** ascending by `encoding/json`. The v1 contract structs
  contain no maps; this rule governs `adapter_metadata` only when the adapter
  builds it from a map, and does not apply to a `RawMessage` that arrived
  already serialized (§5.3).
- **no insignificant whitespace** — no spaces after `:` or `,`, no newlines,
  no trailing newline. The canonical form of every document in §6 is a single
  line.
- **times in UTC RFC3339, nanoseconds elided when zero** — Go's `time.Time`
  marshalling. `2026-01-02T09:05:00Z`, never an offset like `+01:00`. Writers
  must convert to UTC before marshalling; a non-UTC time produces
  non-canonical bytes that will not reproduce.
- **HTML-escaped `<`, `>`, `&`** as `\u003c`, `\u003e`, `\u0026`. This is
  `json.Marshal`'s default and it is therefore part of the canonical bytes.
  It applies inside `adapter_metadata` too, because marshalling a
  `json.RawMessage` re-escapes it. A workspace path or title containing `&`
  appears escaped, and a recovery operator's `jq` will show the unescaped
  character — expected, not corruption.
- **`json.RawMessage` values must already be canonical** — see
  `CanonicalRawMessage`.
- **integers are JSON numbers without exponent**; sizes are `int64` and never
  quoted.

### 5.5 Segmentation, byte reuse, and unknown fields

A generation's manifest is partitioned into at most 256 segments.
`archive.PartitionOf` maps a session key to the first byte of
`sha256(session_key)` in lowercase hex. Every revision of a session lands in
the *session's* partition, so a partition whose member entries are all
unchanged produces byte-identical canonical bytes and is reused by digest
across generations. Hourly publication cost and permanent storage cost then
scale with the change rate, not with corpus size (SPEC.md §6.1, decision 41).

`archive.BuildSegments` fixes segment construction:

1. group entries by `PartitionOf(entry.session_key)`;
2. sort partitions ascending by hex string;
3. within a partition, sort entries by `session_key`, then by `revision_key`;
4. reject duplicate `revision_key` within a partition — a hard error, since a
   revision key is a content identity and duplication means the caller built
   inconsistent input; and
5. marshal canonically and derive
   `SegmentRef{partition, object{digest,size}, entries}`.

`Segment` itself is `{segment_schema, partition, entries}`. A partition with
no members is omitted from the generation index entirely rather than being
published as an empty segment.

**Unknown-field preservation is at segment granularity.** SPEC.md §3 requires
Babel to preserve unknown extension fields while reading a compatible
manifest. The mechanism is byte reuse, not field capture: `ManifestEntry` is a
fixed Go struct, so `json.Unmarshal` silently drops fields it does not know
and re-marshalling the parsed entry erases them. Therefore:

- a reader that does not modify a partition **must** carry the segment forward
  by its existing digest — reusing the exact bytes — which preserves every
  unknown field written by a newer producer;
- a reader must never round-trip a segment through parse-and-remarshal for any
  reason other than a genuine entry change; and
- a producer that must rewrite a partition containing entries it does not
  fully understand has no v1-defined way to preserve their unknown fields.
  This is an open freeze item (§10).

## 6. Generation index, commit record, and latest hint

The documents below form one complete, self-consistent synthetic generation
for host `example-host` at generation `7`. Every digest, size, and key shown is
the real SHA-256 of the real canonical bytes, so the chain
`latest.json → commit record → generation index → segments → payloads`
verifies end to end. Documents are listed indented for readability, with the
canonical single-line form given alongside for the three small documents;
**only the canonical form hashes to the stated digest.**

The generation contains two sessions and three revisions:

| Session key | Partition | Revisions |
| --- | --- | --- |
| `omp/example-host/synthetic-session-0001` | `db` | `full` at gen 3, `append-delta` at gen 7 |
| `codex/example-host/synthetic-session-0002` | `3d` | `full` at gen 5 |

### 6.1 Manifest segments

Partition `db`, key
`cas/sha256/ec/ecc9163e8545d058de757f99dc120e06045a0d8b8b241c4c61e2b78e40a88f07`,
canonical size 2769 bytes. Entries are sorted by `(session_key,
revision_key)`, which for a single session orders by content digest — hence
the delta revision precedes the full revision here. That ordering is
lexicographic on the key string and carries no chronological meaning;
`generation_added` and `snapshot_time` carry that.

```json
{
  "segment_schema": 1,
  "partition": "db",
  "entries": [
    {
      "manifest_schema": 1,
      "harness": "omp",
      "adapter_schema": 1,
      "host_id": "example-host",
      "source_id": "synthetic-session-0001",
      "session_key": "omp/example-host/synthetic-session-0001",
      "revision_key": "omp/example-host/synthetic-session-0001@sha256:24c6d3d819d0f0ab4e9a3114db0ac4e69da4b35092d703f9db32ac72cd5a91e4",
      "generation_added": 7,
      "snapshot_time": "2026-01-02T09:00:00Z",
      "encoding": "append-delta",
      "content": {
        "digest": "sha256:24c6d3d819d0f0ab4e9a3114db0ac4e69da4b35092d703f9db32ac72cd5a91e4",
        "size": 226
      },
      "object": {
        "digest": "sha256:7756d5eff7a6cf549fc90e2563508a559ab426045c00a411ab66a607502171a5",
        "size": 113
      },
      "parent_revision": "omp/example-host/synthetic-session-0001@sha256:3224edc9079549e66af37040e4ace91d2658def7dbfbfc74719d3dfeae9e541c",
      "chain_depth": 1,
      "title": "synthetic fixture session one",
      "workspace": "/synthetic/workspace/alpha",
      "created_at": "2026-01-02T02:00:00Z",
      "modified_at": "2026-01-02T08:59:12Z",
      "lifecycle": "idle",
      "repo": {
        "remote": "git@example.invalid:synthetic/alpha.git",
        "commit": "1111111111111111111111111111111111111111",
        "branch": "main",
        "dirty": false
      },
      "artifacts": [
        {
          "path": "artifacts/synthetic-note.txt",
          "digest": "sha256:a8e76045938ba087ed03495ebc5b9dfa21439cc29dbbd8b02c692501b7181719",
          "size": 31
        }
      ],
      "blobs": [
        {
          "digest": "sha256:a8e94f45de9007b782e2bf245f88b6317ebadc5de5f0523341e1c00f8fa5b1e8",
          "size": 31
        }
      ],
      "continuation_grade": true,
      "adapter_metadata_schema": 2,
      "adapter_metadata": {
        "collaboration": {
          "siblings": 1
        },
        "jsonl_lines": 4
      }
    },
    {
      "manifest_schema": 1,
      "harness": "omp",
      "adapter_schema": 1,
      "host_id": "example-host",
      "source_id": "synthetic-session-0001",
      "session_key": "omp/example-host/synthetic-session-0001",
      "revision_key": "omp/example-host/synthetic-session-0001@sha256:3224edc9079549e66af37040e4ace91d2658def7dbfbfc74719d3dfeae9e541c",
      "generation_added": 3,
      "snapshot_time": "2026-01-02T03:04:05Z",
      "encoding": "full",
      "content": {
        "digest": "sha256:3224edc9079549e66af37040e4ace91d2658def7dbfbfc74719d3dfeae9e541c",
        "size": 113
      },
      "object": {
        "digest": "sha256:3224edc9079549e66af37040e4ace91d2658def7dbfbfc74719d3dfeae9e541c",
        "size": 113
      },
      "title": "synthetic fixture session one",
      "workspace": "/synthetic/workspace/alpha",
      "created_at": "2026-01-02T02:00:00Z",
      "modified_at": "2026-01-02T03:03:59Z",
      "lifecycle": "idle",
      "repo": {
        "remote": "git@example.invalid:synthetic/alpha.git",
        "commit": "1111111111111111111111111111111111111111",
        "branch": "main",
        "dirty": false
      },
      "artifacts": [
        {
          "path": "artifacts/synthetic-note.txt",
          "digest": "sha256:a8e76045938ba087ed03495ebc5b9dfa21439cc29dbbd8b02c692501b7181719",
          "size": 31
        }
      ],
      "blobs": [
        {
          "digest": "sha256:a8e94f45de9007b782e2bf245f88b6317ebadc5de5f0523341e1c00f8fa5b1e8",
          "size": 31
        }
      ],
      "continuation_grade": true,
      "adapter_metadata_schema": 2,
      "adapter_metadata": {
        "collaboration": {
          "siblings": 1
        },
        "jsonl_lines": 4
      }
    }
  ]
}
```

Partition `3d`, key
`cas/sha256/bc/bc80cbb22ad9cd770b3a63a6fd91c43128435c089125fc5da08af14aa8b47ba2`,
canonical size 1208 bytes, holds the Codex session and demonstrates §5.2 —
four absent nullable fields, each explained, plus an unresolved attachment
forcing `continuation_grade: false`:

```json
{
  "segment_schema": 1,
  "partition": "3d",
  "entries": [
    {
      "manifest_schema": 1,
      "harness": "codex",
      "adapter_schema": 1,
      "host_id": "example-host",
      "source_id": "synthetic-session-0002",
      "session_key": "codex/example-host/synthetic-session-0002",
      "revision_key": "codex/example-host/synthetic-session-0002@sha256:3ecceb6712f521ce31119218565539640b435eef2298f91317c63dda313b86b7",
      "generation_added": 5,
      "snapshot_time": "2026-01-02T05:06:07Z",
      "encoding": "full",
      "content": {
        "digest": "sha256:3ecceb6712f521ce31119218565539640b435eef2298f91317c63dda313b86b7",
        "size": 60
      },
      "object": {
        "digest": "sha256:3ecceb6712f521ce31119218565539640b435eef2298f91317c63dda313b86b7",
        "size": 60
      },
      "created_at": "2026-01-02T04:30:00Z",
      "modified_at": "2026-01-02T05:05:00Z",
      "completeness": [
        { "field": "title", "reason": "source format exposes no session title" },
        { "field": "workspace", "reason": "source format exposes no workspace path" },
        { "field": "lifecycle", "reason": "source format exposes no lifecycle state" },
        { "field": "repo", "reason": "workspace unknown, repository not resolvable" }
      ],
      "unresolved_blob_refs": ["attachment/synthetic-missing-0001"],
      "continuation_grade": false,
      "adapter_metadata_schema": 1,
      "adapter_metadata": {
        "history_lines": 2,
        "session_index_present": true
      }
    }
  ]
}
```

### 6.2 Generation index

Key `cas/sha256/2e/2e3ea39f37d9f975869c26bba86d023612f0a30190f3647010c9559bb49fe207`,
canonical size 408 bytes. The index plus its segments are a complete,
self-contained description of the generation: no other generation's documents
need to be readable to enumerate it.

```json
{
  "index_schema": 1,
  "host_id": "example-host",
  "generation": 7,
  "created_at": "2026-01-02T09:05:00Z",
  "segments": [
    {
      "partition": "3d",
      "object": {
        "digest": "sha256:bc80cbb22ad9cd770b3a63a6fd91c43128435c089125fc5da08af14aa8b47ba2",
        "size": 1208
      },
      "entries": 1
    },
    {
      "partition": "db",
      "object": {
        "digest": "sha256:ecc9163e8545d058de757f99dc120e06045a0d8b8b241c4c61e2b78e40a88f07",
        "size": 2769
      },
      "entries": 2
    }
  ],
  "sessions": 2,
  "revisions": 3
}
```

Canonical bytes:

```text
{"index_schema":1,"host_id":"example-host","generation":7,"created_at":"2026-01-02T09:05:00Z","segments":[{"partition":"3d","object":{"digest":"sha256:bc80cbb22ad9cd770b3a63a6fd91c43128435c089125fc5da08af14aa8b47ba2","size":1208},"entries":1},{"partition":"db","object":{"digest":"sha256:ecc9163e8545d058de757f99dc120e06045a0d8b8b241c4c61e2b78e40a88f07","size":2769},"entries":2}],"sessions":2,"revisions":3}
```

`segments` is ordered ascending by `partition`. `sessions` is the count of
distinct `session_key` values and `revisions` the total entry count across
every segment (`archive.CountEntries`); `sum(segments[].entries)` must equal
`revisions`. Both counts are redundant with the segments and exist so a reader
can report generation size and detect truncation without loading them.
`entries` per segment is checked against the loaded segment (§7), which makes
a size-matching but wrong-partition object detectable.

### 6.3 Commit record

Key
`hosts/example-host/commits/g0000000007-f8df7a1165f2ef0e58a49f64ab64770ba337497cc95cfe9737990b1813acce48.json`,
canonical size 663 bytes, self-digest
`sha256:f8df7a1165f2ef0e58a49f64ab64770ba337497cc95cfe9737990b1813acce48`.

A generation **exists exactly when** its digest-valid commit record is durable
and readable. Nothing else — not an uploaded index, not a replaced hint, not a
PostgreSQL row — makes a generation current.

```json
{
  "commit_schema": 1,
  "host_id": "example-host",
  "host_display_name": "Example Workstation",
  "generation": 7,
  "created_at": "2026-01-02T09:05:30Z",
  "index": {
    "digest": "sha256:2e3ea39f37d9f975869c26bba86d023612f0a30190f3647010c9559bb49fe207",
    "size": 408
  },
  "coverage": [
    {
      "harness": "claude",
      "adapter_schema": 1,
      "scanned": 0,
      "published": 0,
      "carried_forward": 0,
      "deferred": 0,
      "complete": true
    },
    {
      "harness": "codex",
      "adapter_schema": 1,
      "scanned": 1,
      "published": 0,
      "carried_forward": 1,
      "deferred": 0,
      "complete": true
    },
    {
      "harness": "omp",
      "adapter_schema": 1,
      "scanned": 1,
      "published": 1,
      "carried_forward": 1,
      "deferred": 0,
      "complete": true
    }
  ],
  "bootstrap": false,
  "bootstrap_complete": true,
  "babel_version": "0.1.0"
}
```

Canonical bytes:

```text
{"commit_schema":1,"host_id":"example-host","host_display_name":"Example Workstation","generation":7,"created_at":"2026-01-02T09:05:30Z","index":{"digest":"sha256:2e3ea39f37d9f975869c26bba86d023612f0a30190f3647010c9559bb49fe207","size":408},"coverage":[{"harness":"claude","adapter_schema":1,"scanned":0,"published":0,"carried_forward":0,"deferred":0,"complete":true},{"harness":"codex","adapter_schema":1,"scanned":1,"published":0,"carried_forward":1,"deferred":0,"complete":true},{"harness":"omp","adapter_schema":1,"scanned":1,"published":1,"carried_forward":1,"deferred":0,"complete":true}],"bootstrap":false,"bootstrap_complete":true,"babel_version":"0.1.0"}
```

Field rules:

- `host_display_name` is `omitempty` and mutable across generations; the
  newest committed value wins for catalog display while prior values remain in
  history (SPEC.md §6.1, decision 8).
- `coverage` records one `AdapterCoverage` per configured adapter, including
  adapters that found nothing — `claude` above scanned zero sources and is
  still listed, which distinguishes "no sessions" from "adapter not run".
- `scanned` counts sources the adapter discovered; `published` counts new
  revisions committed in this generation; `carried_forward` counts entries
  inherited from the previous valid generation; `deferred` counts sources
  skipped. Here `published` 1 plus `carried_forward` 2 equals the index's
  `revisions` 3.
- `deferred_reasons` is `omitempty` and carries format-level reasons only —
  never a path, title, or transcript excerpt. A generation with deferrals
  looks like
  `{"harness":"codex","adapter_schema":1,"scanned":2,"published":1,"carried_forward":0,"deferred":1,"deferred_reasons":["source changed during snapshot"],"complete":false}`.
- `complete` is the adapter's own claim that its scan covered its roots.
- `bootstrap` marks the explicit first bootstrap/backfill push;
  `bootstrap_complete` records whether that full scan finished with no
  deferrals. Both are always present. A bootstrap generation is committed only
  after the full scan finishes, with deferred sources visible and retried
  rather than silently omitted (SPEC.md §6.1).
- `babel_version` is `omitempty` provenance and carries no semantics.

### 6.4 Latest hint

Key `hosts/example-host/latest.json`, canonical size 162 bytes.

```json
{
  "hint_schema": 1,
  "host_id": "example-host",
  "generation": 7,
  "commit": {
    "digest": "sha256:f8df7a1165f2ef0e58a49f64ab64770ba337497cc95cfe9737990b1813acce48",
    "size": 663
  }
}
```

Canonical bytes:

```text
{"hint_schema":1,"host_id":"example-host","generation":7,"commit":{"digest":"sha256:f8df7a1165f2ef0e58a49f64ab64770ba337497cc95cfe9737990b1813acce48","size":663}}
```

The hint is the only mutable object in the contract and the only one written
with `ReplacePointer`. It is **non-authoritative**: last writer wins, and its
`generation` and `commit.digest` are exactly enough to reconstruct the commit
key (`CommitKey(host_id, generation, commit.digest)`) and jump straight to a
candidate. `archive.ReadLatestHint` returns `(nil, nil)` for an absent,
unparseable, wrong-schema, or foreign-host hint: a bad hint degrades to the
verified scan and is never an error. A stale hint naming an older generation is
legal and self-correcting.

## 7. Reader selection algorithm

`archive.VerifiedHead(ctx, store, hostID)` is the normative reader. It
implements exactly this, and no step may be skipped or reordered:

1. `List(CommitPrefix(hostID))` — the store returns keys in ascending
   lexicographic order.
2. Filter to keys accepted by `ParseCommitKey`. Foreign and malformed keys are
   skipped silently.
3. If no key survives, return `(nil, nil)` — the host has never published.
   This is not an error; an absent host is empty, not broken.
4. Sort surviving keys **descending** and evaluate candidates from the highest
   down. Because the generation field is zero-padded and fixed-width,
   descending lexicographic order is descending generation order, with the
   larger record digest first within a generation.
5. For each candidate, in order, run the verification below. Return the
   **first** candidate that fully verifies.
6. If candidates exist but none verifies, return an error naming the candidate
   count and wrapping the first failure. Corruption and infrastructure failure
   are indistinguishable at this layer and must never silently yield a *less
   verified* generation — but an older **fully verified** generation is
   exactly the fallback the contract requires, and step 5 already returns it.

Per-candidate verification:

1. read the record's bytes in full;
2. `DigestBytes(raw)` must equal the digest embedded in the key — the
   integrity check that makes the weak `Put` safe, since a torn or clobbered
   record cannot match its own key;
3. `json.Unmarshal` into `CommitRecord` must succeed;
4. `commit_schema` must equal `CommitSchemaVersion`; an unsupported schema
   fails this candidate and the scan continues to older records;
5. `host_id` and `generation` must match the requested host and the key's
   generation — identity confusion fails the candidate;
6. read `index` from `CASKey(index.digest)` and verify **size then digest**
   (`readVerified`); parse it; `index_schema` must equal
   `IndexSchemaVersion`; and its `host_id`/`generation` must match the
   record; and
7. for every `segments[]` entry, `Stat(CASKey(object.digest))` must succeed
   and the reported size must equal `object.size`.

Step 7 is deliberately a `Stat`, not a read: head selection proves the
generation is *materialized* — every segment present at the right size —
without downloading manifests. That is the default verification tier (SPEC.md
decision 42). Full segment digest verification happens in `LoadSegment`, which
reads through `readVerified` (size then digest), checks `segment_schema`, and
requires `partition` and `len(entries)` to match the `SegmentRef`.
`LoadEntries` walks every segment of a generation in canonical partition order
and concatenates their entries.

**The hint is never authoritative and `VerifiedHead` never consults it.** The
scan *is* the semantics. A caller wanting the fast path may read the hint,
reconstruct the commit key, verify that single candidate, and fall back to
`VerifiedHead` on any failure; the outcome must be identical to the scan's.

## 8. Crash and interruption matrix

Publication stages, in order (SPEC.md §6.1). The private local journal, keyed
by host and intended generation, advances only **after** each remote read-back,
so restarting any stage is idempotent, and the journal is never the ordering
authority — the writer derives its next generation from the highest verified
remote commit record, cross-checked against the shared catalog in shared mode
(decision 43).

| # | Stage | Interrupted here → observable state | Recovery |
| --- | --- | --- | --- |
| 1 | Discover and stage/hash sources | Nothing remote written. Prior generation current. | Re-run. Staging lives under `$XDG_CACHE_HOME/babel/` and is disposable. |
| 2 | Source changed during snapshot | Nothing published for that session. Prior generation current. | Adapter returns `adapter.ErrUnstable`; the publisher retries within a bound, then defers the session with a reason in `coverage[].deferred_reasons`. A changing source never yields a committed continuation-grade entry (SPEC.md §11). |
| 3 | Upload bundle/blob/artifact CAS objects | Orphan immutable CAS objects, referenced by nothing. Prior generation current. | Harmless. Retry reuses them by digest; content-addressed keys make duplicate writes idempotent. Never deleted — v1 is remote append-only. |
| 4 | Upload + read back manifest segments | Orphan segment objects. Prior generation current. | As stage 3; reused by digest on retry. |
| 5 | Upload + read back generation index | Orphan index referencing present segments. Prior generation current. No commit record names it, so no reader can see it. | As stage 3. |
| 6 | Upload commit record, before read-back | Ambiguous: the record may or may not be durable. | Read it back. Byte-identical read-back means the publication succeeded and is committed. Absent record means retry at the same generation. A **different** record at the same generation means a concurrent writer won: republish at a later generation, never rewrite in place (§2.4). |
| 7 | After commit-record read-back, before `latest` replacement | **The new generation is committed and current.** The hint still names the previous generation. | Recoverable with no action: `VerifiedHead` scans and selects the new record; the stale hint only misroutes the fast path, which then falls back. The next push replaces the hint. |
| 8 | During/after `ReplacePointer` of `latest` | Publication complete. `ReplacePointer` is atomic, so the hint is either the old or the new document, never torn. | None needed. |
| 9 | After object-store commit, PostgreSQL insert fails (shared mode) | Archive valid and readable; the shared catalog is missing the row and is visibly `catalog-pending`. | Any authorized instance reconciles by scanning and verifying immutable commit records. No bytes are republished (SPEC.md §9, §11). |
| 10 | Object store unavailable | No new commit possible. The last complete local catalog remains browsable, marked stale. | Retry later. PostgreSQL never references an object that was not uploaded and read back. |
| 11 | Reader finds candidates but none verifies | Error, not a silent downgrade. | Operator investigation. Older *verified* generations are already selected automatically by the descending scan, so reaching this state means every record failed. |
| 12 | Append-delta parent chain unverifiable | `verify` and `fetch` report the revision **incomplete**; nothing is silently reassembled. | The newest verified `full` revision at or below the damage remains fetchable; `MaxChainDepth` bounds the affected history to at most 24 deltas. |

Readers therefore see either the previous complete generation or a verified new
commit, never an uncommitted manifest.

## 9. Direct-rclone disaster recovery

This is the frozen recovery promise: **one session recoverable with rclone,
sha256sum, jq, and cat — no Babel binary, no PostgreSQL, no local state.** The
walkthrough recovers the newest revision of
`omp/example-host/synthetic-session-0001` from the §6 generation. It was
executed end to end against a materialized copy of that generation; every
digest and size printed below is the one recorded in §6.

Set the remote once. `rclone cat` decrypts through `crypt`, so every byte that
reaches the pipeline is plaintext and hashes to the recorded digest (§3).

```sh
R=archive:babel/v1
```

**Step 1 — find the newest commit record.** Commit keys sort by generation, so
a reverse sort puts the head first.

```sh
rclone lsf "$R/hosts/example-host/commits/" | sort -r | head -n 5
# g0000000007-f8df7a1165f2ef0e58a49f64ab64770ba337497cc95cfe9737990b1813acce48.json
KEY=$(rclone lsf "$R/hosts/example-host/commits/" | sort -r | head -n1)
```

`hosts/example-host/latest.json` may be consulted as a shortcut, but it is a
hint and this procedure deliberately does not depend on it. Cross-check it if
present:

```sh
rclone cat "$R/hosts/example-host/latest.json"
# {"hint_schema":1,"host_id":"example-host","generation":7,"commit":{"digest":"sha256:f8df7a11…","size":663}}
```

**Step 2 — verify the record against its own key.** The key embeds the digest
of the record's canonical bytes, so this is a self-contained integrity proof.

```sh
rclone cat "$R/hosts/example-host/commits/$KEY" | sha256sum | cut -d' ' -f1
# f8df7a1165f2ef0e58a49f64ab64770ba337497cc95cfe9737990b1813acce48
echo "$KEY" | sed 's/^g[0-9]\{10\}-//; s/\.json$//'
# f8df7a1165f2ef0e58a49f64ab64770ba337497cc95cfe9737990b1813acce48
```

The two lines must be identical. If they differ, the record is corrupt or was
clobbered: discard it and take the next key from the reverse-sorted list. This
is the same fallback `VerifiedHead` performs (§7).

**Step 3 — follow the record to the generation index.** CAS keys are
`cas/sha256/<first two hex>/<full hex>`.

```sh
IDX=$(rclone cat "$R/hosts/example-host/commits/$KEY" | jq -r .index.digest | cut -d: -f2)
rclone cat "$R/cas/sha256/${IDX:0:2}/$IDX" | sha256sum | cut -d' ' -f1
# 2e3ea39f37d9f975869c26bba86d023612f0a30190f3647010c9559bb49fe207   == $IDX
rclone cat "$R/cas/sha256/${IDX:0:2}/$IDX" | wc -c
# 408                                                                == index.size
```

**Step 4 — locate the session's segment.** Either compute the partition
directly or scan every segment. Computing it is one line, because the partition
is the first byte of `sha256(session_key)`:

```sh
printf '%s' omp/example-host/synthetic-session-0001 | sha256sum | cut -c1-2
# db
SEG=$(rclone cat "$R/cas/sha256/${IDX:0:2}/$IDX" \
  | jq -r '.segments[] | select(.partition=="db") | .object.digest' | cut -d: -f2)
rclone cat "$R/cas/sha256/${SEG:0:2}/$SEG" | sha256sum | cut -d' ' -f1
# ecc9163e8545d058de757f99dc120e06045a0d8b8b241c4c61e2b78e40a88f07   == $SEG
```

If the partition function is unavailable, loop over every
`.segments[].object.digest` and grep for the session key; there are at most 256
segments.

**Step 5 — select the newest revision and read its chain.** The highest
`generation_added` is the newest committed revision of the session.

```sh
E=$(rclone cat "$R/cas/sha256/${SEG:0:2}/$SEG" \
  | jq -c --arg s omp/example-host/synthetic-session-0001 \
      '[.entries[] | select(.session_key==$s)] | sort_by(.generation_added) | last')
echo "$E" | jq '{revision_key, encoding, chain_depth, content, object, parent_revision}'
# encoding        = "append-delta"
# chain_depth     = 1
# content         = {digest: "sha256:24c6d3d8…", size: 226}   <- reassembled plaintext
# object          = {digest: "sha256:7756d5ef…", size: 113}   <- appended tail only
# parent_revision = "omp/example-host/synthetic-session-0001@sha256:3224edc9…"
```

**Step 6 — reassemble by concatenation.** Walk `parent_revision` to the `full`
ancestor, then `cat` root-to-leaf. With `chain_depth: 1` the chain is the
parent's full object followed by this revision's tail. For deeper chains,
repeat step 5's lookup on each `parent_revision` until `encoding` is `full`,
collecting `object.digest` values, then reverse the list. `MaxChainDepth`
guarantees at most 25 objects.

```sh
PARENT=$(echo "$E" | jq -r .parent_revision | sed 's/.*@sha256://')
TAIL=$(echo "$E" | jq -r .object.digest | cut -d: -f2)
rclone cat "$R/cas/sha256/${PARENT:0:2}/$PARENT"  > recovered.jsonl
rclone cat "$R/cas/sha256/${TAIL:0:2}/$TAIL"     >> recovered.jsonl
```

**Step 7 — verify the reassembled plaintext.** This is the acceptance test: the
concatenation must hash to `content.digest` and match `content.size`.

```sh
sha256sum < recovered.jsonl | cut -d' ' -f1
# 24c6d3d819d0f0ab4e9a3114db0ac4e69da4b35092d703f9db32ac72cd5a91e4
echo "$E" | jq -r .content.digest
# sha256:24c6d3d819d0f0ab4e9a3114db0ac4e69da4b35092d703f9db32ac72cd5a91e4
wc -c < recovered.jsonl
# 226
```

Recovered content (synthetic fixture):

```text
{"role":"user","text":"synthetic fixture message one"}
{"role":"assistant","text":"synthetic fixture reply one"}
{"role":"user","text":"synthetic fixture message two"}
{"role":"assistant","text":"synthetic fixture reply two"}
```

**Step 8 — recover the declared closure.** Artifacts and blobs are plain CAS
objects; `artifacts[].path` is the source-relative destination.

```sh
echo "$E" | jq -r '.artifacts[] | [.digest, .path] | @tsv' \
  | while IFS=$'\t' read -r D P; do
      H=${D#sha256:}
      mkdir -p "$(dirname "$P")"
      rclone cat "$R/cas/sha256/${H:0:2}/$H" > "$P"
      sha256sum "$P"
    done
echo "$E" | jq -r '.blobs[].digest'
echo "$E" | jq -r '.unresolved_blob_refs // [] | .[]'   # known-missing, if any
```

A non-empty `unresolved_blob_refs` means the closure was incomplete at snapshot
time and `continuation_grade` is `false`: the transcript is intact, but
continuing the session elsewhere is not promised (§5.2).

**Rebuilding the whole catalog** is the same procedure widened: for each
`hosts/*/` prefix take the highest verifying commit record, load every segment
of its index, and treat the union of entries as the catalog. No PostgreSQL
state is required; the shared catalog is rebuildable from these records by
construction (SPEC.md §9, decision 28).

## 10. Open freeze items

These must be resolved and recorded before the gate closes. Each is a real
decision, not a documentation gap.

| # | Item | Current state | Decision needed |
| --- | --- | --- | --- |
| 1 | **Commit-key form and ordering model** | `keys.go` carries an explicit `DRAFT CONTRACT SURFACE` marker: the `g<10>-<digest>.json` name, its write-once rationale, and the descending-scan total order require explicit operator approval before any durable `babel/v1` remote write. | Operator approves the key grammar, the 10-digit generation cap, and the digest tie-break — or specifies an alternative. Approving this freezes §2.1. |
| 2 | **Append-chain size factor** | `MaxChainDepth = 24` bounds chain *depth* only (`types.go`, decision 40). SPEC.md §6.1 promises a frozen "chain-length/size limit"; no byte-size factor exists. | Decide whether a cumulative-delta-bytes or delta-to-full ratio trigger joins the depth bound, and its value. A depth-only bound permits 24 one-line deltas above a large full revision, or 24 large deltas dwarfing a small one. |
| 3 | **`Put` same-size collision semantics** | `objectstore.Store.Put` returns `created=false` and leaves the object untouched when a same-key object has the **same size**; byte equality is not verified, and `ErrImmutableConflict` fires only on size mismatch. Correctness rests on commit-record read-back plus content-addressing. | Decide whether the port gains a byte-comparing or conditional-write (`If-None-Match`) variant for non-CAS keys, or whether writer read-back remains the sole guard. The answer determines whether a same-size clobber is detectable at write time or only at read time. |
| 4 | **`verify` local coverage** | Head selection `Stat`s segment sizes only (§7); `LoadSegment` adds digest verification. Decision 42 splits default and `--deep` tiers, but the per-tier checklist is unwritten. | Enumerate, per tier: commit-record self-digest, index digest, segment digests, payload/artifact/blob presence and size, append-chain reachability and prefix re-proof, `sum(segments[].entries)` vs. `revisions`, hint agreement, duplicate-generation warning, and cross-host scope. Also decide whether `--deep` re-proves the exact-byte-prefix property or only the leaf content digest. |
| 5 | **Unknown-field rules for cross-version segment rewrites** | Unknown fields survive only because unchanged segments are reused byte-for-byte (§5.5). `json.Unmarshal` into `ManifestEntry` drops unknown fields, so any parse-and-remarshal erases them — confirmed empirically against the committed types. | Decide the rule when a producer must rewrite a partition containing entries written by a newer producer: refuse to rewrite that partition, quarantine unknown entries into a byte-preserved segment, add a catch-all raw-extension field to `ManifestEntry`, or accept lossy rewrite with a recorded completeness reason. Also freeze whether an unknown `manifest_schema` inside a known `segment_schema` is a skip or a hard failure. |

Related gates already recorded in SPEC.md §14 interact with this contract but
are not owned by it: the unified `storage.json` schema and host identity, the
minimal Phase A PostgreSQL schema with its immutable commit-to-catalog mapping,
and the minimum repository fingerprint that distinguishes a compatible checkout
from a misleading same-path checkout.
