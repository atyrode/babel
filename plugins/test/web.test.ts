import { describe, expect, test } from "bun:test";
import { HostCallError } from "@manifold/plugin-kit";
import {
  attachWebGuest,
  defineWebPlugin,
  type GuestHost,
  type OpenTerminalOptions,
} from "@manifold/plugin-kit/web";
import {
  UiNodeSchema,
  type ActionOutcome,
  type MachineSummary,
  type TerminalInfo,
  type UiNode,
  type WebIsolateWorkerFrame,
} from "@manifold/protocol";
import {
  BASELINE_ID,
  LIST_RUNS_DOOR,
  REPORTS_PANEL,
  RUN_DOOR,
  type RunRecord,
} from "../atyrode.babel/contract.ts";
import { reports, type ReportsState } from "../atyrode.babel/sessions/web.ts";

/*
  The `reports` panel as a program, driven by a fake host: every tree it renders parses against
  the protocol's `UiNodeSchema` (the runtime faults a tree that does not), and the launch
  event dispatches the baseline's door and opens the terminal with the exact argv.
 */

const machines: readonly MachineSummary[] = [
  { id: "m-dev", name: "dev-01", online: true },
  { id: "m-ws", name: "ws-linux", online: false },
  { id: "m-hub", name: "hub", online: true },
];

const record = (report: RunRecord["report"], at: string): RunRecord => ({
  runId: `run-${at}`,
  machineId: "m-dev",
  report,
  argv: report === "version" ? ["babel", "version"] : ["babel", ...report.split("-")],
  recordedAt: at,
});

interface Fake {
  readonly host: GuestHost;
  readonly actions: { name: string; args: unknown }[];
  readonly terminals: OpenTerminalOptions[];
  machines: readonly MachineSummary[];
  answer(name: string): ActionOutcome;
  openTerminal(options: OpenTerminalOptions): Promise<TerminalInfo>;
}

function terminal(id: string, options: OpenTerminalOptions): TerminalInfo {
  return {
    id,
    containerId: "c1",
    name: null,
    machineId: options.machineId ?? "m-dev",
    status: "running",
    exitCode: null,
    cols: options.cols,
    rows: options.rows,
    controllerId: null,
    createdBy: "p1",
  };
}

function fake(): Fake {
  const actions: Fake["actions"] = [];
  const terminals: OpenTerminalOptions[] = [];
  const runs: RunRecord[] = [record("version", "2026-09-05T21:00:00.000Z")];
  const self: Fake = {
    actions,
    terminals,
    machines,
    answer: (name) => {
      if (name === LIST_RUNS_DOOR) return { ok: true, result: { runs } };
      if (name === RUN_DOOR) {
        const fresh = record("archive-status", "2026-09-05T22:00:00.000Z");
        runs.unshift(fresh);
        return {
          ok: true,
          result: { runId: fresh.runId, argv: fresh.argv, recordedAt: fresh.recordedAt },
        };
      }
      return { ok: false, denial: { rule: "unknown_action", message: `no door ${name}` } };
    },
    openTerminal: (options) => {
      terminals.push(options);
      return Promise.resolve(terminal(`t-${String(terminals.length)}`, options));
    },
    host: {
      principal: { id: "p1", kind: "human", name: "Ada", color: "#e03131" },
      caps: ["terminals:spawn", "containers:read"],
      containerId: "c1",
      action: (name, args) => {
        actions.push({ name, args });
        return Promise.resolve(self.answer(name));
      },
      machines: () => Promise.resolve(self.machines),
      openTerminal: (options) => self.openTerminal(options),
      place: () => Promise.reject(new Error("not expected")),
      selfCaps: () => Promise.reject(new Error("not expected")),
      resolve: () => Promise.reject(new Error("not expected")),
      navigate: () => Promise.reject(new Error("not expected")),
      sendTerminalInput: () => Promise.reject(new Error("not expected")),
      terminalsByContainer: () => Promise.reject(new Error("not expected")),
    },
  };
  return self;
}

/** Parses the projected tree against the protocol, as the worker runtime does before posting. */
function view(state: ReportsState): UiNode {
  return UiNodeSchema.parse(reports.view(state));
}

/** Every node of a tree, depth first. */
function nodes(tree: UiNode): UiNode[] {
  return tree.type === "box" ? [tree, ...tree.children.flatMap(nodes)] : [tree];
}

describe("init and view", () => {
  test("selects the first online machine, lists the recorded runs, and renders a valid tree", async () => {
    const f = fake();
    const state = await reports.init(f.host);
    expect(state.machineId).toBe("m-dev");
    expect(state.machines.map((machine) => machine.id)).toEqual(["m-dev", "m-hub"]);
    expect(state.report).toBe("archive-status");
    expect(state.runs).toHaveLength(1);
    expect(f.actions).toEqual([{ name: LIST_RUNS_DOOR, args: {} }]);

    const tree = view(state);
    const all = nodes(tree);
    const machineSelect = all.find((node) => node.type === "select" && node.event === "machine");
    expect(machineSelect).toMatchObject({
      value: "m-dev",
      options: [
        { value: "m-dev", label: "dev-01" },
        { value: "m-hub", label: "hub" },
      ],
    });
    const reportSelect = all.find((node) => node.type === "select" && node.event === "report");
    expect(reportSelect).toMatchObject({
      value: "archive-status",
      options: expect.arrayContaining([{ value: "version", label: "version - build identity" }]),
    });
    expect(all.find((node) => node.type === "button")).toMatchObject({
      event: "run",
      action: RUN_DOOR,
      disabled: false,
    });
    expect(all.find((node) => node.type === "list")).toMatchObject({
      items: [{ key: "run-2026-09-05T21:00:00.000Z", primary: "babel version on dev-01" }],
    });
  });

  test("with no machine online the button is disabled and the panel says so", async () => {
    const f = fake();
    f.machines = machines.map((machine) => ({ ...machine, online: false }));
    const state = await reports.init(f.host);
    expect(state.machineId).toBeNull();
    const all = nodes(view(state));
    expect(all.find((node) => node.type === "button")).toMatchObject({ disabled: true });
    expect(all).toContainEqual({ type: "empty", text: "No machine is online." });
  });

  test("a denial from the list door is shown as text, and the tree is still valid", async () => {
    const f = fake();
    f.answer = () => ({
      ok: false,
      denial: { rule: "plugin_disabled", message: 'plugin "atyrode.babel" is disabled' },
    });
    const state = await reports.init(f.host);
    expect(state.runs).toEqual([]);
    expect(nodes(view(state))).toContainEqual({
      type: "text",
      text: 'plugin "atyrode.babel" is disabled',
      tone: "danger",
      wrap: true,
    });
  });

  test("a host call that throws becomes text rather than a fault", async () => {
    const f = fake();
    f.machines = [];
    f.host.machines = () => Promise.reject(new HostCallError("machines", "socket is down"));
    const state = await reports.init(f.host);
    expect(state.status).toEqual({ text: "socket is down", tone: "danger" });
    view(state);
  });
});

describe("update", () => {
  test("run dispatches the door with the selection and opens the terminal with the exact argv", async () => {
    const f = fake();
    const before = await reports.init(f.host);
    const chosen = await reports.update(before, { event: "report", payload: "archive-fleet" }, f.host);
    expect(chosen.report).toBe("archive-fleet");
    const onHub = await reports.update(chosen, { event: "machine", payload: "m-hub" }, f.host);
    expect(onHub.machineId).toBe("m-hub");

    const after = await reports.update(onHub, { event: "run" }, f.host);
    expect(f.actions.at(-2)).toEqual({
      name: RUN_DOOR,
      args: { machineId: "m-hub", report: "archive-fleet" },
    });
    expect(f.actions.at(-1)).toEqual({ name: LIST_RUNS_DOOR, args: {} });
    expect(f.terminals).toHaveLength(1);
    const opened = f.terminals[0];
    expect(opened).toMatchObject({
      cols: 120,
      rows: 40,
      machineId: "m-hub",
      placement: "tile",
      program: {
        argv: [
          "sh",
          "-c",
          "babel archive status; printf '\\n[babel exited %s]\\n' $?; exec sleep 600",
        ],
      },
    });
    expect(opened?.elementId).toMatch(/^[0-9a-f-]{36}$/);
    expect(after.status).toEqual({
      text: "Terminal t-1 on hub: babel archive status (run run-2026-09-05T22:00:00.000Z recorded).",
      tone: "success",
    });
    expect(after.runs).toHaveLength(2);
    view(after);
  });

  test("the door's denial is shown and no terminal is opened", async () => {
    const f = fake();
    const state = await reports.init(f.host);
    f.answer = () => ({
      ok: false,
      denial: { rule: "refused", message: 'machine "m-dev" is not online' },
    });
    const after = await reports.update(state, { event: "run" }, f.host);
    expect(f.terminals).toEqual([]);
    expect(after.status).toEqual({ text: 'machine "m-dev" is not online', tone: "danger" });
    expect(after.runs).toEqual(state.runs);
    view(after);
  });

  test("the host's refusal to open a terminal is shown by name, the run stays recorded", async () => {
    const f = fake();
    const state = await reports.init(f.host);
    f.openTerminal = () =>
      Promise.reject(new HostCallError("openTerminal", "no occupant view of container c1 is mounted"));
    const after = await reports.update(state, { event: "run" }, f.host);
    expect(f.actions.filter((call) => call.name === RUN_DOOR)).toHaveLength(1);
    expect(after.status).toEqual({
      text: "Run run-2026-09-05T22:00:00.000Z recorded, but no terminal opened: no occupant view of container c1 is mounted",
      tone: "danger",
    });
    expect(after.runs).toHaveLength(2);
    view(after);
  });

  test("run with no machine selected does nothing", async () => {
    const f = fake();
    f.machines = [];
    const state = await reports.init(f.host);
    expect(await reports.update(state, { event: "run" }, f.host)).toBe(state);
    expect(f.actions.filter((call) => call.name === RUN_DOOR)).toEqual([]);
  });

  test("an unknown report or machine leaves the state alone", async () => {
    const f = fake();
    const state = await reports.init(f.host);
    expect(await reports.update(state, { event: "report", payload: "archive-push" }, f.host)).toBe(
      state,
    );
    expect(await reports.update(state, { event: "machine", payload: "m-ws" }, f.host)).toBe(state);
  });

  test("poll re-reads machines and drops a selection that went offline", async () => {
    const f = fake();
    const state = await reports.init(f.host);
    const onHub = await reports.update(state, { event: "machine", payload: "m-hub" }, f.host);
    f.machines = machines.map((machine) =>
      machine.id === "m-hub" ? { ...machine, online: false } : machine,
    );
    const polled = await reports.update(onHub, { event: "poll" }, f.host);
    expect(polled.machines.map((machine) => machine.id)).toEqual(["m-dev"]);
    expect(polled.machineId).toBe("m-dev");
  });
});

describe("subscribe", () => {
  test("polls every ten seconds until stopped", () => {
    const f = fake();
    const events: string[] = [];
    const stop = reports.subscribe?.(f.host, (event) => {
      events.push(event.event);
    });
    expect(stop).toBeFunction();
    stop?.();
    expect(events).toEqual([]);
  });
});

describe("the baseline's web half", () => {
  test("answers ready with no panels", async () => {
    const posted: WebIsolateWorkerFrame[] = [];
    let listener: (data: unknown) => void = () => {};
    attachWebGuest(
      { id: BASELINE_ID, panels: {} },
      {
        post: (frame) => {
          posted.push(frame);
        },
        onMessage: (next) => {
          listener = next;
        },
        warn: () => {},
      },
    );
    listener({
      t: "init",
      pluginId: BASELINE_ID,
      principal: { id: "p1", kind: "human", name: "Ada", color: "#e03131" },
      caps: [],
      containerId: null,
    });
    expect(posted).toEqual([{ t: "ready", panels: [] }]);
    // And the sub-plugin's, for symmetry: it serves exactly the panel its manifest declares.
    expect(() => defineWebPlugin({ id: "atyrode.babel.sessions", panels: { [REPORTS_PANEL]: reports } })).not.toThrow();
  });
});
