import { OVERREACH } from "../voters/overreach.ts";
import { SETTLEABLE } from "../voters/settleable.ts";
import { SPECIFICITY } from "../voters/specificity.ts";
import type { Screener } from "./screener.ts";

/*
  THE ROSTER, AND IT IS APPEND-ONLY BY CONVENTION RATHER THAN BY TYPE.

  Every voter is one `Screener` constant exported from `voters/<name>.ts` and one line here. That
  is the whole of adding one, and the whole of removing one, which matters more than it looks:
  the alternative — each voter registering itself on import — makes the roster depend on which
  modules happened to be loaded, and a pass whose membership is a side effect of import order is
  a pass nobody can state the contents of before running it.

  ORDER IS NOT PRECEDENCE. Nothing here ranks, filters or resolves a disagreement between two
  voters, because a suggestion is not a verdict: two voters that both have something to say about
  one record produce two suggestions beside it, of different kinds, and the operator answers each
  through `decide`. There is nowhere in this file for a weight, and that is the same argument
  `tally()` makes one layer down.
*/

/**
 * The voters this bundle ships, in the order they are consulted, which is not an order of
 * precedence — see the head.
 */
export const SCREENERS: readonly Screener[] = [OVERREACH, SETTLEABLE, SPECIFICITY];
