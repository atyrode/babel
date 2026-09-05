import { HostCallError, ui } from "@manifold/plugin-kit";
import { definePanel, defineWebPlugin, type GuestHost } from "@manifold/plugin-kit/web";
import type { MachineSummary, UiNode } from "@manifold/protocol";
import {
  LIST_RUNS_DOOR,
  ListRunsResult,
  REPORT_IDS,
  REPORT_LABELS,
  REPORTS_PANEL,
  ReportIdSchema,
  RUN_DOOR,
  RunResult,
  SESSIONS_ID,
  holdOpenArgv,
  type ReportId,
  type RunRecord,
} from "../contract.ts";

/*
  `atyrode.babel.sessions`, web half: the `reports` panel, a program over its own state.

  What it does tonight, and all it does: the viewer picks an online machine and one of
  babel's read-only reports; "Run here" dispatches `atyrode.babel.run` through the host (the
  baseline records the run and answers the argv), then opens a terminal tile on that machine
  running the argv under `sh -c` so the output stays readable after babel exits. The host
  refuses `openTerminal` by name when the panel's container has no mounted composition view,
  and that sentence is shown in the panel as text.

  What it does not do: browse archived sessions. `babel web` binds 127.0.0.1 behind a
  one-time nonce and is not reachable from a hub-origin Worker; the browser waits on a
  hub-reachable babel API (docs/manifold-transition.md §7).
 */

interface Status {
  readonly text: string;
  readonly tone: "success" | "danger";
}

export interface ReportsState {
  readonly machines: readonly MachineSummary[];
  readonly machineId: string | null;
  readonly report: ReportId;
  readonly runs: readonly RunRecord[];
  readonly status: Status | null;
}

const MACHINES_POLL_MS = 10_000;

/** The host's refusal sentence, verbatim; anything else is an error the panel did not expect. */
function sentence(error: unknown): string {
  if (error instanceof HostCallError) return error.detail;
  return error instanceof Error ? error.message : String(error);
}

/** Online machines only; a selection that went offline is dropped rather than launched into. */
async function readMachines(
  host: GuestHost,
  selected: string | null,
): Promise<Pick<ReportsState, "machines" | "machineId">> {
  const machines = (await host.machines()).filter((machine) => machine.online);
  const still = machines.some((machine) => machine.id === selected);
  return { machines, machineId: still ? selected : (machines[0]?.id ?? null) };
}

/** The baseline's record through its door; a denial is a sentence, never a throw. */
async function readRuns(
  host: GuestHost,
): Promise<{ readonly runs: readonly RunRecord[] } | { readonly denial: string }> {
  const outcome = await host.action(LIST_RUNS_DOOR, {});
  if (!outcome.ok) return { denial: outcome.denial.message };
  const parsed = ListRunsResult.safeParse(outcome.result);
  if (!parsed.success) return { denial: `${LIST_RUNS_DOOR} answered outside its contract` };
  return { runs: parsed.data.runs };
}

export const reports = definePanel<ReportsState>({
  init: async (host) => {
    const base: ReportsState = {
      machines: [],
      machineId: null,
      report: REPORT_IDS[0],
      runs: [],
      status: null,
    };
    try {
      const [machines, runs] = await Promise.all([readMachines(host, null), readRuns(host)]);
      return "denial" in runs
        ? { ...base, ...machines, status: { text: runs.denial, tone: "danger" } }
        : { ...base, ...machines, runs: runs.runs };
    } catch (error) {
      return { ...base, status: { text: sentence(error), tone: "danger" } };
    }
  },

  view: (state): UiNode =>
    ui.box({ direction: "column", gap: 2 }, [
      ui.heading("babel", 2),
      ui.text(
        "Runs one of babel's read-only reports in a terminal on the chosen machine and records the run. Session browsing waits on a hub-reachable babel API; babel web is loopback-only.",
        { tone: "muted", wrap: true },
      ),
      state.machines.length === 0
        ? ui.empty("No machine is online.")
        : ui.select(
            "machine",
            state.machineId,
            state.machines.map((machine) => ({ value: machine.id, label: machine.name })),
            { label: "Machine" },
          ),
      ui.select(
        "report",
        state.report,
        REPORT_IDS.map((id) => ({ value: id, label: REPORT_LABELS[id] })),
        { label: "Report" },
      ),
      ui.button("Run here", "run", {
        tone: "accent",
        action: RUN_DOOR,
        disabled: state.machineId === null,
      }),
      state.status === null
        ? ui.divider()
        : ui.text(state.status.text, { tone: state.status.tone, wrap: true }),
      ui.heading("Recent runs", 3),
      state.runs.length === 0
        ? ui.empty("No run recorded yet.")
        : ui.list(
            state.runs.map((run) => ({
              key: run.runId,
              primary: `${run.argv.join(" ")} on ${state.machines.find((machine) => machine.id === run.machineId)?.name ?? run.machineId}`,
              secondary: run.recordedAt,
            })),
          ),
    ]),

  update: async (state, event, host) => {
    switch (event.event) {
      case "machine": {
        const id = String(event.payload);
        return state.machines.some((machine) => machine.id === id)
          ? { ...state, machineId: id }
          : state;
      }
      case "report": {
        const report = ReportIdSchema.safeParse(event.payload);
        return report.success ? { ...state, report: report.data } : state;
      }
      case "poll": {
        try {
          return { ...state, ...(await readMachines(host, state.machineId)) };
        } catch (error) {
          return { ...state, status: { text: sentence(error), tone: "danger" } };
        }
      }
      case "run": {
        if (state.machineId === null) return state;
        const machineId = state.machineId;
        const machine =
          state.machines.find((candidate) => candidate.id === machineId)?.name ?? machineId;
        const outcome = await host.action(RUN_DOOR, { machineId, report: state.report });
        if (!outcome.ok) {
          return { ...state, status: { text: outcome.denial.message, tone: "danger" } };
        }
        const recorded = RunResult.safeParse(outcome.result);
        if (!recorded.success) {
          return {
            ...state,
            status: { text: `${RUN_DOOR} answered outside its contract`, tone: "danger" },
          };
        }
        const { runId, argv } = recorded.data;
        let status: Status;
        try {
          const terminal = await host.openTerminal({
            elementId: crypto.randomUUID(),
            cols: 120,
            rows: 40,
            machineId,
            placement: "tile",
            program: { argv: [...holdOpenArgv(argv)] },
          });
          status = {
            text: `Terminal ${terminal.id} on ${machine}: ${argv.join(" ")} (run ${runId} recorded).`,
            tone: "success",
          };
        } catch (error) {
          status = {
            text: `Run ${runId} recorded, but no terminal opened: ${sentence(error)}`,
            tone: "danger",
          };
        }
        try {
          const runs = await readRuns(host);
          return "denial" in runs ? { ...state, status } : { ...state, status, runs: runs.runs };
        } catch {
          return { ...state, status };
        }
      }
      default:
        return state;
    }
  },

  subscribe: (_host, emit) => {
    const timer = setInterval(() => emit({ event: "poll" }), MACHINES_POLL_MS);
    return () => clearInterval(timer);
  },
});

defineWebPlugin({ id: SESSIONS_ID, panels: { [REPORTS_PANEL]: reports } });
