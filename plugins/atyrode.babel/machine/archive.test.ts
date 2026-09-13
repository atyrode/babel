/*
  The archive operation against a REAL restic repository — a temporary one, under the test's own
  directory, with synthetic session roots. restic is the thing being integrated with; a fake of
  its JSON protocol would only prove this file's idea of restic, and the two facts that matter
  (a snapshot per root, and a second backup finding its parent) are facts about restic.

  The storage service is real too, for the same reason: a loopback listener on 127.0.0.1 that
  demands the capability and answers `GET /storage`, which is exactly the shape the engine's own
  job service proxy presents to a job (packages/agent/src/job-service-proxy.ts). The operation
  therefore goes through the whole delivery it will go through on a machine — a file holding
  {url, bearer}, one authenticated request, one storage document — and no secret is ever an
  environment value the test could have leaked into the child by accident.
*/

import { afterAll, beforeAll, beforeEach, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RESTIC_SERVICE, type Receipt } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import type { OutputFile, OutputSink } from "./output.ts";
import { ArchiveInputSchema, archive, type ArchiveDeps } from "./archive.ts";
import { BABEL_TAG, RESTIC_ENV, openRepo, resticConfig } from "./restic.ts";

const MACHINE = "test-machine-01";
const RESTIC_TIMEOUT = 120_000;
const PASSWORD = "babel-archive-test";
/** The shape the engine mints: 32 random bytes, base64url. */
const BEARER = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64url");

let home = "";
let repository = "";
let ompRoot = "";
let codexRoot = "";
let credentialFile = "";
let service: Bun.Server<undefined> | null = null;

/** What the service answers with, and how, for the test in hand. */
let document = "";
let status = 200;
let asked = 0;

/** `<root>/<harness>/<id>.jsonl` is one session; anything else in a root is archived but names
 *  no catalog row, exactly as a blob store or an index inside a real root does. */
const claim = (path: string): SessionRef | null => {
  const match = /\/(omp|codex)\/([^/]+)\.jsonl$/.exec(path);
  if (match === null) return null;
  const [, harness, sourceId] = match;
  if (harness === undefined || sourceId === undefined) return null;
  return {
    harness: harness === "omp" ? "omp" : "codex",
    sourceId,
    selector: `${harness}/${sourceId}`,
    primaryPath: path,
  };
};

/** The bindings the dispatcher hands the operation on a machine; the paths exist once the
 *  temporary host below does. */
let deps: ArchiveDeps;

class Recorder implements OutputSink {
  readonly files: Record<string, readonly unknown[]> = {};
  written: Receipt | null = null;

  async write(file: OutputFile, rows: readonly unknown[]): Promise<void> {
    this.files[file] = rows;
  }

  async receipt(receipt: Receipt): Promise<void> {
    this.written = receipt;
  }
}

interface SessionRow {
  readonly selector: string;
  readonly host: string;
  readonly harness: string;
  readonly source_id: string;
  readonly snapshot_id: string;
  readonly archived_at: string;
  readonly seen_at: string;
}

function rows(recorder: Recorder): readonly SessionRow[] {
  return (recorder.files["sessions"] ?? []) as readonly SessionRow[];
}

/** The storage document the service is to answer with, as JSON. */
function storage(overrides: Partial<Record<string, string>> = {}): string {
  return JSON.stringify({ repository, password: PASSWORD, ...overrides });
}

async function run(
  input: Partial<{ machineId: string; roots: string[] }> = {},
  overrides: Partial<ArchiveDeps> = {},
): Promise<{ receipt: Receipt; sessions: readonly SessionRow[] }> {
  const recorder = new Recorder();
  const receipt = await archive(
    ArchiveInputSchema.parse({ machineId: MACHINE, ...input }),
    recorder,
    { ...deps, ...overrides },
  );
  expect(recorder.written).toEqual(receipt);
  return { receipt, sessions: rows(recorder) };
}

/** A handle on the repository through the same delivery the operation uses. */
async function inspect() {
  return openRepo(await resticConfig({ credentialFile, env: process.env }));
}

beforeAll(async () => {
  home = mkdtempSync(join(tmpdir(), "babel-archive-"));
  repository = join(home, "repo");
  ompRoot = join(home, "roots", "omp");
  codexRoot = join(home, "roots", "codex");
  credentialFile = join(home, "restic-binding.json");
  mkdirSync(ompRoot, { recursive: true });
  mkdirSync(codexRoot, { recursive: true });
  writeFileSync(join(ompRoot, "a1b2c3.jsonl"), '{"type":"user","text":"first"}\n');
  writeFileSync(join(ompRoot, "d4e5f6.jsonl"), '{"type":"user","text":"second"}\n');
  writeFileSync(join(ompRoot, "blob-not-a-session"), "opaque\n");
  writeFileSync(join(codexRoot, "0192ab.jsonl"), '{"type":"message","text":"third"}\n');

  document = storage();
  service = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    fetch(request) {
      asked += 1;
      if (request.headers.get("authorization") !== `Bearer ${BEARER}`) {
        return new Response("unauthorized", { status: 401 });
      }
      if (new URL(request.url).pathname !== RESTIC_SERVICE.path) {
        return new Response("unknown", { status: 404 });
      }
      if (status !== 200) return new Response("refused", { status });
      return new Response(document, { headers: { "content-type": "application/json" } });
    },
  });
  // Exactly what the engine materializes for the binding: this job's proxy and its capability.
  writeFileSync(
    credentialFile,
    JSON.stringify({ url: `http://127.0.0.1:${service.port}`, bearer: BEARER }),
  );
  deps = { roots: async () => [ompRoot, codexRoot], claim, credentialFile };

  process.env[RESTIC_ENV.cacheDir] = join(home, "cache");
  // The repository is created by hand, once, for the deployment: the operation under test
  // never creates one, so the test plays the operator.
  expect(await (await inspect()).init()).toBe(true);
}, RESTIC_TIMEOUT);

afterAll(() => {
  service?.stop(true);
  delete process.env[RESTIC_ENV.cacheDir];
  delete process.env["RESTIC_REPOSITORY"];
  delete process.env["RESTIC_PASSWORD"];
  if (home !== "") rmSync(home, { recursive: true, force: true });
});

beforeEach(() => {
  document = storage();
  status = 200;
  asked = 0;
});

test(
  "a backup snapshots each root on its own and catalogues every session it archived",
  async () => {
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("completed");
    expect(receipt.kind).toBe("archive");
    expect(receipt.machineId).toBe(MACHINE);
    expect(receipt.runId).toMatch(/^run_/);
    expect(receipt.counts["roots"]).toBe(2);
    expect(receipt.counts["snapshots"]).toBe(2);
    expect(receipt.counts["sessions"]).toBe(3);
    // The file no adapter claims is archived and simply names no row.
    expect(receipt.counts["unclaimed"]).toBe(1);
    expect(receipt.counts["filesNew"]).toBe(4);
    // One storage document per run, asked for once.
    expect(asked).toBe(1);

    const snapshots = await (await inspect()).snapshots();
    expect(snapshots.length).toBe(2);
    const byRoot = new Map(snapshots.map((snapshot) => [snapshot.paths[0], snapshot]));
    expect([...byRoot.keys()].sort()).toEqual([codexRoot, ompRoot].sort());
    for (const snapshot of snapshots) {
      expect(snapshot.host).toBe(MACHINE);
      expect(snapshot.tags).toEqual([BABEL_TAG]);
      expect(snapshot.parentId).toBeNull();
    }

    // One snapshot id per root, and each session carries its own root's.
    const omp = byRoot.get(ompRoot);
    const codex = byRoot.get(codexRoot);
    expect(omp === undefined || codex === undefined).toBe(false);
    expect(omp?.id).not.toBe(codex?.id);
    const snapshotBySelector = Object.fromEntries(
      sessions.map((row) => [row.selector, row.snapshot_id]),
    );
    expect(snapshotBySelector).toEqual({
      "omp/a1b2c3": omp?.id ?? "",
      "omp/d4e5f6": omp?.id ?? "",
      "codex/0192ab": codex?.id ?? "",
    });
    for (const row of sessions) {
      expect(row.host).toBe(MACHINE);
      expect(`${row.harness}/${row.source_id}`).toBe(row.selector);
      // restic's own recorded time for that snapshot, so a catalog row and `restic snapshots`
      // never disagree about when the capture was taken.
      expect(row.archived_at).toBe((row.harness === "omp" ? omp?.time : codex?.time) ?? "");
    }
  },
  RESTIC_TIMEOUT,
);

test(
  "a second backup of unchanged roots is a parent-linked snapshot that adds no file",
  async () => {
    const before = await (await inspect()).snapshots();
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("completed");
    expect(receipt.counts["snapshots"]).toBe(2);
    expect(receipt.counts["filesNew"]).toBe(0);
    expect(receipt.counts["filesChanged"]).toBe(0);
    expect(receipt.counts["filesUnmodified"]).toBe(4);
    // Both new snapshots found a parent: the roots were re-read against the last capture
    // instead of from scratch, which is what per-root snapshots protect.
    expect(receipt.counts["snapshotsParented"]).toBe(2);

    const after = await (await inspect()).snapshots();
    expect(after.length).toBe(before.length + 2);
    const minted = new Set(sessions.map((row) => row.snapshot_id));
    expect(minted.size).toBe(2);
    const parents = new Map(before.map((snapshot) => [snapshot.paths[0], snapshot.id]));
    for (const snapshot of after.filter((candidate) => minted.has(candidate.id))) {
      expect(snapshot.parentId).toBe(parents.get(snapshot.paths[0]) ?? "");
    }
  },
  RESTIC_TIMEOUT,
);

test(
  "a changed session is archived again and recatalogued under the new snapshot",
  async () => {
    writeFileSync(join(ompRoot, "a1b2c3.jsonl"), '{"type":"user","text":"first"}\n{"type":"agent"}\n');
    const { receipt, sessions } = await run({ roots: [ompRoot] });

    expect(receipt.closure).toBe("completed");
    expect(receipt.counts["roots"]).toBe(1);
    expect(receipt.counts["filesChanged"]).toBe(1);
    expect(receipt.counts["filesUnmodified"]).toBe(2);
    expect(sessions.map((row) => row.selector).sort()).toEqual(["omp/a1b2c3", "omp/d4e5f6"]);
  },
  RESTIC_TIMEOUT,
);

test(
  "the repository is the service's answer, never the ambient environment",
  async () => {
    // A machine whose shell exports its own restic coordinates must not change where Babel
    // archives, or under which password: the child's environment is built, not inherited, and
    // the only source of either value is the storage document.
    process.env["RESTIC_REPOSITORY"] = join(home, "ambient-repo");
    process.env["RESTIC_PASSWORD"] = "not-the-repository-password";
    const { receipt } = await run({ roots: [codexRoot] });

    expect(receipt.closure).toBe("completed");
    expect(readdirSync(home)).not.toContain("ambient-repo");
  },
  RESTIC_TIMEOUT,
);

test("a job carrying no service binding fails and says which file was not there", async () => {
  const missing = join(home, "no-binding.json");
  const { receipt, sessions } = await run({}, { credentialFile: missing });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain(RESTIC_SERVICE.serviceId);
  expect(receipt.reason).toContain(missing);
  expect(sessions).toEqual([]);
  expect(receipt.counts["snapshots"]).toBe(0);
  // The service was never reached: there was nothing to reach it with.
  expect(asked).toBe(0);
});

test("a binding that is not one is refused rather than followed", async () => {
  // The engine is the only writer of this file. Anything else — a hand-edited document, a
  // service pointed off the machine — is a refusal, not a fetch from wherever it named.
  const forged = join(home, "forged-binding.json");
  writeFileSync(forged, JSON.stringify({ url: "https://archive.example.invalid", bearer: BEARER }));
  const { receipt } = await run({}, { credentialFile: forged });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain(forged);
  expect(receipt.reason).toContain("binding");
  expect(asked).toBe(0);
});

test("a service that refuses the route fails the run and carries its status", async () => {
  status = 403;
  const { receipt, sessions } = await run();

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain(RESTIC_SERVICE.path);
  expect(receipt.reason).toContain("403");
  expect(sessions).toEqual([]);
  expect(asked).toBe(1);
});

test("an answer that is not a storage document names the field, never a value", async () => {
  document = JSON.stringify({ repository, password: "" });
  const { receipt } = await run();

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("password");
  expect(receipt.reason).not.toContain(PASSWORD);
  expect(receipt.reason).toContain(RESTIC_SERVICE.serviceId);
});

test("an s3: repository without its object-store credential is refused before restic runs", async () => {
  // SPEC decision 50: the credential is required for an `s3:` locator and refused in halves.
  // A policy installed half-way must fail on its own terms, not as an unexplained restic exit.
  const s3 = "s3:https://object.invalid/bucket/babel";
  document = storage({ repository: s3 });
  const absent = await run();
  expect(absent.receipt.closure).toBe("failed");
  expect(absent.receipt.reason).toContain("object-store credential");
  expect(absent.receipt.counts["snapshots"]).toBe(0);

  document = JSON.stringify({
    repository: s3,
    password: PASSWORD,
    accessKeyId: "SYNTHETICACCESSKEYID",
  });
  const halved = await run();
  expect(halved.receipt.closure).toBe("failed");
  expect(halved.receipt.reason).toContain("two values or none");
});

test(
  "a root that cannot be backed up loses its own snapshot, not the others",
  async () => {
    const missing = join(home, "roots", "was-here");
    const { receipt, sessions } = await run({ roots: [ompRoot, missing] });

    expect(receipt.counts["roots"]).toBe(2);
    expect(receipt.counts["snapshots"]).toBe(1);
    // The healthy root is archived and catalogued all the same.
    expect(sessions.map((row) => row.selector).sort()).toEqual(["omp/a1b2c3", "omp/d4e5f6"]);
    expect(receipt.closure).toBe("failed");
    expect(receipt.reason).toContain(missing);
    // restic's own diagnosis reaches the operator, unwrapped from its --json error envelope.
    expect(receipt.reason).toContain("do not exist");
    expect(receipt.reason).not.toContain("message_type");
  },
  RESTIC_TIMEOUT,
);

test(
  "a password that does not open the repository says so, and is not read as a missing one",
  async () => {
    // The two failures need different remedies — fix the policy, or create the deployment's
    // repository — so restic's own diagnosis is what the receipt carries.
    document = storage({ password: "not-the-repository-password" });
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("failed");
    expect(receipt.reason).not.toContain("no repository at");
    expect(receipt.reason?.toLowerCase()).toContain("password");
    expect(sessions).toEqual([]);
  },
  RESTIC_TIMEOUT,
);

test(
  "a repository that does not exist is a failure, never a repository this run created",
  async () => {
    const absent = join(home, "absent-repo");
    document = storage({ repository: absent });
    const { receipt, sessions } = await run();

    expect(receipt.closure).toBe("failed");
    expect(receipt.reason).toContain(absent);
    expect(sessions).toEqual([]);
    expect(readdirSync(home)).not.toContain("absent-repo");
  },
  RESTIC_TIMEOUT,
);

test("a machine with no session root is skipped, and its service is never asked", async () => {
  const recorder = new Recorder();
  const receipt = await archive(ArchiveInputSchema.parse({ machineId: MACHINE }), recorder, {
    ...deps,
    roots: async () => [],
  });

  expect(receipt.closure).toBe("skipped");
  expect(receipt.reason).toBe("no session root exists on this host");
  expect(receipt.counts["snapshots"]).toBe(0);
  // A machine with nothing to archive must not be able to fail on the operator's policy.
  expect(asked).toBe(0);
  // The declared output is still written, as an empty document rather than a missing file.
  expect(recorder.files["sessions"]).toEqual([]);
});
