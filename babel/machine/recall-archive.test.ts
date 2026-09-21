import { expect, spyOn, test } from "bun:test";
import { Database, type SQLQueryBindings } from "bun:sqlite";
import { mkdtemp, mkdir, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import {
  RECALL_MAX_PAYLOAD_BYTES,
  RECALL_MAX_RESULT_BYTES,
  RECALL_MAX_SERVED_BYTES,
  RECALL_REQUEST_TTL_MS,
  RecallRequestSchema,
  RecallResultSchema,
  SESSION_RECORD_COORDINATES,
  type RecallLocator,
  type RecallPolicy,
  type RecallRequest,
} from "../contract.ts";
import { claim } from "./adapters/index.ts";
import { createRecallArchive, type RecallArchive } from "./recall-archive.ts";
import { BABEL_TAG, openRepo, type ArchivedEntry, type Repo } from "./restic.ts";

const HOST = "synthetic-archive-host";
const TIME = "2026-09-01T01:00:00.000Z";
const POLICY: RecallPolicy = {
  version: 1,
  classes: [
    { id: "public", label: "Public", ceiling: 0 },
    { id: "private", label: "Private", ceiling: 3 },
  ],
  subjects: [{ name: "synthetic sessions", host: HOST, harness: "omp", sensitivity: 0 }],
};
const PARTITIONED_POLICY: RecallPolicy = {
  ...POLICY,
  classes: Array.from({ length: 16 }, (_, index) => ({
    id: `class-${index}`,
    label: `Class ${index}`,
    ceiling: 0,
  })),
};
const HEADER =
  '{"cwd":"/archived/work","timestamp":"2026-09-01T00:00:00.000Z","title":"Archived title","type":"session","version":3}\n';
function message(text: string, time: string | null = TIME, role = "user"): string {
  return (
    JSON.stringify({
      message: { content: [{ text, type: "text" }], role },
      ...(time === null ? {} : { timestamp: time }),
      type: "message",
    }) + "\n"
  );
}
const SOURCE =
  HEADER +
  message("needle café 漢字 \ufeffand archived content") +
  message("assistant answer", TIME, "assistant") +
  message("second user turn", "2026-09-02T01:00:00.000Z") +
  message("second answer", TIME, "assistant");
const request = (value: unknown): RecallRequest => RecallRequestSchema.parse(value);
const search = (extra: object = {}): RecallRequest =>
  request({ kind: "search", query: "needle", ...extra });
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
async function fixture(
  body: (fixture: Fixture) => Promise<void>,
  options: {
    contents?: string[];
    policy?: RecallPolicy;
  } = {},
): Promise<void> {
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
    const repo = openRepo({
      repository: join(home, "repo"),
      password: "synthetic-test-password",
      binary: Bun.which("restic") ?? "/missing-restic",
      cacheDir: join(home, "restic-cache"),
      objectStore: null,
    });
    await repo.init();
    await repo.backup([root], { host: HOST, tags: [BABEL_TAG] });
    archive = await createRecallArchive({
      repo,
      cacheDir,
      policy: options.policy ?? POLICY,
      temporaryDir: home,
      now: () => clock.now,
    });
    const source = sources[0];
    if (source === undefined) throw new Error("fixture requires a source");
    await body({ home, source, sources, cacheDir, repo, archive, clock });
  } finally {
    await archive?.close();
    await rm(home, { recursive: true, force: true });
  }
}

withRestic(
  "cold and warm search use immutable redacted archive bytes, never changed live sources",
  async () => {
    await fixture(async ({ archive, source }) => {
      await Bun.write(source, HEADER + message("live only replacement"));
      const cold = await archive.execute("public", search());
      expect(RecallResultSchema.parse(cold).refusal).toBeNull();
      expect(cold.coverage).toEqual({ eligible: 1, indexed: 1, complete: true, overBound: 0 });
      expect(cold.cost.fetchedFiles).toBe(1);
      expect(cold.cost.fetchedBytes).toBe(Buffer.byteLength(SOURCE));
      expect(cold.cost.replayedBytes).toBe(2 * Buffer.byteLength(SOURCE));
      expect(cold.cost.indexedFiles).toBe(1);
      expect(cold.cost.listedSnapshots).toBe(1);
      expect(cold.hits[0]?.excerpt.text).toContain("archived content");
      expect(cold.hits[0]?.title).toBe("Archived title");
      expect(cold.hits[0]?.workspace).toBe("/archived/work");
      expect(cold.hits[0]?.metadataOrigin).toBe("archive");
      const warm = await archive.execute("public", search({ maxFetchBytes: 0 }));
      expect(warm.cost.fetchedBytes).toBe(0);
      expect(warm.cost.cacheHits).toBe(1);
      expect(warm.cost.replayedBytes).toBe(Buffer.byteLength(SOURCE));
      expect(warm.hits).toEqual(cold.hits);
      const live = await archive.execute("public", search({ query: "replacement" }));
      expect(live.matches).toBe(0);
    });
  },
  TIMEOUT,
);

withRestic(
  "newest captures replace indexed bytes and refuse old locators even when the path stays fixed",
  async () => {
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
  },
  TIMEOUT,
);

withRestic(
  "a bounded cold scope converges across requests and does not claim exhaustive matches",
  async () => {
    await fixture(
      async ({ archive }) => {
        const first = await archive.execute(
          "public",
          search({ maxFetchBytes: Buffer.byteLength(SOURCE) }),
        );
        expect(first.coverage).toEqual({ eligible: 2, indexed: 1, complete: false, overBound: 1 });
        expect(first.cost.fetchedBytes).toBe(Buffer.byteLength(SOURCE));
        expect(first.matches).toBe(1);
        const next = await archive.execute(
          "public",
          search({ maxFetchBytes: Buffer.byteLength(SOURCE) }),
        );
        expect(next.coverage).toEqual({ eligible: 2, indexed: 2, complete: true, overBound: 0 });
        expect(next.matches).toBe(2);
        expect(new Set(next.hits.map((hit) => hit.locator.session)).size).toBe(2);
      },
      { contents: [SOURCE, SOURCE] },
    );
  },
  TIMEOUT,
);

withRestic(
  "filters use archived metadata and record timestamps, excluding unknown record times",
  async () => {
    await fixture(
      async ({ archive }) => {
        const all = await archive.execute("public", search());
        expect(all.matches).toBe(2);
        const timed = await archive.execute(
          "public",
          search({ filter: { since: TIME, until: TIME } }),
        );
        expect(timed.matches).toBe(1);
        expect(timed.hits[0]?.locator.record.time).toBe(TIME);
        const workspace = await archive.execute(
          "public",
          search({ filter: { workspace: "/archived/work" } }),
        );
        expect(workspace.matches).toBe(2);
        expect(
          (await archive.execute("public", search({ filter: { workspace: "/live/work" } })))
            .matches,
        ).toBe(0);
        const foreign = await archive.execute(
          "public",
          search({ filter: { host: "another-host" } }),
        );
        expect(foreign.cost.listedSnapshots).toBe(0);
        expect(foreign.cost.fetchedBytes).toBe(0);
        const harness = await archive.execute("public", search({ filter: { harness: "codex" } }));
        expect(harness.cost.fetchedBytes).toBe(0);
        expect(harness.matches).toBe(0);
      },
      { contents: [SOURCE + message("needle undated", null)] },
    );
  },
  TIMEOUT,
);

withRestic(
  "owner associations are labelled and cannot upgrade a subject's disclosure class",
  async () => {
    const policy: RecallPolicy = {
      ...POLICY,
      subjects: [{ ...POLICY.subjects[0]!, workspace: "/owner/work", repository: "owner/repo" }],
    };
    await fixture(
      async ({ archive }) => {
        const result = await archive.execute(
          "public",
          search({ filter: { repository: "owner/repo" } }),
        );
        expect(result.hits[0]?.workspace).toBe("/owner/work");
        expect(result.hits[0]?.repository).toBe("owner/repo");
        expect(result.hits[0]?.metadataOrigin).toBe("owner-association");
        expect(
          (await archive.execute("public", search({ filter: { workspace: "/archived/work" } })))
            .matches,
        ).toBe(0);
      },
      { policy },
    );
  },
  TIMEOUT,
);

withRestic(
  "show verifies record locators and serves surrounding records or explicit user turns",
  async () => {
    await fixture(async ({ archive }) => {
      const found = await archive.execute("public", search());
      const locator = found.hits[0]?.locator;
      if (locator === undefined) throw new Error("missing locator");
      const around = await archive.execute(
        "public",
        request({ kind: "show", locator, selection: { kind: "around", records: 1 } }),
      );
      expect(around.refusal).toBeNull();
      expect(around.hits[0]?.excerpt.text).toContain("Archived title");
      expect(around.hits[0]?.excerpt.text).toContain("assistant answer");
      expect(around.hits[0]?.excerpt.text).not.toContain("second user turn");
      const turns = await archive.execute(
        "public",
        request({ kind: "show", locator, selection: { kind: "turns", first: 2, last: 2 } }),
      );
      expect(turns.refusal).toBeNull();
      expect(turns.hits[0]?.excerpt.text).toContain("second user turn");
      expect(turns.hits[0]?.excerpt.text).toContain("second answer");
      expect(turns.hits[0]?.excerpt.text).not.toContain("needle");
      const falseRecord = {
        ...locator,
        record: { ...locator.record, digest: `sha256:${"0".repeat(64)}` },
      };
      expect(
        (await archive.execute("public", request({ kind: "show", locator: falseRecord }))).refusal,
      ).toBe("locator-mismatch");
    });
  },
  TIMEOUT,
);

withRestic(
  "size-first preview pages reconstruct every verified byte with sequential and retry-safe offsets",
  async () => {
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
      expect(
        (await archive.execute("private", request({ kind: "session", previewId: token }))).refusal,
      ).toBe("disclosure");
      expect(
        (await archive.execute("public", request({ kind: "session", previewId: token, offset: 1 })))
          .refusal,
      ).toBe("invalid-offset");
      const relative = (await readdir(cacheDir, { recursive: true })).find((path) =>
        path.endsWith(".records"),
      );
      if (relative === undefined) throw new Error("missing kept stream");
      const keptPath = join(cacheDir, relative);
      // The token owns a verified snapshot, not a pointer to a subsequently changed kept stream.
      await Bun.write(keptPath, SOURCE.replace("needle", "forged"));
      const chunks: string[] = [];
      let offset = 0;
      for (;;) {
        // Force one page to start with U+FEFF: decoding must not silently strip an interior BOM.
        const maxBytes =
          offset === 0 ? Buffer.byteLength(SOURCE.slice(0, SOURCE.indexOf("\ufeff"))) : 17;
        const input = request({ kind: "session", previewId: token, offset, maxBytes });
        const page = await archive.execute("public", input);
        expect(
          Buffer.byteLength(
            JSON.stringify({ requestId: crypto.randomUUID(), state: "complete", result: page }),
          ),
        ).toBeLessThanOrEqual(RECALL_MAX_RESULT_BYTES);
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
      expect(`sha256:${new Bun.CryptoHasher("sha256").update(reconstructed).digest("hex")}`).toBe(
        preview.preview!.sourceDigest,
      );
      expect(
        (await archive.execute("public", request({ kind: "session", previewId: token, offset: 0 })))
          .refusal,
      ).toBe("invalid-offset");
      await Bun.write(keptPath, SOURCE);
      clock.now += RECALL_REQUEST_TTL_MS;
      expect(
        (await archive.execute("public", request({ kind: "session", previewId: token, offset })))
          .refusal,
      ).toBe("preview-expired");
      const second = await archive.execute("public", request({ kind: "preview", locator }));
      expect(second.preview).toBeDefined();
      await archive.close();
      const files = await readdir(cacheDir, { recursive: true });
      expect(files.some((path) => path.includes("widening-"))).toBe(false);
      expect(files.some((path) => path.endsWith(".records"))).toBe(true);
    });
  },
  TIMEOUT,
);

withRestic(
  "corrupt replay refuses before publishing any hit or widening token and can refetch",
  async () => {
    await fixture(async ({ archive, cacheDir }) => {
      const found = await archive.execute("public", search());
      const locator = found.hits[0]?.locator;
      if (locator === undefined) throw new Error("missing locator");
      const relative = (await readdir(cacheDir, { recursive: true })).find((path) =>
        path.endsWith(".records"),
      );
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
  },
  TIMEOUT,
);

withRestic(
  "redaction precedes indexing and widening, and full sessions are not silently truncated",
  async () => {
    const secret = "synthetic-super-secret-password-42";
    const content =
      HEADER +
      message(`needle password=${secret}`) +
      message("long tail " + "archivedword ".repeat(1600));
    await fixture(
      async ({ archive }) => {
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
          const page = await archive.execute(
            "public",
            request({ kind: "session", previewId: token, offset }),
          );
          if (page.page === undefined) throw new Error("missing page");
          full += page.hits[0]?.excerpt.text ?? "";
          offset = page.page.nextOffset;
          if (page.page.complete) break;
        } while (offset < (preview.preview?.servedBytes ?? 0));
        expect(Buffer.byteLength(full)).toBe(preview.preview!.servedBytes);
        expect(full).not.toContain(secret);
        expect(full).toContain("[[babel-redacted:");
        expect(full.endsWith(message("long tail " + "archivedword ".repeat(1600)))).toBe(true);
      },
      { contents: [content] },
    );
  },
  TIMEOUT,
);

withRestic(
  "near-bound ranked results reserve the service envelope instead of failing delivery",
  async () => {
    const policy: RecallPolicy = {
      ...POLICY,
      subjects: [{ ...POLICY.subjects[0]!, workspace: "w", repository: "r" }],
    };
    await fixture(
      async ({ archive, repo, home, cacheDir, clock }) => {
        await archive.execute("public", search());
        const warm = await archive.execute("public", search());
        expect(warm.hits).toHaveLength(10);
        // Construct a valid ten-hit result precisely inside the former 72-byte failure window.
        let remaining = RECALL_MAX_RESULT_BYTES - 32 - Buffer.byteLength(JSON.stringify(warm));
        const subjects = warm.hits.map((hit, index) => {
          const metadata = { workspace: "w", repository: "r" };
          for (const field of ["workspace", "repository"] as const) {
            const extra = Math.min(remaining, 4094);
            metadata[field] += "\\".repeat(Math.floor(extra / 2)) + "x".repeat(extra % 2);
            remaining -= extra;
          }
          return {
            name: `synthetic ${index}`,
            host: HOST,
            harness: "omp" as const,
            sensitivity: 0,
            selectorPrefix: hit.locator.session,
            ...metadata,
          };
        });
        expect(remaining).toBe(0);
        await archive.close();
        const bounded = await createRecallArchive({
          repo,
          cacheDir,
          policy: { ...POLICY, subjects },
          temporaryDir: home,
          now: () => clock.now,
        });
        try {
          const result = await bounded.execute("public", search());
          expect(RecallResultSchema.parse(result).matches).toBe(10);
          expect(Buffer.byteLength(JSON.stringify(result))).toBeLessThanOrEqual(
            RECALL_MAX_PAYLOAD_BYTES,
          );
          expect(
            Buffer.byteLength(
              JSON.stringify({ requestId: crypto.randomUUID(), state: "complete", result }),
            ),
          ).toBeLessThanOrEqual(RECALL_MAX_RESULT_BYTES);
          expect(result.omitted).toBe(10 - result.hits.length);
          expect(result.omitted).toBeGreaterThan(0);
          expect(result.hits.length).toBeGreaterThan(0);
        } finally {
          await bounded.close();
        }
      },
      { contents: Array.from({ length: 10 }, () => SOURCE), policy },
    );
  },
  TIMEOUT,
);

// This fake repository tests the authority boundary: no source IO may be attempted.
test("highest matching owner sensitivity and unknown subjects refuse before dump", async () => {
  const home = await mkdtemp(join(tmpdir(), "babel-recall-authority-"));
  const path = "/synthetic/.omp/agent/sessions/project/2026-09-01T00-00-00-000Z_0.jsonl";
  const session = claim(path);
  if (session === null) throw new Error("unclaimed synthetic session");
  const id = "a".repeat(64);
  let dumps = 0;
  const forbidden = async (): Promise<never> => {
    throw new Error("forbidden synthetic operation");
  };
  const repo: Repo = {
    repository: "authority-only",
    exists: forbidden,
    init: forbidden,
    backup: forbidden,
    check: forbidden,
    restore: forbidden,
    dump: forbidden,
    ls: forbidden,
    snapshots: async () => [
      {
        id,
        shortId: id.slice(0, 8),
        time: TIME,
        parentId: null,
        host: HOST,
        paths: [path],
        tags: [BABEL_TAG],
      },
    ],
    lsTo: async (_snapshot, sink) => {
      await sink({ path, type: "file", size: SOURCE.length, modifiedAt: TIME });
    },
    dumpTo: async () => {
      dumps++;
      throw new Error("SECRET provider message");
    },
  };
  const policy: RecallPolicy = {
    ...POLICY,
    subjects: [
      ...POLICY.subjects,
      {
        name: "restricted project",
        host: HOST,
        selectorPrefix: session.selector,
        sensitivity: 3,
        workspace: "/archived/work",
        repository: "public-looking-name",
      },
    ],
  };
  const archive = await createRecallArchive({ repo, cacheDir: home, policy });
  const locator: RecallLocator = {
    coordinates: SESSION_RECORD_COORDINATES,
    host: HOST,
    harness: "omp",
    session: session.selector,
    snapshot: id,
    path,
    captureDigest: `sha256:${"0".repeat(64)}`,
    sourceDigest: `sha256:${"0".repeat(64)}`,
    record: {
      line: 1,
      byteOffset: 0,
      byteLength: 1,
      digest: `sha256:${"0".repeat(64)}`,
      time: null,
    },
  };
  try {
    const result = await archive.execute("public", search());
    expect(result.refusedSubjects).toEqual(["restricted project"]);
    expect(result.hits).toEqual([]);
    for (const kind of ["show", "preview"] as const) {
      expect((await archive.execute("public", request({ kind, locator }))).refusal).toBe(
        "disclosure",
      );
      expect(
        (
          await archive.execute(
            "public",
            request({ kind, locator: { ...locator, host: "unclassified-host" } }),
          )
        ).refusal,
      ).toBe("unclassified");
    }
    expect(dumps).toBe(0);
  } finally {
    await archive.close();
    await rm(home, { recursive: true, force: true });
  }
});

withRestic(
  "identical selectors on separate hosts never share a cache stream or index capture",
  async () => {
    const otherHost = "synthetic-other-host";
    const policy: RecallPolicy = {
      ...POLICY,
      subjects: [...POLICY.subjects, { name: "second host", host: otherHost, sensitivity: 0 }],
    };
    await fixture(
      async ({ archive, source, repo }) => {
        const first = await archive.execute("public", search({ filter: { host: HOST } }));
        await Bun.write(source, HEADER + message("needle secondhostcontent"));
        await repo.backup([dirname(source)], { host: otherHost, tags: [BABEL_TAG] });
        const other = await archive.execute("public", search({ filter: { host: otherHost } }));
        expect(other.hits[0]?.locator.session).toBe(first.hits[0]?.locator.session);
        expect(other.hits[0]?.locator.host).toBe(otherHost);
        expect(other.hits[0]?.excerpt.text).toContain("secondhostcontent");
        const original = await archive.execute(
          "public",
          search({ filter: { host: HOST }, maxFetchBytes: 0 }),
        );
        expect(original.cost.fetchedBytes).toBe(0);
        expect(original.hits).toEqual(first.hits);
        const both = await archive.execute("public", search({ maxFetchBytes: 0 }));
        expect(both.matches).toBe(2);
        expect(new Set(both.hits.map((hit) => hit.locator.host))).toEqual(
          new Set([HOST, otherHost]),
        );
      },
      { policy },
    );
  },
  TIMEOUT,
);

withRestic(
  "an unavailable selected repository yields only fixed refusal words, never a live fallback",
  async () => {
    await fixture(async ({ archive, home }) => {
      await rm(join(home, "repo"), { recursive: true, force: true });
      const result = await archive.execute("public", search());
      expect(result.refusal).toBe("archive-unavailable");
      expect(result.hits).toEqual([]);
      expect(result.cost.fetchedBytes).toBe(0);
      expect(JSON.stringify(result)).not.toContain(home);
      expect(JSON.stringify(result)).not.toContain("synthetic-test-password");
    });
  },
  TIMEOUT,
);

withRestic(
  "a corrupt kept stream retains capture-changed while rebuilding a missing index",
  async () => {
    await fixture(async ({ archive, cacheDir, repo, home }) => {
      await archive.execute("public", search());
      await archive.close();
      const files = await readdir(cacheDir, { recursive: true });
      const stream = files.find((path) => path.endsWith(".records"));
      const database = files.find((path) => path.endsWith("tokens.sqlite"));
      if (stream === undefined || database === undefined) throw new Error("missing kept capture");
      await Bun.write(join(cacheDir, stream), SOURCE.replace("needle", "forged"));
      await rm(dirname(join(cacheDir, database)), { recursive: true });
      const reopened = await createRecallArchive({
        repo,
        cacheDir,
        policy: POLICY,
        temporaryDir: home,
      });
      try {
        const result = await reopened.execute("public", search({ maxFetchBytes: 0 }));
        expect(result.refusal).toBe("capture-changed");
        expect(result.hits).toEqual([]);
        expect(result.cost.fetchedBytes).toBe(0);
        expect(result.cost.replayedBytes).toBe(Buffer.byteLength(SOURCE));
        expect(JSON.stringify(result)).not.toContain("forged");
      } finally {
        await reopened.close();
      }
    });
  },
  TIMEOUT,
);

withRestic(
  "a busy index record sink retains index-busy without leaking its cause",
  async () => {
    await fixture(async ({ archive }) => {
      const query = Database.prototype.query;
      const fault = spyOn(Database.prototype, "query").mockImplementation(function <
        Row,
        Bindings extends SQLQueryBindings | SQLQueryBindings[],
      >(this: Database, sql: string) {
        const statement = query.bind(this)<Row, Bindings>(sql);
        if (sql.startsWith("INSERT INTO session_records")) {
          const run = statement.run;
          statement.run = () => {
            statement.run = run;
            throw Object.assign(new Error("PRIVATE synthetic SQL cause"), { code: "SQLITE_BUSY" });
          };
        }
        return statement;
      });
      try {
        const result = await archive.execute("public", search());
        expect(result.refusal).toBe("index-busy");
        expect(result.hits).toEqual([]);
        expect(result.coverage.indexed).toBe(0);
        expect(result.cost.replayedBytes).toBe(Buffer.byteLength(SOURCE));
        expect(JSON.stringify(result)).not.toContain("PRIVATE");
      } finally {
        fault.mockRestore();
      }
      const recovered = await archive.execute("public", search({ maxFetchBytes: 0 }));
      expect(recovered.refusal).toBeNull();
      expect(recovered.matches).toBe(1);
    });
  },
  TIMEOUT,
);

withRestic(
  "held metadata survives restart and safely rebuilds malformed or stale sidecars",
  async () => {
    await fixture(async ({ archive, cacheDir, repo, home }) => {
      const input = search({ query: "unmatched", filter: { workspace: "/archived/work" } });
      const cold = await archive.execute("public", input);
      expect(cold.cost.replayedBytes).toBe(Buffer.byteLength(SOURCE));
      await archive.close();
      const sidecar = (await readdir(cacheDir, { recursive: true })).find((path) =>
        path.endsWith(".recall-metadata.json"),
      );
      if (sidecar === undefined) throw new Error("missing metadata sidecar");
      const path = join(cacheDir, sidecar);
      const retained = await Bun.file(path).text();
      // Each restart must use matching metadata without opening the held transcript.
      for (const damaged of [
        null,
        "{",
        " ".repeat(64 * 1024 + 1),
        JSON.stringify({
          key: "0".repeat(64),
          value: {
            title: "wrong capture",
            workspace: "/wrong/work",
            repository: null,
            metadataOrigin: "archive",
          },
        }),
      ]) {
        if (damaged !== null) await Bun.write(path, damaged);
        const reopened = await createRecallArchive({
          repo,
          cacheDir,
          policy: POLICY,
          temporaryDir: home,
        });
        try {
          const result = await reopened.execute("public", input);
          expect(result.refusal).toBeNull();
          expect(result.coverage).toEqual({
            eligible: 1,
            indexed: 1,
            complete: true,
            overBound: 0,
          });
          expect(result.cost.fetchedBytes).toBe(0);
          expect(result.cost.replayedBytes).toBe(damaged === null ? 0 : Buffer.byteLength(SOURCE));
          const matched = await reopened.execute(
            "public",
            search({ filter: { workspace: "/archived/work" }, maxFetchBytes: 0 }),
          );
          expect(matched.matches).toBe(1);
          expect(matched.hits[0]?.workspace).toBe("/archived/work");
          expect(matched.cost.replayedBytes).toBe(Buffer.byteLength(SOURCE));
          expect(await Bun.file(path).text()).toBe(retained);
        } finally {
          await reopened.close();
        }
      }
    });
  },
  TIMEOUT,
);

withRestic(
  "widening lives in owned scratch and closing one archive preserves another's pages",
  async () => {
    await fixture(async ({ archive, cacheDir, repo, home }) => {
      const locator = (await archive.execute("public", search())).hits[0]?.locator;
      if (locator === undefined) throw new Error("missing locator");
      const other = await createRecallArchive({
        repo,
        cacheDir,
        policy: POLICY,
        temporaryDir: home,
      });
      try {
        await archive.execute("public", request({ kind: "preview", locator }));
        const preview = await other.execute("public", request({ kind: "preview", locator }));
        const previewId = preview.preview?.previewId;
        if (previewId === undefined) throw new Error("missing preview");
        expect(
          (await readdir(cacheDir, { recursive: true })).some((path) => path.includes("widening-")),
        ).toBe(false);
        expect(
          (await readdir(home)).filter((path) => path.startsWith("babel-recall-widening-")),
        ).toHaveLength(2);
        await archive.close();
        const page = await other.execute("public", request({ kind: "session", previewId }));
        expect(page.refusal).toBeNull();
        expect(page.hits[0]?.excerpt.text).toBe(SOURCE);
        expect(page.page?.complete).toBe(true);
      } finally {
        await other.close();
      }
      expect(
        (await readdir(home)).filter((path) => path.startsWith("babel-recall-widening-")),
      ).toEqual([]);
    });
  },
  TIMEOUT,
);

/** A deterministic archived listing: no subprocess, live source reads or provider state. */
function listedRepo(entries: ArchivedEntry[], source: string): Repo {
  const bytes = new TextEncoder().encode(source);
  const forbidden = async (): Promise<never> => {
    throw new Error("unexpected synthetic operation");
  };
  const id = "b".repeat(64);
  return {
    repository: "synthetic-listed-archive",
    exists: forbidden,
    init: forbidden,
    backup: forbidden,
    check: forbidden,
    restore: forbidden,
    dump: forbidden,
    ls: forbidden,
    snapshots: async () => [
      {
        id,
        shortId: id.slice(0, 8),
        time: TIME,
        parentId: null,
        host: HOST,
        paths: ["/synthetic"],
        tags: [BABEL_TAG],
      },
    ],
    lsTo: async (_snapshot, sink) => {
      for (const entry of entries) await sink(entry);
    },
    dumpTo: async (_snapshot, _path, sink) => {
      await sink(bytes);
      return { bytes: bytes.byteLength };
    },
  };
}

async function listedFixture(
  body: (
    fixture: Pick<Fixture, "home" | "cacheDir" | "repo" | "archive" | "clock">,
  ) => Promise<void>,
  options: { source?: string; policy?: RecallPolicy } = {},
): Promise<void> {
  const home = await mkdtemp(join(tmpdir(), "babel-recall-listed-"));
  const cacheDir = join(home, "cache");
  const source = options.source ?? SOURCE;
  const repo = listedRepo(
    [
      {
        path: "/synthetic/.omp/agent/sessions/project/2026-09-01T00-00-00-000Z_fixture.jsonl",
        type: "file",
        size: Buffer.byteLength(source),
        modifiedAt: TIME,
      },
    ],
    source,
  );
  const clock = { now: Date.parse(TIME) };
  const archive = await createRecallArchive({
    repo,
    cacheDir,
    policy: options.policy ?? POLICY,
    temporaryDir: home,
    now: () => clock.now,
  });
  try {
    await body({ home, cacheDir, repo, archive, clock });
  } finally {
    await archive.close();
    await rm(home, { recursive: true, force: true });
  }
}

test(
  "widening reserves class bytes, reclaims completed staging and never decrements it twice",
  async () => {
    const size = 16 * 1024 * 1024;
    const prefix = HEADER + message("needle");
    const block = JSON.stringify({ padding: "x".repeat(64 * 1024) }) + "\n";
    const count = Math.floor((size - prefix.length) / block.length);
    const remaining = size - prefix.length - count * block.length;
    const source =
      prefix +
      block.repeat(count) +
      JSON.stringify({
        padding: "x".repeat(remaining - (JSON.stringify({ padding: "" }).length + 1)),
      }) +
      "\n";
    await listedFixture(
      async ({ archive, clock, home }) => {
        const found = await archive.execute("class-0", search());
        expect(found.previewByteLimit).toBe(32 * 1024 * 1024);
        const locator = found.hits[0]?.locator;
        if (locator === undefined) throw new Error("missing locator");
        const preview = async (classId: string): Promise<string> => {
          const result = await archive.execute(classId, request({ kind: "preview", locator }));
          expect(result.refusal).toBeNull();
          expect(result.preview?.servedBytes).toBe(size);
          if (result.preview === undefined) throw new Error("missing preview");
          return result.preview.previewId;
        };
        const first = await preview("class-0");
        const second = await preview("class-0");
        const full = await archive.execute("class-0", request({ kind: "preview", locator }));
        expect(full.refusal).toBe("fetch-bound");
        expect(full.previewByteLimit).toBe(
          RECALL_MAX_SERVED_BYTES / PARTITIONED_POLICY.classes.length,
        );
        expect(full.preview).toBeUndefined();
        const other = await preview("class-1");
        let offset = 0;
        const digest = new Bun.CryptoHasher("sha256");
        for (;;) {
          const result = await archive.execute(
            "class-0",
            request({ kind: "session", previewId: first, offset }),
          );
          expect(result.refusal).toBeNull();
          if (result.page === undefined || result.hits[0] === undefined)
            throw new Error("missing page");
          digest.update(result.hits[0].excerpt.text);
          offset = result.page.nextOffset;
          if (result.page.complete) break;
        }
        expect(offset).toBe(size);
        expect(`sha256:${digest.digest("hex")}`).toBe(locator.sourceDigest);
        const scratch = (await readdir(home)).find((path) =>
          path.startsWith("babel-recall-widening-"),
        );
        if (scratch === undefined) throw new Error("missing owned scratch");
        expect(await Bun.file(join(home, scratch, first)).exists()).toBe(false);
        expect(await Bun.file(join(home, scratch, second)).exists()).toBe(true);
        clock.now += RECALL_REQUEST_TTL_MS / 2;
        // Keep both active classes alive while the completed handle ages out.
        for (const [classId, previewId] of [
          ["class-0", second],
          ["class-1", other],
        ] as const) {
          const result = await archive.execute(
            classId,
            request({ kind: "session", previewId, maxBytes: 17 }),
          );
          expect(result.refusal).toBeNull();
          expect(result.hits[0]?.excerpt.text).toBe(source.slice(0, 17));
        }
        await preview("class-0");
        clock.now += RECALL_REQUEST_TTL_MS / 2;
        expect(
          (await archive.execute("class-0", request({ kind: "session", previewId: first, offset })))
            .refusal,
        ).toBe("preview-expired");
        // Expiring the already-released first handle must not free a second 16 MiB.
        expect(
          (await archive.execute("class-0", request({ kind: "preview", locator }))).refusal,
        ).toBe("fetch-bound");
        expect(
          (
            await archive.execute(
              "class-1",
              request({ kind: "session", previewId: other, offset: 17, maxBytes: 17 }),
            )
          ).refusal,
        ).toBeNull();
        await archive.close();
        expect(
          (await readdir(home)).filter((path) => path.startsWith("babel-recall-widening-")),
        ).toEqual([]);
      },
      { source, policy: PARTITIONED_POLICY },
    );
  },
  TIMEOUT,
);

test("class handle capacity evicts only completed replays and preserves final pages after unlink", async () => {
  await listedFixture(
    async ({ archive, home }) => {
      const locator = (await archive.execute("class-0", search())).hits[0]?.locator;
      if (locator === undefined) throw new Error("missing locator");
      const preview = async (classId: string): Promise<string> => {
        const result = await archive.execute(classId, request({ kind: "preview", locator }));
        expect(result.refusal).toBeNull();
        if (result.preview === undefined) throw new Error("missing preview");
        return result.preview.previewId;
      };
      const active: string[] = [];
      for (let index = 0; index < 8; index++) active.push(await preview("class-0"));
      expect(
        (await archive.execute("class-0", request({ kind: "preview", locator }))).refusal,
      ).toBe("fetch-bound");
      const other = await preview("class-1");
      const scratch = (await readdir(home)).find((path) =>
        path.startsWith("babel-recall-widening-"),
      );
      if (scratch === undefined) throw new Error("missing owned scratch");
      let completed = active[3]!;
      // More than one class's handle budget can complete without completed retries blocking new work.
      for (let index = 0; index < 16; index++) {
        const input = request({ kind: "session", previewId: completed });
        const final = await archive.execute("class-0", input);
        expect(final.refusal).toBeNull();
        expect(final.hits[0]?.excerpt.text).toBe(SOURCE);
        expect(final.page?.complete).toBe(true);
        expect(await Bun.file(join(home, scratch, completed)).exists()).toBe(false);
        const terminal = await archive.execute(
          "class-0",
          request({
            kind: "session",
            previewId: completed,
            offset: Buffer.byteLength(SOURCE),
          }),
        );
        expect(terminal.refusal).toBeNull();
        expect(terminal.page).toEqual({
          offset: Buffer.byteLength(SOURCE),
          nextOffset: Buffer.byteLength(SOURCE),
          totalBytes: Buffer.byteLength(SOURCE),
          complete: true,
        });
        expect(terminal.hits[0]?.excerpt.text).toBe("");
        const retry = await archive.execute("class-0", input);
        expect(retry.page).toEqual(final.page);
        expect(retry.hits).toEqual(final.hits);
        const replacement = await preview("class-0");
        expect((await archive.execute("class-0", input)).refusal).toBe("preview-expired");
        completed = replacement;
      }
      for (const [classId, previewId] of [
        ["class-0", active[0]!],
        ["class-1", other],
      ] as const) {
        const result = await archive.execute(
          classId,
          request({ kind: "session", previewId, maxBytes: 17 }),
        );
        expect(result.refusal).toBeNull();
        expect(result.hits[0]?.excerpt.text).toBe(SOURCE.slice(0, 17));
      }
    },
    { policy: PARTITIONED_POLICY },
  );
});

test("only successful page progress renews the idle widening deadline", async () => {
  await listedFixture(async ({ archive, clock }) => {
    const locator = (await archive.execute("public", search())).hits[0]?.locator;
    if (locator === undefined) throw new Error("missing locator");
    const preview = await archive.execute("public", request({ kind: "preview", locator }));
    const previewId = preview.preview?.previewId;
    if (previewId === undefined) throw new Error("missing preview");
    clock.now += (RECALL_REQUEST_TTL_MS * 3) / 4;
    const first = await archive.execute(
      "public",
      request({ kind: "session", previewId, maxBytes: 17 }),
    );
    expect(first.refusal).toBeNull();
    clock.now += (RECALL_REQUEST_TTL_MS * 3) / 4;
    // This is past the original fixed deadline, but not past the last progress's idle deadline.
    const input = request({ kind: "session", previewId, offset: 17, maxBytes: 17 });
    const second = await archive.execute("public", input);
    expect(second.refusal).toBeNull();
    expect(second.page?.nextOffset).toBe(34);
    clock.now += (RECALL_REQUEST_TTL_MS * 3) / 4;
    expect(
      (await archive.execute("public", request({ kind: "session", previewId, offset: 35 })))
        .refusal,
    ).toBe("invalid-offset");
    expect((await archive.execute("public", search())).refusal).toBeNull();
    const retry = await archive.execute("public", input);
    expect(retry.hits).toEqual(second.hits);
    expect(retry.page).toEqual(second.page);
    clock.now += RECALL_REQUEST_TTL_MS / 4;
    expect(
      (await archive.execute("public", request({ kind: "session", previewId, offset: 34 })))
        .refusal,
    ).toBe("preview-expired");
  });
});

test("an unwritable metadata sidecar never excludes a verified held or rebuilt index", async () => {
  await listedFixture(async ({ archive, cacheDir, repo, home }) => {
    expect((await archive.execute("public", search())).matches).toBe(1);
    await archive.close();
    const files = await readdir(cacheDir, { recursive: true });
    const sidecar = files.find((path) => path.endsWith(".recall-metadata.json"));
    const database = files.find((path) => path.endsWith("tokens.sqlite"));
    if (sidecar === undefined || database === undefined) throw new Error("missing cache files");
    const path = join(cacheDir, sidecar);
    await rm(path);
    // Unlike permissions under root, renaming a regular file over this directory always fails.
    await mkdir(path);
    await Bun.write(join(path, "unowned"), "must remain");
    for (const rebuild of [false, true]) {
      if (rebuild) await rm(dirname(join(cacheDir, database)), { recursive: true });
      const reopened = await createRecallArchive({
        repo,
        cacheDir,
        policy: POLICY,
        temporaryDir: home,
      });
      try {
        const result = await reopened.execute(
          "public",
          search({
            maxFetchBytes: 0,
            filter: { workspace: "/archived/work" },
          }),
        );
        expect(result.refusal).toBeNull();
        expect(result.coverage).toEqual({ eligible: 1, indexed: 1, complete: true, overBound: 0 });
        expect(result.matches).toBe(1);
        expect(result.hits[0]?.workspace).toBe("/archived/work");
        expect(result.hits[0]?.excerpt.text).toContain("needle");
        expect(result.cost.fetchedBytes).toBe(0);
        expect(result.cost.indexedFiles).toBe(rebuild ? 1 : 0);
        expect(await Bun.file(join(path, "unowned")).text()).toBe("must remain");
        expect(
          (await readdir(cacheDir, { recursive: true })).filter((name) =>
            name.startsWith(`${sidecar}.`),
          ),
        ).toEqual([]);
      } finally {
        await reopened.close();
      }
    }
  });
});

test("snapshot freshness excludes unknown hosts and follows the enumeration host filter", async () => {
  await listedFixture(async ({ archive, repo }) => {
    const [known] = await repo.snapshots();
    if (known === undefined) throw new Error("missing snapshot");
    const unknownHost = "unclassified-host";
    repo.snapshots = async () => [
      known,
      { ...known, id: "c".repeat(64), host: unknownHost, time: "2026-09-20T00:00:00.000Z" },
      { ...known, id: "d".repeat(64), tags: [], time: "2026-09-19T00:00:00.000Z" },
      { ...known, id: "not-a-snapshot", time: "2026-09-18T00:00:00.000Z" },
    ];
    const unfiltered = await archive.execute("public", search());
    expect(unfiltered.newestSnapshotAt).toBe(TIME);
    expect(unfiltered.cost.listedSnapshots).toBe(1);
    expect(unfiltered.matches).toBe(1);
    const filtered = await archive.execute("public", search({ filter: { host: HOST } }));
    expect(filtered.newestSnapshotAt).toBe(TIME);
    expect(filtered.cost.listedSnapshots).toBe(1);
    const unknown = await archive.execute("public", search({ filter: { host: unknownHost } }));
    expect(unknown.refusal).toBeNull();
    expect(unknown.newestSnapshotAt).toBeNull();
    expect(unknown.coverage).toEqual({ eligible: 0, indexed: 0, complete: true, overBound: 0 });
    expect(unknown.cost.listedSnapshots).toBe(0);
    expect(unknown.matches).toBe(0);
  });
});

test("archived history eligibility is listing-order independent and ignores live sibling directories", async () => {
  const home = await mkdtemp(join(tmpdir(), "babel-recall-listing-"));
  const root = join(home, "foreign-codex");
  const source = '{"session_id":"synthetic","ts":1788224400,"text":"needle archived history"}\n';
  const entries: ArchivedEntry[] = [
    {
      path: join(root, "history.jsonl"),
      type: "file",
      size: Buffer.byteLength(source),
      modifiedAt: TIME,
    },
    { path: join(root, "sessions"), type: "dir", size: 0, modifiedAt: TIME },
  ];
  const policy: RecallPolicy = {
    ...POLICY,
    subjects: [{ name: "history", host: HOST, harness: "codex", sensitivity: 0 }],
  };
  const archive = await createRecallArchive({
    repo: listedRepo(entries, source),
    cacheDir: join(home, "cache"),
    policy,
    temporaryDir: home,
  });
  try {
    const first = await archive.execute("public", search());
    expect(first.coverage.eligible).toBe(1);
    expect(first.hits[0]?.locator.session).toBe("codex/state");
    entries.reverse();
    const reversed = await archive.execute("public", search());
    expect(reversed.hits).toEqual(first.hits);
    // A live sibling cannot substitute for an absent directory in this snapshot.
    await mkdir(join(root, "sessions"), { recursive: true });
    entries.splice(0, 1);
    const absent = await archive.execute("public", search());
    expect(absent.coverage.eligible).toBe(0);
    expect(absent.matches).toBe(0);
  } finally {
    await archive.close();
    await rm(home, { recursive: true, force: true });
  }
});

test(
  "held metadata does not replay a corpus larger than the former 10000-entry map",
  async () => {
    const home = await mkdtemp(join(tmpdir(), "babel-recall-metadata-"));
    const count = 10001;
    const entries: ArchivedEntry[] = Array.from({ length: count }, (_, index) => ({
      path: `/synthetic/.omp/agent/sessions/project/2026-09-01T00-00-00-000Z_${index}.jsonl`,
      type: "file",
      size: Buffer.byteLength(HEADER),
      modifiedAt: TIME,
    }));
    const repo = listedRepo(entries, HEADER);
    let archive = await createRecallArchive({
      repo,
      cacheDir: home,
      policy: POLICY,
      temporaryDir: home,
    });
    const input = search({ filter: { workspace: "/archived/work" } });
    try {
      const cold = await archive.execute("public", input);
      expect(cold.refusal).toBeNull();
      expect(cold.coverage).toEqual({
        eligible: count,
        indexed: count,
        complete: true,
        overBound: 0,
      });
      expect(cold.cost.replayedBytes).toBe(count * Buffer.byteLength(HEADER));
      const warm = await archive.execute("public", input);
      expect(warm.cost.replayedBytes).toBe(0);
      expect(warm.coverage.indexed).toBe(count);
      await archive.close();
      archive = await createRecallArchive({
        repo,
        cacheDir: home,
        policy: POLICY,
        temporaryDir: home,
      });
      const restarted = await archive.execute("public", input);
      expect(restarted.cost.fetchedBytes).toBe(0);
      expect(restarted.cost.replayedBytes).toBe(0);
      expect(restarted.coverage).toEqual(cold.coverage);
    } finally {
      await archive.close();
      await rm(home, { recursive: true, force: true });
    }
  },
  TIMEOUT,
);

withRestic(
  "owner metadata and refused labels are scanned without changing locator identity",
  async () => {
    const secret = "synthetic-super-secret-password-42";
    const ownerWorkspace = `workspace password=${secret}`;
    const ownerRepository = `repository password=${secret}`;
    const policy: RecallPolicy = {
      ...POLICY,
      subjects: [
        {
          ...POLICY.subjects[0]!,
          name: `subject password=${secret}`,
          workspace: ownerWorkspace,
          repository: ownerRepository,
        },
      ],
    };
    await fixture(
      async ({ archive, repo, cacheDir, home }) => {
        const found = await archive.execute(
          "public",
          search({ filter: { workspace: ownerWorkspace } }),
        );
        expect(found.matches).toBe(1);
        expect(found.hits[0]?.workspace).toContain("[[babel-redacted:");
        expect(found.hits[0]?.repository).toContain("[[babel-redacted:");
        expect(JSON.stringify(found)).not.toContain(secret);
        const locator = found.hits[0]?.locator;
        if (locator === undefined) throw new Error("missing locator");
        const shown = await archive.execute("public", request({ kind: "show", locator }));
        expect(shown.refusal).toBeNull();
        expect(shown.hits[0]?.locator).toEqual(locator);
        expect(JSON.stringify(shown)).not.toContain(secret);
        const preview = await archive.execute("public", request({ kind: "preview", locator }));
        const previewId = preview.preview?.previewId;
        if (previewId === undefined) throw new Error("missing preview");
        const page = await archive.execute("public", request({ kind: "session", previewId }));
        expect(page.hits[0]?.locator).toEqual(locator);
        expect(JSON.stringify(page)).not.toContain(secret);
        const restricted = await createRecallArchive({
          repo,
          cacheDir,
          temporaryDir: home,
          policy: {
            ...policy,
            subjects: policy.subjects.map((subject) => ({ ...subject, sensitivity: 3 })),
          },
        });
        try {
          const refused = await restricted.execute("public", search());
          expect(refused.refusedSubjects[0]).toContain("[[babel-redacted:");
          expect(JSON.stringify(refused)).not.toContain(secret);
          expect(refused.cost.fetchedBytes).toBe(0);
        } finally {
          await restricted.close();
        }
      },
      { policy },
    );
  },
  TIMEOUT,
);
