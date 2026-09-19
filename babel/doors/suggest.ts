/*
  THE ONE DOOR A DEPENDENT PLUGIN MAY WRITE THROUGH (#410, decided on #360).

  Babel has two writer classes and a dependent plugin is neither: the operator is AUTHENTICATED
  and acts under his own principal, and a run is MEDIATED — its output never touches a door and
  the conductor validates it against a schema the baseline owns. Admitting a third class on the
  frontier would mean re-reasoning about immutability, "a ruling ends the argument" and "a run may
  propose and may never rule" at every table, for ever. So there is no third class: a plugin the
  operator has allowed may write a SUGGESTION — one `next_actions` row — which renders where the
  record is read and which he accepts or declines through the `decide` door that already exists.

  HOW THE CALLER IS IDENTIFIED, because it is the first thing this door has to get right and the
  answer is not the obvious one. The host supplies NO caller plugin id: a dispatch carries the
  trace, the principal, its caps, its root flag, its container scope and the clock
  (`IsolateDispatchCtxSchema`, manifold `packages/protocol/src/isolate.ts`), and `ctx.pluginId`
  (`packages/plugin-kit/src/server.ts`) is *this* plugin's own manifest id, not the caller's. The
  caller IS known host-side — `PluginHost.actionCalls(caller, …)` dispatches with
  `origin: { plugin: caller, … }` (`packages/server/src/plugin-host.ts`) — but that reaches the
  trace ledger and the cycle/depth bound only; it is never placed on the handler's context.

  What the host does authenticate is `ctx.principal`. So the suggester is the plugin the
  operator's allow-list binds to that principal, resolved in `store/acts.ts` and never read out of
  the input document — which has nowhere to name an author anyway, both schemas being strict. A
  principal nobody listed is refused by name and told exactly what would allow it.

  The write cap is `containers:write` like every other act: this is a write into the workspace the
  plugin serves. The read is `containers:read`. Neither door touches a slice of the host.
*/

import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  EVENTS,
  SuggestInputSchema,
  SuggestedSchema,
  SuggestionsQuerySchema,
  SuggestionsResultSchema,
} from "../contract.ts";
import { suggest, suggesterFor, suggestionsOf, type ActsStore } from "../store/acts.ts";
import { acted } from "./acts.ts";
import { defineDoor, type Door } from "./door.ts";

/** Every act is news on this plugin's own node; the id is the contract's, as in `doors/acts.ts`. */
const OWN_NODE = { kind: "plugin", pluginId: BABEL_PLUGIN_ID } as const;

const suggestAction = defineServerAction({
  name: ACTIONS.suggest,
  title: "Suggest a next action on one record revision (allowed plugins only)",
  caps: ["containers:write"],
  input: SuggestInputSchema,
  result: SuggestedSchema,
});

/**
 * The reading half, and it is not decoration: #360 requires the imported corpus to be swept, and
 * a sweep that cannot state how many suggestions it would add before it adds them recreates the
 * one undifferentiated list the desk/queue/shelf split was built to end.
 *
 * It also NAMES the records of the gap when asked to (#356), because no other reading door can:
 * `suggest` requires the revision it judged and `records.seq` is on no peel, so a sweep that
 * could not read it here could not name one either. Asking for none — the default — is the
 * sizing call, which costs one query and spends nothing.
 */
const suggestionsAction = defineServerAction({
  name: ACTIONS.suggestions,
  title: "How many suggestions this caller has outstanding, and what a sweep would add",
  caps: ["containers:read"],
  input: SuggestionsQuerySchema,
  result: SuggestionsResultSchema,
});

export function suggestDoors(store: ActsStore): readonly Door[] {
  return [
    defineDoor(
      suggestAction,
      async (ctx, args) =>
        await acted(async () => {
          const suggested = await suggest(
            store,
            {
              recordId: args.recordId,
              revision: args.revision,
              kind: args.kind,
              subject: args.subject,
              summary: args.summary,
              rationale: args.rationale,
              basis: args.basis,
            },
            ctx.principal.id,
          );
          // The generic "something was written about this record": a suggestion is not a
          // verdict, so it is not `ruled`, and the panels answer this one with a re-read.
          ctx.emit(OWN_NODE, EVENTS.recordWritten, {
            id: suggested.id,
            recordId: suggested.recordId,
            suggester: suggested.suggester,
          });
          return suggested;
        }),
    ),

    defineDoor(suggestionsAction, async (ctx, args) =>
      acted(async () => {
        const suggester = await suggesterFor(store, ctx.principal.id);
        return await suggestionsOf(store, suggester, args);
      }),
    ),
  ];
}
