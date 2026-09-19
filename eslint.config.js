import eslint from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

/*
  THE LINTER, AND IT IS THE SIBLING'S.

  Manifold's own `eslint.config.js` is the shape this follows — the same recommended bases, the
  same four rule adjustments, and `react-hooks` over the halves that render — because Babel moves
  in lockstep with that repository and a third convention across three trees in one dependency
  order is a cost with no payer. Where this differs it is because the tree differs: the web halves
  live in `babel/feed` and `babel/watch` rather than in `packages/web`, and there
  is no service worker or isolate fixture to declare globals for.

  `consistent-type-imports` is the rule worth naming: the kit inlines this family's modules into
  every bundle that imports them, so a value import of something that is only a type is a runtime
  import of a module the bundle did not need.
*/
export default tseslint.config(
  {
    ignores: ["**/dist/**", "**/node_modules/**", "**/.integration/**", "babel/machine.js"],
  },
  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  {
    rules: {
      // `_` means deliberately unused. The sibling spells that for arguments; this tree needs it
      // for a binding too, because counting runes iterates a string without wanting the rune.
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/consistent-type-imports": ["error", { prefer: "type-imports" }],
      "no-console": "off",
      eqeqeq: ["error", "smart"],
    },
  },
  {
    files: ["babel/feed/**/*.{ts,tsx}", "babel/watch/**/*.{ts,tsx}"],
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      /*
        TWO RULES ARE WARNINGS, AND THE REASON IS RECORDED RATHER THAN THE RULE REMOVED.

        `refs` and `set-state-in-effect` find four real defects in the reading surface today: a
        ref written during render in `feed/home.tsx`, and three effects that call `setState` in
        their own body (`feed/home.tsx`, `feed/record.tsx`, `feed/topic.tsx`). Each is a
        cascading-render hazard on the front page, each is a genuine finding, and none of them is
        safe to change without exercising the rendered surface on a hub — which is a different
        change from installing a linter.

        So they warn, they stay visible on every run, and the issue that fixes them owns turning
        them back to `error`. Deleting the rules would have hidden four defects to make a gate
        green; fixing render semantics inside a tooling PR would have shipped an unverified
        change to the page the operator reads.
      */
      "react-hooks/refs": "warn",
      "react-hooks/set-state-in-effect": "warn",
    },
  },
  /*
    A PART REACHES THE BASELINE THROUGH ITS DOORS, AND THROUGH ONE FILE.

    `docs/building.md` states the rule — the baseline is not a library, a part calls it with
    `host.client.action` or `ctx.actions.call` — and until now nothing checked it. The one import
    a part may write is `contract.ts`, where every id, door name and result schema of the family
    is spelled once: a name shared is not a dependency, while a reached-into `store/`, `doors/`
    or `server/` module is, and it is the kind that survives the part being disabled.

    The patterns are keyed by how deep the file sits, because a pattern matches the SPECIFIER's
    text and not the file it resolves to: `../web.tsx` from `watch/test/` stays inside the part
    while `../store/store.ts` from `watch/` leaves it, and the two are the same shape. So a
    part's own top level is checked against `../` and a directory below it against `../../`.
    Every part sits at the same depth inside the baseline's directory, so every part is covered
    by those two rules and none needs one of its own.
  */
  {
    files: ["babel/feed/*.{ts,tsx}", "babel/watch/*.{ts,tsx}", "babel/jev/*.{ts,tsx}"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              group: ["../**", "!../contract.ts"],
              message:
                "a part reaches the baseline through its doors; the only module it may import is ../contract.ts",
            },
          ],
        },
      ],
    },
  },
  {
    files: [
      "babel/feed/*/**/*.{ts,tsx}",
      "babel/watch/*/**/*.{ts,tsx}",
      "babel/jev/*/**/*.{ts,tsx}",
    ],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              group: ["../../**", "!../../contract.ts", "../../../**"],
              message:
                "a part reaches the baseline through its doors; the only module it may import is the family's contract.ts",
            },
          ],
        },
      ],
    },
  },
  /*
    AND BABEL NEVER DEPENDS ON JEV, which is the direction that actually costs something.

    `test/optional-part.test.ts` catches a door that CALLS the part — it dispatches every read
    door against a hub that refuses the edge the way the host does. An IMPORT is the other road
    and no test sees it: a `settleSession` that reached into `babel/jev/voters.ts` would
    inline the part's code into the baseline's own bundle and answer fine on a hub where the part
    was never installed, which is how an optional part becomes a required one without anyone
    deciding to. So it is refused here, for every half of the baseline and its reading parts.
  */
  {
    files: ["babel/**/*.{ts,tsx}", "scripts/**/*.ts"],
    // The reading parts have their own, stricter `no-restricted-imports` above, and a flat
    // config's last word on a rule is the whole of it: matching them here would replace that
    // rule rather than add to it. They reach the part no more than the baseline does — their
    // own groups refuse every specifier that leaves the part but `contract.ts`.
    ignores: ["babel/feed/**", "babel/watch/**", "babel/jev/**"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              group: ["**/babel/jev/**"],
              message:
                "Babel never depends on the judgement part: it is enabled, disabled and removed on its own, and code that imports it is code that stops working when it is gone",
            },
          ],
        },
      ],
    },
  },
);
