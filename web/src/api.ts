export interface VersionInfo {
  version: string;
  commit: string;
  dirty: boolean;
  go: string;
  platform: string;
}

export interface StateInfo {
  configured: boolean;
  repository: string;
  host_id: string;
}

export interface SessionSummary {
  harness: string;
  source_id: string;
  selector: string;
  size: number;
  modified: string | null;
  title: string | null;
  workspace: string | null;
  continuation_grade: boolean;
}

export interface ScanState {
  running: boolean;
  described: number;
  total: number;
  failed: number;
  harness?: string;
  started_at?: string;
  finished_at?: string;
  error?: string;
}

export interface SessionsResponse {
  sessions: SessionSummary[];
  refreshed_at: string;
  scan: ScanState;
}

export interface CompletenessReason {
  field: string;
  reason: string;
}

export interface RepoInfo {
  remote?: string;
  commit?: string;
  branch?: string;
}

export interface Artifact {
  rel_path: string;
  source_path: string;
  size: number;
}

export interface Blob {
  digest: string;
  source_path: string;
  size: number;
}

export interface SessionDetail {
  harness: string;
  source_id: string;
  selector: string;
  primary_path: string;
  primary_size: number;
  described_at: string;
  hint?: string;
  title: string | null;
  workspace: string | null;
  created_at: string | null;
  modified_at: string | null;
  lifecycle: string | null;
  repo: RepoInfo | null;
  completeness?: CompletenessReason[];
  adapter_metadata_schema: number;
  adapter_metadata?: unknown;
  artifacts?: Artifact[];
  blobs?: Blob[];
  unresolved_blob_refs?: string[];
  continuation_grade: boolean;
}

export interface TranscriptEvent {
  index: number;
  role: string;
  kind: string;
  time: string | null;
  text: string;
}

export interface TranscriptResponse {
  total: number;
  events: TranscriptEvent[];
}

export interface ArchiveHost {
  host: string;
  snapshots: number;
  latest_time: string;
  latest_id: string;
  latest_short_id: string;
  tags?: string[];
}

export interface ArchiveStatus {
  repository: string;
  snapshots: number;
  hosts: ArchiveHost[];
}

export interface VerifyResult {
  repository: string;
  deep: boolean;
  ok: boolean;
  error?: string;
}

export interface FetchResult {
  selector: string;
  snapshot_id: string;
  snapshot_short_id: string;
  snapshot_time: string;
  target: string;
  files: number;
  bytes: number;
  included: string[];
  missing?: string[];
  already_present: boolean;
}

const TOKEN_KEY = "babel.web.token";

// The launch token arrives in the URL fragment, which the browser never sends
// to any server (SPEC.md §146). It is copied into session storage and the
// fragment is erased immediately, so a reload or a screenshot carries no
// credential. Every later request presents it as a bearer header.
//
// Honest accounting of that erasure: the HashRouter's own first navigation
// replaces the entry with "#/sessions", which clears the token as well, so
// this replaceState is not the only thing keeping it out of the address bar.
// It is kept because dropping it would make a security property depend on
// react-router continuing to replace rather than push. Removing it is not
// observable today — a browser test cannot distinguish the two mechanisms, and
// one written to try was proven non-discriminating. What the test defends is
// the property itself: no reachable history entry retains the token.
//
// The fragment is also route state, so this must read it before the router
// mounts. ES module evaluation guarantees that: main.tsx imports App, which
// imports this module, so this function runs during import and therefore
// before createRoot().render(). Lazy-loading this module would let the router
// rewrite the fragment to "#/sessions" first and silently break
// authentication. The nonce-to-cookie exchange in SPEC.md §146 would remove
// this coupling by using the fragment exactly once.
function bootstrapToken(): string {
  const hash = window.location.hash.replace(/^#/u, "");
  const supplied = new URLSearchParams(hash).get("token") ?? "";
  if (supplied) {
    try {
      window.sessionStorage.setItem(TOKEN_KEY, supplied);
    } catch {
      // A locked-down browser can disable session storage; the current request still works.
    }
    const url = new URL(window.location.href);
    window.history.replaceState(
      window.history.state,
      "",
      `${url.pathname}${url.search}`,
    );
    return supplied;
  }
  try {
    return window.sessionStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

const token = bootstrapToken();
const errorListeners = new Set<(message: string | null) => void>();
let currentError: string | null = null;

function safeMessage(value: unknown): string {
  const message = value instanceof Error ? value.message : String(value);
  return message.replace(/[\u0000-\u001f\u007f-\u009f]/gu, "").trim() || "Request failed";
}

function publishError(value: unknown): void {
  currentError = safeMessage(value);
  for (const listener of errorListeners) listener(currentError);
}

export function subscribeAPIErrors(listener: (message: string | null) => void): () => void {
  errorListeners.add(listener);
  listener(currentError);
  return () => errorListeners.delete(listener);
}

export function dismissAPIError(): void {
  currentError = null;
  for (const listener of errorListeners) listener(null);
}

export class APIError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "APIError";
    this.status = status;
  }
}

// Every request is bounded so a stalled server surfaces an error instead of an
// interface that spins forever.
const REQUEST_TIMEOUT_MS = 20_000;

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const headers = new Headers(init.headers);
    headers.set("Authorization", `Bearer ${token}`);
    const response = await fetch(path, {
      ...init,
      headers,
      cache: "no-store",
      signal: controller.signal,
    });
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try {
        const body = (await response.json()) as { error?: unknown };
        if (typeof body.error === "string" && body.error) message = body.error;
      } catch {
        // Keep the status fallback for malformed or empty error responses.
      }
      throw new APIError(response.status, message);
    }
    return (await response.json()) as T;
  } catch (error) {
    const failure = controller.signal.aborted
      ? new APIError(408, `${path} did not respond within ${REQUEST_TIMEOUT_MS / 1000}s`)
      : error;
    publishError(failure);
    throw failure;
  } finally {
    window.clearTimeout(timer);
  }
}

function query(values: Record<string, string | number>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) params.set(key, String(value));
  return params.toString();
}

export function getVersion(): Promise<VersionInfo> {
  return request<VersionInfo>("/api/version");
}

export function getState(): Promise<StateInfo> {
  return request<StateInfo>("/api/state");
}

export function getSessions(): Promise<SessionsResponse> {
  return request<SessionsResponse>("/api/sessions");
}

export function getScan(): Promise<ScanState> {
  return request<ScanState>("/api/scan");
}

export function refreshSessions(): Promise<ScanState> {
  return request<ScanState>("/api/sessions/refresh", { method: "POST" });
}

export function getSession(selector: string): Promise<SessionDetail> {
  return request<SessionDetail>(`/api/session?${query({ selector })}`);
}

export function getTranscript(
  selector: string,
  offset = 0,
  limit = 200,
): Promise<TranscriptResponse> {
  return request<TranscriptResponse>(
    `/api/transcript?${query({ selector, offset, limit })}`,
  );
}

export function getArchiveStatus(): Promise<ArchiveStatus> {
  return request<ArchiveStatus>("/api/archive/status");
}

export function verifyArchive(deep: boolean): Promise<VerifyResult> {
  return request<VerifyResult>(`/api/archive/verify?${query({ deep: deep ? 1 : 0 })}`, {
    method: "POST",
  });
}

export function fetchSession(selector: string, snapshot?: string): Promise<FetchResult> {
  const values: Record<string, string> = { selector };
  if (snapshot?.trim()) values.snapshot = snapshot.trim();
  return request<FetchResult>(`/api/fetch?${query(values)}`, { method: "POST" });
}
