import { defineServerPlugin, type ServerPluginDef } from "@manifold/plugin-kit/server";
import { PluginManifestSchema } from "@manifold/protocol";
import manifestJson from "./manifest.json";
import { SWEEP_ACTIONS, SWEEP_HANDLERS } from "./sweep/doors.ts";

/*
  THE SERVER HALF OF atyrode.babel.jev, AND THE TWO DOORS THE CORPUS SWEEP NEEDS.

  Jev is a part: typed judgement over the records the baseline holds, enabled and removed on its
  own. The whole design constraint is that Babel never depends on it — absent, disabled or out of
  credit, every baseline door, panel and conductor path answers exactly as it does today. The
  sweep changed only the part's OWN roster: nothing inside it can wake itself — it has no cycle,
  no job and no lifecycle hook — so a driver asks one door what a bounded pass would cost, then
  asks the other to run one. Neither writes; every suggestion comes back to the caller for
  delivery, which leaves the baseline unaware of the part and keeps the dependency edge one-way.

  IT HOLDS TWO AUTHORITIES, AND EACH ARRIVED WITH THE CHILD THAT SPENDS IT. `services:invoke` is
  the judgement call. The key Jev is reached with is the operator's; the part names the service
  and the host injects the credential by reference, so the part never receives it
  (`server/credential.ts`, the whole argument). Declaring the capability is what makes the
  fallback mechanical instead of a promise: authority over a service nobody installed reaches
  nothing, so no binding is no call, and the code below is the only code in the bundle that
  could make one. It is re-exported here rather than left loose because a manifest that asks for
  an authority no shipped module can exercise is the same mistake in the other direction.

  IT REACHES THE BASELINE ONLY THROUGH ITS DOORS (`ctx.actions.call`), never as a library, and
  the host is what enforces that rather than a convention: a call to a plugin this manifest does
  not declare is refused `undeclared_dependency`, and a call to one it declares that is absent or
  disabled is refused `dependency_unavailable`. The edge is one-way — this manifest names
  `atyrode.babel` required, the baseline's names nothing of this part — which is why removing the
  directory removes a plugin and not a dependency.

  `containers:read` IS WHAT THAT EDGE IS WORTH, and it is the second authority (#404). A
  cross-plugin call is bounded by the CALLER's own ceiling — the host refuses `caller_ceiling`
  for any capability the callee's door declares that the calling manifest does not hold, and
  there is no implication between the names — so a part declaring only the service authority
  could open none of the nine reading doors of the baseline it declares required. Declaring the
  edge buys the composition and the install order; declaring the read is what buys the call.
  `test/part-ceiling.test.ts` dispatches one against a host that grades the ceiling.

  `containers:write` IS STILL NOT DECLARED, and after #360 that is a narrower statement than it
  was. The decision is taken: Jev may write a SUGGESTION — one `next_actions` row through
  `babel.suggest`, attributed to the part and answered by the operator through `decide` — and it
  may not annotate a record, write an edge, change a standing, rule or delete. What has not been
  granted is the capability that would carry that one call: a cross-plugin call is graded against
  the CALLER's manifest ceiling for every engine capability the callee's door declares, and
  `containers:write` is the authority EVERY ruling door carries. Declaring it here to reach one
  door would open `rule`, `decide` and the rest of the acts to the part at the same stroke, with
  only the operator's own principal behind them — which is the opposite of what #360 decided.
  The door itself exists — #412 landed `babel.suggest`, whose allow-list is keyed on PRINCIPAL
  rather than on a capability — and the missing piece is a host mechanism that admits that one
  write without the rest, open upstream as atyrode/manifold#770. So NOTHING IN THIS BUNDLE CALLS
  IT: `screen/pass.ts` hands each suggestion to a caller-supplied function and `sweep/sweep.ts`
  hands the resulting rows back to whoever knocked. `test/part-ceiling.test.ts` holds the refusal
  the part would meet if it tried.

  The id and the sweep's two door names are spelled once in the family's vocabulary
  (`JEV_PLUGIN_ID` and `JEV_ACTIONS`, `babel/contract.ts`) and `test/contract.test.ts` pins the
  manifest and service id to that vocabulary. A part may import that one baseline module
  (`docs/building.md`); every other reach into Babel is a door.
*/

export {
  askJev,
  JEV_CALL_CAP_BYTES,
  JEV_SERVICE,
  withinCallCap,
  type JevAnswer,
  type JevServices,
} from "./server/credential.ts";
export {
  JEV_ANSWERS_HELD,
  JevAnswers,
  judge,
  requestFor,
  requestKey,
  type JevRequest,
} from "./server/judge.ts";
export {
  screenPass,
  screenRecord,
  sweepSize,
  type PassReport,
  type PassSuggestion,
  type ScreenFailure,
  type ScreenResult,
  type SweepSize,
} from "./screen/pass.ts";
export {
  answersOf,
  type ScreenedRecord,
  type Screener,
  type ScreenSubject,
  type ScreenSuggestion,
} from "./screen/screener.ts";
export { SCREENERS } from "./screen/screeners.ts";
export {
  positionFor,
  positionOf,
  STANDINGS,
  type RecordPosition,
  type Standing,
} from "./tally/position.ts";
export {
  basisFor,
  sweep,
  sweepPlan,
  SWEPT_KINDS,
  type SweepAsk,
  type SweepDeps,
} from "./sweep/sweep.ts";

export const plugin: ServerPluginDef = {
  manifest: PluginManifestSchema.parse(manifestJson),
  actions: SWEEP_ACTIONS,
  handlers: SWEEP_HANDLERS,
};

export default plugin;
defineServerPlugin(plugin);
