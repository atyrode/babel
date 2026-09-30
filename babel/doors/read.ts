/*
  THE READING DOORS: the nine questions a reader asks of Babel.

  Every one of them is a `defineServerAction` over the contract's own schemas, and the kit parses
  both ends — a request outside the input schema is refused by name, and a result outside the
  result schema refuses the dispatch rather than reaching a panel. That is what makes the
  vocabulary checkable: a misspelled sort answered with the default order would show a reader a
  different feed from the one he asked for and say nothing, and a kind nothing matches answered
  with an empty list reads as a deployment that has produced none of them.

  Every shape they take and answer is the contract's own, imported from `contract.ts` and spelled
  nowhere else: the queries, the rows and the results are one vocabulary, so a panel's rendering
  code and a door's declaration cannot drift apart by a field.

  Reading requires `containers:read` of its caller and nothing else. The reading handlers return
  this plugin's own rows; the post-dispatch pulse/runs observation may fold progress into those
  rows, but never posts new work under a reader's authority.
*/

import { z } from "zod";
import {
  ACTIONS,
  CONDUCTOR_CYCLE_KEY,
  CycleReportSchema,
  EntityIdSchema,
  FeedQuerySchema,
  FeedResultSchema,
  PolicyResultSchema,
  PulseResultSchema,
  RecordPeelSchema,
  RecordQuerySchema,
  RunQuerySchema,
  RunResultSchema,
  RunsQuerySchema,
  RunsResultSchema,
  ThreadQuerySchema,
  ThreadResultSchema,
  TopicQuerySchema,
  TopicResultSchema,
  TopicsResultSchema,
} from "../contract.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";
import { defineServerAction, type GuestStorage } from "@manifold/plugin-kit/server";

/** Reading is a read of the plugin's own rows; the caller needs the workspace it asked about. */
const READ_CAPS = ["containers:read"] as const;

/*
  `pulse` and `runs` wake a read-only observation of unfinished work. The observer folds live
  progress and settled receipts, but does not register schedules, post preparations or Code
  sessions, or refill a drain. That keeps these doors callable by read-only parts such as Jev.
  Native job reads and Code's readSession still need the declared bridge; none of the posting
  authority carried by a write wake is lent to a reader.
*/
const WAKING_CAPS = READ_CAPS;
const WAKING_DELEGATES = ["jobs:read", "services:read"] as const;

/** `pulse` and `topics` are asked without arguments; a strict empty object says so on the wire. */
const NoQuerySchema = z.strictObject({});

/**
 * THE LAST CYCLE'S OWN VERDICT, read from where the conductor left it (#328).
 *
 * It is a key rather than a table because the loop is not the store: a cycle is a fresh
 * conductor over whatever wake caused it, and its verdict is one small document that the next
 * cycle replaces. The pulse is the door that already answers "what has Babel been doing", so
 * this is the least invented home for "and why did the last cycle do nothing".
 *
 * A KEY THAT CANNOT BE READ, OR HOLDS A SHAPE THIS BUILD DOES NOT KNOW, IS NO CYCLE. The
 * alternative is a door that refuses the whole pulse because a value written by an older
 * conductor no longer parses — today's counts would disappear from Home to report that an
 * explanation could not be read, which is the wrong half to lose.
 */
async function lastCycle(storage: GuestStorage): Promise<z.infer<typeof CycleReportSchema> | null> {
  try {
    const held = await storage.get(CONDUCTOR_CYCLE_KEY);
    if (held === null) return null;
    const parsed = CycleReportSchema.safeParse(JSON.parse(held) as unknown);
    return parsed.success ? parsed.data : null;
  } catch {
    return null;
  }
}

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
        input: RecordQuerySchema,
        result: RecordPeelSchema,
      }),
      async (_ctx, { id }) => {
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
        caps: WAKING_CAPS,
        delegates: WAKING_DELEGATES,
        input: NoQuerySchema,
        result: PulseResultSchema,
      }),
      // Two halves of one answer, from the two places a plugin keeps state: the store measures
      // the day, the keys hold what the loop last decided.
      async (ctx) => ({ ...(await store.pulse()), cycle: await lastCycle(ctx.storage) }),
    ),

    defineDoor(
      defineServerAction({
        name: ACTIONS.runs,
        title: "Read the runs",
        caps: WAKING_CAPS,
        delegates: WAKING_DELEGATES,
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
