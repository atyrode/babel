#!/usr/bin/env bun
import { actionResultProjectionDigest } from "@manifold/protocol";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  RECALL_RESULT_PROJECTION,
  RECALL_SKILL_PROJECTION,
} from "../babel/contract.ts";

// This is a source contract, never approval of a declaration discovered from a live hub.
const resultDigest = await actionResultProjectionDigest(RECALL_RESULT_PROJECTION);
const profile = [
  ...[
    ACTIONS.recallSearch,
    ACTIONS.recallShow,
    ACTIONS.recallPreview,
    ACTIONS.recallSession,
    ACTIONS.recallPoll,
  ].map((action) => ({
    door: `${BABEL_PLUGIN_ID}.${action}`,
    contractDigest: resultDigest,
    maxResultBytes: RECALL_RESULT_PROJECTION.maxResultBytes,
  })),
  {
    door: `${BABEL_PLUGIN_ID}.${ACTIONS.recallSkill}`,
    contractDigest: await actionResultProjectionDigest(RECALL_SKILL_PROJECTION),
    maxResultBytes: RECALL_SKILL_PROJECTION.maxResultBytes,
  },
];
const expected = `${JSON.stringify(profile, null, 2)}\n`;
const destination = new URL("../babel/recall-profile.json", import.meta.url);
const args = process.argv.slice(2);
if (args.length === 1 && args[0] === "--write") {
  await Bun.write(destination, expected);
} else if (args.length === 0) {
  if (!(await Bun.file(destination).exists()) || (await Bun.file(destination).text()) !== expected)
    throw new Error(
      "Recall source approvals drifted; review the declarations and run bun run recall:profile.",
    );
  console.log("Recall source approvals match the reviewed declarations.");
} else {
  throw new Error("Usage: bun scripts/recall-profile.ts [--write]");
}
