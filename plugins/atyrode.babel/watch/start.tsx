import { Stack } from "@manifold/ui";

/*
  START SOMETHING — which Babel cannot, and says so (#279).

  What stood here was five preset cards, a machine picker, a knob apiece, a session picker (the
  account, the model, how hard it thinks) and a button that posted `launch`. All of it was
  Babel deciding what a model run is, and none of it is Babel's: `atyrode.babel` depends on
  `atyrode.code`, which depends on `atyrode.omp`. Code owns the profiles and Code launches omp.
  The operator picks a saved Code profile, or opens Code's generator and parametrizes the run
  there, and Babel posts it through Code's `runSession` door.

  So this section is one sentence and no button. A form whose button always refused would be an
  interface asking the operator to discover by pressing, and the two issues the sentence names
  are what a reader needs to know when the section comes back: atyrode/manifold#575 (a plugin's
  server calling a sibling plugin's door) and atyrode/code#170 (that door).

  The rest of Watch is untouched. Runs, Recipes, Ceilings and the drain panel all read and
  govern work that already exists, and the drain stays wired to the same launch path the button
  used — inert, and refusing the same sentence, until the engine returns.
*/

/** The refusal in the operator's own reading order: what a run is, then what is missing. */
export function Start() {
  return (
    <Stack gap="var(--babel-space-2)">
      <p className="plugin-atyrode_babel_watch__pending">
        Babel runs are Code sessions; Code&rsquo;s <code>runSession</code> door is not yet
        available.
      </p>
      <p className="plugin-atyrode_babel_watch__muted">
        A run&rsquo;s model, thinking level and account belong to a Code profile: you pick a
        saved one or parametrize the run in Code&rsquo;s generator, and Babel posts it through
        Code. Babel composes no session and launches no engine of its own. Two pieces are in
        flight:{" "}
        <a href="https://github.com/atyrode/manifold/issues/575">manifold#575</a>, which lets a
        plugin&rsquo;s server call a sibling plugin&rsquo;s door, and{" "}
        <a href="https://github.com/atyrode/code/issues/170">code#170</a>, which is that door.
        Until both land, every launch — this section&rsquo;s and the drain&rsquo;s — answers{" "}
        <code>engine_pending</code>.
      </p>
    </Stack>
  );
}
