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

  Reading requires `containers:read` of its caller and nothing else. None of these doors writes,
  and none of them touches a slice of the host: the store is the plugin's own tables.
*/

import { z } from "zod";
import {
  ACTIONS,
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
import { defineServerAction } from "@manifold/plugin-kit/server";

/** Reading is a read of the plugin's own rows; the caller needs the workspace it asked about. */
const READ_CAPS = ["containers:read"] as const;

/*
  THE TWO READS THAT WAKE THE LOOP CARRY ONE NATIVE CEILING, AND IT IS NOT A SECOND PERMISSION.

  `pulse` and `runs` are the doors a cycle follows (server.ts's `WAKES`), and the half of a cycle
  that matters when no settlement arrived — a hook that overran, a hub restarted mid-run — is
  INGESTION: read the jobs this store is still waiting on, take their sealed outputs, close the
  runs. All of that is `jobs:read`, and the dispatcher attenuates `ctx.jobs` to what the door
  declared, so without it here the safety net could not read a single job and every cycle behind
  a read was a list of refusals.

  It is a DELEGATE, not a cap: a delegate is the native ceiling this door's job authority may
  reach, while a cap is what the caller must hold. The caller is unchanged — it still needs only
  `containers:read` — and nothing is widened, because the ceiling is intersected with the
  CALLER's own capabilities before any job verb runs, and the engine still requires the
  operator's version-bound consent for `jobs:read` at each operation node before it answers.

  STARTING work is deliberately not reachable from here. `machines:run` is absent from this
  ceiling, so a cycle behind a five-second poll cannot dispatch: only `launch`, which declares it
  at the node its arguments name, and `onJobSettled`, which carries the credential the job ran
  under, can ask a machine to run anything.
*/
const WAKING_DELEGATES = ["jobs:read"] as const;

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
        input: RecordQuerySchema,
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
        delegates: WAKING_DELEGATES,
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
