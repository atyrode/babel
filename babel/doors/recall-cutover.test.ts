import { afterEach, expect, test } from "bun:test";
import { resolve } from "node:path";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import type { SqlParam, SqlRow } from "@manifold/plugin";
import type {
  ConfigureInstanceServiceArgs,
  InstanceServiceConfigurationRead,
  JobDescription,
  ServiceConfigurationRead,
} from "@manifold/protocol";
import type { RecallPolicy } from "../contract.ts";
import type { TestStore } from "../store/testdb.ts";
import type * as Contract from "../contract.ts";
import type * as RecallServiceDoors from "./recall-services.ts";
import type * as SessionExclusionDoors from "./session-exclusions.ts";
import type * as TestDatabase from "../store/testdb.ts";
import type * as Exclusions from "../store/exclusions.ts";
import type * as ServiceDigests from "./services.ts";

// Static imports cannot select the before/after checkout at runtime. Run this same synthetic
// regression against either checkout without copying tests or data:
// BABEL_NATIVE_POLICY_ROOT=/path/to/checkout bun test babel/doors/recall-cutover.test.ts
const projectRoot = resolve(process.env["BABEL_NATIVE_POLICY_ROOT"] ?? `${import.meta.dir}/../..`);
const {
  ACTIONS,
  BABEL_PLUGIN_ID,
  MACHINE_OPERATIONS,
  RECALL_SERVICE_ID,
  INPUT_FIELD,
  ExcludeSessionResultSchema,
  RecallInstalledSchema,
  RecallRuntimeInputSchema,
  RecallSetupPreviewSchema,
}: typeof Contract = await import(resolve(projectRoot, "babel/contract.ts"));
const { recallServiceDoors }: typeof RecallServiceDoors = await import(
  resolve(projectRoot, "babel/doors/recall-services.ts")
);
const { sessionExclusionDoors }: typeof SessionExclusionDoors = await import(
  resolve(projectRoot, "babel/doors/session-exclusions.ts")
);
const { openTestStore, insert }: typeof TestDatabase = await import(
  resolve(projectRoot, "babel/store/testdb.ts")
);
const { readSessionExclusions, recallEnforcesSessionExclusions }: typeof Exclusions = await import(
  resolve(projectRoot, "babel/store/exclusions.ts")
);
const { digestOf }: typeof ServiceDigests = await import(
  resolve(projectRoot, "babel/doors/services.ts")
);

const MACHINE = "synthetic-recall-owner";
const A = "omp/synthetic-cutover-a";
const B = "omp/synthetic-cutover-b";
const OWNER_ONLY = "omp/synthetic-owner-only";
const LEDGER_READ = "SELECT selector FROM session_exclusions ORDER BY selector";
const RUNTIME = {
  installationRevision: "synthetic-install-1",
  artifactSha256: "a".repeat(64),
  resourceBindingDigest: "b".repeat(64),
};
const POLICY: RecallPolicy = {
  version: 1,
  classes: [{ id: "owner", label: "Owner", ceiling: 3 }],
  subjects: [{ name: "Synthetic owner", host: "synthetic-archive", sensitivity: 2 }],
};

const opened: TestStore[] = [];
afterEach(() => {
  for (const fixture of opened.splice(0)) fixture.close();
});

function gate() {
  let entered!: () => void;
  let release!: () => void;
  const reached = new Promise<void>((resolve) => {
    entered = resolve;
  });
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  return {
    reached,
    release,
    async stop() {
      entered();
      await released;
    },
  };
}

async function owner() {
  const fixture = await openTestStore(Date.UTC(2026, 9, 3));
  opened.push(fixture);
  for (const selector of [A, B]) {
    await insert(fixture.db, "sessions", {
      selector,
      host: "synthetic-host",
      harness: "omp",
      source_id: selector.slice(4),
      seen_at: new Date(fixture.store.now()).toISOString(),
    });
  }
  const installed: InstanceServiceConfigurationRead = {
    description: {
      serviceId: RECALL_SERVICE_ID,
      defaultOwner: null,
      owner: null,
      configuration: null,
      connected: true,
      state: "unconfigured",
      reason: null,
    },
    policy: null,
  };
  const described: JobDescription = {
    machineId: MACHINE,
    pluginId: BABEL_PLUGIN_ID,
    admissionPublicKey: "-----BEGIN PUBLIC KEY-----synthetic",
    connected: true,
    platforms: ["linux-x64"],
    operations: {
      [MACHINE_OPERATIONS.recall]: {
        ready: true,
        reason: null,
        resourceBindingDigest: RUNTIME.resourceBindingDigest,
      },
    },
    installation: {
      revision: RUNTIME.installationRevision,
      artifactSha256: RUNTIME.artifactSha256,
      enabled: true,
      ready: true,
      purgeRequested: false,
    },
    retainedInstallations: [],
    consents: [],
  };
  const native: ServiceConfigurationRead = {
    configuration: { revision: null, policies: [] },
    connected: true,
    credentialReferences: [],
    runtimeCandidates: [
      {
        runtime: { pluginId: BABEL_PLUGIN_ID, operationId: MACHINE_OPERATIONS.recall, ...RUNTIME },
        ready: true,
        reason: null,
      },
    ],
  };
  const violations: { phase: string; omitted: readonly string[] }[] = [];
  const ledgerQuery = fixture.db.query.bind(fixture.db);
  function policy() {
    const input = installed.policy?.runtime?.input[INPUT_FIELD];
    if (input === undefined || !("literal" in input))
      throw new Error("Synthetic native configuration has no literal owner policy");
    return RecallRuntimeInputSchema.parse(JSON.parse(String(input.literal))).policy;
  }
  async function audit(phase: string) {
    if (!installed.description.configuration?.enabled) return;
    const durable = await ledgerQuery<{ selector: string }>(LEDGER_READ);
    const covered = policy().excludedSessions ?? [];
    const omitted = durable
      .map((row) => row.selector)
      .filter((selector) => !covered.includes(selector));
    if (omitted.length !== 0) violations.push({ phase, omitted });
  }
  const control = { failNextEnable: false };
  let revision = 0;
  const ctx = {
    auth: { isRoot: true, principal: { id: "synthetic-owner" }, allows: async () => true },
    jobs: { describe: async () => structuredClone(described) },
    services: {
      describeInstance: async () => structuredClone(installed.description),
      readConfiguration: async () => structuredClone(native),
      readInstanceConfiguration: async () => structuredClone(installed),
      configureInstance: async (args: ConfigureInstanceServiceArgs) => {
        if (args.expectedRevision !== (installed.description.configuration?.revision ?? null))
          throw new Error("synthetic native compare-and-swap conflict");
        if (args.enabled && control.failNextEnable) {
          control.failNextEnable = false;
          throw new Error("synthetic failed native activation");
        }
        installed.policy = structuredClone(args.policy);
        installed.description.owner = { machineId: args.machineId!, name: "Synthetic", online: true };
        installed.description.configuration = {
          revision: (++revision).toString(16).padStart(64, "0"),
          pluginId: BABEL_PLUGIN_ID,
          enabled: args.enabled,
          policySha256: digestOf(args.policy),
        };
        installed.description.state = "starting";
        await audit("native enable");
        return structuredClone(installed.description);
      },
    },
  } as unknown as GuestCtx;
  const batch = fixture.db.batch.bind(fixture.db);
  fixture.db.batch = async (statements) => {
    const result = await batch(statements);
    if (statements.some((statement) => statement.sql.startsWith("INSERT INTO session_exclusions(")))
      await audit("durable ban commit");
    return result;
  };
  async function knock(name: string, args: unknown) {
    // Recreate doors on each request, as separate callers do; the lock cannot live on a door.
    const door = [...recallServiceDoors(fixture.store), ...sessionExclusionDoors(fixture.store)].find(
      (candidate) => candidate.action.name === name,
    );
    if (door === undefined) throw new Error("Synthetic configuration door is missing");
    return await door.handler(ctx, door.action.input.parse(args) as never);
  }
  async function preview(ownerPolicy = POLICY) {
    return RecallSetupPreviewSchema.parse(
      await knock(ACTIONS.previewRecall, { machineId: MACHINE, policy: ownerPolicy }),
    );
  }
  async function install(
    shown: { expectedRevision: string | null; previewDigest: string },
    ownerPolicy = POLICY,
  ) {
    return await knock(ACTIONS.installRecall, {
      machineId: MACHINE,
      policy: ownerPolicy,
      expectedRevision: shown.expectedRevision,
      previewDigest: shown.previewDigest,
    });
  }
  RecallInstalledSchema.parse(await install(await preview()));
  installed.description.state = "ready";
  return { fixture, installed, control, violations, policy, knock, preview, install };
}

function holdExclusionPolicy(fixture: TestStore, selector: string) {
  const held = gate();
  const query = fixture.db.query.bind(fixture.db);
  // After the ban, status reads the ledger once; the next read is the policy snapshot.
  let remaining = 2;
  fixture.db.query = async <Row extends SqlRow = SqlRow>(
    sql: string,
    params?: readonly SqlParam[],
  ) => {
    const rows = await query<Row>(sql, params);
    if (
      remaining > 0 &&
      sql === LEDGER_READ &&
      rows.length === 1 &&
      rows[0]?.["selector"] === selector &&
      --remaining === 0
    ) {
      await held.stop();
    }
    return rows;
  };
  return held;
}

test("concurrent exclusions never enable an A-only policy after B becomes durable", async () => {
  const fleet = await owner();
  const held = holdExclusionPolicy(fleet.fixture, A);
  const first = fleet.knock(ACTIONS.excludeSession, { selector: A });
  await held.reached;
  const second = fleet.knock(ACTIONS.excludeSession, { selector: B });
  try {
    // All fixture I/O is temporary SQLite or immediately resolved native promises. Advancing
    // one event-loop turn drains B if it is runnable, without a time-based race or sleep.
    await new Promise<void>((resolve) => setImmediate(resolve));
  } finally {
    held.release();
  }
  const results = await Promise.all([first, second]);
  expect(results.map((result) => ExcludeSessionResultSchema.parse(result).selector)).toEqual([A, B]);
  expect(await readSessionExclusions(fleet.fixture.db)).toEqual([A, B]);
  expect(fleet.policy().excludedSessions).toEqual([A, B]);
  expect(fleet.violations).toEqual([]);
  fleet.installed.description.state = "ready";
  expect(await recallEnforcesSessionExclusions(fleet.fixture.db, fleet.installed.description)).toBe(true);

  const replacement = { ...POLICY, excludedSessions: [OWNER_ONLY] };
  RecallInstalledSchema.parse(await fleet.install(await fleet.preview(replacement), replacement));
  expect(fleet.policy().excludedSessions).toEqual([A, B, OWNER_ONLY].sort());
  fleet.installed.description.state = "ready";
  expect(await recallEnforcesSessionExclusions(fleet.fixture.db, fleet.installed.description)).toBe(true);
});

test("an owner install cannot reopen Recall between the exclusion pause and ledger commit", async () => {
  const fleet = await owner();
  const beforeLedger = gate();
  const afterLedger = gate();
  const batch = fleet.fixture.db.batch.bind(fleet.fixture.db);
  fleet.fixture.db.batch = async (statements) => {
    const excludesB = statements.some(
      (statement) =>
        statement.sql.startsWith("INSERT INTO session_exclusions(") && statement.params?.[0] === B,
    );
    if (excludesB) await beforeLedger.stop();
    const result = await batch(statements);
    if (excludesB) await afterLedger.stop();
    return result;
  };
  const exclusion = fleet.knock(ACTIONS.excludeSession, { selector: B });
  await beforeLedger.reached;
  expect(fleet.installed.description.configuration?.enabled).toBe(false);
  // This is a genuinely fresh owner preview of the paused revision, not a stale CAS.
  const installation = fleet.install(await fleet.preview());
  try {
    await new Promise<void>((resolve) => setImmediate(resolve));
    beforeLedger.release();
    await afterLedger.reached;
    expect(await readSessionExclusions(fleet.fixture.db)).toEqual([B]);
  } finally {
    beforeLedger.release();
    afterLedger.release();
  }
  const [excluded, installed] = await Promise.all([exclusion, installation]);
  expect(ExcludeSessionResultSchema.parse(excluded).excluded).toBe(true);
  expect(installed).toHaveProperty("refused");
  expect(fleet.policy().excludedSessions).toEqual([B]);
  expect(fleet.violations).toEqual([]);
});

test("failed enforcement preserves the durable ban and pause while releasing a queued owner install", async () => {
  const fleet = await owner();
  const shown = await fleet.preview();
  const held = holdExclusionPolicy(fleet.fixture, A);
  const exclusion = fleet.knock(ACTIONS.excludeSession, { selector: A });
  await held.reached;
  fleet.control.failNextEnable = true;
  const installation = fleet.install(shown);
  held.release();
  const [excluded, installed] = await Promise.all([exclusion, installation]);
  const result = ExcludeSessionResultSchema.parse(excluded);
  expect(result.excluded).toBe(true);
  expect(result.recallEnforced).toBe(false);
  expect(installed).toHaveProperty("refused");
  expect(fleet.installed.description.configuration?.enabled).toBe(false);
  expect(await readSessionExclusions(fleet.fixture.db)).toEqual([A]);
  expect(fleet.violations).toEqual([]);

  // A subsequent owner install must still merge the ban, even when its policy omits it.
  RecallInstalledSchema.parse(await fleet.install(await fleet.preview()));
  expect(fleet.policy().excludedSessions).toEqual([A]);
  fleet.installed.description.state = "ready";
  expect(await recallEnforcesSessionExclusions(fleet.fixture.db, fleet.installed.description)).toBe(true);
});
