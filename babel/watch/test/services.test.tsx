import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { ACTIONS } from "../../contract.ts";
import type { ServicesPreview } from "../api.ts";
import { Watch } from "../web.tsx";
import {
  MACHINES,
  fakeHost,
  runsResult,
  servicePreview,
  servicesPreview,
  watchDoors,
  type FakeHost,
} from "./host.ts";
import { choose, click, mount, settle, unmountAll, type } from "./render.tsx";

/*
  THE SERVICE SECTION (#400), and the distinction it exists to draw.

  A machine with no policy and a machine whose policy is installed and whose credential is not
  on the disk produce the SAME thing at the job: the archive refuses and nothing on any screen
  says which of the two it was. That is the whole defect, so what is asserted here is that the
  three states read differently, that the sentence for the middle one names the file the
  operator must write, and that the install carries the revision and the digest it previewed —
  which is what makes it a compare-and-swap rather than a button that overwrites.

  Nothing here asserts that a key can be typed, because there is no field for one and there
  cannot be: Manifold has no path anywhere for a person to supply a credential value.
*/

const SECTION = ".plugin-atyrode_babel_watch__services-section";
const MACHINE = "m-dev-01";
const ORIGIN = "https://store.example";
const CREDENTIAL_FILE = "/run/credentials/babel-restic-token";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

/**
 * Mounts Watch, picks the machine and presses Check once.
 *
 * `previews` is answered in order and the last is repeated, because the section's whole shape
 * is read-then-press: an endpoint typed after a Check is not in the preview yet, so a test
 * about installing has to Check again exactly as an operator does.
 */
async function checked(
  previews: readonly unknown[],
  install?: (args: unknown) => unknown,
): Promise<{ section: Element; fake: FakeHost; check: () => Promise<void> }> {
  let asked = 0;
  const fake = fakeHost(
    watchDoors({
      runs: () => runsResult([]),
      previewServices: () => previews[Math.min(asked++, previews.length - 1)],
      ...(install === undefined ? {} : { installServices: install }),
    }),
    MACHINES,
  );
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  const section = root.querySelector(SECTION);
  if (section === null) throw new Error("no services section on the panel");
  const check = async () => {
    await click(section.querySelector(`[data-action='atyrode.babel.${ACTIONS.previewServices}']`));
    await settle();
  };
  await choose(section.querySelector("[data-field='machine']"), MACHINE);
  await check();
  return { section, fake, check };
}

test("before anything is read the panel says so rather than showing an empty policy", async () => {
  const fake = fakeHost(watchDoors({ runs: () => runsResult([]) }), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  const section = root.querySelector(SECTION);
  expect(section?.querySelector("[data-field='unread']")?.textContent).toBe(
    "Nothing has been read yet. Pick a machine and press Check.",
  );
  // AND THE OWNER-ONLY DOOR IS NOT POLLED. A read of a machine's whole service configuration
  // on every mount would be a refusal every time for anyone who is not the hub's owner, about
  // a machine nobody has named.
  expect(fake.callsTo(ACTIONS.previewServices)).toEqual([]);
});

test("a machine with no policy reads as not configured, and names the file to write", async () => {
  const { section } = await checked([servicesPreview()]);
  expect(section.querySelector("[data-field='state']")?.textContent).toBe("not configured");
  expect(section.querySelector("[data-field='reason']")?.textContent).toBe(
    `no policy is installed under atyrode.babel.restic on ${MACHINE}`,
  );
  const credential = section.querySelector("[data-field='credential']")?.textContent ?? "";
  expect(credential).toContain("babel-restic");
  expect(credential).toContain(CREDENTIAL_FILE);
  expect(credential).toContain("does not see it yet");
  // There is no field for the value, and the section's one input is an endpoint.
  const inputs = [...section.querySelectorAll("input")].map((field) => field.dataset["field"]);
  expect(inputs).toEqual(["origin"]);
});

test("a policy installed whose credential is not on the machine is not the same screen", async () => {
  // The state that used to be indistinguishable from the one above. It must read differently
  // AND its sentence must be the remedy, because the remedy is entirely different: nothing to
  // install here, a file to write.
  const { section } = await checked([
    servicesPreview({
      expectedRevision: "a".repeat(64),
      current: true,
      services: [
        servicePreview({
          origin: ORIGIN,
          standing: "installed",
          reason:
            `${MACHINE} advertises no credential named babel-restic: write the token to ` +
            `${CREDENTIAL_FILE} on that machine, then check again`,
        }),
      ],
    }),
  ]);
  expect(section.querySelector("[data-field='state']")?.textContent).toBe("configured, not ready");
  expect(section.querySelector("[data-field='reason']")?.textContent).toContain(CREDENTIAL_FILE);
  // And installing it again is not the fix, so the button says what it would do instead.
  const install = section.querySelector(
    `[data-action='atyrode.babel.${ACTIONS.installServices}']`,
  ) as HTMLButtonElement | null;
  expect(install?.disabled).toBe(true);
  expect(section.textContent).toContain("The composed policy is already what stands there.");
});

test("a binding that came up reads as ready and has nothing left to say", async () => {
  const { section } = await checked([
    servicesPreview({
      expectedRevision: "a".repeat(64),
      current: true,
      services: [
        servicePreview({
          origin: ORIGIN,
          standing: "installed",
          reason: "",
          credential: {
            ref: "babel-restic",
            file: CREDENTIAL_FILE,
            advertised: true,
            readable: true,
          },
        }),
      ],
    }),
  ]);
  expect(section.querySelector("[data-field='state']")?.textContent).toBe("ready");
  expect(section.querySelector("[data-field='reason']")).toBeNull();
  expect(section.querySelector("[data-field='credential']")?.textContent).toContain(
    "reads it there",
  );
});

test("an endpoint typed since the last read is not installable until it has been read", async () => {
  // The digest the press would carry is the digest of a policy pointing somewhere else, which
  // the door refuses by name. The panel refuses it first, and says what to press.
  const { section } = await checked([servicesPreview()]);
  await type(section.querySelector("[data-field='origin']"), ORIGIN);
  const install = section.querySelector(
    `[data-action='atyrode.babel.${ACTIONS.installServices}']`,
  ) as HTMLButtonElement | null;
  expect(install?.disabled).toBe(true);
  expect(section.textContent).toContain("Press Check to compose the policy with that endpoint.");
});

function composed(): ServicesPreview {
  return servicesPreview({
    expectedRevision: "a".repeat(64),
    previewDigest: "c".repeat(64),
    services: [servicePreview({ origin: ORIGIN })],
  });
}

test("the install carries the revision and the digest that were previewed", async () => {
  const { section, fake, check } = await checked([servicesPreview(), composed()]);
  await type(section.querySelector("[data-field='origin']"), ORIGIN);
  await check();
  await click(section.querySelector(`[data-action='atyrode.babel.${ACTIONS.installServices}']`));
  await settle();
  // WHAT MAKES IT A COMPARE-AND-SWAP: the press posts the revision the operator read and the
  // digest of what he read, not whatever the server happens to hold now.
  expect(fake.callsTo(ACTIONS.installServices).at(0)?.args).toEqual({
    machineId: MACHINE,
    origins: [{ serviceId: "atyrode.babel.restic", origin: ORIGIN }],
    expectedRevision: "a".repeat(64),
    previewDigest: "c".repeat(64),
  });
  expect(section.textContent).toContain("Installed atyrode.babel.restic on m-dev-01");
  // And the answer is read back, because the revision the operator now holds is the one the
  // install just minted: a second press against the old one would be refused.
  expect(fake.callsTo(ACTIONS.previewServices)).toHaveLength(3);
});

test("a refused install is the sentence under the section, and nothing is re-read", async () => {
  const stale =
    `the preview was composed against ${"a".repeat(64)} and ${MACHINE} now stands at ` +
    `${"b".repeat(64)}: read the preview again before installing`;
  const { section, fake, check } = await checked([servicesPreview(), composed()], () => {
    throw new Error(stale);
  });
  await type(section.querySelector("[data-field='origin']"), ORIGIN);
  await check();
  await click(section.querySelector(`[data-action='atyrode.babel.${ACTIONS.installServices}']`));
  await settle();
  expect(section.querySelector(".plugin-atyrode_babel_watch__note")?.textContent).toBe(stale);
  // The refusal leaves the preview it was refused against on the screen: re-reading here would
  // replace the digest the operator is being told is stale with a fresh one, and the sentence
  // would then be about something no longer shown.
  expect(fake.callsTo(ACTIONS.previewServices)).toHaveLength(2);
});
