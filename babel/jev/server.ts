import { defineServerPlugin, type ServerPluginDef } from "@manifold/plugin-kit/server";
import { PluginManifestSchema } from "@manifold/protocol";
import manifestJson from "./manifest.json";

/*
  THE SERVER HALF OF atyrode.babel.jev, AND IT PUBLISHES NOTHING.

  Jev is a part: typed judgement over the records the baseline holds, enabled and removed on its
  own. The whole design constraint is that Babel never depends on it — absent, disabled or out of
  credit, every door, panel and conductor path answers exactly as it does today — and this file
  is the cheapest structural proof of that, standing before a single voter exists to complicate
  the question. So the roster it registers is empty: no door, no lifecycle hook, no store, no
  tool grant. Each of those arrives with the child that needs it, and one acquired here "because
  it will be needed" is how an optional part stops being one.

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

  `containers:write` IS NOT DECLARED, and that is a decision rather than an omission (#360):
  it is the authority every ruling door carries, and whether a dependent plugin may write at
  all is open. Babel's writers are the operator, authenticated through a door under his own
  principal, and a run, mediated, whose output the conductor ingests against a schema the
  baseline owns — a part is neither, and a manifest edit is not where that would be settled.

  The id is spelled once in the family's vocabulary (`JEV_PLUGIN_ID`, `babel/contract.ts`)
  and `test/contract.test.ts` pins this manifest and the service id under it to that name. Nothing
  here imports that file: the kit inlines every imported module into the bundle, and a part with
  this little code of its own has no reason to carry the baseline's whole contract for one string
  it does not use at runtime.
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

export const plugin: ServerPluginDef = {
  manifest: PluginManifestSchema.parse(manifestJson),
  actions: [],
  handlers: {},
};

export default plugin;
defineServerPlugin(plugin);
