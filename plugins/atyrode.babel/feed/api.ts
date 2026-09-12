import { useSyncExternalStore } from "react";
import type { HostServices } from "@manifold/plugin";
import type { ManifoldRef } from "@manifold/protocol";
import { z } from "zod";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  FeedResultSchema,
  FeedQuerySchema,
  PulseResultSchema,
  RecordPeelSchema,
  RuleResultSchema,
  ThreadResultSchema,
  TopicProposalSchema,
  TopicRowSchema,
  TopicsResultSchema,
  door,
  type ActionName,
} from "../contract.ts";

/*
  THE PANELS' SEAM ONTO THE HUB, and the one place the three of them agree on anything.

  Three things live here and nothing else does:

    - `ask`, the only way a panel talks to the baseline. Every door is one row in `RESULTS`
      with the schema its answer is parsed against, so a call site names a door and gets the
      contract's own type back; a denial is data on the wire and a thrown message here,
      because every call site in this plugin renders the sentence rather than the rule.
    - THE SELECTION. A panel is a tile leaf addressed by `{kind: "panel", panelId}` and
      `PanelProps` carries the host and nothing else — there is no argument to open a panel
      WITH (protocol/src/layout.ts, packages/plugin/src/host.ts). But a plugin's web half is
      one module in the page, so what Home is looking at is a fact the record and topic
      panels can read directly: `look()` writes it, `useSelection()` reads it, and a record
      panel tiled beside the list IS §8.7's peek pane, walked by the same `j`/`k`.
    - `since`, because five surfaces print the same ages out of the same ISO strings.
*/

// ---------------------------------------------------------------------------- doors

/**
 * The topic door's answer. It is spelled here rather than in `contract.ts` because the
 * baseline owns that file and the door landed in this wave; the rows inside it are the
 * contract's own, so nothing here reinterprets a shape. Move it up when the contract grows
 * `TopicQuerySchema`/`TopicResultSchema`.
 */
export const TopicResultSchema = z.strictObject({
  topic: TopicRowSchema.nullable(),
  proposed: z.array(TopicProposalSchema),
  feed: FeedResultSchema,
});

/**
 * What an act answers when the contract spells only its input. The panels read nothing off
 * these — an act is confirmed by the read that follows it — so the honest parse is none.
 */
const AcceptedSchema = z.unknown();

const RESULTS = {
  feed: FeedResultSchema,
  record: RecordPeelSchema,
  thread: ThreadResultSchema,
  topics: TopicsResultSchema,
  topic: TopicResultSchema,
  pulse: PulseResultSchema,
  rule: RuleResultSchema,
  comment: AcceptedSchema,
  answer: AcceptedSchema,
  interest: AcceptedSchema,
  file: AcceptedSchema,
  unfile: AcceptedSchema,
  tell: AcceptedSchema,
} as const satisfies Partial<Record<ActionName, z.ZodType>>;

export type DoorName = keyof typeof RESULTS;
export type DoorResult<K extends DoorName> = z.infer<(typeof RESULTS)[K]>;

export type FeedQuery = z.infer<typeof FeedQuerySchema>;
export type FeedResult = z.infer<typeof FeedResultSchema>;
export type FeedPost = FeedResult["posts"][number];
export type FeedVote = FeedPost["votes"][number];
export type RecordPeel = z.infer<typeof RecordPeelSchema>;
export type ThreadResult = z.infer<typeof ThreadResultSchema>;
export type ThreadComment = ThreadResult["comments"][number];
export type ThreadAct = ThreadResult["acts"][number];
export type TopicsResult = z.infer<typeof TopicsResultSchema>;
export type TopicRow = z.infer<typeof TopicRowSchema>;
export type TopicProposal = z.infer<typeof TopicProposalSchema>;
export type TopicResult = z.infer<typeof TopicResultSchema>;
export type PulseResult = z.infer<typeof PulseResultSchema>;

/**
 * Calls one door and answers what the contract says it answers.
 *
 * A denial is a refusal the hub decided (`{ok: false, denial}`), and it is thrown as its own
 * message: every caller here is a control the operator just pressed, so the sentence goes
 * where the control was. An answer that does not parse is the same kind of fault as a denial
 * and is thrown with the field that failed, because a panel drawing a half-parsed record is
 * how a wrong figure gets read as a right one.
 */
export async function ask<K extends DoorName>(
  host: HostServices,
  name: K,
  args: unknown,
): Promise<DoorResult<K>> {
  const outcome = await host.client.action(door(name as ActionName), args);
  if (!outcome.ok) throw new Error(outcome.denial.message);
  const parsed = RESULTS[name].safeParse(outcome.result);
  if (!parsed.success) {
    const first = parsed.error.issues[0];
    const where = first === undefined ? "(root)" : first.path.map(String).join(".") || "(root)";
    throw new Error(`${ACTIONS[name]} answered something this build cannot read: ${where}`);
  }
  return parsed.data as DoorResult<K>;
}

/** What the baseline's events are news about: one node, so one subscription feeds every panel. */
export const BABEL_NODE: ManifoldRef = { kind: "plugin", pluginId: BABEL_PLUGIN_ID };

/** The message a failed call leaves on the surface that made it. */
export function refusal(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

// ---------------------------------------------------------------------------- the selection

export interface Selection {
  /** The record the peek pane is showing, or "" when nothing has been opened. */
  readonly recordId: string;
  /** The topic the topic panel is showing: an entity id or a name, as the door takes it. */
  readonly topic: string;
}

const EMPTY: Selection = { recordId: "", topic: "" };
let selection: Selection = EMPTY;
const watchers = new Set<() => void>();

/** What the panels are looking at right now, for a caller outside React (a key handler). */
export function looking(): Selection {
  return selection;
}

/** Points the panels at something. Unchanged fields stay; an unchanged selection notifies nobody. */
export function look(at: Partial<Selection>): void {
  const next: Selection = { recordId: at.recordId ?? selection.recordId, topic: at.topic ?? selection.topic };
  if (next.recordId === selection.recordId && next.topic === selection.topic) return;
  selection = next;
  for (const watcher of watchers) watcher();
}

/** Test seam: the selection outlives a test the way a module outlives a render. */
export function forgetSelection(): void {
  selection = EMPTY;
  for (const watcher of watchers) watcher();
}

function watchSelection(notify: () => void): () => void {
  watchers.add(notify);
  return () => {
    watchers.delete(notify);
  };
}

export function useSelection(): Selection {
  return useSyncExternalStore(watchSelection, looking, looking);
}

// ---------------------------------------------------------------------------- time

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * An age in the words a row has room for. An empty or unreadable stamp prints nothing at
 * all: a time this build cannot parse is a reason to say nothing, never to say "just now".
 */
export function since(value: string, now: number = Date.now()): string {
  if (value === "") return "";
  const at = Date.parse(value);
  if (Number.isNaN(at)) return "";
  const ago = Math.max(0, now - at);
  if (ago < MINUTE) return "just now";
  if (ago < HOUR) return `${Math.floor(ago / MINUTE)}m ago`;
  if (ago < DAY) return `${Math.floor(ago / HOUR)}h ago`;
  if (ago < 30 * DAY) return `${Math.floor(ago / DAY)}d ago`;
  return new Date(at).toISOString().slice(0, 10);
}
