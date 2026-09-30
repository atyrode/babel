import { createHash } from "node:crypto";
import type { SqlParam, SqlRow } from "@manifold/plugin";
import {
  DuplicateAppliedSchema,
  DuplicateIntentSchema,
  DuplicateMemberSchema,
  type DuplicateApplied,
  type DuplicateApplyInput,
  type DuplicateIntent,
  type DuplicateMember,
  type DuplicatePlan,
  type DuplicatePlanInput,
  type DuplicatePreview,
  type Suggested,
} from "../contract.ts";
import {
  ActRefused,
  newId,
  stamp,
  suggestionsOf,
  type ActsStore,
  type SuggestArgs,
} from "./acts.ts";
import { duplicateEligibleSql, duplicateSnapshotGuard, duplicateSnapshotSql } from "./schema.ts";

const hash = (value: string): string => createHash("sha256").update(value).digest("hex");
const compare = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

type SnapshotRecord = Record<string, unknown> & {
  id: string;
  kind: DuplicateMember["kind"];
  seq: number;
  run_id: string | null;
  title: string;
  payload: string;
};
interface Snapshot {
  records: SnapshotRecord[];
  sources: string[];
  unresolvedSources: number;
}
interface Candidate {
  member: DuplicateMember;
  title: string;
  claim: string;
  snapshot: string;
}

function candidate(row: SqlRow): Candidate {
  const serialized = String(row["snapshot"]);
  const snapshot = JSON.parse(serialized) as Snapshot;
  const record = snapshot.records.find((held) => held.id === row["id"]);
  if (record === undefined) throw new ActRefused("a duplicate snapshot lost its record");
  const parsed = DuplicateMemberSchema.safeParse({
    recordId: record.id,
    revision: record.seq,
    fingerprint: hash(serialized),
    kind: record.kind,
    runId: record.run_id ?? "",
    sourceIds: snapshot.sources,
    unresolvedSources: snapshot.unresolvedSources,
  });
  if (!parsed.success) {
    throw new ActRefused(
      `duplicate member ${record.id} has unrepresentable provenance or revision`,
    );
  }
  let payload: unknown;
  try {
    payload = JSON.parse(record.payload);
  } catch {
    throw new ActRefused(`duplicate member ${record.id} has unreadable content`);
  }
  // A proposal's problem alone omits its remedy; a finding's pattern alone omits its scope.
  // Compare the complete immutable content, not a title or a one-field summary. Jev's explicit
  // per-call bound refuses oversized pairs rather than silently discarding distinguishing text.
  const claim = JSON.stringify({ kind: record.kind, title: record.title, content: payload });
  return {
    member: parsed.data,
    title: record.title,
    claim,
    snapshot: serialized,
  };
}

async function candidates(
  store: ActsStore,
  predicate: string,
  params: readonly SqlParam[],
): Promise<Candidate[]> {
  const rows = await store.db.query(
    `SELECT r.id, ${duplicateSnapshotSql("r.id")} AS snapshot FROM records r
      WHERE ${duplicateEligibleSql("r")} AND ${predicate} ORDER BY r.id`,
    params,
  );
  return rows.map(candidate);
}

/** Free, bounded read. Exact-id rereads omit unavailable members rather than substituting others. */
export async function duplicatePlan(
  store: ActsStore,
  args: DuplicatePlanInput,
): Promise<DuplicatePlan> {
  const ids = [...new Set(args.ids)].sort(compare);
  const predicate =
    ids.length > 0
      ? `r.id IN (${ids.map(() => "?").join(",")})`
      : `r.id > ?
    AND NOT EXISTS (SELECT 1 FROM next_actions n, json_each(n.payload, '$.intent.members') m
      WHERE json_extract(n.payload, '$.duplicateCluster') IS NOT NULL
        AND json_extract(m.value, '$.recordId') = r.id
        AND json_extract(m.value, '$.revision') = r.seq
        AND ${duplicateSnapshotGuard("json_extract(n.payload, '$.duplicateSnapshots')")})`;
  const params = ids.length > 0 ? ids : [args.after];
  const [count] = await store.db.query(
    `SELECT COUNT(*) AS n FROM records r WHERE ${duplicateEligibleSql("r")} AND ${predicate}`,
    params,
  );
  const eligible = Number(count?.["n"] ?? 0);
  // The LIMIT is applied before expensive provenance expansion, not after reading the corpus.
  const page = await candidates(
    store,
    `r.id IN (SELECT r.id FROM records r
    WHERE ${duplicateEligibleSql("r")} AND ${predicate} ORDER BY r.id LIMIT ?)`,
    [...params, ids.length > 0 ? ids.length : args.limit],
  );
  const size = page.length;
  return {
    candidates: page.map(({ member, title, claim }) => ({ ...member, title, claim })),
    eligible,
    continuation: ids.length === 0 && eligible > size ? (page.at(-1)?.member.recordId ?? "") : "",
    maxPairs: (size * (size - 1)) / 2,
    newSuggestionsUpperBound: Math.floor(size / 2),
  };
}

function audit(members: readonly DuplicateMember[]): DuplicateIntent["audit"] {
  return {
    records: members.length,
    distinctRuns: new Set(members.map((member) => member.runId).filter((id) => id !== "")).size,
    distinctSources: new Set(members.flatMap((member) => member.sourceIds)).size,
    missingRuns: members.filter((member) => member.runId === "").length,
    missingSources: members.filter(
      (member) => member.sourceIds.length === 0 || member.unresolvedSources > 0,
    ).length,
  };
}

/** Canonical topology; none of the supplied provenance or audit is an authority. */
function canonicalIntent(input: DuplicateIntent): DuplicateIntent {
  const parsed = DuplicateIntentSchema.safeParse(input);
  if (!parsed.success) throw new ActRefused("invalid duplicate intent");
  const members = parsed.data.members
    .map((member) => ({ ...member, sourceIds: [...new Set(member.sourceIds)].sort(compare) }))
    .sort((a, b) => compare(a.recordId, b.recordId));
  const ids = new Set(members.map((member) => member.recordId));
  if (ids.size !== members.length || !ids.has(input.representative)) {
    throw new ActRefused("duplicate cluster needs distinct members and a member representative");
  }
  if (new Set(members.map((member) => member.kind)).size !== 1) {
    throw new ActRefused("duplicate cluster members must have the same record kind");
  }
  const pairs = parsed.data.pairs
    .map((pair) => (pair.a < pair.b ? pair : { a: pair.b, b: pair.a, evidence: pair.evidence }))
    .sort((a, b) => compare(a.a, b.a) || compare(a.b, b.b));
  const seen = new Set<string>();
  for (const pair of pairs) {
    const key = `${pair.a}:${pair.b}`;
    if (
      pair.a === pair.b ||
      !ids.has(pair.a) ||
      !ids.has(pair.b) ||
      seen.has(key) ||
      pair.evidence.trim() === ""
    ) {
      throw new ActRefused("duplicate cluster has an invalid or repeated evidence pair");
    }
    seen.add(key);
  }
  const reached = new Set([input.representative]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const pair of pairs) {
      if (reached.has(pair.a) === reached.has(pair.b)) continue;
      reached.add(pair.a);
      reached.add(pair.b);
      changed = true;
    }
  }
  if (reached.size !== members.length)
    throw new ActRefused("duplicate cluster evidence is disconnected");
  return {
    kind: "merge-duplicate-records",
    representative: input.representative,
    members,
    pairs,
    audit: audit(members),
  };
}

async function validateMembers(store: ActsStore, intent: DuplicateIntent): Promise<Candidate[]> {
  const ids = intent.members.map((member) => member.recordId);
  const held = await candidates(store, `r.id IN (${ids.map(() => "?").join(",")})`, ids);
  if (held.length !== ids.length)
    throw new ActRefused("duplicate member is missing, superseded or no longer eligible");
  for (let index = 0; index < held.length; index++) {
    if (JSON.stringify(held[index]?.member) !== JSON.stringify(intent.members[index])) {
      throw new ActRefused("duplicate member revision, fingerprint or provenance changed");
    }
  }
  return held;
}

function clusterKey(intent: DuplicateIntent): string {
  return hash(
    JSON.stringify(
      intent.members.map((member) => [member.recordId, member.revision, member.fingerprint]),
    ),
  );
}

/** Called only after suggest() resolves the allow-listed principal. It still writes only a suggestion. */
export async function suggestDuplicate(
  store: ActsStore,
  args: SuggestArgs,
  suggester: string,
): Promise<Suggested> {
  if (args.intent === undefined) throw new ActRefused("duplicate suggestion has no intent");
  const intent = canonicalIntent(args.intent);
  const representative = intent.members.find((member) => member.recordId === intent.representative);
  if (
    args.kind !== "ask-question" ||
    args.subject !== "" ||
    args.aspect !== "duplicate-cluster" ||
    args.recordId !== intent.representative ||
    args.revision !== representative?.revision
  ) {
    throw new ActRefused(
      "duplicate suggestion must name its representative and duplicate-cluster intent",
    );
  }
  const held = await validateMembers(store, intent);
  // Recompute from rows even though validateMembers also refuses forged provenance.
  intent.members = held.map((entry) => entry.member);
  intent.audit = audit(intent.members);
  const key = clusterKey(intent);
  const membersKey = hash(
    JSON.stringify(intent.members.map((member) => [member.recordId, member.revision])),
  );
  const [previous] = await store.db.query(
    `SELECT n.id FROM next_actions n
    WHERE json_extract(n.payload, '$.duplicateMembers') = ?
      AND json_extract(n.payload, '$.duplicateCluster') <> ?
      AND NOT EXISTS (SELECT 1 FROM next_actions s WHERE json_extract(s.payload, '$.supersedes') = n.id)
    ORDER BY n.created_at DESC, n.id DESC LIMIT 1`,
    [membersKey, key],
  );
  const at = stamp(store.now());
  const payload = JSON.stringify({
    rationale: args.rationale,
    revision: args.revision,
    suggester,
    supersedes: previous === undefined ? "" : String(previous["id"]),
    subject: "",
    aspect: "duplicate-cluster",
    basis: args.basis,
    intent,
    duplicateCluster: key,
    duplicateMembers: membersKey,
    duplicateSnapshots: held.map((entry) => ({
      recordId: entry.member.recordId,
      snapshot: entry.snapshot,
    })),
  });
  try {
    await store.db.batch([
      {
        sql: `INSERT INTO next_actions(id, record_id, kind, proposed_by_kind, proposed_by_id, summary, created_at, payload)
        SELECT ?, ?, 'ask-question', 'engine', ?, ?, ?, ? WHERE NOT EXISTS (
          SELECT 1 FROM next_actions WHERE json_extract(payload, '$.duplicateCluster') = ?)`,
        params: [newId("nxt"), args.recordId, suggester, args.summary, at, payload, key],
      },
    ]);
  } catch (error) {
    if (error instanceof Error && error.message.includes("duplicate members changed"))
      throw new ActRefused(error.message);
    throw error;
  }
  const [row] = await store.db.query(
    `SELECT id, record_id, proposed_by_id, created_at, payload
    FROM next_actions WHERE json_extract(payload, '$.duplicateCluster') = ? ORDER BY created_at, id LIMIT 1`,
    [key],
  );
  if (row === undefined) throw new Error("duplicate suggestion was not retained");
  const retained = JSON.parse(String(row["payload"])) as { revision: number; supersedes: string };
  store.touch();
  return {
    id: String(row["id"]),
    recordId: String(row["record_id"]),
    revision: retained.revision,
    kind: "ask-question",
    subject: "",
    aspect: "duplicate-cluster",
    suggester: String(row["proposed_by_id"]),
    supersedes: retained.supersedes,
    at: String(row["created_at"]),
    outstanding: (await suggestionsOf(store, suggester)).outstanding,
  };
}

async function application(
  store: ActsStore,
  id: string,
): Promise<{ fingerprint: string; result: DuplicateApplied } | null> {
  const [row] = await store.db.query(
    `SELECT fingerprint, outcome FROM duplicate_applications WHERE next_action_id = ?`,
    [id],
  );
  return row === undefined
    ? null
    : {
        fingerprint: String(row["fingerprint"]),
        result: DuplicateAppliedSchema.parse(JSON.parse(String(row["outcome"]))),
      };
}

async function proposed(
  store: ActsStore,
  id: string,
): Promise<{ intent: DuplicateIntent; fingerprint: string }> {
  const [row] = await store.db.query(
    `SELECT record_id, kind, proposed_by_kind, payload FROM next_actions WHERE id = ?`,
    [id],
  );
  if (row === undefined) throw new ActRefused(`no proposed action ${id}`);
  const payload = JSON.parse(String(row["payload"])) as Record<string, unknown>;
  const parsed = DuplicateIntentSchema.safeParse(payload["intent"]);
  if (
    !parsed.success ||
    row["proposed_by_kind"] !== "engine" ||
    row["kind"] !== "ask-question" ||
    !Array.isArray(payload["duplicateSnapshots"]) ||
    typeof payload["duplicateCluster"] !== "string"
  ) {
    throw new ActRefused(
      "this suggestion has no admitted duplicate intent; generic decisions apply nothing",
    );
  }
  const intent = canonicalIntent(parsed.data);
  if (
    row["record_id"] !== intent.representative ||
    payload["duplicateCluster"] !== clusterKey(intent)
  ) {
    throw new ActRefused("duplicate suggestion identity is invalid");
  }
  return {
    intent,
    fingerprint: hash(
      JSON.stringify({ nextActionId: id, intent, snapshots: payload["duplicateSnapshots"] }),
    ),
  };
}

/** Read-only operator preview; stale members are an explicit refused state, never a partial plan. */
export async function duplicatePreview(
  store: ActsStore,
  nextActionId: string,
): Promise<DuplicatePreview> {
  const { intent, fingerprint } = await proposed(store, nextActionId);
  const applied = await application(store, nextActionId);
  let reason = "";
  if (applied === null) {
    try {
      await validateMembers(store, intent);
      const [state] = await store.db.query(
        `SELECT
        (SELECT decision FROM next_action_rulings WHERE next_action_id = ? ORDER BY seq DESC LIMIT 1) AS decision,
        EXISTS(SELECT 1 FROM next_actions WHERE json_extract(payload, '$.supersedes') = ?) AS replaced`,
        [nextActionId, nextActionId],
      );
      if (state?.["decision"] === "declined" || Number(state?.["replaced"] ?? 0) !== 0) {
        throw new ActRefused("duplicate suggestion is declined or superseded");
      }
    } catch (error) {
      if (!(error instanceof ActRefused)) throw error;
      reason = error.message;
    }
  }
  const existing = await store.db.query(
    `SELECT to_id FROM edges WHERE from_id = ? AND kind = 'corroborates'
    AND from_kind = ? AND to_kind = ?`,
    [intent.representative, intent.members[0]?.kind ?? "", intent.members[0]?.kind ?? ""],
  );
  const targets = new Set(existing.map((row) => String(row["to_id"])));
  const links: DuplicatePreview["links"] = intent.members
    .filter((member) => member.recordId !== intent.representative)
    .map((member) => ({
      fromId: intent.representative,
      toId: member.recordId,
      kind: "corroborates",
      exists: targets.has(member.recordId),
    }));
  return {
    nextActionId,
    recordId: intent.representative,
    intent,
    fingerprint: applied?.fingerprint ?? hash(JSON.stringify({ fingerprint, links })),
    links,
    state: applied !== null ? "applied" : reason === "" ? "ready" : "refused",
    reason,
    application: applied?.result ?? null,
  };
}

/** The trigger rechecks eligibility and exact snapshots inside the same transaction that appends links. */
export async function duplicateApply(
  store: ActsStore,
  args: DuplicateApplyInput,
  operatorId: string,
): Promise<DuplicateApplied> {
  if (operatorId === "" || args.confirm !== true)
    throw new ActRefused("duplicate application needs an operator and explicit confirmation");
  const prior = await application(store, args.nextActionId);
  if (prior !== null) {
    if (prior.fingerprint !== args.fingerprint)
      throw new ActRefused("duplicate preview fingerprint changed");
    return prior.result;
  }
  const preview = await duplicatePreview(store, args.nextActionId);
  if (preview.fingerprint !== args.fingerprint)
    throw new ActRefused("duplicate preview fingerprint changed");
  if (preview.state === "refused") throw new ActRefused(preview.reason);
  const outcome: DuplicateApplied = {
    nextActionId: args.nextActionId,
    recordId: preview.recordId,
    operatorId,
    at: stamp(store.now()),
    links: preview.links.map(({ fromId, toId, kind }) => ({ fromId, toId, kind })),
  };
  try {
    await store.db.batch([
      {
        sql: `INSERT INTO duplicate_applications(next_action_id, fingerprint, operator_id, applied_at, outcome, expected_links)
        SELECT ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM duplicate_applications WHERE next_action_id = ?)`,
        params: [
          args.nextActionId,
          args.fingerprint,
          operatorId,
          outcome.at,
          JSON.stringify(outcome),
          JSON.stringify(preview.links),
          args.nextActionId,
        ],
      },
    ]);
  } catch (error) {
    if (error instanceof Error && error.message.includes("duplicate application refused"))
      throw new ActRefused(error.message);
    throw error;
  }
  const retained = await application(store, args.nextActionId);
  if (retained === null) throw new Error("duplicate application was not retained");
  if (retained.fingerprint !== args.fingerprint)
    throw new ActRefused("duplicate preview fingerprint changed");
  store.touch();
  return retained.result;
}
