import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  CitationBackfillRequestSchema,
  CitationBackfillResultSchema,
  CitationFactPageInputSchema,
  CitationFactPageResultSchema,
  CitationPlanInputSchema,
  CitationPlanResultSchema,
  INPUT_FIELD,
  MACHINE_OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
} from "../contract.ts";
import { describeHost } from "../server/conductor.ts";
import {
  citationFactReport,
  planCitationFacts,
  readCitationFacts,
} from "../store/citation-facts.ts";
import type { BabelStore } from "../store/store.ts";
import type { LaunchDeps } from "./launch.ts";
import { POSTING_DELEGATES } from "./launch.ts";
import { defineDoor, type Door } from "./door.ts";

const MAX_JOB_INPUT = 65_536;
const OWNER_ONLY = "historical citation source facts require the owner";

export function citationFactDoors(store: BabelStore, deps: LaunchDeps): readonly Door[] {
  const plan = defineDoor(
    defineServerAction({
      name: ACTIONS.citationPlan,
      title: "Size a historical citation backfill without archive access",
      caps: ["containers:read"],
      trace: "opaque",
      input: CitationPlanInputSchema,
      result: CitationPlanResultSchema,
    }),
    async (ctx, input) => {
      if (!ctx.auth.isRoot) return { refused: OWNER_ONLY };
      const options = {
        ...(input.recordId === undefined ? {} : { recordId: input.recordId }),
        limit: input.limit,
        retryUnavailable: input.retryUnavailable,
      };
      const page = await planCitationFacts(store.db, options);
      return {
        plan: { ...page, tasks: [...page.tasks] },
        report: await citationFactReport(
          store.db,
          input.recordId === undefined ? {} : { recordId: input.recordId },
        ),
      };
    },
  );

  const facts = defineDoor(
    defineServerAction({
      name: ACTIONS.citationFacts,
      title: "Read one owner's redacted historical citation fact page",
      caps: ["containers:read"],
      trace: "opaque",
      input: CitationFactPageInputSchema,
      result: CitationFactPageResultSchema,
    }),
    async (ctx, input) => {
      if (!ctx.auth.isRoot) return { refused: OWNER_ONLY };
      const page = await readCitationFacts(store.db, input.recordId, {
        after: input.after,
        limit: input.limit,
      });
      return { ...page, facts: [...page.facts] };
    },
  );

  const backfill = defineDoor(
    defineServerAction({
      name: ACTIONS.citationBackfill,
      title: "Reconcile a bounded page of archived citation facts",
      caps: ["containers:write"],
      delegates: ["jobs:read", "machines:read", ...POSTING_DELEGATES],
      trace: "opaque",
      input: CitationBackfillRequestSchema,
      result: CitationBackfillResultSchema,
    }),
    async (ctx, input) => {
      if (!ctx.auth.isRoot) return { refused: OWNER_ONLY };
      if (
        input.operation.machineId !== input.machineId ||
        input.operation.operationId !== MACHINE_OPERATIONS.citationBackfill
      )
        return { refused: "citation backfill operation does not match the named machine" };
      const { policy, version } = await deps.coordinator.policy();
      if (!policy.enabled)
        return {
          refused: `the evaluation policy (${version}) is disabled; native receipts would not settle`,
        };
      const page = await planCitationFacts(store.db, {
        ...(input.recordId === undefined ? {} : { recordId: input.recordId }),
        retryUnavailable: input.retryUnavailable,
        limit: input.limit,
      });
      if (page.tasks.length === 0)
        return { refused: "no pending citation positions match this page" };
      const jobs = deps.jobs(ctx);
      const described = await describeHost(
        jobs,
        input.machineId,
        MACHINE_OPERATIONS.citationBackfill,
      );
      if ("refused" in described) return { refused: described.refused };
      const minted = await ctx.newId();
      const runId = `run_${minted}`;
      const jobId = `job_${minted}`;
      const attemptId = runId;
      const tasks = [...page.tasks];
      let document = "";
      while (tasks.length > 0) {
        document = JSON.stringify({ runId, machineId: input.machineId, attemptId, tasks });
        if (
          new TextEncoder().encode(JSON.stringify({ [INPUT_FIELD]: document })).byteLength <=
          MAX_JOB_INPUT
        )
          break;
        tasks.pop();
      }
      if (tasks.length === 0)
        return { refused: "one citation position exceeds the native input bound" };
      const at = new Date(deps.now()).toISOString();
      const intention = JSON.stringify({ attemptId, tasks });
      await store.db.run(
        `INSERT INTO runs
      (id,kind,machine_id,job_id,recipe_id,authority_kind,authority_id,preparation,started_at,records,chain,payload)
      VALUES (?,?,?,?,?,?,?,?,?,0,?,?)`,
        [
          runId,
          MACHINE_OPERATIONS.citationBackfill,
          input.machineId,
          jobId,
          "",
          "operator",
          ctx.principal.id,
          intention,
          at,
          `principal:${ctx.principal.id}`,
          JSON.stringify({ closure: null, requestedAt: deps.now() }),
        ],
      );
      store.touch();
      try {
        await jobs.execute({
          jobId,
          machineId: input.machineId,
          operationId: MACHINE_OPERATIONS.citationBackfill,
          input: { [INPUT_FIELD]: document },
          outputs: [{ name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [jobId] }],
          limits: deps.plan(policy, MACHINE_OPERATIONS.citationBackfill).limits,
          ...(described.readiness.installation === null
            ? {}
            : {
                installationRevision: described.readiness.installation.revision,
                artifactSha256: described.readiness.installation.artifactSha256,
              }),
        });
      } catch {
        // The transport may have lost a successful admission. Keep the pollable run; a later
        // settlement or owner check can decide it without minting a second attempt.
        return { refused: "citation job admission was not confirmed; inspect the retained run" };
      }
      return {
        runId,
        jobId,
        machineId: input.machineId,
        planned: tasks.length,
        remaining: page.pending - tasks.length,
      };
    },
  );
  return [plan, facts, backfill];
}
