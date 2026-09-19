import { defineServerAction, type ServerHandler } from "@manifold/plugin-kit/server";
import { JEV_ACTIONS, PairsInputSchema, PairsReportSchema } from "../../contract.ts";
import { pairPass } from "./pass.ts";

/*
  THE DOOR A PAIR PASS IS DRIVEN THROUGH (#357, #358).

  One action and not two, which is the difference from the sweep's pair of doors: there is no
  sizing call here because the caller already holds the size. It names the anchors, so it knows
  how many records will be read, and it names the judgement budget, so it knows the ceiling on
  what will be spent — a `pairsPlan` could only tell it back what it just said, plus a count of
  candidate pairs that costs the very searches a plan exists to avoid.

  `containers:read` AND NOTHING ELSE, like the two beside it. The pass reads through
  `babel.record` and retrieves through `babel.search`; it invokes the judgement service the
  operator installed, which is the manifest's other authority; and it writes nothing at all. The
  suggestions it answers with are for the caller to deliver under its own principal, each one
  carrying the counterpart in `subject` so a record that is half of two pairs does not have its
  first finding superseded by its second.
*/

export const pairsAction = defineServerAction({
  name: JEV_ACTIONS.pairs,
  title: "Judge the candidate pairs of named anchors for contradiction and supersession",
  caps: ["containers:read"],
  input: PairsInputSchema,
  result: PairsReportSchema,
});

export const PAIR_ACTIONS = [pairsAction] as const;

/**
 * The handler under the name the declaration publishes, kept beside it so a registration cannot
 * carry one without the other.
 *
 * The cuts arrive as an object with two optional leaves and reach the detectors as a map keyed
 * by question: an absent leaf must be an ABSENT KEY and never a zero, because zero is a line
 * every answer clears and `statedCut` is what tells "no line stated" from "the line was low".
 */
export const PAIR_HANDLERS: Readonly<Record<string, ServerHandler>> = {
  [JEV_ACTIONS.pairs]: async (ctx, args: unknown) => {
    const parsed = PairsInputSchema.parse(args);
    const cuts: Record<string, number> = {};
    for (const [question, cut] of Object.entries(parsed.cuts)) {
      if (cut !== undefined) cuts[question] = cut;
    }
    return await pairPass(
      { actions: ctx.actions, services: ctx.services },
      { anchors: parsed.anchors, cuts, judgements: parsed.judgements },
    );
  },
};
