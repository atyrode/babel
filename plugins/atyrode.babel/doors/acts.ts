import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  AnswerInputSchema,
  BABEL_PLUGIN_ID,
  CommentInputSchema,
  EVENTS,
  FileInputSchema,
  ImportChunkSchema,
  InterestInputSchema,
  RuleInputSchema,
  RuleResultSchema,
  TellInputSchema,
  UnfileInputSchema,
} from "../contract.ts";

import {
  ActRefused,
  AnsweredSchema,
  BudgetClearedSchema,
  BudgetSetSchema,
  ClearBudgetInputSchema,
  CommentedSchema,
  FiledSchema,
  ImportedSchema,
  InterestedSchema,
  PolicySetSchema,
  SetBudgetInputSchema,
  SetPolicyInputSchema,
  ToldSchema,
  answer,
  clearBudget,
  comment,
  file,
  importLedger,
  interest,
  rule,
  setBudget,
  setPolicy,
  tell,
  unfile,
  type ActsStore,
} from "../store/acts.ts";
import { defineDoor, type Door } from "./door.ts";

/**
 * Every act is news on this plugin's own node. The id is the contract's, not the context's:
 * a hardened guest context carries `pluginId` and the in-realm one does not, and an emission
 * on `manifold://plugin/undefined` is refused as another plugin's node.
 */
const OWN_NODE = { kind: "plugin", pluginId: BABEL_PLUGIN_ID } as const;

/*
  THE DOORS THE OPERATOR ACTS THROUGH. Nine of them, and they are thin on purpose: each parses
  its arguments against `contract.ts`'s schema, takes the actor from the dispatch's principal,
  calls the one write in `store/acts.ts` that performs the act, and emits the event the reading
  surfaces listen for.

  Two things are the door's own business rather than the store's.

  WHO IS ACTING is never an argument. Every act is attributed to `ctx.principal.id` — the
  identity the host authenticated — so a caller cannot rule as somebody else by saying so, and
  §4.7's "attributed" is a property of the transport rather than a field a client fills in.

  A REFUSAL IS NOT A FAILURE. `ActRefused` is the store saying the ask was wrong — an unknown
  record, a ruling that says nothing new, a lease under the floor — and it comes back as
  `{ refused }`, which denies the dispatch by rule and leaves the trace clean. Anything else
  raises: a broken statement or a lost handle is this plugin's bug and is logged as one.

  The caps are `containers:write` because these are writes into the workspace the plugin serves,
  and `importLedger` carries none and asks `ctx.auth.isRoot` instead: the crossing is the
  owner's one-off act, and no capability in Manifold's closed vocabulary means "the owner".
*/

const ACT_CAPS = ["containers:write"] as const;

/**
 * Runs one act and turns the store's refusal into the dispatch's. It is the only place the two
 * kinds of error are told apart, so no door can accidentally report a bug as a refusal or a
 * refusal as a 500.
 */
async function acted<Out>(run: () => Promise<Out>): Promise<Out | { refused: string }> {
  try {
    return await run();
  } catch (error) {
    if (error instanceof ActRefused) return { refused: error.message };
    throw error;
  }
}

const ruleAction = defineServerAction({
  name: ACTIONS.rule,
  title: "Rule on a record, applying the plan it carries",
  caps: ACT_CAPS,
  input: RuleInputSchema,
  result: RuleResultSchema,
});

const commentAction = defineServerAction({
  name: ACTIONS.comment,
  title: "Say something about a record, or ask Babel a question about it",
  caps: ACT_CAPS,
  input: CommentInputSchema,
  result: CommentedSchema,
});

const answerAction = defineServerAction({
  name: ACTIONS.answer,
  title: "Answer a question Babel raised",
  caps: ACT_CAPS,
  input: AnswerInputSchema,
  result: AnsweredSchema,
});

const interestAction = defineServerAction({
  name: ACTIONS.interest,
  title: "State the stance toward a topic",
  caps: ACT_CAPS,
  input: InterestInputSchema,
  result: InterestedSchema,
});

const fileAction = defineServerAction({
  name: ACTIONS.file,
  title: "File a record under a topic",
  caps: ACT_CAPS,
  input: FileInputSchema,
  result: FiledSchema,
});

const unfileAction = defineServerAction({
  name: ACTIONS.unfile,
  title: "Withdraw a filing, with the reason",
  caps: ACT_CAPS,
  input: UnfileInputSchema,
  result: FiledSchema,
});

const tellAction = defineServerAction({
  name: ACTIONS.tell,
  title: "Tell Babel something",
  caps: ACT_CAPS,
  input: TellInputSchema,
  result: ToldSchema,
});

const setPolicyAction = defineServerAction({
  name: ACTIONS.setPolicy,
  title: "Install an evaluation policy",
  caps: ACT_CAPS,
  input: SetPolicyInputSchema,
  result: PolicySetSchema,
});

/*
  THE TWO DOORS OF #260, and they carry exactly the authority `setPolicy` carries. Setting a
  batch of sixty-four for two hours and setting it for ever are the same act with different
  words: both decide how much of an operator's money a loop may commit without asking again. A
  cheaper door for the temporary one would make "temporary" the way round the check.

  What they are NOT is a scheduler command. `budget.set` starts nothing and `budget.clear` stops
  nothing: the next admission reads the newest unexpired row, and that is the whole mechanism.
*/
const setBudgetAction = defineServerAction({
  name: ACTIONS.setBudget,
  title: "Set a budget overlay with a TTL",
  caps: ACT_CAPS,
  input: SetBudgetInputSchema,
  result: BudgetSetSchema,
});

const clearBudgetAction = defineServerAction({
  name: ACTIONS.clearBudget,
  title: "Clear a budget overlay before it expires",
  caps: ACT_CAPS,
  input: ClearBudgetInputSchema,
  result: BudgetClearedSchema,
});

const importLedgerAction = defineServerAction({
  name: ACTIONS.importLedger,
  title: "Import one chunk of the store the Go tree held (owner only)",
  caps: [],
  input: ImportChunkSchema,
  result: ImportedSchema,
});

/**
 * `concurrentJobs` is the manifest's ceiling, handed down from the wiring: the two acts that
 * write a bound — installing a policy and overlaying one — refuse a per-machine bound above
 * what the machine half will run, so the operator learns it at the door instead of paying for
 * postings the hub refuses (#281). It is `null` when no operation this bundle declares states
 * one (#279), and then there is no bound to refuse against: the hub refuses nothing at
 * `execute` for a ceiling nobody declared.
 */
export function actDoors(store: ActsStore, concurrentJobs: number | null): readonly Door[] {
  return [
    defineDoor(ruleAction, async (ctx, args) =>
      await acted(async () => {
        const ruled = await rule(
          store,
          { id: args.id, ruling: args.ruling, note: args.note, duplicateOf: args.duplicateOf },
          ctx.principal.id,
        );
        ctx.emit(OWN_NODE, EVENTS.ruled, {
          id: ruled.id,
          ruling: args.ruling,
          standing: ruled.standing,
          seq: ruled.seq,
        });
        // A plan that moved the ledger is its own event: a reader of the feed learns a record was
        // ruled on, and a reader of the topics page learns an entity now exists.
        if (ruled.plan?.applied === true) {
          ctx.emit(OWN_NODE, EVENTS.planApplied, {
            id: ruled.id,
            kind: ruled.plan.kind,
            operation: ruled.plan.operation,
            entityId: ruled.plan.entityId ?? "",
          });
        }
        return ruled;
      }),
    ),

    defineDoor(commentAction, async (ctx, args) =>
      await acted(async () => {
        const commented = await comment(
          store,
          { id: args.id, text: args.text, kind: args.kind, relatedId: args.relatedId },
          ctx.principal.id,
        );
        ctx.emit(OWN_NODE, EVENTS.recordWritten, {
          id: commented.id,
          recordId: commented.recordId,
          question: commented.question,
        });
        return commented;
      }),
    ),

    defineDoor(answerAction, async (ctx, args) =>
      await acted(async () => {
        const answered = await answer(store, args, ctx.principal.id);
        ctx.emit(OWN_NODE, EVENTS.recordWritten, {
          id: answered.id,
          questionId: answered.questionId,
          state: answered.state,
        });
        return answered;
      }),
    ),

    defineDoor(interestAction, async (ctx, args) =>
      await acted(async () => {
        const stated = await interest(
          store,
          { entityId: args.entityId, state: args.state, reason: args.reason },
          ctx.principal.id,
        );
        ctx.emit(OWN_NODE, EVENTS.recordWritten, {
          entityId: stated.entityId,
          state: stated.state,
          facts: stated.facts.length,
        });
        return stated;
      }),
    ),

    defineDoor(fileAction, async (ctx, args) =>
      await acted(async () => {
        const filed = await file(store, args, ctx.principal.id);
        ctx.emit(OWN_NODE, EVENTS.recordWritten, {
          id: filed.id,
          recordId: filed.recordId,
          entityId: filed.entityId,
        });
        return filed;
      }),
    ),

    defineDoor(unfileAction, async (ctx, args) =>
      await acted(async () => {
        const withdrawn = await unfile(store, args, ctx.principal.id);
        ctx.emit(OWN_NODE, EVENTS.recordWritten, {
          id: withdrawn.id,
          recordId: withdrawn.recordId,
          entityId: withdrawn.entityId,
          withdrawn: true,
        });
        return withdrawn;
      }),
    ),

    defineDoor(tellAction, async (ctx, args) =>
      await acted(async () => {
        const told = await tell(
          store,
          { text: args.text, target: args.target, replyTo: args.replyTo },
          ctx.principal.id,
        );
        ctx.emit(OWN_NODE, EVENTS.recordWritten, { id: told.id, rootId: told.rootId, seq: told.seq });
        return told;
      }),
    ),

    defineDoor(setPolicyAction, async (ctx, args) =>
      await acted(async () => {
        const installed = await setPolicy(store, args.policy, args.reason, ctx.principal.id, concurrentJobs);
        ctx.emit(OWN_NODE, EVENTS.recordWritten, { version: installed.version, seq: installed.seq });
        return installed;
      }),
    ),

    defineDoor(setBudgetAction, async (ctx, args) =>
      await acted(async () => {
        const overlaid = await setBudget(store, args, ctx.principal.id, concurrentJobs);
        ctx.emit(OWN_NODE, EVENTS.recordWritten, {
          budgetId: overlaid.id,
          expiresAt: overlaid.expiresAt,
          changes: overlaid.changes.length,
        });
        return overlaid;
      }),
    ),

    defineDoor(clearBudgetAction, async (ctx, args) =>
      await acted(async () => {
        const cleared = await clearBudget(store, args, ctx.principal.id);
        ctx.emit(OWN_NODE, EVENTS.recordWritten, { budgetId: cleared.id, cleared: true });
        return cleared;
      }),
    ),

    defineDoor(importLedgerAction, async (ctx, args) =>
      await acted(async () => {
        // The crossing rewrites provenance: it inserts rows carrying identifiers the Go tree
        // minted, under their original timestamps, into tables whose triggers refuse an edit.
        // Only the owner may do that, and a capability grant is not enough — there is no cap in
        // the vocabulary that means "the owner", so the principal itself is the check.
        if (!ctx.auth.isRoot) {
          return { refused: "the crossing is the owner's act; this principal is not the owner" };
        }
        return await importLedger(store, args);
      }),
    ),
  ];
}
