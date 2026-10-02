import { createHash } from "node:crypto";
import type { GuestCtx, GuestServices } from "@manifold/plugin-kit/server";
import {
  JEV_SERVICE_ID,
  RecordKindSchema,
  REVIEW_READINGS_HELD,
  REVIEW_READINGS_TTL_MS,
  reviewReadingProviderRevision,
  type ReviewReading,
  type ReviewReadingPublication,
  type ReviewSelection,
} from "../contract.ts";
import type { BabelStore } from "../store/store.ts";
import { readExcludedRecordIds } from "../store/source-privacy.ts";

export type ReadingMetadata = Pick<GuestCtx, "host"> & {
  readonly services: Pick<GuestServices, "listInstances">;
};
export interface ReviewReadingSnapshot {
  readonly readings: ReadonlyMap<string, ReviewReading>;
  readonly reason: ReviewSelection["reason"];
  readonly providerRevision: string;
  readonly policyRevision: string;
}

/**
 * A bounded inbox, not a record store or a provider cache. Only the authenticated part's door
 * publishes here. The original answer stays in the part's existing memo; this short-lived
 * handoff carries no authority to judge, rule, spend or withhold eligible work.
 */
export class ReviewReadings {
  readonly #held = new Map<string, { reading: ReviewReading; expiresAt: number }>();
  #basis: { providerRevision: string; policyRevision: string } | undefined;

  constructor(
    private readonly store: Pick<BabelStore, "db" | "record">,
    private readonly now: () => number,
  ) {}

  async records(ids: readonly string[]) {
    const records = [];
    const excluded = await readExcludedRecordIds(this.store.db);
    for (const id of new Set(ids.slice(0, REVIEW_READINGS_HELD))) {
      if (excluded.has(id)) continue;
      const rows = await this.store.db.query<{ seq: number; kind: string }>(
        `SELECT r.seq, r.kind FROM records r WHERE r.id = ?
         AND NOT EXISTS (SELECT 1 FROM records newer WHERE newer.root_id = r.root_id AND newer.seq > r.seq)`,
        [id],
      );
      const row = rows[0];
      const kind = RecordKindSchema.safeParse(row?.kind);
      if (row === undefined || !kind.success) continue;
      const peel = await this.store.record(id);
      if (!peel || peel.claim.statement === "" || peel.claim.statement.length > 8192) continue;
      records.push({
        recordId: id,
        revision: Number(row.seq),
        kind: kind.data,
        text: peel.claim.statement,
      });
    }
    return records;
  }

  private async current(ctx: ReadingMetadata | undefined): Promise<
    | {
        providerRevision: string;
        policyRevision: string;
      }
    | ReviewSelection["reason"]
  > {
    if (ctx === undefined) return "metadata-unavailable";
    try {
      const providerRevision = reviewReadingProviderRevision(await ctx.host.roster());
      if (providerRevision === null) return "provider-unavailable";
      const services = await ctx.services.listInstances({});
      const service = services.services.find((row) => row.serviceId === JEV_SERVICE_ID);
      if (service?.state !== "ready" || !service.configuration) return "provider-unavailable";
      if (reviewReadingProviderRevision(await ctx.host.roster()) !== providerRevision)
        return "provider-changed";
      return { providerRevision, policyRevision: service.configuration.revision };
    } catch {
      return "metadata-unavailable";
    }
  }

  async publish(ctx: ReadingMetadata, publication: ReviewReadingPublication): Promise<number> {
    const before = await this.current(ctx);
    if (
      typeof before === "string" ||
      before.providerRevision !== publication.providerRevision ||
      before.policyRevision !== publication.policyRevision
    )
      return 0;
    const records = new Map(
      (await this.records(publication.records)).map((record) => [record.recordId, record]),
    );
    const accepted: ReviewReading[] = [];
    for (const reading of publication.readings) {
      const record = records.get(reading.recordId);
      if (
        !record ||
        record.revision !== reading.revision ||
        record.kind !== reading.kind ||
        createHash("sha256").update(record.text).digest("hex") !== reading.textDigest
      )
        continue;
      accepted.push(reading);
    }
    const after = await this.current(ctx);
    if (
      typeof after === "string" ||
      after.providerRevision !== before.providerRevision ||
      after.policyRevision !== before.policyRevision
    )
      return 0;
    if (
      this.#basis?.providerRevision !== before.providerRevision ||
      this.#basis.policyRevision !== before.policyRevision
    )
      this.#held.clear();
    this.#basis = before;
    // A newly observed miss withdraws an earlier handoff for that exact record, not a review.
    for (const id of publication.records) this.#held.delete(id);
    const expiresAt = this.now() + REVIEW_READINGS_TTL_MS;
    for (const reading of accepted) {
      this.#held.delete(reading.recordId);
      this.#held.set(reading.recordId, { reading, expiresAt });
    }
    for (const id of this.#held.keys()) {
      if (this.#held.size <= REVIEW_READINGS_HELD) break;
      this.#held.delete(id);
    }
    return accepted.length;
  }

  async snapshot(ctx: ReadingMetadata | undefined): Promise<ReviewReadingSnapshot | undefined> {
    const basis = this.#basis;
    if (basis === undefined) return undefined; // No part: no metadata calls and exactly the old draw.
    let expired = this.prune();
    if (this.#held.size === 0)
      return {
        ...basis,
        readings: new Map(),
        reason: expired ? "readings-expired" : "missing-reading",
      };
    const current = await this.current(ctx);
    const reason =
      typeof current === "string"
        ? current
        : current.providerRevision !== basis.providerRevision
          ? "provider-changed"
          : current.policyRevision !== basis.policyRevision
            ? "policy-changed"
            : "cached-current";
    if (reason !== "cached-current") {
      // An unavailable dispatch does not prove a global revocation. It still cannot consume.
      if (reason !== "metadata-unavailable") this.#held.clear();
      return { ...basis, readings: new Map(), reason };
    }
    // Metadata RPCs can outlive the remaining TTL. Validate freshness at consumption too.
    const excluded = await readExcludedRecordIds(this.store.db);
    for (const id of excluded) this.#held.delete(id);
    expired = this.prune() || expired;
    return {
      ...basis,
      readings: new Map([...this.#held].map(([id, entry]) => [id, entry.reading])),
      reason: this.#held.size === 0 ? (expired ? "readings-expired" : "missing-reading") : reason,
    };
  }

  private prune(): boolean {
    const moment = this.now();
    let expired = false;
    for (const [id, entry] of this.#held) {
      if (entry.expiresAt > moment) continue;
      this.#held.delete(id);
      expired = true;
    }
    return expired;
  }
}
