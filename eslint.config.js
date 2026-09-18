import eslint from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

/*
  THE LINTER, AND IT IS THE SIBLING'S.

  Manifold's own `eslint.config.js` is the shape this follows — the same recommended bases, the
  same four rule adjustments, and `react-hooks` over the halves that render — because Babel moves
  in lockstep with that repository and a third convention across three trees in one dependency
  order is a cost with no payer. Where this differs it is because the tree differs: the web halves
  live in `atyrode.babel/feed` and `atyrode.babel/watch` rather than in `packages/web`, and there
  is no service worker or isolate fixture to declare globals for.

  `consistent-type-imports` is the rule worth naming: the kit inlines this family's modules into
  every bundle that imports them, so a value import of something that is only a type is a runtime
  import of a module the bundle did not need.
*/
export default tseslint.config(
  { ignores: ["**/dist/**", "**/node_modules/**", "**/.integration/**", "atyrode.babel/machine.js"] },
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
    files: ["atyrode.babel/feed/**/*.{ts,tsx}", "atyrode.babel/watch/**/*.{ts,tsx}"],
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
);
