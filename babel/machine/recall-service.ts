import { timingSafeEqual } from "node:crypto";
import { openWorkerContext } from "@manifold/sdk/worker";
import {
  RECALL_ENV,
  RECALL_MAX_REQUEST_BODY_BYTES,
  RECALL_MAX_REQUESTS,
  RECALL_MAX_RESULT_BYTES,
  RECALL_REQUEST_TTL_MS,
  RESTIC_CREDENTIAL_FILE,
  ArchiveServiceReplySchema,
  ArchiveServiceRequestSchema,
  TranscriptMapNativeRequestSchema,
  RecallRuntimeInputSchema,
  RecallServiceBodySchema,
  type ArchiveServiceReply,
  type ArchiveServiceRequest,
  type RecallPolicy,
  type RecallReply,
  type RecallRequest,
} from "../contract.ts";
import { createRecallArchive, type RecallArchive } from "./recall-archive.ts";
import { openRepo, resticConfig } from "./restic.ts";

interface Pending {
  readonly classId: string;
  readonly request: Exclude<ArchiveServiceRequest["request"], { kind: "poll" }>;
  readonly privileged: boolean;
  readonly digest: string;
  reply: ArchiveServiceReply;
  expiresAt: number;
}

/** The native owner supplies the bearer and fixes each class's route. Request JSON has neither. */
export function openRecallService(options: {
  archive: RecallArchive;
  policy: RecallPolicy;
  bearer: string;
  now?: () => number;
}): { port: number; stop(): Promise<void> } {
  if (!/^[A-Za-z0-9_-]{32,128}$/.test(options.bearer))
    throw new Error("Recall service binding is unavailable.");
  const authorization = Buffer.from(`Bearer ${options.bearer}`);
  const routes = new Map<string, { classId: string; privileged: boolean; maps: boolean }>(options.policy.classes.flatMap(({ id }) => [
    [`/recall/${id}`, { classId: id, privileged: false, maps: false }],
    [`/maps/${id}`, { classId: id, privileged: false, maps: true }],
  ] as const));
  if (options.policy.mappingClassId)
    routes.set("/mapping", { classId: options.policy.mappingClassId, privileged: true, maps: true });
  const classQueueLimit = Math.max(1, Math.floor(RECALL_MAX_REQUESTS / options.policy.classes.length));
  const now = options.now ?? Date.now;
  const pending = new Map<string, Pending>();
  const queues = new Map<string, Pending[]>(options.policy.classes.map(({ id }) => [id, []]));
  const classQueues = [...queues.values()];
  let nextClass = 0;
  let stopped = false;
  let pumping: Promise<void> | undefined;
  let stopping: Promise<void> | undefined;
  const pump = (): void => {
    if (pumping !== undefined) return;
    pumping = (async () => {
      while (!stopped) {
        let entry: Pending | undefined;
        // Rotate after every operation, including when work arrives while another class is active.
        for (let scanned = 0; scanned < classQueues.length; scanned++) {
          const queue = classQueues[nextClass]!;
          nextClass = (nextClass + 1) % classQueues.length;
          entry = queue.shift();
          if (entry !== undefined) break;
        }
        if (entry === undefined) break;
        try {
          const mapped = TranscriptMapNativeRequestSchema.safeParse(entry.request);
          const result = mapped.success
            ? await options.archive.executeMap(entry.classId, mapped.data, entry.privileged)
            : await options.archive.execute(entry.classId, entry.request as RecallRequest);
          const reply = ArchiveServiceReplySchema.parse({ ...entry.reply, state: "complete", result });
          if (Buffer.byteLength(JSON.stringify(reply)) > RECALL_MAX_RESULT_BYTES)
            throw new Error("Recall response exceeds its bound.");
          entry.reply = reply;
        } catch {
          // Never put parser errors, archived values or restic diagnostics into the transport.
          entry.reply = { requestId: entry.reply.requestId, state: "failed" };
        }
        entry.expiresAt = now() + RECALL_REQUEST_TTL_MS;
      }
    })().finally(() => {
      pumping = undefined;
      if (!stopped && classQueues.some((queue) => queue.length !== 0)) pump();
    });
  };
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    maxRequestBodySize: RECALL_MAX_REQUEST_BODY_BYTES,
    idleTimeout: 30,
    error: () => new Response(null, { status: 500 }),
    async fetch(request) {
      if (stopped) return new Response(null, { status: 503 });
      const supplied = Buffer.from(request.headers.get("authorization") ?? "");
      if (supplied.length !== authorization.length || !timingSafeEqual(supplied, authorization))
        return new Response(null, { status: 401 });
      const route = routes.get(new URL(request.url).pathname);
      if (route === undefined || request.method !== "POST")
        return new Response(null, { status: 404 });
      const { classId, privileged, maps } = route;
      let frame: ArchiveServiceRequest;
      try {
        const bytes = await request.arrayBuffer();
        if (bytes.byteLength > RECALL_MAX_REQUEST_BODY_BYTES)
          return new Response(null, { status: 413 });
        const body = RecallServiceBodySchema.parse(
          JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)),
        );
        frame = ArchiveServiceRequestSchema.parse(JSON.parse(body.request));
      } catch {
        return new Response(null, { status: 400 });
      }
      if (frame.request.kind !== "poll") {
        const mapping = TranscriptMapNativeRequestSchema.safeParse(frame.request);
        if (maps !== mapping.success) return new Response(null, { status: 403 });
        if (mapping.success && !privileged && !["map-context", "map-inventory", "map-authorize", "map-span"].includes(mapping.data.kind))
          return new Response(null, { status: 403 });
      }
      if (stopped) return new Response(null, { status: 503 });
      const at = now();
      for (const [key, entry] of pending) {
        if (entry.reply.state !== "pending" && entry.expiresAt <= at) pending.delete(key);
      }
      const key = `${privileged ? "mapping" : maps ? "maps" : "recall"}/${classId}/${frame.requestId}`;
      const held = pending.get(key);
      if (frame.request.kind === "poll")
        return Response.json(
          held?.reply ??
            ({
              requestId: frame.requestId,
              state: "expired",
            } satisfies RecallReply),
        );
      const digest = new Bun.CryptoHasher("sha256")
        .update(JSON.stringify(frame.request))
        .digest("hex");
      if (held !== undefined) {
        if (held.digest !== digest) return new Response(null, { status: 409 });
        return Response.json(held.reply);
      }
      let active = 0;
      let retained = 0;
      let oldestTerminal: string | undefined;
      for (const [retainedKey, entry] of pending) {
        if (entry.classId !== classId) continue;
        retained++;
        if (entry.reply.state === "pending") active++;
        else oldestTerminal ??= retainedKey;
      }
      if (active >= classQueueLimit)
        return Response.json({ requestId: frame.requestId, state: "busy" } satisfies RecallReply);
      // Completed responses are a per-class bounded replay cache, not an hourly admission quota.
      // Eviction and the TTL both make later polls expire; pending work is never evicted.
      if (retained >= RECALL_MAX_REQUESTS && oldestTerminal !== undefined)
        pending.delete(oldestTerminal);
      const entry: Pending = {
        classId,
        privileged,
        request: frame.request,
        digest,
        reply: { requestId: frame.requestId, state: "pending" },
        expiresAt: at + RECALL_REQUEST_TTL_MS,
      };
      pending.set(key, entry);
      queues.get(classId)!.push(entry);
      pump();
      return Response.json(entry.reply);
    },
  });
  return {
    port: server.port!,
    stop() {
      stopping ??= (async () => {
        stopped = true;
        for (const queue of classQueues) queue.length = 0;
        await server.stop(true);
        await pumping;
        pending.clear();
        await options.archive.close();
      })();
      return stopping;
    },
  };
}

/** Only a native service launch owns the SDK channel and generated service-bearer file. */
export async function runRecallService(raw: unknown): Promise<void> {
  const controller = new AbortController();
  const stop = (): void => controller.abort();
  process.once("SIGTERM", stop);
  process.once("SIGINT", stop);
  let context: ReturnType<typeof openWorkerContext> | undefined;
  let archive: RecallArchive | undefined;
  let service: ReturnType<typeof openRecallService> | undefined;
  try {
    const { policy } = RecallRuntimeInputSchema.parse(raw);
    const cacheDir = process.env[RECALL_ENV.cacheDir] ?? "";
    const bearerFile = process.env[RECALL_ENV.serviceBearerFile] ?? "";
    if (!cacheDir.startsWith("/") || !bearerFile.startsWith("/"))
      throw new Error("Recall runtime bindings are unavailable.");
    context = openWorkerContext({ signal: controller.signal });
    await context.ready;
    const config = await resticConfig({ credentialFile: RESTIC_CREDENTIAL_FILE, env: process.env });
    archive = await createRecallArchive({
      repo: openRepo(config),
      cacheDir,
      temporaryDir: "/tmp",
      policy,
    });
    const bearer: unknown = await Bun.file(bearerFile).json();
    if (typeof bearer !== "string") throw new Error("Recall service binding is unavailable.");
    service = openRecallService({ archive, policy, bearer });
    await context.announceServiceReady(service.port);
    const signal = AbortSignal.any([controller.signal, context.signal]);
    if (!signal.aborted)
      await new Promise<void>((resolve) =>
        signal.addEventListener("abort", () => resolve(), { once: true }),
      );
  } catch {
    throw new Error("Recall service could not start or lost its native owner.");
  } finally {
    process.off("SIGTERM", stop);
    process.off("SIGINT", stop);
    if (service !== undefined) await service.stop();
    else await archive?.close();
    context?.close();
  }
}
