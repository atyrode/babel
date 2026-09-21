import { expect, test } from "bun:test";
import {
  RECALL_MAX_REQUESTS,
  RECALL_MAX_SERVED_BYTES,
  RECALL_REQUEST_TTL_MS,
  RecallReplySchema,
  type RecallPolicy,
  type RecallReply,
  type RecallResult,
} from "../contract.ts";
import { type RecallArchive } from "./recall-archive.ts";
import { openRecallService } from "./recall-service.ts";

const POLICY: RecallPolicy = {
  version: 1,
  classes: [
    { id: "public", label: "Public", ceiling: 0 },
    { id: "private", label: "Private", ceiling: 3 },
  ],
  subjects: [{ name: "Synthetic sessions", host: "synthetic-host", sensitivity: 0 }],
};
const BEARER = "synthetic_recall_runtime_bearer_123456789";
const SEARCH = { kind: "search", query: "needle" };
const POLL = { kind: "poll" };
const id = (n: number): string => `00000000-0000-4000-8000-${n.toString(16).padStart(12, "0")}`;
const frame = (requestId: string, request: unknown): string =>
  JSON.stringify({
    request: JSON.stringify({ requestId, request }),
  });
function result(matches = 0): RecallResult {
  return {
    operation: "search",
    observedAt: "2026-09-21T00:00:00.000Z",
    newestSnapshotAt: null,
    previewByteLimit: Math.floor(RECALL_MAX_SERVED_BYTES / POLICY.classes.length),
    cost: {
      fetchedFiles: 0,
      fetchedBytes: 0,
      cacheHits: 0,
      indexedFiles: 0,
      listedSnapshots: 0,
      listedEntries: 0,
      replayedBytes: 0,
    },
    coverage: { eligible: 0, indexed: 0, complete: true, overBound: 0 },
    matches,
    omitted: 0,
    omittedSubjects: 0,
    refusedSubjects: [],
    refusal: null,
    hits: [],
  };
}

interface Gate {
  promise: Promise<void>;
  resolve(): void;
}
interface Harness {
  archive: RecallArchive;
  clock: { now: number };
  gate(): Gate;
  raw(body: BodyInit, path?: string, init?: RequestInit): Promise<Response>;
  send(classId: string, requestId: string, request: unknown): Promise<Response>;
  reply(classId: string, requestId: string, request: unknown): Promise<RecallReply>;
  terminal(classId: string, requestId: string): Promise<RecallReply>;
  stop(): Promise<void>;
}
async function fixture(body: (h: Harness) => Promise<void>): Promise<void> {
  const gates: Gate[] = [];
  const clock = { now: Date.parse("2026-09-21T00:00:00.000Z") };
  const archive: RecallArchive = { execute: async () => result(), close: async () => {} };
  const service = openRecallService({
    archive,
    policy: POLICY,
    bearer: BEARER,
    now: () => clock.now,
  });
  const raw: Harness["raw"] = (body, path = "/recall/public", init = {}) =>
    fetch(`http://127.0.0.1:${service.port}${path}`, {
      method: "POST",
      headers: { authorization: `Bearer ${BEARER}` },
      body,
      signal: AbortSignal.timeout(2_000),
      ...init,
    });
  const send: Harness["send"] = (classId, requestId, request) =>
    raw(frame(requestId, request), `/recall/${classId}`);
  const reply: Harness["reply"] = async (classId, requestId, request) => {
    const response = await send(classId, requestId, request);
    expect(response.status).toBe(200);
    return RecallReplySchema.parse(await response.json());
  };
  try {
    await body({
      archive,
      clock,
      raw,
      send,
      reply,
      stop: service.stop,
      gate() {
        const gate = Promise.withResolvers<void>();
        gates.push(gate);
        return gate;
      },
      async terminal(classId, requestId) {
        for (let attempt = 0; attempt < 32; attempt++) {
          const current = await reply(classId, requestId, POLL);
          if (current.state !== "pending") return current;
        }
        throw new Error("Recall did not reach a terminal state within 32 polls.");
      },
    });
  } finally {
    for (const gate of gates) gate.resolve();
    await service.stop();
  }
}

test("HTTP admission requires the bearer, a declared route and strict caller-independent request bodies", async () => {
  await fixture(async (h) => {
    let executed = 0;
    h.archive.execute = async () => {
      executed++;
      return result();
    };
    const body = frame(id(1), SEARCH);
    for (const authorization of ["", `Bearer ${"x".repeat(BEARER.length)}`]) {
      const denied = await h.raw(body, undefined, { headers: { authorization } });
      expect(denied.status).toBe(401);
      expect(await denied.text()).toBe("");
    }
    expect((await h.raw(body, "/recall/undeclared")).status).toBe(404);
    expect((await h.raw(body, undefined, { method: "GET", body: null })).status).toBe(404);
    const malformed = [
      "{",
      new Uint8Array([0xff]),
      JSON.stringify({ request: "{" }),
      JSON.stringify({
        request: JSON.stringify({ requestId: id(1), request: SEARCH }),
        clearance: 3,
      }),
      JSON.stringify({
        request: JSON.stringify({ requestId: id(1), request: SEARCH, classId: "private" }),
      }),
      frame(id(1), { ...SEARCH, clearance: 3 }),
      frame("not-a-uuid", SEARCH),
    ];
    for (const invalid of malformed) {
      const denied = await h.raw(invalid);
      expect(denied.status).toBe(400);
      expect(await denied.text()).toBe("");
    }
    expect(executed).toBe(0);
    await h.reply("public", id(1), SEARCH);
    expect((await h.terminal("public", id(1))).state).toBe("complete");
    expect(executed).toBe(1);
  });
});

test("UUID replay is idempotent within a class, conflicts cannot replace it, and other classes cannot poll it", async () => {
  await fixture(async (h) => {
    const entered = h.gate();
    const release = h.gate();
    const executions: string[] = [];
    h.archive.execute = async (classId) => {
      executions.push(classId);
      if (classId === "public") {
        entered.resolve();
        await release.promise;
      }
      return result(classId === "public" ? 1 : 2);
    };
    const pending: RecallReply = { requestId: id(1), state: "pending" };
    expect(await h.reply("public", id(1), SEARCH)).toEqual(pending);
    await entered.promise;
    expect(await h.reply("public", id(1), SEARCH)).toEqual(pending);
    expect((await h.send("public", id(1), { ...SEARCH, query: "different" })).status).toBe(409);
    expect(await h.reply("public", id(1), POLL)).toEqual(pending);
    expect(await h.reply("private", id(1), POLL)).toEqual({ requestId: id(1), state: "expired" });
    expect(await h.reply("private", id(1), { ...SEARCH, query: "private" })).toEqual(pending);
    release.resolve();
    const completed = await h.terminal("public", id(1));
    expect(completed).toEqual({ requestId: id(1), state: "complete", result: result(1) });
    expect(await h.reply("public", id(1), SEARCH)).toEqual(completed);
    expect((await h.send("public", id(1), { ...SEARCH, query: "different" })).status).toBe(409);
    expect(await h.terminal("private", id(1))).toEqual({
      requestId: id(1),
      state: "complete",
      result: result(2),
    });
    expect(executions).toEqual(["public", "private"]);
  });
});

test("one class filling its pending quota cannot block admission to another class", async () => {
  await fixture(async (h) => {
    const entered = h.gate();
    const release = h.gate();
    h.archive.execute = async () => {
      entered.resolve();
      await release.promise;
      return result();
    };
    const quota = Math.floor(RECALL_MAX_REQUESTS / POLICY.classes.length);
    expect((await h.reply("public", id(1), SEARCH)).state).toBe("pending");
    await entered.promise;
    for (let n = 2; n <= quota; n++) {
      expect((await h.reply("public", id(n), SEARCH)).state).toBe("pending");
    }
    expect(await h.reply("public", id(quota + 1), SEARCH)).toEqual({
      requestId: id(quota + 1),
      state: "busy",
    });
    expect((await h.reply("public", id(1), SEARCH)).state).toBe("pending");
    expect((await h.reply("private", id(1), SEARCH)).state).toBe("pending");
    release.resolve();
    expect((await h.terminal("private", id(1))).state).toBe("complete");
    await h.reply("public", id(quota + 1), SEARCH);
    expect((await h.terminal("public", id(quota + 1))).state).toBe("complete");
  });
});

test("queued classes rotate fairly while preserving class FIFO and serial archive execution", async () => {
  await fixture(async (h) => {
    const expected = [
      { classId: "public", query: "A1" },
      { classId: "private", query: "B1" },
      { classId: "public", query: "A2" },
      { classId: "private", query: "B2" },
      { classId: "public", query: "A3" },
      { classId: "public", query: "A4" },
    ];
    const entered = expected.map(() => h.gate());
    const release = expected.map(() => h.gate());
    const executions: typeof expected = [];
    h.archive.execute = async (classId, request) => {
      if (request.kind !== "search") throw new Error("Expected a search request.");
      const index = executions.length;
      executions.push({ classId, query: request.query });
      entered[index]!.resolve();
      await release[index]!.promise;
      return result();
    };
    expect((await h.reply("public", id(1), { ...SEARCH, query: "A1" })).state).toBe("pending");
    await entered[0]!.promise;
    for (let n = 2; n <= 4; n++) {
      expect((await h.reply("public", id(n), { ...SEARCH, query: `A${n}` })).state).toBe("pending");
    }
    for (let n = 1; n <= 2; n++) {
      expect((await h.reply("private", id(n), { ...SEARCH, query: `B${n}` })).state).toBe(
        "pending",
      );
    }
    // Later classes cannot preempt active work or execute concurrently with it.
    expect(executions).toEqual(expected.slice(0, 1));
    for (let index = 1; index < expected.length; index++) {
      release[index - 1]!.resolve();
      await entered[index]!.promise;
      expect(executions).toEqual(expected.slice(0, index + 1));
    }
    release[expected.length - 1]!.resolve();
    expect((await h.terminal("public", id(4))).state).toBe("complete");
  });
});

test("completed response eviction permits more than 128 sequential requests without advancing the clock", async () => {
  await fixture(async (h) => {
    let executions = 0;
    h.archive.execute = async () => result(++executions);
    await h.reply("private", id(1), SEARCH);
    expect((await h.terminal("private", id(1))).state).toBe("complete");
    for (let n = 1; n <= RECALL_MAX_REQUESTS + 1; n++) {
      const accepted = await h.reply("public", id(n), SEARCH);
      expect(["pending", "complete"]).toContain(accepted.state);
      expect((await h.terminal("public", id(n))).state).toBe("complete");
    }
    expect(await h.reply("public", id(1), POLL)).toEqual({ requestId: id(1), state: "expired" });
    expect((await h.reply("public", id(2), POLL)).state).toBe("complete");
    expect((await h.reply("private", id(1), POLL)).state).toBe("complete");
    expect(executions).toBe(RECALL_MAX_REQUESTS + 2);
    await h.reply("public", id(1), SEARCH);
    expect((await h.terminal("public", id(1))).state).toBe("complete");
    expect(executions).toBe(RECALL_MAX_REQUESTS + 3);
  });
}, 15_000);

test("failures stay opaque and terminal TTL starts at completion, while active and queued work never expires", async () => {
  await fixture(async (h) => {
    const entered = h.gate();
    const release = h.gate();
    let executions = 0;
    h.archive.execute = async () => {
      executions++;
      if (executions === 1)
        throw new Error("synthetic secret: restic password and archived content");
      entered.resolve();
      await release.promise;
      return result();
    };
    await h.reply("public", id(1), SEARCH);
    const failed: RecallReply = { requestId: id(1), state: "failed" };
    expect(await h.terminal("public", id(1))).toEqual(failed);
    expect(await h.reply("public", id(1), SEARCH)).toEqual(failed);
    expect(executions).toBe(1);
    expect((await h.reply("public", id(2), SEARCH)).state).toBe("pending");
    await entered.promise;
    expect((await h.reply("public", id(3), SEARCH)).state).toBe("pending");
    h.clock.now += RECALL_REQUEST_TTL_MS * 2;
    expect((await h.reply("public", id(1), POLL)).state).toBe("expired");
    for (const requestId of [id(2), id(3)]) {
      expect(await h.reply("public", requestId, POLL)).toEqual({ requestId, state: "pending" });
      expect((await h.reply("public", requestId, SEARCH)).state).toBe("pending");
    }
    release.resolve();
    expect((await h.terminal("public", id(3))).state).toBe("complete");
    h.clock.now += RECALL_REQUEST_TTL_MS - 1;
    expect((await h.reply("public", id(2), POLL)).state).toBe("complete");
    h.clock.now++;
    expect((await h.reply("public", id(2), POLL)).state).toBe("expired");
    expect((await h.reply("public", id(3), POLL)).state).toBe("expired");
    expect(executions).toBe(3);
  });
});

test("stop closes admission, waits for active work and drops queued work before closing the archive", async () => {
  await fixture(async (h) => {
    const entered = h.gate();
    const release = h.gate();
    const events: string[] = [];
    h.archive.execute = async () => {
      events.push("execute");
      entered.resolve();
      await release.promise;
      events.push("finished");
      return result();
    };
    h.archive.close = async () => {
      events.push("close");
    };
    expect((await h.reply("public", id(1), SEARCH)).state).toBe("pending");
    await entered.promise;
    expect((await h.reply("private", id(2), SEARCH)).state).toBe("pending");
    expect((await h.reply("public", id(3), SEARCH)).state).toBe("pending");
    let stopped = false;
    const stopping = h.stop().then(() => {
      stopped = true;
    });
    // An actual rejected connection observes shutdown without an ordering sleep.
    await expect(h.send("public", id(4), SEARCH)).rejects.toThrow();
    expect(stopped).toBe(false);
    expect(events).toEqual(["execute"]);
    release.resolve();
    await stopping;
    await h.stop();
    expect(events).toEqual(["execute", "finished", "close"]);
  });
});
