import { defineServerAction } from "@manifold/plugin-kit/server";
import { ACTIONS, SearchQuerySchema, SearchResultSchema } from "../contract.ts";
import { embedder } from "../server/embed.ts";
import { searchCorpus } from "../store/corpus.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE DOOR THAT RETRIEVES OVER THE CORPUS (#337).

  It is one door and it is beside the reads, because it writes nothing, publishes nothing and
  spends nothing an operator has to authorize. What it does that no other read does is reach an
  origin — once, for the query's own embedding — and only when the operator installed a service
  that says it may.

  `containers:read` IS THE CALLER'S SIDE and it is the same authority every other read carries: a
  reader asking what the corpus says about a subject is reading this hub's own rows, and a panel
  that may open the feed may open this.

  `services:invoke` IS A DELEGATE AND NOT A PERMISSION. The kit's own words for the field are
  "native job/service ceiling, not caller permission", which is what the shape needs: the two
  panels declare no capabilities of their own and call the baseline's doors as the viewer, so a
  `caps` entry here would make the search box unreachable from the surface it exists for. What
  the delegate buys is that the HANDLER may reach the service the operator installed; what it
  cannot do is let a caller reach anything, because the invocation is `server/embed.ts`'s and that
  module reaches nothing until a policy names an origin.

  THE EMBEDDER IS BUILT PER DISPATCH, from that dispatch's own service authority. It is not a
  process-level object: `GuestServices` is served to a dispatch, and a handle captured at load
  would outlive the credential it was minted for.
*/

/** Reading is a read of the plugin's own rows; the caller needs the workspace it asked about. */
const READ_CAPS = ["containers:read"] as const;

/**
 * The one native ceiling this door holds: the embedding service the operator installed. It is
 * declared on the door rather than on the plugin's own cycle because a query's embedding is
 * needed by a READ — the backfill's is taken from the same authority, through the drain tick a
 * dispatch woke (`server/drain.ts`).
 */
const EMBEDDING_DELEGATES = ["services:invoke"] as const;

export function searchDoors(store: BabelStore): readonly Door[] {
  return [
    defineDoor(
      defineServerAction({
        name: ACTIONS.search,
        title: "Search the corpus",
        caps: READ_CAPS,
        delegates: EMBEDDING_DELEGATES,
        input: SearchQuerySchema,
        result: SearchResultSchema,
      }),
      async (ctx, query) =>
        await searchCorpus(store, embedder(ctx.services), {
          query: query.query,
          limit: query.limit,
          kinds: query.kinds,
        }),
    ),
  ];
}
