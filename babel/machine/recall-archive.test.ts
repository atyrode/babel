import { expect, test } from "bun:test";
import { mkdtemp, mkdir, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import {
  RECALL_MAX_RESULT_BYTES, RECALL_REQUEST_TTL_MS, RecallRequestSchema, RecallResultSchema,
  SESSION_RECORD_COORDINATES, type RecallLocator, type RecallPolicy, type RecallRequest,
} from "../contract.ts";
import { claim } from "./adapters/index.ts";
import { createRecallArchive, type RecallArchive } from "./recall-archive.ts";
import { BABEL_TAG, openRepo, type Repo } from "./restic.ts";

const HOST = "synthetic-archive-host";
const TIME = "2026-09-01T01:00:00.000Z";
const POLICY: RecallPolicy = {
  version: 1,
  classes: [{ id: "public", label: "Public", ceiling: 0 }, { id: "private", label: "Private", ceiling: 3 }],
  subjects: [{ name: "synthetic sessions", host: HOST, harness: "omp", sensitivity: 0 }],
};
const HEADER = '{"cwd":"/archived/work","timestamp":"2026-09-01T00:00:00.000Z","title":"Archived title","type":"session","version":3}\n';
function message(text: string, time: string | null = TIME, role = "user"): string {
  return JSON.stringify({ message: { content: [{ text, type: "text" }], role },
    ...(time === null ? {} : { timestamp: time }), type: "message" }) + "\n";
}
const SOURCE = HEADER + message("needle café 漢字 \ufeffand archived content") + message("assistant answer", TIME, "assistant") +
  message("second user turn", "2026-09-02T01:00:00.000Z") + message("second answer", TIME, "assistant");
const request = (value: unknown): RecallRequest => RecallRequestSchema.parse(value);
const search = (extra: object = {}): RecallRequest => request({ kind: "search", query: "needle", ...extra });
const withRestic = Bun.which("restic") === null ? test.skip : test;
const TIMEOUT = 120_000;

interface Fixture {
  home: string;
  source: string;
  sources: string[];
  cacheDir: string;
  repo: Repo;
  archive: RecallArchive;
  clock: { now: number };
}
async function fixture(body: (fixture: Fixture) => Promise<void>, options: {
  contents?: string[]; policy?: RecallPolicy;
} = {}): Promise<void> {
  const home = await mkdtemp(join(tmpdir(), "babel-recall-archive-"));
  const root = join(home, ".omp", "agent", "sessions", "synthetic-project");
  const cacheDir = join(home, "kept");
  const clock = { now: Date.parse("2026-09-21T00:00:00.000Z") };
  let archive: RecallArchive | undefined;
  try {
    await mkdir(root, { recursive: true });
    const sources: string[] = [];
    for (const [index, contents] of (options.contents ?? [SOURCE]).entries()) {
      const path = join(root, `2026-09-01T00-00-00-000Z_${index}.jsonl`);
      await Bun.write(path, contents);
      sources.push(path);
    }
    const repo = openRepo({ repository: join(home, "repo"), password: "synthetic-test-password",
      binary: Bun.which("restic") ?? "/missing-restic", cacheDir: join(home, "restic-cache"), objectStore: null });
    await repo.init();
    await repo.backup([root], { host: HOST, tags: [BABEL_TAG] });
    archive = await createRecallArchive({ repo, cacheDir, policy: options.policy ?? POLICY, now: () => clock.now });
    const source = sources[0];
    if (source === undefined) throw new Error("fixture requires a source");
    await body({ home, source, sources, cacheDir, repo, archive, clock });
  } finally {
    await archive?.close();
    await rm(home, { recursive: true, force: true });
  }
}

withRestic("cold and warm search use immutable redacted archive bytes, never changed live sources", async () => {
  await fixture(async ({ archive, source }) => {
    await Bun.write(source, HEADER + message("live only replacement"));
    const cold = await archive.execute("public", search());
    expect(RecallResultSchema.parse(cold).refusal).toBeNull();
    expect(cold.coverage).toEqual({ eligible: 1, indexed: 1, complete: true, overBound: 0 });
    expect(cold.cost.fetchedFiles).toBe(1);
    expect(cold.cost.fetchedBytes).toBe(Buffer.byteLength(SOURCE));
    expect(cold.cost.indexedFiles).toBe(1);
    expect(cold.cost.listedSnapshots).toBe(1);
    expect(cold.hits[0]?.excerpt.text).toContain("archived content");
    expect(cold.hits[0]?.title).toBe("Archived title");
    expect(cold.hits[0]?.workspace).toBe("/archived/work");
    expect(cold.hits[0]?.metadataOrigin).toBe("archive");
    const warm = await archive.execute("public", search({ maxFetchBytes: 0 }));
    expect(warm.cost.fetchedBytes).toBe(0);
    expect(warm.cost.cacheHits).toBe(1);
    expect(warm.hits).toEqual(cold.hits);
    const live = await archive.execute("public", search({ query: "replacement" }));
    expect(live.matches).toBe(0);
  });
}, TIMEOUT);

withRestic("newest captures replace indexed bytes and refuse old locators even when the path stays fixed", async () => {
  await fixture(async ({ archive, source, repo }) => {
    const before = await archive.execute("public", search());
    const old = before.hits[0]?.locator;
    if (old === undefined) throw new Error("missing first capture");
    await Bun.write(source, HEADER + message("replacement changed capture"));
    await repo.backup([dirname(source)], { host: HOST, tags: ["not-babel"] });
    expect((await archive.execute("public", search())).hits[0]?.locator).toEqual(old);
    const backup = await repo.backup([dirname(source)], { host: HOST, tags: [BABEL_TAG] });
    const replaced = await archive.execute("public", search({ query: "replacement" }));
    expect(replaced.hits[0]?.locator.snapshot).toBe(backup.snapshotId);
    expect(replaced.hits[0]?.locator.sourceDigest).not.toBe(old.sourceDigest);
    expect((await archive.execute("public", search())).matches).toBe(0);
    const stale = await archive.execute("public", request({ kind: "show", locator: old }));
    expect(stale.refusal).toBe("locator-mismatch");
    expect(stale.cost.fetchedFiles).toBe(0);
    expect(stale.hits).toEqual([]);
  });
}, TIMEOUT);

withRestic("a bounded cold scope converges across requests and does not claim exhaustive matches", async () => {
  await fixture(async ({ archive }) => {
    const first = await archive.execute("public", search({ maxFetchBytes: Buffer.byteLength(SOURCE) }));
    expect(first.coverage).toEqual({ eligible: 2, indexed: 1, complete: false, overBound: 1 });
    expect(first.cost.fetchedBytes).toBe(Buffer.byteLength(SOURCE));
    expect(first.matches).toBe(1);
    const next = await archive.execute("public", search({ maxFetchBytes: Buffer.byteLength(SOURCE) }));
    expect(next.coverage).toEqual({ eligible: 2, indexed: 2, complete: true, overBound: 0 });
    expect(next.matches).toBe(2);
    expect(new Set(next.hits.map(hit => hit.locator.session)).size).toBe(2);
  }, { contents: [SOURCE, SOURCE] });
}, TIMEOUT);

withRestic("filters use archived metadata and record timestamps, excluding unknown record times", async () => {
  await fixture(async ({ archive }) => {
    const all = await archive.execute("public", search());
    expect(all.matches).toBe(2);
    const timed = await archive.execute("public", search({ filter: { since: TIME, until: TIME } }));
    expect(timed.matches).toBe(1);
    expect(timed.hits[0]?.locator.record.time).toBe(TIME);
    const workspace = await archive.execute("public", search({ filter: { workspace: "/archived/work" } }));
    expect(workspace.matches).toBe(2);
    expect((await archive.execute("public", search({ filter: { workspace: "/live/work" } }))).matches).toBe(0);
    const foreign = await archive.execute("public", search({ filter: { host: "another-host" } }));
    expect(foreign.cost.listedSnapshots).toBe(0);
    expect(foreign.cost.fetchedBytes).toBe(0);
    const harness = await archive.execute("public", search({ filter: { harness: "codex" } }));
    expect(harness.cost.fetchedBytes).toBe(0);
    expect(harness.matches).toBe(0);
  }, { contents: [SOURCE + message("needle undated", null)] });
}, TIMEOUT);

withRestic("owner associations are labelled and cannot upgrade a subject's disclosure class", async () => {
  const policy: RecallPolicy = { ...POLICY, subjects: [{ ...POLICY.subjects[0]!, workspace: "/owner/work", repository: "owner/repo" }] };
  await fixture(async ({ archive }) => {
    const result = await archive.execute("public", search({ filter: { repository: "owner/repo" } }));
    expect(result.hits[0]?.workspace).toBe("/owner/work");
    expect(result.hits[0]?.repository).toBe("owner/repo");
    expect(result.hits[0]?.metadataOrigin).toBe("owner-association");
    expect((await archive.execute("public", search({ filter: { workspace: "/archived/work" } }))).matches).toBe(0);
  }, { policy });
}, TIMEOUT);

withRestic("show verifies record locators and serves surrounding records or explicit user turns", async () => {
  await fixture(async ({ archive }) => {
    const found = await archive.execute("public", search());
    const locator = found.hits[0]?.locator;
    if (locator === undefined) throw new Error("missing locator");
    const around = await archive.execute("public", request({ kind: "show", locator, selection: { kind: "around", records: 1 } }));
    expect(around.refusal).toBeNull();
    expect(around.hits[0]?.excerpt.text).toContain("Archived title");
    expect(around.hits[0]?.excerpt.text).toContain("assistant answer");
    expect(around.hits[0]?.excerpt.text).not.toContain("second user turn");
    const turns = await archive.execute("public", request({ kind: "show", locator, selection: { kind: "turns", first: 2, last: 2 } }));
    expect(turns.refusal).toBeNull();
    expect(turns.hits[0]?.excerpt.text).toContain("second user turn");
    expect(turns.hits[0]?.excerpt.text).toContain("second answer");
    expect(turns.hits[0]?.excerpt.text).not.toContain("needle");
    const falseRecord = { ...locator, record: { ...locator.record, digest: `sha256:${"0".repeat(64)}` } };
    expect((await archive.execute("public", request({ kind: "show", locator: falseRecord }))).refusal).toBe("locator-mismatch");
  });
}, TIMEOUT);

withRestic("size-first preview pages reconstruct every verified byte with sequential and retry-safe offsets", async () => {
  await fixture(async ({ archive, clock, cacheDir }) => {
    const found = await archive.execute("public", search());
    const locator = found.hits[0]?.locator;
    if (locator === undefined) throw new Error("missing locator");
    const preview = await archive.execute("public", request({ kind: "preview", locator }));
    expect(preview.hits).toEqual([]);
    expect(preview.preview?.sourceBytes).toBe(Buffer.byteLength(SOURCE));
    expect(preview.preview?.servedBytes).toBe(Buffer.byteLength(SOURCE));
    const token = preview.preview?.previewId;
    if (token === undefined) throw new Error("missing preview token");
    expect((await archive.execute("private", request({ kind: "session", previewId: token }))).refusal).toBe("disclosure");
    expect((await archive.execute("public", request({ kind: "session", previewId: token, offset: 1 }))).refusal).toBe("invalid-offset");
    const relative = (await readdir(cacheDir, { recursive: true })).find(path => path.endsWith(".records"));
    if (relative === undefined) throw new Error("missing kept stream");
    const keptPath = join(cacheDir, relative);
    // The token owns a verified snapshot, not a pointer to a subsequently changed kept stream.
    await Bun.write(keptPath, SOURCE.replace("needle", "forged"));
    const chunks: string[] = [];
    let offset = 0;
    for (;;) {
      // Force one page to start with U+FEFF: decoding must not silently strip an interior BOM.
      const maxBytes = offset === 0 ? Buffer.byteLength(SOURCE.slice(0, SOURCE.indexOf("\ufeff"))) : 17;
      const input = request({ kind: "session", previewId: token, offset, maxBytes });
      const page = await archive.execute("public", input);
      expect(RecallResultSchema.parse(page).refusal).toBeNull();
      expect(page.page?.offset).toBe(offset);
      expect(page.hits[0]?.locator.sourceDigest).toBe(preview.preview?.sourceDigest);
      const text = page.hits[0]?.excerpt.text;
      if (text === undefined || page.page === undefined) throw new Error("missing page");
      expect(text).not.toContain("\ufffd");
      expect(Buffer.byteLength(text)).toBe(page.page.nextOffset - offset);
      const retry = await archive.execute("public", input);
      expect(retry.hits).toEqual(page.hits);
      expect(retry.page).toEqual(page.page);
      chunks.push(text);
      offset = page.page.nextOffset;
      if (page.page.complete) break;
    }
    const reconstructed = chunks.join("");
    expect(reconstructed).toBe(SOURCE);
    expect(`sha256:${new Bun.CryptoHasher("sha256").update(reconstructed).digest("hex")}`).toBe(preview.preview!.sourceDigest);
    expect((await archive.execute("public", request({ kind: "session", previewId: token, offset: 0 }))).refusal).toBe("invalid-offset");
    await Bun.write(keptPath, SOURCE);
    clock.now += RECALL_REQUEST_TTL_MS;
    expect((await archive.execute("public", request({ kind: "session", previewId: token, offset }))).refusal).toBe("preview-expired");
    const second = await archive.execute("public", request({ kind: "preview", locator }));
    expect(second.preview).toBeDefined();
    await archive.close();
    const files = await readdir(cacheDir, { recursive: true });
    expect(files.some(path => path.includes("widening-"))).toBe(false);
    expect(files.some(path => path.endsWith(".records"))).toBe(true);
  });
}, TIMEOUT);

withRestic("corrupt replay refuses before publishing any hit or widening token and can refetch", async () => {
  await fixture(async ({ archive, cacheDir }) => {
    const found = await archive.execute("public", search());
    const locator = found.hits[0]?.locator;
    if (locator === undefined) throw new Error("missing locator");
    const relative = (await readdir(cacheDir, { recursive: true })).find(path => path.endsWith(".records"));
    if (relative === undefined) throw new Error("missing kept stream");
    const path = join(cacheDir, relative);
    const original = await Bun.file(path).text();
    await Bun.write(path, original.replace("needle", "forged"));
    const corrupt = await archive.execute("public", request({ kind: "preview", locator }));
    expect(corrupt.refusal).toBe("capture-changed");
    expect(corrupt.preview).toBeUndefined();
    expect(corrupt.hits).toEqual([]);
    const rebuilt = await archive.execute("public", request({ kind: "show", locator }));
    expect(rebuilt.refusal).toBeNull();
    expect(rebuilt.cost.fetchedFiles).toBe(1);
    expect(rebuilt.hits[0]?.excerpt.text).toContain("needle");
  });
}, TIMEOUT);

withRestic("redaction precedes indexing and widening, and full sessions are not silently truncated", async () => {
  const secret = "synthetic-super-secret-password-42";
  const content = HEADER + message(`needle password=${secret}`) + message("long tail " + "archivedword ".repeat(1600));
  await fixture(async ({ archive }) => {
    const found = await archive.execute("public", search());
    expect(JSON.stringify(found)).not.toContain(secret);
    const locator = found.hits[0]?.locator;
    if (locator === undefined) throw new Error("missing locator");
    const preview = await archive.execute("public", request({ kind: "preview", locator }));
    const token = preview.preview?.previewId;
    if (token === undefined) throw new Error("missing token");
    let offset = 0;
    let full = "";
    do {
      const page = await archive.execute("public", request({ kind: "session", previewId: token, offset }));
      if (page.page === undefined) throw new Error("missing page");
      full += page.hits[0]?.excerpt.text ?? "";
      offset = page.page.nextOffset;
      if (page.page.complete) break;
    } while (offset < (preview.preview?.servedBytes ?? 0));
    expect(Buffer.byteLength(full)).toBe(preview.preview!.servedBytes);
    expect(full).not.toContain(secret);
    expect(full).toContain("[[babel-redacted:");
    expect(full.endsWith(message("long tail " + "archivedword ".repeat(1600)))).toBe(true);
  }, { contents: [content] });
}, TIMEOUT);

withRestic("large valid ranked results omit lower hits under the serialized response bound", async () => {
  const content = HEADER + Array.from({ length: 10 }, () => message("needle " + "\\\"".repeat(3000))).join("");
  const policy: RecallPolicy = { ...POLICY, subjects: [{ ...POLICY.subjects[0]!,
    workspace: "\\".repeat(2048), repository: "\\".repeat(2048) }] };
  await fixture(async ({ archive }) => {
    const result = await archive.execute("public", search());
    expect(RecallResultSchema.parse(result).matches).toBe(10);
    expect(Buffer.byteLength(JSON.stringify(result))).toBeLessThanOrEqual(RECALL_MAX_RESULT_BYTES);
    expect(result.omitted).toBe(10 - result.hits.length);
    expect(result.omitted).toBeGreaterThan(0);
    expect(result.hits.length).toBeGreaterThan(0);
  }, { contents: [content], policy });
}, TIMEOUT);

// The sole fake repository tests the authority boundary: no source IO may be attempted.
test("highest matching owner sensitivity and unknown subjects refuse before dump", async () => {
  const home = await mkdtemp(join(tmpdir(), "babel-recall-authority-"));
  const path = "/synthetic/.omp/agent/sessions/project/2026-09-01T00-00-00-000Z_0.jsonl";
  const session = claim(path);
  if (session === null) throw new Error("unclaimed synthetic session");
  const id = "a".repeat(64);
  let dumps = 0;
  const forbidden = async (): Promise<never> => { throw new Error("forbidden synthetic operation"); };
  const repo: Repo = {
    repository: "authority-only", exists: forbidden, init: forbidden, backup: forbidden,
    check: forbidden, restore: forbidden, dump: forbidden, ls: forbidden,
    snapshots: async () => [{ id, shortId: id.slice(0, 8), time: TIME, parentId: null,
      host: HOST, paths: [path], tags: [BABEL_TAG] }],
    lsTo: async (_snapshot, sink) => { await sink({ path, type: "file", size: SOURCE.length, modifiedAt: TIME }); },
    dumpTo: async () => { dumps++; throw new Error("SECRET provider message"); },
  };
  const policy: RecallPolicy = { ...POLICY, subjects: [...POLICY.subjects,
    { name: "restricted project", host: HOST, selectorPrefix: session.selector, sensitivity: 3,
      workspace: "/archived/work", repository: "public-looking-name" }] };
  const archive = await createRecallArchive({ repo, cacheDir: home, policy });
  const locator: RecallLocator = { coordinates: SESSION_RECORD_COORDINATES, host: HOST, harness: "omp",
    session: session.selector, snapshot: id, path, captureDigest: `sha256:${"0".repeat(64)}`,
    sourceDigest: `sha256:${"0".repeat(64)}`, record: { line: 1, byteOffset: 0, byteLength: 1,
      digest: `sha256:${"0".repeat(64)}`, time: null } };
  try {
    const result = await archive.execute("public", search());
    expect(result.refusedSubjects).toEqual(["restricted project"]);
    expect(result.hits).toEqual([]);
    for (const kind of ["show", "preview"] as const) {
      expect((await archive.execute("public", request({ kind, locator }))).refusal).toBe("disclosure");
      expect((await archive.execute("public", request({ kind, locator: { ...locator, host: "unclassified-host" } }))).refusal).toBe("unclassified");
    }
    expect(dumps).toBe(0);
  } finally { await archive.close(); await rm(home, { recursive: true, force: true }); }
});

withRestic("identical selectors on separate hosts never share a cache stream or index capture", async () => {
  const otherHost = "synthetic-other-host";
  const policy: RecallPolicy = { ...POLICY, subjects: [...POLICY.subjects,
    { name: "second host", host: otherHost, sensitivity: 0 }] };
  await fixture(async ({ archive, source, repo }) => {
    const first = await archive.execute("public", search({ filter: { host: HOST } }));
    await Bun.write(source, HEADER + message("needle secondhostcontent"));
    await repo.backup([dirname(source)], { host: otherHost, tags: [BABEL_TAG] });
    const other = await archive.execute("public", search({ filter: { host: otherHost } }));
    expect(other.hits[0]?.locator.session).toBe(first.hits[0]?.locator.session);
    expect(other.hits[0]?.locator.host).toBe(otherHost);
    expect(other.hits[0]?.excerpt.text).toContain("secondhostcontent");
    const original = await archive.execute("public", search({ filter: { host: HOST }, maxFetchBytes: 0 }));
    expect(original.cost.fetchedBytes).toBe(0);
    expect(original.hits).toEqual(first.hits);
    const both = await archive.execute("public", search({ maxFetchBytes: 0 }));
    expect(both.matches).toBe(2);
    expect(new Set(both.hits.map(hit => hit.locator.host))).toEqual(new Set([HOST, otherHost]));
  }, { policy });
}, TIMEOUT);

withRestic("an unavailable selected repository yields only fixed refusal words, never a live fallback", async () => {
  await fixture(async ({ archive, home }) => {
    await rm(join(home, "repo"), { recursive: true, force: true });
    const result = await archive.execute("public", search());
    expect(result.refusal).toBe("archive-unavailable");
    expect(result.hits).toEqual([]);
    expect(result.cost.fetchedBytes).toBe(0);
    expect(JSON.stringify(result)).not.toContain(home);
    expect(JSON.stringify(result)).not.toContain("synthetic-test-password");
  });
}, TIMEOUT);
