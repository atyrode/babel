import { createHash } from "node:crypto";
import { z } from "zod";
import {
  PREFLIGHT_SCHEMA,
  RECALL_MAX_RESULT_BYTES,
  RECALL_REQUEST_TTL_MS,
  TRANSCRIPT_MAP_SERVICE_FILE,
  TRANSCRIPT_MAP_OUTPUT_FILE,
  TRANSCRIPT_MAP_JOB_PAGE_NODES,
  TRANSCRIPT_MAP_NATIVE_PAGE_NODES,
  TRANSCRIPT_MAP_MAX_PAGE_BYTES,
  TranscriptMapCatalogInputSchema,
  TranscriptMapPrepareInputSchema,
  TranscriptMapNativeReplySchema,
  ReceiptSchema,
  type TranscriptMapNativeRequest,
  type TranscriptMapNativeResult,
  type TranscriptMapCatalogInput,
  type TranscriptMapPrepareInput,
  type Receipt,
} from "../contract.ts";
import type { MaterialSink, OutputSink } from "./output.ts";
import { PREFLIGHT_DETECTORS, secretScan } from "./preflight.ts";

export type TranscriptMapClient = (
  request: TranscriptMapNativeRequest,
) => Promise<TranscriptMapNativeResult>;
function fail(): never {
  throw new Error("Mapping native material is unavailable or no longer authorized.");
}
const sha = (text: string): string => `sha256:${createHash("sha256").update(text).digest("hex")}`;

/** The engine materializes this job-only proxy binding. No restic/provider credentials,
 * arbitrary endpoints or owner classifications are accepted in job inputs. */
export async function openTranscriptMapClient(): Promise<TranscriptMapClient> {
  let endpoint: { url: string; bearer: string };
  try {
    const raw = await Bun.file(`/inputs/${TRANSCRIPT_MAP_SERVICE_FILE}`).slice(0, 8193).text();
    if (Buffer.byteLength(raw) > 8192) fail();
    endpoint = z
      .strictObject({ url: z.url(), bearer: z.string().min(1).max(512) })
      .parse(JSON.parse(raw));
    const url = new URL(endpoint.url);
    if (
      url.protocol !== "http:" ||
      !["127.0.0.1", "localhost", "[::1]"].includes(url.hostname) ||
      url.username ||
      url.password ||
      url.search ||
      url.hash ||
      url.pathname !== "/"
    )
      fail();
  } catch {
    return fail();
  }
  return async (request) => {
    const requestId = crypto.randomUUID();
    const deadline = Date.now() + Math.min(RECALL_REQUEST_TTL_MS, 120_000);
    let poll = false;
    try {
      while (Date.now() < deadline) {
        const response = await fetch(`${endpoint.url.replace(/\/$/, "")}/mapping`, {
          method: "POST",
          redirect: "error",
          signal: AbortSignal.timeout(Math.min(30_000, deadline - Date.now())),
          headers: {
            authorization: `Bearer ${endpoint.bearer}`,
            "content-type": "application/json",
          },
          body: JSON.stringify({
            request: JSON.stringify({ requestId, request: poll ? { kind: "poll" } : request }),
          }),
        });
        if (!response.ok || !response.body) fail();
        const chunks: Uint8Array[] = [];
        let bytes = 0;
        for await (const chunk of response.body!) {
          bytes += chunk.length;
          if (bytes > RECALL_MAX_RESULT_BYTES) fail();
          chunks.push(chunk);
        }
        const reply = TranscriptMapNativeReplySchema.parse(
          JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks))),
        );
        if (reply.requestId !== requestId) fail();
        if (reply.state === "complete") {
          if (!reply.result || reply.result.operation !== request.kind || reply.result.refusal)
            fail();
          return reply.result;
        }
        if (reply.state !== "pending") fail();
        poll = true;
        await Bun.sleep(100);
      }
    } catch {
      return fail();
    }
    return fail();
  };
}

export async function mapCatalog(
  input: TranscriptMapCatalogInput,
  out: OutputSink,
  client: TranscriptMapClient,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  try {
    const parsed = TranscriptMapCatalogInputSchema.parse(input);
    const result = await client(parsed.request);
    if (!result.context) fail();
    const plan = result.plan;
    if (parsed.request.kind === "map-plan") {
      if (!plan || plan.offset !== parsed.request.offset) fail();
      while (plan!.nextOffset !== null && plan!.nodes.length < TRANSCRIPT_MAP_JOB_PAGE_NODES) {
        const next = await client({
          ...parsed.request,
          offset: plan!.nextOffset!,
          maxNodes: Math.min(
            TRANSCRIPT_MAP_NATIVE_PAGE_NODES,
            TRANSCRIPT_MAP_JOB_PAGE_NODES - plan!.nodes.length,
          ),
        });
        if (
          next.context?.digest !== result.context!.digest ||
          !next.plan ||
          JSON.stringify(next.plan.header) !== JSON.stringify(plan!.header) ||
          next.plan.offset !== plan!.nextOffset ||
          next.plan.nodes.length === 0 ||
          (next.plan.nextOffset !== null &&
            next.plan.nextOffset !== next.plan.offset + next.plan.nodes.length)
        )
          fail();
        plan!.nodes.push(...next.plan!.nodes);
        plan!.nextOffset = next.plan!.nextOffset;
      }
    }
    const receipt = ReceiptSchema.parse({
      runId: parsed.runId,
      machineId: parsed.machineId,
      kind: "mapCatalog",
      startedAt,
      finishedAt: new Date().toISOString(),
      closure: "completed",
      counts: { captures: result.entries.length, nodes: plan?.nodes.length ?? 0 },
      mapping: {
        kind: "catalog",
        context: result.context,
        entries: result.entries,
        nextCursor: result.nextCursor,
        ...(result.accesses[0] ? { access: result.accesses[0] } : {}),
        ...(plan ? { plan } : {}),
      },
    });
    await out.receipt(receipt);
    return receipt;
  } catch {
    return fail();
  }
}

/** The exact cached native node, not a caller-authored node or a guessed manifest offset. */
async function validateNode(input: TranscriptMapPrepareInput, client: TranscriptMapClient) {
  const found = await client({
    kind: "map-node",
    source: input.source,
    segmentation: input.segmentation,
    nodeId: input.nodeId,
  });
  const node = found.plan?.nodes[0];
  const access = found.accesses[0];
  if (
    !node ||
    !found.context ||
    found.context.policyDigest !== input.expectedPolicyDigest ||
    JSON.stringify(found.plan?.header.source) !== JSON.stringify(input.source) ||
    node.id !== input.nodeId ||
    !access ||
    access.captureId !== input.source.id ||
    access.contextDigest !== found.context.digest ||
    access.sensitivity > found.context.ceiling
  )
    fail();
  return { context: found.context!, access: access!, node: node! };
}

export async function mapPrepare(
  input: TranscriptMapPrepareInput,
  out: OutputSink,
  material: MaterialSink,
  client: TranscriptMapClient,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  try {
    const parsed = TranscriptMapPrepareInputSchema.parse(input);
    const { node, ...authorized } = await validateNode(parsed, client);
    if (
      node.gap ||
      (parsed.mode !== "generate" && !parsed.baseSummary) ||
      (parsed.mode === "generate" && parsed.baseSummary)
    )
      fail();
    if (
      parsed.children.length !== node.children.length ||
      parsed.children.some((child) =>
        child.gap === null
          ? child.summaryId === null || child.text === null
          : child.summaryId !== null || child.text !== null,
      )
    )
      fail();
    const scan = secretScan();
    let line = 0;
    const clean = (text: string): string => {
      const encoded = JSON.stringify(text);
      const redacted = scan.redact(encoded, ++line);
      // Existing immutable prose must not silently acquire different model-input identity.
      if (redacted !== encoded) fail();
      return text;
    };
    const children = parsed.children.map((child, index) => ({
      nodeId: node.children[index]!,
      ...child,
      text: child.text === null ? null : clean(child.text),
    }));
    const baseSummary = parsed.baseSummary
      ? { ...parsed.baseSummary, text: clean(parsed.baseSummary.text) }
      : null;
    const feedback = parsed.feedback === undefined ? null : clean(parsed.feedback);
    let text: string | null = null;
    if (node.children.length === 0) {
      const preview = await client({ kind: "map-preview", source: parsed.source, span: node.span });
      if (
        preview.context?.digest !== authorized.context.digest ||
        !preview.preview ||
        preview.preview.bytes !== node.span.byteLength ||
        preview.preview.sourceDigest !== parsed.source.sourceDigest ||
        preview.preview.spanDigest !== node.span.digest
      )
        fail();
      const handle = preview.preview!;
      try {
        const parts: string[] = [];
        let offset = 0;
        while (offset < handle.bytes) {
          const response = await client({
            kind: "map-page",
            previewId: handle.previewId,
            offset,
            maxBytes: TRANSCRIPT_MAP_MAX_PAGE_BYTES,
          });
          const page = response.page;
          if (
            !page ||
            page.offset !== offset ||
            page.totalBytes !== handle.bytes ||
            page.nextOffset !== offset + Buffer.byteLength(page.text) ||
            page.nextOffset <= offset ||
            page.nextOffset > handle.bytes ||
            page.complete !== (page.nextOffset === handle.bytes)
          )
            fail();
          parts.push(page!.text);
          offset = page!.nextOffset;
        }
        text = parts.join("");
        if (sha(text) !== node.span.digest) fail();
        for (const record of text.split("\n"))
          if (record) {
            if (scan.redact(record, ++line) !== record) fail();
          }
      } finally {
        await client({ kind: "map-release", previewId: handle.previewId }).catch(() => undefined);
      }
    }
    // Parents see only the ordered child summaries and explicit gaps, never an ancestor's
    // full raw span. Span/source metadata is navigation identity, not supplied source bytes.
    const document = JSON.stringify({
      inference: true,
      source: parsed.source,
      node,
      mode: parsed.mode,
      text,
      children,
      baseSummary,
      feedback,
    });
    const inputDigest = sha(document);
    const report = scan.report();
    const receipt = ReceiptSchema.parse({
      runId: parsed.runId,
      machineId: parsed.machineId,
      kind: "mapPrepare",
      startedAt,
      finishedAt: new Date().toISOString(),
      closure: "completed",
      counts: {
        suppliedBytes: text === null ? 0 : Buffer.byteLength(text),
        children: children.length,
      },
      mapping: {
        kind: "material",
        ...authorized,
        source: parsed.source,
        node,
        mode: parsed.mode,
        inputDigest,
      },
      preflight: {
        schema: PREFLIGHT_SCHEMA,
        detectors: PREFLIGHT_DETECTORS,
        mode: "redact",
        records: report.records,
        redactions: report.redactions,
        classes: report.classes,
        sites: [],
        sitesOmitted: report.sitesOmitted,
      },
    });
    await material.document(TRANSCRIPT_MAP_OUTPUT_FILE, document);
    await out.receipt(receipt);
    return receipt;
  } catch {
    return fail();
  }
}
