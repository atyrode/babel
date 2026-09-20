import { expect, test } from "bun:test";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  utimesSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { MATERIAL_SESSIONS, type Receipt } from "../contract.ts";
import type { SessionRef } from "./adapters/index.ts";
import { materialSink, type OutputSink } from "./output.ts";
import { PREFLIGHT_DETECTORS } from "./preflight.ts";
import {
  PREPARATION_SCHEMA,
  PrepareInputSchema,
  digests,
  observe,
  prepare,
  type PrepareDeps,
  type PrepareInput,
} from "./prepare.ts";
import { sessionIndex, type SessionIndex } from "./session-index.ts";

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "babel-content-selection-"));
  const cache = join(root, "cache");
  const sessions: SessionRef[] = [];
  const reads: string[] = [];
  let serial = 0;
  const output: OutputSink = { write: async () => {}, receipt: async () => {} };
  const deps: PrepareDeps = {
    discover: async () => sessions,
    observe,
    digests: async (session, sink, scan) => {
      reads.push(session.selector);
      return await digests(session, sink, scan);
    },
    cacheDir: cache,
  };
  const replace = (session: SessionRef, text: string, live = false) => {
    writeFileSync(session.primaryPath, JSON.stringify({ type: "user", text }) + "\n");
    if (!live) {
      const settled = new Date(Date.now() - 3_600_000);
      utimesSync(session.primaryPath, settled, settled);
    }
  };
  return {
    cache,
    sessions,
    reads,
    deps,
    output,
    replace,
    add: (name: string, text: string, live = false) => {
      const session: SessionRef = {
        harness: "omp",
        sourceId: name,
        selector: `omp/${name}`,
        primaryPath: join(root, `${name}.jsonl`),
      };
      replace(session, text, live);
      sessions.push(session);
      return session;
    },
    run: async (input: Partial<PrepareInput> = {}, overrides: Partial<PrepareDeps> = {}) => {
      reads.length = 0;
      const material = join(root, `material-${String(serial++)}`);
      const receipt = await prepare(
        PrepareInputSchema.parse({ machineId: "content-test", ...input }),
        output,
        { ...deps, material: materialSink(material), ...overrides },
      );
      return { receipt, material, reads: [...reads] };
    },
    drop: () => rmSync(root, { recursive: true, force: true }),
  };
}

const selected = (receipt: Receipt) =>
  receipt.material?.sessions.map((session) => session.selector) ?? [];

test("content selection seals exact bounded matches, reuses readings, and replaces changed coverage", async () => {
  const f = fixture();
  try {
    const alpha = f.add("alpha", "orchid");
    const beta = f.add("beta", "orchid");
    f.add("gamma", "granite");
    const query = { text: "orchid", limit: 1 };
    const first = await f.run({ query });
    expect(first.receipt.closure).toBe("completed");
    expect(first.reads).toEqual(["omp/alpha", "omp/beta", "omp/gamma"]);
    expect(first.receipt.retrieval).toMatchObject({
      status: "complete",
      eligible: 3,
      indexed: 3,
      reused: 0,
      matches: 2,
      overBound: 0,
    });
    expect(selected(first.receipt)).toHaveLength(1);
    const match = first.receipt.material?.sessions[0];
    if (match === undefined) throw new Error("missing selected material");
    const body = readFileSync(join(first.material, MATERIAL_SESSIONS, match.file));
    expect(body.toString()).toContain("orchid");
    expect(`sha256:${new Bun.CryptoHasher("sha256").update(body).digest("hex")}`).toBe(
      match.sourceDigest,
    );
    const ordinary = await f.run({ selectors: [match.selector] });
    expect(ordinary.receipt.preparation?.id).toBe(first.receipt.preparation?.id);
    expect(ordinary.receipt.retrieval).toBeUndefined();
    const again = await f.run({ query });
    expect(again.reads).toEqual([]);
    expect(again.receipt.preparation?.id).toBe(first.receipt.preparation?.id);
    expect(again.receipt.retrieval).toMatchObject({ indexed: 0, reused: 3 });

    f.replace(alpha, "granite, no longer a match");
    const changed = await f.run({ query });
    expect(changed.reads).toEqual([alpha.selector]);
    expect(selected(changed.receipt)).toEqual([beta.selector]);
    expect(changed.receipt.retrieval).toMatchObject({
      indexed: 1,
      reused: 2,
      matches: 1,
      overBound: 0,
    });
    const absent = await f.run({ query: { text: "absentword", limit: 24 } });
    expect(absent.receipt.closure).toBe("skipped");
    expect(absent.receipt.retrieval?.matches).toBe(0);
    expect(absent.receipt.preparation).toBeUndefined();
    expect(absent.receipt.material).toBeUndefined();
    expect(absent.reads).toEqual([]);
  } finally {
    f.drop();
  }
});

test("first-sight content indexing excludes live and own logs before reads, with only own logs opt-in", async () => {
  const f = fixture();
  try {
    const settled = f.add("settled", "orchid");
    f.add("live", "orchid", true);
    const own = f.add("run-private.babel", "orchid");
    const query = { text: "orchid", limit: 24 };
    const first = await f.run({ query });
    expect(first.reads).toEqual([settled.selector]);
    expect(selected(first.receipt)).toEqual([settled.selector]);
    expect(first.receipt.counts).toMatchObject({ live: 1, agent: 1 });
    expect(first.receipt.retrieval).toMatchObject({ eligible: 1, indexed: 1, matches: 1 });
    const opted = await f.run({ query, agentSessions: true });
    expect(opted.reads).toEqual([own.selector]);
    expect(selected(opted.receipt).sort()).toEqual([own.selector, settled.selector].sort());
    expect(opted.receipt.counts).toMatchObject({ live: 1, agent: 0 });
  } finally {
    f.drop();
  }
});

test("a source changed during indexing refuses coverage and is read afresh after settling", async () => {
  const f = fixture();
  try {
    const session = f.add("changing", "orchid");
    const query = { text: "orchid", limit: 24 };
    const changed = await f.run(
      { query },
      {
        digests: async (ref, sink, scan) => {
          const reading = await f.deps.digests(ref, sink, scan);
          f.replace(ref, "orchid has changed while indexed", true);
          return reading;
        },
      },
    );
    expect(changed.receipt.closure).toBe("failed");
    expect(changed.receipt.retrieval).toMatchObject({
      status: "unavailable",
      matches: null,
      indexed: 0,
      unavailable: 1,
    });
    expect(changed.receipt.material).toBeUndefined();
    f.replace(session, "orchid settled again with new content");
    const next = await f.run({ query });
    expect(next.receipt.closure).toBe("completed");
    expect(next.reads).toEqual([session.selector]);
    expect(next.receipt.retrieval).toMatchObject({ indexed: 1, matches: 1 });
  } finally {
    f.drop();
  }
});

test("query selection refuses disappearance while sealing rather than shrinking the scope", async () => {
  const f = fixture();
  try {
    const session = f.add("disappearing", "orchid");
    const query = { text: "orchid", limit: 24 };
    await f.run({ query });
    const material = materialSink(join(f.cache, "unsealed"));
    let removed = false;
    const result = await f.run(
      { query },
      {
        material: {
          ...material,
          session: async (file) => {
            if (!removed) {
              rmSync(session.primaryPath);
              removed = true;
            }
            return await material.session(file);
          },
        },
      },
    );
    expect(result.receipt.closure).toBe("failed");
    expect(result.receipt.retrieval).toMatchObject({ status: "unavailable", matches: 1 });
    expect(result.receipt.material).toBeUndefined();
    expect(result.receipt.preparation).toBeUndefined();
    expect(result.reads).toEqual([]);
  } finally {
    f.drop();
  }
});

test.each(["terminated", "unterminated"] as const)(
  "corrupt cached records are refused and replaced on the next query (%s)",
  async (ending) => {
    const f = fixture();
    try {
      f.add("cached", "orchid");
      await f.run();
      const kept = readdirSync(f.cache).find((name) => name.endsWith(".records"));
      if (kept === undefined) throw new Error("missing kept reading");
      const path = join(f.cache, kept);
      const suffix = ending === "terminated" ? "\n" : "";
      writeFileSync(path, "x".repeat(statSync(path).size - suffix.length) + suffix);
      const rejected = await f.run({ query: { text: "orchid", limit: 24 } });
      expect(rejected.reads).toEqual([]);
      expect(rejected.receipt.closure).toBe("failed");
      expect(rejected.receipt.retrieval).toMatchObject({ status: "unavailable", matches: null });
      expect(rejected.receipt.material).toBeUndefined();
      const next = await f.run({ query: { text: "orchid", limit: 24 } });
      expect(next.reads).toEqual(["omp/cached"]);
      expect(selected(next.receipt)).toEqual(["omp/cached"]);
    } finally {
      f.drop();
    }
  },
);

test("content queries always index redacted readings while material retains requested preflight", async () => {
  const f = fixture();
  try {
    const secret = `${"AKIA"}IOSFODNN7SYNTH01`;
    f.add("secret", `orchid ${secret}`);
    const absent = await f.run({ preflight: "off", query: { text: secret, limit: 24 } });
    expect(absent.receipt.closure).toBe("skipped");
    expect(absent.receipt.retrieval?.matches).toBe(0);
    const refused = await f.run({ preflight: "refuse", query: { text: "orchid", limit: 24 } });
    expect(refused.receipt.closure).toBe("failed");
    expect(refused.receipt.preflight?.redactions).toBe(1);
    expect(refused.receipt.material).toBeUndefined();
    const raw = await f.run({ preflight: "off", query: { text: "orchid", limit: 24 } });
    expect(raw.receipt.closure).toBe("completed");
    const entry = raw.receipt.material?.sessions[0];
    if (entry === undefined) throw new Error("missing raw material");
    expect(readFileSync(join(raw.material, MATERIAL_SESSIONS, entry.file), "utf8")).toContain(
      secret,
    );
    const redacted = await f.run({ query: { text: "orchid", limit: 24 } });
    expect(redacted.reads).toEqual(["omp/secret"]);
    expect(redacted.receipt.preflight?.redactions).toBe(1);
    expect(redacted.receipt.preparation?.id).not.toBe(raw.receipt.preparation?.id);
  } finally {
    f.drop();
  }
});

test("content selection requires managed storage and refuses mixed selectors without reading", async () => {
  const f = fixture();
  try {
    f.add("ordinary", "orchid");
    const query = { text: "orchid", limit: 24 };
    const missing = await f.run({ query }, { cacheDir: "" });
    expect(missing.receipt.closure).toBe("failed");
    expect(missing.receipt.retrieval).toMatchObject({ status: "unavailable", matches: null });
    expect(missing.reads).toEqual([]);
    const mixed = await f.run({ query, selectors: ["ordinary"] });
    expect(mixed.receipt.closure).toBe("failed");
    expect(mixed.receipt.retrieval?.matches).toBeNull();
    expect(mixed.reads).toEqual([]);
    const ordinary = await f.run({ selectors: ["ordinary"] }, { cacheDir: "" });
    expect(selected(ordinary.receipt)).toEqual(["omp/ordinary"]);
    expect(ordinary.receipt.retrieval).toBeUndefined();
  } finally {
    f.drop();
  }
});

test("a busy content builder refuses a query but cannot block ordinary selector preparation", async () => {
  const f = fixture();
  let unlock: (() => void) | undefined;
  let held: Promise<unknown> | undefined;
  let index: SessionIndex | undefined;
  try {
    const session = f.add("contended", "orchid");
    mkdirSync(f.cache, { recursive: true });
    index = await sessionIndex(f.cache, {
      schema: PREPARATION_SCHEMA,
      detectors: PREFLIGHT_DETECTORS,
      mode: "redact",
    });
    const entered = Promise.withResolvers<void>();
    const gate = Promise.withResolvers<void>();
    unlock = () => gate.resolve();
    held = index
      .build({ session, seen: await observe(session) }, async () => {
        entered.resolve();
        await gate.promise;
        throw new Error("release test builder");
      })
      .catch(() => undefined);
    await entered.promise;
    const busy = await f.run({ query: { text: "orchid", limit: 24 } });
    expect(busy.receipt.closure).toBe("failed");
    expect(busy.receipt.retrieval).toMatchObject({ status: "busy", matches: null });
    expect(busy.reads).toEqual([]);
    const ordinary = await f.run({ selectors: [session.selector] });
    expect(ordinary.receipt.closure).toBe("completed");
    expect(selected(ordinary.receipt)).toEqual([session.selector]);
  } finally {
    unlock?.();
    await held;
    index?.close();
    f.drop();
  }
});
