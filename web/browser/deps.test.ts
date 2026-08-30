// The web bundle is compiled into the babel binary and §2.7 forbids external
// assets, so every npm dependency is a permanent size and supply-chain cost
// carried by a security-sensitive surface. This test freezes the dependency
// set: adding one is an explicit, reviewed act that must update this baseline
// in the same change, never a side effect of reaching for a convenience.

import { expect, test } from "bun:test";
import pkg from "../package.json";

test("no new npm dependency enters package.json", () => {
  expect(Object.keys(pkg.dependencies ?? {}).sort()).toEqual([
    "react",
    "react-dom",
    "react-router-dom",
  ]);
  expect(Object.keys(pkg.devDependencies ?? {}).sort()).toEqual([
    "@types/bun",
    "@types/react",
    "@types/react-dom",
    "puppeteer-core",
    "typescript",
    "vite",
  ]);
});
