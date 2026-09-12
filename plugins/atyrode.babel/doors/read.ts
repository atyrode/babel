/*
  THE READING DOORS: the nine questions a reader asks of Babel.

  Every one of them is a `defineServerAction` over the contract's own schemas, and the kit parses
  both ends — a request outside the input schema is refused by name, and a result outside the
  result schema refuses the dispatch rather than reaching a panel. That is what makes the
  vocabulary checkable: a misspelled sort answered with the default order would show a reader a
  different feed from the one he asked for and say nothing, and a kind nothing matches answered
  with an empty list reads as a deployment that has produced none of them.

  Five of the results are not in `contract.ts` yet, and they are declared here rather than
  invented twice: the thread's and the topic's queries, one topic, one run, and the policy. Each
  is built out of the contract's own row schemas, so a panel's rendering code is unaffected when
  they move; they are reported as contract additions.

  Reading requires `containers:read` of its caller and nothing else. None of these doors writes,
  and none of them touches a slice of the host: the store is the plugin's own tables.
*/

import { z } from "zod";
import {
  ACTIONS,
  EntityIdSchema,
  FeedQuerySchema,
  FeedResultSchema,
  PulseResultSchema,
  RecordIdSchema,
  RecordPeelSchema,
  RunRowSchema,
  RunsResultSchema,
  ThreadResultSchema,
  TopicProposalSchema,
  TopicRowSchema,
  TopicsResultSchema,
} from "../contract.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";
import { defineServerAction } from "@manifold/plugin-kit/server";

/** Reading is a read of the plugin's own rows; the caller needs the workspace it asked about. */
const READ_CAPS = ["containers:read"] as const;

// ---------------------------------------------------------------------------- the unspelled five

/** `thread` takes the record whose conversation is wanted; the same identifier `record` takes. */
export const ThreadQuerySchema = z.strictObject({ id: RecordIdSchema });

/** `topic` takes an entity id, a topic name, or the reserved `unfiled`. */
export const TopicQuerySchema = z.strictObject({ topic: z.string().trim().min(1).max(200) });

/**
 * One topic, its open proposals and its own feed, in one answer, because they are one decision:
 * a reader on a topic page is choosing between what is filed under it, what Babel proposes to do
 * to it, and where he stands toward it, and a page that had to ask three times would let the
 * three disagree about what exists.
 */
export const TopicResultSchema = z.strictObject({
  topic: TopicRowSchema.nullable(),
  proposed: z.array(TopicProposalSchema),
  feed: FeedResultSchema,
});

export const RunsQuerySchema = z.strictObject({
  limit: z.number().int().min(1).max(100).default(25),
  offset: z.number().int().min(0).default(0),
  state: z.enum(["queued", "running", "finished", "failed", "stopped"]).optional(),
  machineId: z.string().max(120).optional(),
  kind: z.string().max(40).optional(),
});

export const RunQuerySchema = z.strictObject({ id: z.string().min(1).max(200) });

/**
 * One run and the receipt it wrote. The receipt travels as the document the machine half
 * produced rather than as a projection of it: §7 makes the receipt the run's own account of what
 * it was asked, read, produced and cost, and a surface that re-stated it in its own fields would
 * be a second answer to a question the run already answered.
 */
export const RunResultSchema = z.strictObject({
  run: RunRowSchema.nullable(),
  receipt: z.record(z.string(), z.unknown()).nullable(),
});

export const RecipeRowSchema = z.strictObject({
  id: z.string(),
  title: z.string(),
  looksFor: z.string(),
  enabled: z.boolean(),
  lastRanAt: z.string(),
  lastRunId: z.string(),
  runs: z.number().int(),
});

/**
 * The evaluation policy in force, as Watch reads it: the ceilings and the lanes projected out of
 * the stored document, what has been spent against them today, the recipes joined to what has
 * actually run under them — and the document itself, so the projection above can be checked
 * against the row it came from rather than believed.
 */
export const PolicyResultSchema = z.strictObject({
  version: z.string(),
  seq: z.number().int(),
  actorId: z.string(),
  reason: z.string(),
  recordedAt: z.string(),
  ceilings: z.strictObject({
    perRunUsd: z.number(),
    perDayUsd: z.number(),
    concurrent: z.number(),
  }),
  spentTodayUsd: z.number(),
  lanes: z.array(z.strictObject({ lane: z.string(), role: z.string(), share: z.number() })),
  recipes: z.array(RecipeRowSchema),
  payload: z.record(z.string(), z.unknown()),
});

/** `pulse` and `topics` are asked without arguments; a strict empty object says so on the wire. */
const NoQuerySchema = z.strictObject({});

// ---------------------------------------------------------------------------- the doors

export function readDoors(store: BabelStore): readonly Door[] {
  return [
    defineDoor(
      defineServerAction({
        name: ACTIONS.feed,
        title: "Read the feed",
        caps: READ_CAPS,
        input: FeedQuerySchema,
        result: FeedResultSchema,
      }),
      async (_ctx, query) => await store.feed(query),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.record,
        title: "Read one record",
        caps: READ_CAPS,
        input: z.strictObject({ id: RecordIdSchema }),
        result: RecordPeelSchema,
      }),
      async (_ctx, { id }) => {
        // An observation is evidence rather than a post (§4.13), and the peel's top row is a
        // `FeedPost` whose kind is one of the four post kinds — so serving one would mean
        // calling it a hypothesis. It is refused by name and reached from the records that cite
        // it, until `RecordPeelSchema`'s post can carry a record kind.
        if (id.startsWith("obs_")) {
          return { refused: `${id} is an observation: evidence for the records that cite it, not a post` };
        }
        const peeled = await store.record(id);
        // A record this deployment does not hold is a refusal naming the identifier rather than
        // an empty document: a peel of nothing renders as a record that says nothing.
        return peeled ?? { refused: `no record ${id}` };
      },
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.thread,
        title: "Read the conversation under a record",
        caps: READ_CAPS,
        input: ThreadQuerySchema,
        result: ThreadResultSchema,
      }),
      async (_ctx, { id }) => await store.thread(id),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.topics,
        title: "Read what the records are about",
        caps: READ_CAPS,
        input: NoQuerySchema,
        result: TopicsResultSchema,
      }),
      async () => await store.topics(),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.topic,
        title: "Read one topic",
        caps: READ_CAPS,
        input: TopicQuerySchema,
        result: TopicResultSchema,
      }),
      async (_ctx, { topic }) => await store.topic(topic),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.pulse,
        title: "Read what Babel did today",
        caps: READ_CAPS,
        input: NoQuerySchema,
        result: PulseResultSchema,
      }),
      async () => await store.pulse(),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.runs,
        title: "Read the runs",
        caps: READ_CAPS,
        input: RunsQuerySchema,
        result: RunsResultSchema,
      }),
      async (_ctx, query) => await store.runs(query),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.run,
        title: "Read one run",
        caps: READ_CAPS,
        input: RunQuerySchema,
        result: RunResultSchema,
      }),
      async (_ctx, { id }) => await store.run(id),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.policy,
        title: "Read the evaluation policy",
        caps: READ_CAPS,
        input: NoQuerySchema,
        result: PolicyResultSchema,
      }),
      async () => await store.policy(),
    ),
  ];
}

/** The entity id shape a topic row carries, re-exported so a panel can validate a route. */
export { EntityIdSchema };
