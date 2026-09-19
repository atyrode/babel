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
  THE TWO READS THAT WAKE THE LOOP CARRY TWO NATIVE CEILINGS, AND NEITHER IS A SECOND PERMISSION.

  `pulse` and `runs` are the doors a cycle follows (server.ts's `WAKES`), and the half of a cycle
  that matters when no settlement arrived — a hook that overran, a hub restarted mid-run — is
  INGESTION: read the jobs this store is still waiting on, take their sealed outputs, close the
  runs. All of that is `jobs:read`, and the dispatcher attenuates `ctx.jobs` to what the door
  declared, so without it here the safety net could not read a single job and every cycle behind
  a read was a list of refusals.

  AND A CYCLE ASKS ONE THING OF A MACHINE BEFORE IT CAN KEEP A CADENCE AT ALL, which is what
  `machines:read` is here for. The loop's beat is registered on the machine the policy routes its
  work to, and it is registered only once that machine has said it can run it: `reconcileSchedule`
  asks `describeHost`, which is one `engine.jobs.describe` (`server/conductor.ts`). That read
  moved off `machines:run` and onto the narrower word (atyrode/manifold#736), and the same
  attenuation rule governs it: a door's native bridge is its own caps plus its delegates, so a
  `describe` this door never declared is refused `job_capability_absent:machines:read` however
  privileged the CALLER is — the cycle notes that the beat cannot be registered, nothing is
  registered, and Babel beats only for as long as somebody keeps pressing something.

  IT COULD NOT BE DELEGATED AT ALL UNTIL atyrode/manifold#740 (#739). `NATIVE_DELEGATE_CAPS`
  (protocol/src/plugin.ts) — the closed set `ActionDelegatesSchema` admits — held the job,
  location, operation, service and network capabilities and `machines:run`, and an action naming
  this one was refused at assembly as an unsupported delegated capability. The set's own rule is
  "only native job/resource/service APIs can discharge these at concrete targets", `describe` and
  `engine.machines.repository` are both native reads at `manifold://machine/<id>`, and the set
  already lent the strictly greater authority to make a machine RUN something — so the omission
  was the accident and the delegate is now where this belongs. The bundle's MANIFEST still
  declares it, because that is the ceiling an operator consents to at install and what the folder
  question a cycle asks through `ctx.machines` (#535) is served against; a delegate is the
  per-door ceiling underneath that grant, never a replacement for it.

  BOTH ARE DELEGATES, NOT CAPS: a delegate is the native ceiling this door's job authority may
  reach, while a cap is what the caller must hold. The caller is unchanged — it still needs only
  `containers:read`, and a reader asking for his own pulse is not asking a machine anything — and
  nothing is widened, because the ceiling is intersected with the CALLER's own capabilities and
  with the plugin's install grant before any job verb runs, and the engine still requires the
  operator's version-bound consent at each operation node before it answers.

  STARTING work is deliberately not reachable from here. `machines:run` is absent from this
  ceiling, so a cycle behind a five-second poll cannot dispatch: only `launch`, which discharges
  it at the effect for the one operation this manifest declares, and `onJobSettled`, which
  carries the credential the job ran under, can ask a machine to run anything.
*/
const WAKING_DELEGATES = ["jobs:read", "machines:read"] as const;

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
        caps: READ_CAPS,
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
