import { extname, join, resolve, sep } from "node:path";
import type {
  ArchiveStatus,
  FetchResult,
  ScanState,
  SessionDetail,
  SessionSummary,
  TranscriptEvent,
  VerifyResult,
  VersionInfo,
} from "../src/api";

const distRoot = resolve(import.meta.dir, "..", "dist");
const port = Number(Bun.env.PORT ?? 4174);

const sessions: SessionSummary[] = [
  {
    harness: "codex",
    source_id: "synthetic-alpha",
    selector: "codex:synthetic-alpha",
    size: 482_311,
    modified: "2026-08-28T10:42:00Z",
    title: "Design a resilient import pipeline",
    workspace: "/home/demo/projects/atlas",
    continuation_grade: true,
  },
  {
    harness: "claude-code",
    source_id: "synthetic-bravo",
    selector: "claude-code:synthetic-bravo",
    size: 91_220,
    modified: "2026-08-27T18:05:00Z",
    title: "Trace a cache invalidation regression",
    workspace: "/home/demo/projects/kepler",
    continuation_grade: false,
  },
  {
    harness: "omp",
    source_id: "synthetic-charlie",
    selector: "omp:synthetic-charlie",
    size: 1_904_802,
    modified: "2026-08-22T08:30:00Z",
    title: null,
    workspace: "/home/demo/scratch",
    continuation_grade: true,
  },
];

const details: Record<string, SessionDetail> = {
  "codex:synthetic-alpha": {
    harness: "codex",
    source_id: "synthetic-alpha",
    selector: "codex:synthetic-alpha",
    primary_path: "/home/demo/.codex/sessions/synthetic-alpha.jsonl",
    primary_size: 482_311,
    described_at: "2026-08-28T10:43:10Z",
    hint: "Synthetic fixture for the Babel web mock",
    title: "Design a resilient import pipeline",
    workspace: "/home/demo/projects/atlas",
    created_at: "2026-08-28T09:10:00Z",
    modified_at: "2026-08-28T10:42:00Z",
    lifecycle: "complete",
    repo: {
      remote: "https://example.invalid/synthetic/atlas.git",
      commit: "4a7c3f0d55e0935be65ef2f8baec8aaef457acda",
      branch: "feature/synthetic-imports",
    },
    completeness: [],
    adapter_metadata_schema: 2,
    adapter_metadata: {
      model: "synthetic-model",
      sandbox: "workspace-write",
      fixture: true,
      counters: { input: 1200, output: 860 },
    },
    artifacts: [
      { rel_path: "session.jsonl", source_path: "/home/demo/.codex/sessions/synthetic-alpha.jsonl", size: 482_311 },
      { rel_path: "workspace/notes.txt", source_path: "/home/demo/projects/atlas/notes.txt", size: 2_048 },
    ],
    blobs: [
      { digest: "sha256:45de9f15b8d833fe2769c2dfac944020395ddd9e3d8328161c648b374721a5df", source_path: "/home/demo/.codex/blobs/synthetic-output.txt", size: 7_812 },
    ],
    unresolved_blob_refs: ["sha256:0000000000000000000000000000000000000000000000000000000000000000"],
    continuation_grade: true,
  },
  "claude-code:synthetic-bravo": {
    harness: "claude-code",
    source_id: "synthetic-bravo",
    selector: "claude-code:synthetic-bravo",
    primary_path: "/home/demo/.claude/projects/synthetic-bravo.jsonl",
    primary_size: 91_220,
    described_at: "2026-08-28T09:30:00Z",
    title: "Trace a cache invalidation regression",
    workspace: "/home/demo/projects/kepler",
    created_at: "2026-08-27T17:15:00Z",
    modified_at: "2026-08-27T18:05:00Z",
    lifecycle: null,
    repo: { branch: "main" },
    completeness: [
      { field: "lifecycle", reason: "The synthetic adapter fixture does not report lifecycle state." },
      { field: "repo.remote", reason: "No remote was included in this fixture." },
    ],
    adapter_metadata_schema: 1,
    adapter_metadata: { model: "synthetic-model", fixture: true },
    artifacts: [
      { rel_path: "synthetic-bravo.jsonl", source_path: "/home/demo/.claude/projects/synthetic-bravo.jsonl", size: 91_220 },
    ],
    blobs: [],
    unresolved_blob_refs: [],
    continuation_grade: false,
  },
  "omp:synthetic-charlie": {
    harness: "omp",
    source_id: "synthetic-charlie",
    selector: "omp:synthetic-charlie",
    primary_path: "/home/demo/.omp/sessions/synthetic-charlie.jsonl",
    primary_size: 1_904_802,
    described_at: "2026-08-28T09:30:00Z",
    hint: "Generated mock-only session",
    title: null,
    workspace: "/home/demo/scratch",
    created_at: "2026-08-21T21:05:00Z",
    modified_at: "2026-08-22T08:30:00Z",
    lifecycle: "interrupted",
    repo: null,
    completeness: [{ field: "title", reason: "This synthetic session has no title event." }],
    adapter_metadata_schema: 1,
    adapter_metadata: { fixture: true, agents: ["synthetic-worker-a", "synthetic-worker-b"] },
    artifacts: [],
    blobs: [],
    unresolved_blob_refs: [],
    continuation_grade: true,
  },
};

const compactTranscript: TranscriptEvent[] = [
  { index: 0, role: "user", kind: "message", time: "2026-08-27T17:15:00Z", text: "This is a synthetic request used only to preview the Babel interface." },
  { index: 1, role: "assistant", kind: "analysis", time: "2026-08-27T17:15:12Z", text: "I will inspect synthetic inputs and describe a safe cache strategy.\n\nNo real repository or transcript content is present." },
  { index: 2, role: "tool", kind: "raw", time: null, text: "{\"fixture\":true,\"result\":\"synthetic tool output\"}" },
  { index: 3, role: "assistant", kind: "message", time: "2026-08-27T18:05:00Z", text: "The synthetic investigation is complete. Invalidate entries when their source fingerprint changes." },
];

const longTranscript: TranscriptEvent[] = Array.from({ length: 207 }, (_, index) => ({
  index,
  role: index % 5 === 0 ? "user" : "assistant",
  kind: index % 17 === 0 ? "analysis" : "message",
  time: new Date(Date.UTC(2026, 7, 28, 9, 10, index * 20)).toISOString(),
  text: index === 0
    ? "Plan a synthetic import pipeline. This mock transcript contains generated preview content only."
    : `Synthetic ${index % 5 === 0 ? "request" : "response"} ${index}. This event exists to exercise transcript pagination and role styling.`,
}));
longTranscript.splice(3, 0, {
  index: 10_003,
  role: "tool",
  kind: "raw",
  time: null,
  text: "{\n  \"fixture\": true,\n  \"paths\": [\"synthetic/input-a\", \"synthetic/input-b\"]\n}",
});

const transcripts: Record<string, TranscriptEvent[]> = {
  "codex:synthetic-alpha": longTranscript,
  "claude-code:synthetic-bravo": compactTranscript,
  "omp:synthetic-charlie": [
    { index: 0, role: "user", kind: "message", time: "2026-08-21T21:05:00Z", text: "Synthetic scratch-session prompt." },
    { index: 1, role: "assistant", kind: "message", time: "2026-08-21T21:05:30Z", text: "Synthetic scratch-session response." },
  ],
};

const archiveStatus: ArchiveStatus = {
  repository: "rest:https://example.invalid/babel-synthetic",
  snapshots: 12,
  hosts: [
    {
      host: "demo-laptop",
      snapshots: 8,
      latest_time: "2026-08-28T07:30:00Z",
      latest_id: "138f14683ef34d28a9b4bc603b87ac55f577cf76801e155f701060ea93c6ac8d",
      latest_short_id: "138f1468",
      tags: ["babel", "sessions"],
    },
    {
      host: "demo-workstation",
      snapshots: 4,
      latest_time: "2026-08-26T20:15:00Z",
      latest_id: "ba183fec9d4296adbbdf202197a03d249d13d80e5bb82e05bc0fd97abdd4c741",
      latest_short_id: "ba183fec",
      tags: ["babel"],
    },
  ],
};

const version: VersionInfo = {
  version: "0.2.0-mock",
  commit: "synthetic",
  dirty: false,
  go: "go1.26.5",
  platform: "linux/amd64",
};

// The real catalog scan describes hundreds of sessions, so the mock needs a
// catalog large enough for the progress bar and the incremental row reveal to be
// observable. Filler sessions are generated, never copied from real data.
const fillerHarnesses = ["omp", "codex", "claude-code"];

function fillerSession(index: number): SessionSummary {
  const harness = fillerHarnesses[index % fillerHarnesses.length];
  const sourceId = `synthetic-filler-${String(index + 1).padStart(2, "0")}`;
  return {
    harness,
    source_id: sourceId,
    selector: `${harness}:${sourceId}`,
    size: 12_000 + index * 37_119,
    modified: new Date(Date.UTC(2026, 7, 27, 6, index * 17)).toISOString(),
    title: index % 4 === 0 ? null : `Synthetic preview session ${index + 1}`,
    workspace: `/home/demo/projects/synthetic-${index % 5}`,
    continuation_grade: index % 3 !== 0,
  };
}

function fillerDetail(summary: SessionSummary): SessionDetail {
  return {
    harness: summary.harness,
    source_id: summary.source_id,
    selector: summary.selector,
    primary_path: `/home/demo/.${summary.harness}/sessions/${summary.source_id}.jsonl`,
    primary_size: summary.size,
    described_at: "2026-08-28T10:45:00Z",
    hint: "Generated filler session for the Babel web mock",
    title: summary.title,
    workspace: summary.workspace,
    created_at: summary.modified,
    modified_at: summary.modified,
    lifecycle: summary.continuation_grade ? "complete" : null,
    repo: null,
    completeness: summary.title ? [] : [{ field: "title", reason: "This generated fixture has no title event." }],
    adapter_metadata_schema: 1,
    adapter_metadata: { fixture: true, filler: true },
    artifacts: [],
    blobs: [],
    unresolved_blob_refs: [],
    continuation_grade: summary.continuation_grade,
  };
}

const catalog: SessionSummary[] = [...sessions];
for (let index = 0; index < 15; index += 1) {
  const summary = fillerSession(index);
  catalog.push(summary);
  details[summary.selector] = fillerDetail(summary);
  transcripts[summary.selector] = [
    { index: 0, role: "user", kind: "message", time: summary.modified, text: `Generated filler prompt for ${summary.selector}.` },
    { index: 1, role: "assistant", kind: "message", time: summary.modified, text: "Generated filler response. No real transcript content is present." },
  ];
}

// Scan simulation. MOCK_SCAN=running (default) starts from a cold cache and
// advances one describe per /api/scan poll; MOCK_SCAN=error fails part-way;
// MOCK_SCAN=idle presents a fully warm cache; MOCK_SCAN=empty presents a cold
// cache with no scan running.
const scanMode = Bun.env.MOCK_SCAN ?? "running";
const failAfter = 6;

interface ScanSimulation {
  state: ScanState;
  described: number;
  refreshedAt: string;
}

function startScan(): ScanSimulation {
  return {
    state: {
      running: true,
      described: 0,
      total: catalog.length,
      failed: 0,
      harness: catalog[0].harness,
      started_at: new Date().toISOString(),
    },
    described: 0,
    refreshedAt: new Date().toISOString(),
  };
}

function warmScan(): ScanSimulation {
  return {
    state: {
      running: false,
      described: catalog.length,
      total: catalog.length,
      failed: 0,
      started_at: "2026-08-28T10:43:00Z",
      finished_at: "2026-08-28T10:43:30Z",
    },
    described: catalog.length,
    refreshedAt: "2026-08-28T10:43:30Z",
  };
}

function emptyScan(): ScanSimulation {
  return {
    state: { running: false, described: 0, total: 0, failed: 0 },
    described: 0,
    refreshedAt: "2026-08-28T10:43:30Z",
  };
}

const openings: Record<string, () => ScanSimulation> = {
  idle: warmScan,
  empty: emptyScan,
  error: startScan,
  running: startScan,
};

let simulation = (openings[scanMode] ?? startScan)();

function stepScan(): void {
  const { state } = simulation;
  if (!state.running) return;
  if (scanMode === "error" && state.described >= failAfter) {
    state.running = false;
    state.finished_at = new Date().toISOString();
    state.error = `synthetic scan failure: cannot read /home/demo/.codex/sessions/${catalog[state.described].source_id}.jsonl: permission denied`;
    return;
  }
  state.described += 1;
  state.harness = catalog[Math.min(state.described, catalog.length - 1)].harness;
  simulation.described = state.described;
  simulation.refreshedAt = new Date().toISOString();
  if (state.described >= state.total) {
    state.running = false;
    state.harness = undefined;
    state.finished_at = new Date().toISOString();
  }
}

function json(body: unknown, status = 200): Response {
  return Response.json(body, {
    status,
    headers: { "Cache-Control": "no-store" },
  });
}

function apiResponse(request: Request, url: URL): Response | null {
  if (!url.pathname.startsWith("/api/")) return null;
  if (request.method === "GET" && url.pathname === "/api/version") return json(version);
  if (request.method === "GET" && url.pathname === "/api/state") {
    return json({ configured: true, repository: archiveStatus.repository, host_id: "demo-laptop" });
  }
  if (request.method === "GET" && url.pathname === "/api/sessions") {
    return json({
      sessions: catalog.slice(0, simulation.described),
      refreshed_at: simulation.refreshedAt,
      scan: simulation.state,
    });
  }
  if (request.method === "GET" && url.pathname === "/api/scan") {
    stepScan();
    return json(simulation.state);
  }
  if (request.method === "POST" && url.pathname === "/api/sessions/refresh") {
    // The mock restarts from a cold cache so the incremental reveal stays easy
    // to preview; the real server keeps already-described rows.
    if (!simulation.state.running) simulation = startScan();
    return json(simulation.state);
  }
  if (request.method === "GET" && url.pathname === "/api/session") {
    const selector = url.searchParams.get("selector") ?? "";
    const detail = details[selector];
    return detail ? json(detail) : json({ error: `synthetic session not found: ${selector}` }, 404);
  }
  if (request.method === "GET" && url.pathname === "/api/transcript") {
    const selector = url.searchParams.get("selector") ?? "";
    const events = transcripts[selector];
    if (!events) return json({ error: `synthetic transcript not found: ${selector}` }, 404);
    const offset = Math.max(0, Number(url.searchParams.get("offset") ?? 0));
    const limit = Math.max(0, Number(url.searchParams.get("limit") ?? 200));
    return json({ total: events.length, events: events.slice(offset, offset + limit) });
  }
  if (request.method === "GET" && url.pathname === "/api/archive/status") return json(archiveStatus);
  if (request.method === "POST" && url.pathname === "/api/archive/verify") {
    const deep = url.searchParams.get("deep") === "1";
    const result: VerifyResult = { repository: archiveStatus.repository, deep, ok: true };
    return json(result);
  }
  if (request.method === "POST" && url.pathname === "/api/fetch") {
    const selector = url.searchParams.get("selector") ?? "";
    if (!details[selector]) return json({ error: `synthetic session not found: ${selector}` }, 404);
    const result: FetchResult = {
      selector,
      snapshot_id: "138f14683ef34d28a9b4bc603b87ac55f577cf76801e155f701060ea93c6ac8d",
      snapshot_short_id: url.searchParams.get("snapshot") || "138f1468",
      snapshot_time: "2026-08-28T07:30:00Z",
      target: `/home/demo/.local/share/babel/fetched/${selector.replaceAll(":", "-")}`,
      files: 2,
      bytes: 490_123,
      included: ["session.jsonl", "workspace/notes.txt"],
      missing: ["synthetic/missing-attachment.bin"],
      already_present: false,
    };
    return json(result);
  }
  return json({ error: "unknown synthetic API endpoint" }, 404);
}

const mimeTypes: Record<string, string> = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
  ".svg": "image/svg+xml",
};

async function staticResponse(url: URL): Promise<Response> {
  const requested = decodeURIComponent(url.pathname);
  const relative = requested === "/" ? "index.html" : requested.replace(/^\/+/, "");
  const candidate = resolve(distRoot, relative);
  const safeCandidate = candidate.startsWith(`${distRoot}${sep}`) ? candidate : join(distRoot, "index.html");
  const file = Bun.file(safeCandidate);
  if (await file.exists()) {
    return new Response(file, {
      headers: { "Content-Type": mimeTypes[extname(safeCandidate)] ?? "application/octet-stream" },
    });
  }
  const index = Bun.file(join(distRoot, "index.html"));
  if (await index.exists()) return new Response(index, { headers: { "Content-Type": mimeTypes[".html"] } });
  return new Response("web/dist is missing; run bun run build first\n", { status: 503 });
}

const server = Bun.serve({
  hostname: "127.0.0.1",
  port,
  async fetch(request) {
    const url = new URL(request.url);
    const api = apiResponse(request, url);
    return api ?? staticResponse(url);
  },
});

console.log(`Babel mock: http://${server.hostname}:${server.port}/?token=synthetic-preview-token`);
console.log(`Scan simulation: MOCK_SCAN=${scanMode} (running | error | idle | empty)`);
