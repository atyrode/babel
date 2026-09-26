import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { ACTIONS } from "../../contract.ts";
import type { PulseResult } from "../api.ts";
import { Watch } from "../web.tsx";
import { MACHINES, fakeHost, pulseResult, runsResult, watchDoors } from "./host.ts";
import { mount, settle, unmountAll } from "./render.tsx";

/*
  THE ARCHIVE LABELS NO MACHINE ANSWERS FOR (#453), as the operator reads them.

  A capture is filed under the restic host label it was taken with, and a label becomes a hub
  machine id only where the owner mapped it. The line has to name each unmapped label verbatim
  with how many sessions it holds, say what the list left out rather than pretend it is whole,
  and stay off the page when every label is mapped.
*/

const SECTION = ".plugin-atyrode_babel_watch__archive";
const LABEL = ".plugin-atyrode_babel_watch__archive-label";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

async function open(archive: PulseResult["archive"]) {
  const fake = fakeHost(
    watchDoors({ runs: () => runsResult([]), pulse: () => pulseResult(undefined, archive) }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return { root, fake };
}

test("each unmapped label is named verbatim with its session count, in the door's order", async () => {
  const { root } = await open({
    unmapped: [
      { label: "workstation-linux", sessions: 1_204 },
      { label: "alex-x86_64-linux-wsl", sessions: 1 },
    ],
    omitted: 0,
  });

  expect(root.querySelector(SECTION)?.querySelector("h2")?.textContent).toBe("Archive");
  expect([...root.querySelectorAll(LABEL)].map((node) => node.textContent)).toEqual([
    "workstation-linux 1,204 sessions",
    "alex-x86_64-linux-wsl 1 session",
  ]);
});

test("labels past the pulse's bound are counted rather than dropped", async () => {
  const { root } = await open({
    unmapped: [{ label: "workstation-linux", sessions: 3 }],
    omitted: 2,
  });

  expect(root.querySelectorAll(LABEL)).toHaveLength(1);
  expect(root.querySelector(SECTION)?.textContent).toContain("and 2 more labels");
});

test("an archive whose every label is mapped renders nothing, from the one pulse read", async () => {
  const { root, fake } = await open({ unmapped: [], omitted: 0 });

  expect(root.querySelector(SECTION)).toBeNull();
  // The cycle and the archive are two halves of one answer, asked for once.
  expect(fake.callsTo(ACTIONS.pulse)).toHaveLength(1);
});
