import type { PluginDatabase, SqlParam } from "@manifold/plugin";
import {
  EntityIdSchema,
  NeighborhoodAnswerSchema,
  NeighborhoodFactSchema,
  NeighborhoodFilingSchema,
  NeighborhoodLinkSchema,
  NeighborhoodNodeSchema,
  NeighborhoodQuestionSchema,
  NeighborhoodRecordSchema,
  NeighborhoodSourceSchema,
  type NeighborhoodQuery,
  type NeighborhoodResult,
} from "../contract.ts";
import type { z } from "zod";

/** SQL identifiers and expressions below are fixed by this module, never caller input. */
function object(fields: Record<string, string>): string {
  return `json_object(${Object.entries(fields)
    .map(([key, value]) => `'${key}', ${value}`)
    .join(", ")})`;
}

function status(table: string, key: string, subject: string, state = "status"): string {
  return `json((SELECT ${object({
    id: "s.id",
    seq: "s.seq",
    state: `s.${state}`,
    actorId: "s.actor_id",
    actorKind:
      table === "status_events" ? "s.actor_kind" : table === "dispositions" ? "'operator'" : "NULL",
    runId: table === "status_events" ? "s.run_id" : "NULL",
    reason: table === "dispositions" ? "s.note" : "s.reason",
    at: "s.recorded_at",
  })} FROM ${table} s WHERE s.${key} = ${subject} ORDER BY s.seq DESC LIMIT 1))`;
}

const ACTIVE_FILING = `f.withdrawn = 0
  AND NOT EXISTS (SELECT 1 FROM filings newer WHERE newer.supersedes_id = f.id)
  AND NOT EXISTS (SELECT 1 FROM filings newer
    WHERE newer.record_id = f.record_id AND newer.entity_id = f.entity_id
      AND (newer.created_at > f.created_at OR
        (newer.created_at = f.created_at AND newer.rowid > f.rowid)))`;

const NODE = object({
  id: "e.id",
  name: "e.name",
  kind: "e.kind",
  canonicalId: "e.canonical_id",
  createdBy: "e.created_by",
  createdAt: "e.created_at",
  depth: "?",
});

/**
 * A records-only projection: containers:read grants the same stored rows as the other read
 * doors, not Recall, transcript maps or archive bytes. No index refresh or durable read trace
 * is needed. Unknown archive access and unknown unreviewed inventory remain explicitly unknown.
 */
export async function readNeighborhood(
  db: PluginDatabase,
  query: NeighborhoodQuery,
): Promise<NeighborhoodResult> {
  const { entityId, ...limits } = query;
  const result: NeighborhoodResult = {
    entityId,
    state: "missing",
    limits,
    nodes: [],
    facts: [],
    records: [],
    filings: [],
    questions: [],
    answers: [],
    links: [],
    sources: [],
    coverage: {
      scope: "stored-linked-material",
      traversalComplete: true,
      recordsComplete: true,
      truncated: false,
      reasons: [],
      visitedNodes: 0,
      returnedItems: 0,
      omittedItems: 0,
      omittedNodesAtLeast: 0,
      unavailableEntities: 0,
      inaccessibleMaterial: null,
      unreviewedMaterial: null,
      resultBytes: 0,
    },
  };
  const coverage = result.coverage;
  const bytes = (value: unknown): number => Buffer.byteLength(JSON.stringify(value), "utf8");
  // Reserve room for changing counters and every possible reason; never cut a stored value.
  let remainingBytes = limits.maxBytes - bytes(result) - 512;
  const reason = (value: NeighborhoodResult["coverage"]["reasons"][number]): void => {
    if (!coverage.reasons.includes(value)) coverage.reasons.push(value);
    coverage.truncated = true;
  };
  const omittedNodes = new Set<string>();
  const unavailable = new Set<string>();
  const visited = new Set<string>();

  const node = async (id: string, depth: number): Promise<boolean> => {
    const rows = await db.query<{ item: string | null }>(
      `WITH candidate AS (SELECT ${NODE} AS item FROM entities e WHERE e.id = ?)
       SELECT CASE WHEN length(CAST(item AS BLOB)) <= ? THEN item END AS item FROM candidate`,
      [depth, id, remainingBytes],
    );
    const row = rows[0];
    if (row === undefined || !EntityIdSchema.safeParse(id).success) {
      unavailable.add(id);
      reason("unavailable");
      coverage.traversalComplete = false;
      return false;
    }
    if (depth === 0) result.state = "found";
    if (row.item === null) {
      omittedNodes.add(id);
      reason("bytes");
      coverage.traversalComplete = false;
      return false;
    }
    const parsed = NeighborhoodNodeSchema.parse(JSON.parse(row.item));
    result.nodes.push(parsed);
    visited.add(id);
    remainingBytes -= bytes(parsed) + 1;
    return true;
  };

  if (await node(entityId, 0)) {
    // Breadth first, with binary id order within each parent. A second parent and a cycle
    // cannot spend another node slot; contains and inverse part-of are one downward spine.
    for (let cursor = 0; cursor < result.nodes.length; cursor++) {
      const parent = result.nodes[cursor]!;
      const excluded = JSON.stringify([...visited, ...omittedNodes, ...unavailable]);
      const children = await db.query<{ id: string }>(
        `SELECT id FROM (
           SELECT to_id AS id FROM edges WHERE from_kind = 'entity' AND to_kind = 'entity'
             AND from_id = ? AND kind = 'contains'
           UNION
           SELECT from_id AS id FROM edges WHERE from_kind = 'entity' AND to_kind = 'entity'
             AND to_id = ? AND kind = 'part-of'
         ) WHERE id NOT IN (SELECT value FROM json_each(?)) ORDER BY id LIMIT ?`,
        [parent.id, parent.id, excluded, limits.maxNodes + 1],
      );
      for (const child of children) {
        // A dangling endpoint is unavailable, not a silently absent leaf. Check existence
        // even at the requested frontier, but do not inspect its facts or relationships.
        const exists = await db.query<{ id: string }>("SELECT id FROM entities WHERE id = ?", [
          child.id,
        ]);
        if (exists.length === 0 || !EntityIdSchema.safeParse(child.id).success) {
          unavailable.add(child.id);
          reason("unavailable");
          coverage.traversalComplete = false;
        } else if (parent.depth >= limits.depth) {
          omittedNodes.add(child.id);
          reason("depth");
          coverage.traversalComplete = false;
        } else if (result.nodes.length >= limits.maxNodes) {
          omittedNodes.add(child.id);
          reason("nodes");
          coverage.traversalComplete = false;
        } else {
          await node(child.id, parent.depth + 1);
        }
      }
      // The sentinel bounds even a hostile high-degree graph. Its unknown tail is not counted.
      if (children.length === limits.maxNodes + 1) {
        reason(parent.depth >= limits.depth ? "depth" : "nodes");
        coverage.traversalComplete = false;
      }
    }
  }

  const nodeIds = JSON.stringify(result.nodes.map((entry) => entry.id));
  const scope = `WITH selected AS (SELECT value AS id FROM json_each(?)),
    filed AS (SELECT f.* FROM filings f WHERE f.entity_id IN (SELECT id FROM selected)
      AND ${ACTIVE_FILING}),
    associated AS (SELECT r.* FROM records r WHERE EXISTS (
      SELECT 1 FROM filed f WHERE f.record_id = r.id)),
    relevant_questions AS (SELECT q.* FROM questions q WHERE
      EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.entities') target
        WHERE target.value IN (SELECT id FROM selected))
      OR EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.subjects') target
        WHERE target.type = 'text' AND (
          target.value IN (SELECT id FROM selected)
          OR target.value IN (SELECT e.name FROM entities e JOIN selected n ON n.id = e.id)
          OR target.value IN (SELECT a.value FROM aliases a JOIN selected n ON n.id = a.entity_id
            WHERE a.retired_at IS NULL)))
      OR EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(q.payload) THEN q.payload ELSE '{}' END, '$.work') work
        WHERE EXISTS (SELECT 1 FROM associated r
          WHERE r.id = json_extract(CASE WHEN work.type = 'object' THEN work.value ELSE '{}' END, '$.id')
            AND r.kind = json_extract(CASE WHEN work.type = 'object' THEN work.value ELSE '{}' END, '$.kind'))))`;

  /** Each category has an exact omitted count within the returned entities, never the unseen tree. */
  const collect = async <T>(
    target: T[],
    schema: z.ZodType<T>,
    select: string,
    from: string,
    order: string,
  ): Promise<void> => {
    const params: SqlParam[] = [nodeIds];
    const counted = await db.query<{ total: bigint | number }>(
      `${scope} SELECT COUNT(*) AS total ${from}`,
      params,
    );
    const total = Number(counted[0]?.total ?? 0);
    const slots = limits.maxItems - coverage.returnedItems;
    if (total > slots) reason("items");
    if (slots > 0 && total > 0) {
      const rows = await db.query<{ item: string | null }>(
        `${scope}, candidates AS (SELECT ${select} AS item ${from} ORDER BY ${order} LIMIT ?)
         SELECT CASE WHEN length(CAST(item AS BLOB)) <= ? THEN item END AS item FROM candidates`,
        [...params, slots, remainingBytes],
      );
      for (const row of rows) {
        if (row.item === null) {
          reason("bytes");
          continue;
        }
        const parsed = schema.safeParse(JSON.parse(row.item));
        if (!parsed.success) {
          // Historical imported identifiers may be unnameable by today's doors (#426).
          reason("unavailable");
          continue;
        }
        const size = bytes(parsed.data) + 1;
        if (size > remainingBytes) {
          reason("bytes");
          continue;
        }
        target.push(parsed.data);
        remainingBytes -= size;
        coverage.returnedItems++;
      }
    }
    coverage.omittedItems += total - target.length;
  };

  if (result.nodes.length > 0) {
    await collect(
      result.facts,
      NeighborhoodFactSchema,
      object({
        id: "f.id",
        entityId: "f.entity_id",
        predicate: "f.predicate",
        value: "f.value",
        objectId: "f.object_id",
        validFrom: "f.valid_from",
        validUntil: "f.valid_until",
        observedAt: "f.observed_at",
        authorityKind: "f.authority_kind",
        authorityId: "f.authority_id",
        confidence: "f.confidence",
        note: "f.note",
        supersedesId: "f.supersedes_id",
        replacedBy:
          "(SELECT id FROM facts newer WHERE newer.supersedes_id = f.id ORDER BY newer.recorded_at DESC, newer.id LIMIT 1)",
        recordedAt: "f.recorded_at",
        status: status("fact_status", "fact_id", "f.id"),
      }),
      "FROM facts f WHERE f.entity_id IN (SELECT id FROM selected)",
      "f.entity_id, f.predicate, f.recorded_at DESC, f.id",
    );

    await collect(
      result.records,
      NeighborhoodRecordSchema,
      object({
        id: "r.id",
        kind: "r.kind",
        rootId: "r.root_id",
        supersedesId: "r.supersedes_id",
        replacedBy:
          "(SELECT id FROM records newer WHERE newer.supersedes_id = r.id ORDER BY newer.seq DESC, newer.id LIMIT 1)",
        parentId: "r.parent_id",
        seq: "r.seq",
        title: "r.title",
        payloadJson: "r.payload",
        runId: "r.run_id",
        recipeId: "r.recipe_id",
        recipeVersion: "r.recipe_version",
        actorKind: "r.actor_kind",
        actorId: "r.actor_id",
        createdAt: "r.created_at",
        status: status("status_events", "record_id", "r.id"),
        ruling: status("dispositions", "record_id", "r.id", "disposition"),
      }),
      "FROM associated r",
      "r.created_at DESC, r.id",
    );

    await collect(
      result.filings,
      NeighborhoodFilingSchema,
      object({
        id: "f.id",
        recordId: "f.record_id",
        entityId: "f.entity_id",
        rationale: "f.rationale",
        authorKind: "f.author_kind",
        authorId: "f.author_id",
        heuristic: "json(CASE WHEN f.heuristic = 0 THEN 'false' ELSE 'true' END)",
        createdAt: "f.created_at",
      }),
      "FROM filed f",
      "f.entity_id, f.record_id, f.id",
    );

    await collect(
      result.questions,
      NeighborhoodQuestionSchema,
      object({
        id: "q.id",
        kind: "q.kind",
        class: "q.class",
        text: "q.text",
        why: "q.why",
        payloadJson: "q.payload",
        raisedByKind: "q.raised_by_kind",
        raisedById: "q.raised_by_id",
        createdAt: "q.created_at",
        status: status("question_events", "question_id", "q.id", "state"),
        effectiveState:
          "COALESCE((SELECT e.state FROM question_events e WHERE e.question_id = q.id ORDER BY e.seq DESC LIMIT 1), 'open')",
      }),
      "FROM relevant_questions q",
      "q.created_at DESC, q.id",
    );

    await collect(
      result.answers,
      NeighborhoodAnswerSchema,
      object({
        id: "a.id",
        questionId: "a.question_id",
        actorId: "a.actor_id",
        outcome: "a.outcome",
        text: "a.text",
        recordedAt: "a.recorded_at",
      }),
      "FROM answers a WHERE a.question_id IN (SELECT id FROM relevant_questions)",
      "a.recorded_at DESC, a.id",
    );

    const incident = `(e.from_kind = 'entity' AND e.from_id IN (SELECT id FROM selected))
      OR (e.to_kind = 'entity' AND e.to_id IN (SELECT id FROM selected))
      OR (e.from_kind = 'fact' AND e.from_id IN (SELECT id FROM facts WHERE entity_id IN (SELECT id FROM selected)))
      OR (e.to_kind = 'fact' AND e.to_id IN (SELECT id FROM facts WHERE entity_id IN (SELECT id FROM selected)))
      OR EXISTS (SELECT 1 FROM associated r WHERE
        (e.from_kind = r.kind AND e.from_id = r.id) OR (e.to_kind = r.kind AND e.to_id = r.id))
      OR EXISTS (SELECT 1 FROM relevant_questions q WHERE
        (e.from_kind = 'question' AND e.from_id = q.id) OR (e.to_kind = 'question' AND e.to_id = q.id))`;
    const containment = "e.from_kind = 'entity' AND e.to_kind = 'entity'";
    await collect(
      result.links,
      NeighborhoodLinkSchema,
      object({
        id: "e.id",
        kind: "e.kind",
        fromKind: "e.from_kind",
        fromId: "e.from_id",
        toKind: "e.to_kind",
        toId: "e.to_id",
        position: "e.position",
        note: "e.note",
        actorKind: "e.actor_kind",
        actorId: "e.actor_id",
        createdAt: "e.created_at",
        parentId: `CASE WHEN ${containment} AND e.kind = 'contains' THEN e.from_id WHEN ${containment} AND e.kind = 'part-of' THEN e.to_id END`,
        childId: `CASE WHEN ${containment} AND e.kind = 'contains' THEN e.to_id WHEN ${containment} AND e.kind = 'part-of' THEN e.from_id END`,
      }),
      `FROM edges e WHERE ${incident}`,
      "e.kind, e.from_kind, e.from_id, e.to_kind, e.to_id, e.id",
    );

    await collect(
      result.sources,
      NeighborhoodSourceSchema,
      object({
        selector: "s.selector",
        title: "s.title",
        repositoryIdentity: "s.repository_identity",
        repositoryRemote: "s.repository_remote",
        snapshotId: "s.snapshot_id",
        archivePath: "s.archive_path",
        archiveLabel: "s.archive_label",
        contentDigest: "s.content_digest",
        modifiedAt: "s.modified_at",
        archivedAt: "s.archived_at",
        authority: "'current-catalog'",
        reviewState: "'unknown'",
      }),
      `FROM sessions s WHERE EXISTS (SELECT 1 FROM edges e JOIN associated r
        ON r.id = e.from_id AND r.kind = e.from_kind
        WHERE e.kind = 'cites' AND e.to_kind = 'session' AND e.to_id = s.selector)
      OR EXISTS (SELECT 1 FROM facts f WHERE f.entity_id IN (SELECT id FROM selected)
        AND f.predicate = 'repository-remote' AND f.value = s.repository_remote)`,
      "s.selector",
    );
  }

  coverage.visitedNodes = visited.size;
  coverage.omittedNodesAtLeast = omittedNodes.size;
  coverage.unavailableEntities = unavailable.size;
  coverage.recordsComplete = coverage.omittedItems === 0 && coverage.traversalComplete;
  // Count the wire JSON including this very field. Decimal digit growth converges immediately.
  let measured = bytes(result);
  while (coverage.resultBytes !== measured) {
    coverage.resultBytes = measured;
    measured = bytes(result);
  }
  return result;
}
